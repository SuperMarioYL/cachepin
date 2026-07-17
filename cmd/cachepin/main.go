// Command cachepin is a harness-neutral, OpenAI-compatible reverse proxy that
// keeps a coding agent's KV Cache alive across turns. Drop it between your
// coding-agent harness and your model server, point OPENAI_BASE_URL at it, and
// it reports (and, with --pin, protects) the server-side prefix cache.
//
// This file is the entry point: it parses flags, builds the proxy with the
// session tracker / metrics reporter / pin reconciler wired in as an
// interceptor, and starts the HTTP listener. The proxy itself lives in
// internal/proxy (milestone m1); the tracker in internal/session (m2); the
// reconciler in internal/pin (m3); the metrics reporter in internal/metrics.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/SuperMarioYL/cachepin/internal/metrics"
	"github.com/SuperMarioYL/cachepin/internal/openai"
	"github.com/SuperMarioYL/cachepin/internal/pin"
	"github.com/SuperMarioYL/cachepin/internal/proxy"
	"github.com/SuperMarioYL/cachepin/internal/session"
)

// Config holds the runtime configuration parsed from flags.
type Config struct {
	// Upstream is the base URL of the OpenAI-compatible model server, e.g.
	// http://localhost:8080.
	Upstream string
	// Listen is the address CachePin's proxy binds to, e.g. :8089.
	Listen string
	// Pin enables reconciliation of mutated requests back to append-only form
	// so the upstream KV Cache survives.
	Pin bool
	// NDJSON, when set, writes one machine-readable metrics object per turn to
	// this file path (in addition to the human-readable stdout line). The file
	// is opened in append mode, so restarting CachePin with the same path
	// accumulates turns rather than truncating the prior log (v0.4.0).
	NDJSON string
	// MaxSessions bounds the number of conversations tracked at once; past it
	// the least-recently-used session is evicted so memory stays bounded under
	// long uptime. 0 means unbounded.
	MaxSessions int
	// IdleTTL, when non-zero, evicts a session whose last observed turn is older
	// than this on the next Observe — so a sparse long-uptime deployment keeps
	// memory bounded even when no new sessions arrive to push idle ones out
	// under the LRU count cap (v0.4.0). 0 disables idle expiry.
	IdleTTL time.Duration
}

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "cachepin:", err)
		os.Exit(2)
	}

	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "cachepin:", err)
		os.Exit(1)
	}
}

func parseFlags(args []string) (Config, error) {
	fs := flag.NewFlagSet("cachepin", flag.ContinueOnError)
	var cfg Config
	fs.StringVar(&cfg.Upstream, "upstream", "", "base URL of the OpenAI-compatible model server (required), e.g. http://localhost:8080")
	fs.StringVar(&cfg.Listen, "listen", ":8089", "address to listen on")
	fs.BoolVar(&cfg.Pin, "pin", false, "reconcile mutated requests to append-only form to preserve the upstream KV cache")
	fs.StringVar(&cfg.NDJSON, "ndjson", "", "optional path to write per-turn metrics as NDJSON")
	fs.IntVar(&cfg.MaxSessions, "max-sessions", session.DefaultMaxSessions, "cap on tracked sessions; the least-recently-used session is evicted past it (0 = unbounded)")
	fs.DurationVar(&cfg.IdleTTL, "idle-ttl", 0, "evict sessions whose last turn is older than this (e.g. 10m, 1h); 0 disables idle expiry and keeps only the --max-sessions count cap")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if cfg.Upstream == "" {
		return Config{}, fmt.Errorf("--upstream is required (e.g. --upstream http://localhost:8080)")
	}
	return cfg, nil
}

