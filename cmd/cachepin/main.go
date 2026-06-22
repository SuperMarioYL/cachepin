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
	// this file path (in addition to the human-readable stdout line).
	NDJSON string
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
		f, err := os.Create(cfg.NDJSON)
		if err != nil {
			return fmt.Errorf("open --ndjson file %q: %w", cfg.NDJSON, err)
		}
		defer f.Close()
		nd = f
	}

	p, err := buildProxy(cfg, os.Stdout, nd)
	if err != nil {
		return err
	}

	fmt.Printf("cachepin listening on %s -> upstream %s (pin=%v)\n", cfg.Listen, cfg.Upstream, cfg.Pin)
	return http.ListenAndServe(cfg.Listen, p)
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

	tracker := session.NewTracker()
	reporter := metrics.NewReporter(human, nd)

	// canonical mirrors the tracker's per-session ground truth for pin mode.
	// The tracker already keeps canonical history internally for metrics; pin
	// mode needs the pre-mutation canonical of the relevant session to rewrite
	// the outgoing body, so we keep a parallel map keyed by session id.
	canonical := make(map[string][]openai.Message)

	p.Intercept = func(path string, body []byte) ([]byte, error) {
		req, err := openai.ParseChatRequest(body)
		if err != nil {
			// Not a parseable chat request (or a body shape we don't model):
			// forward it untouched rather than failing the user's turn.
			return body, nil
		}

		sid := session.SessionID(req.Messages)
		prior := canonical[sid]

		turn := tracker.Observe(req.Messages)
		if err := reporter.Report(turn); err != nil {
			fmt.Fprintln(os.Stderr, "cachepin: report:", err)
		}

		out := body
		if cfg.Pin {
			reconciled, changed := pin.Reconcile(prior, req.Messages)
			if changed {
				req.SetMessages(reconciled)
				b, err := req.Marshal()
				if err != nil {
					return nil, fmt.Errorf("pin: re-marshal request: %w", err)
				}
				out = b
			}
			canonical[sid] = cloneMessages(reconciled)
		} else {
			canonical[sid] = cloneMessages(req.Messages)
		}
		return out, nil
	}

	return p, nil
}

func cloneMessages(msgs []openai.Message) []openai.Message {
	out := make([]openai.Message, len(msgs))
	copy(out, msgs)
	return out
}