// run builds the proxy, wires in the tracker / metrics reporter / (optional) pin
// reconciler as an interceptor, opens the NDJSON sink if requested, and starts
// the HTTP listener. It blocks until the server stops (or fails to start).
func run(cfg Config) error {
	var nd io.Writer
	if cfg.NDJSON != "" {
		f, err := openNDJSON(cfg.NDJSON)
		if err != nil {
			return err
		}
		defer f.Close()
		nd = f
	}

	p, err := buildProxy(cfg, os.Stdout, nd)
	if err != nil {
		return err
	}

	fmt.Printf("cachepin listening on %s -> upstream %s (pin=%v, max-sessions=%d, idle-ttl=%s)\n", cfg.Listen, cfg.Upstream, cfg.Pin, cfg.MaxSessions, cfg.IdleTTL)
	return http.ListenAndServe(cfg.Listen, p)
}

// openNDJSON opens the --ndjson sink in append mode (v0.4.0). os.Create would
// truncate the prior log on every restart, and a non-append open let concurrent
// per-request writes share one file offset and land out of order. O_APPEND
// makes each write atomic-at-end-of-file so restarts accumulate and concurrent
// turns append in call order. It is extracted so the append-mode contract is
// unit-testable without binding a real listener.
func openNDJSON(path string) (io.WriteCloser, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open --ndjson file %q: %w", path, err)
	}
	return f, nil
}

// buildProxy constructs the proxy and installs the interceptor that observes the
// session, reports per-turn metrics, and (when cfg.Pin) reconciles a mutated
// request back to append-only form before it is forwarded upstream. human and nd
// are the metrics sinks (nd may be nil to skip NDJSON). It is separated from run
// so it can be exercised in tests without binding a real listener.
func buildProxy(cfg Config, human, nd io.Writer) (*proxy.Proxy, error) {
	p, err := proxy.New(cfg.Upstream)
	if err != nil {
		return nil, err
	}

	tracker := session.NewTrackerWithMaxTTL(cfg.MaxSessions, cfg.IdleTTL)
	reporter := metrics.NewReporter(human, nd)

	// The reconciled-canonical store lives inside the tracker (v0.3.0 fold): the
	// tracker already keeps canonical history per session, so pin mode reads its
	// pre-mutation ground truth from there instead of maintaining a parallel —
	// and previously unguarded — map in main. Eviction therefore has one owner,
	// and the per-request critical section touches only the tracker's mutex.
	p.Intercept = func(path string, body []byte) ([]byte, error) {
		req, err := openai.ParseChatRequest(body)
		if err != nil {
			// Not a parseable chat request (or a body shape we don't model):
			// forward it untouched rather than failing the user's turn.
			return body, nil
		}

		sid := session.SessionID(req.Messages)
		prior := tracker.Canonical(sid)
		// pinCoverageLost is true only under --pin when the session's canonical
		// was lost to eviction since the last turn — pin.Reconcile(nil, …) is a
		// no-op in that case, so the raw mutated request is forwarded while
		// Observe would otherwise report green 0-reprocessed metrics. The flag
		// surfaces it in both the human line and NDJSON (v0.4.0).
		pinCoverageLost := false

		// What we actually forward upstream (and thus what the upstream caches
		// against). In --pin mode the reconciled array is what's sent upstream,
		// so observe THAT — matching bench/benchmark.go, which feeds the
		// reconciled array to the pinned tracker — so per-turn metrics reflect
		// upstream reality rather than the raw mutated request (v0.3.0 fix).
		// Observing the raw mutated request instead made --pin metrics overstate
		// reprocessing every turn and contradict the benchmark. In non-pin mode
		// the raw request is forwarded verbatim, so observe it as-is.
		observed := req.Messages
		out := body
		if cfg.Pin {
			if prior == nil && tracker.WasEvicted(sid) {
				pinCoverageLost = true
			}
			reconciled, changed := pin.Reconcile(prior, req.Messages)
			observed = reconciled
			if changed {
				req.SetMessages(reconciled)
				b, err := req.Marshal()
				if err != nil {
					return nil, fmt.Errorf("pin: re-marshal request: %w", err)
				}
				out = b
			}
		}

		turn := tracker.Observe(observed)
		turn.PinCoverageLost = pinCoverageLost
		if err := reporter.Report(turn); err != nil {
			fmt.Fprintln(os.Stderr, "cachepin: report:", err)
		}

		return out, nil
	}

	return p, nil
}
