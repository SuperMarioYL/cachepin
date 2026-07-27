package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// TestBuildProxyServesEndToEnd is the smoke test for fix-wire-proxy-into-main:
// before the fix, run() was an inert stub that never built a proxy or listened,
// so the binary proxied zero requests. This drives a real request through the
// wired proxy to a fake upstream and asserts the body is forwarded AND a per-turn
// metrics line is emitted — proving the tracker/metrics interceptor is installed.
func TestBuildProxyServesEndToEnd(t *testing.T) {
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	var human bytes.Buffer
	cfg := Config{Upstream: upstream.URL, Listen: ":0"}
	p, err := buildProxy(cfg, &human, nil)
	if err != nil {
		t.Fatalf("buildProxy: %v", err)
	}
	front := httptest.NewServer(p)
	defer front.Close()

	body := `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`
	resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if upstreamBody != body {
		t.Errorf("upstream got body %q, want forwarded verbatim %q", upstreamBody, body)
	}
	if !strings.Contains(human.String(), "turn 1") {
		t.Errorf("no per-turn metrics line emitted; got %q", human.String())
	}
}

// TestBuildProxyPinReconcilesMutatedRequest proves the pin path is wired: a
// second request that mutates an earlier message is rewritten to append-only
// form before reaching the upstream, so the upstream sees the preserved canonical
// prefix plus the new tail.
func TestBuildProxyPinReconcilesMutatedRequest(t *testing.T) {
	var bodies []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := Config{Upstream: upstream.URL, Listen: ":0", Pin: true}
	p, err := buildProxy(cfg, io.Discard, nil)
	if err != nil {
		t.Fatalf("buildProxy: %v", err)
	}
	front := httptest.NewServer(p)
	defer front.Close()

	// Turn 1 establishes canonical: system, user, assistant.
	first := `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"u1"},{"role":"assistant","content":"a1"}]}`
	// Turn 2 mutates the assistant message (a1 -> a1X) and appends a new user
	// message. Pin must rewrite this back to canonical + new tail.
	second := `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"u1"},{"role":"assistant","content":"a1X"},{"role":"user","content":"u2"}]}`

	post(t, front.URL, first)
	post(t, front.URL, second)

	if len(bodies) != 2 {
		t.Fatalf("upstream saw %d requests, want 2", len(bodies))
	}

	// The reconciled second request must still contain the canonical "a1" (cache
	// preserved) AND the genuinely-new "u2" (no turn dropped).
	got := extractContents(t, bodies[1])
	wantSubset := []string{"a1", "u2"}
	for _, w := range wantSubset {
		found := false
		for _, c := range got {
			if c == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("reconciled upstream body missing %q; got contents %v", w, got)
		}
	}
}

func post(t *testing.T, base, body string) {
	t.Helper()
	resp, err := http.Post(base+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func extractContents(t *testing.T, body string) []string {
	t.Helper()
	var req struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("parse upstream body: %v", err)
	}
	out := make([]string, len(req.Messages))
	for i, m := range req.Messages {
		out[i] = m.Content
	}
	return out
}

// TestBuildProxyConcurrentMultiSessionNoRace covers fix-concurrent-canonical-map-crash:
// the interceptor runs in httputil.ReverseProxy's per-request goroutine, so
// concurrent requests for different sessions used to race the unguarded
// canonical map and crash the process with a Go runtime "concurrent map read
// and map write" fatal. The v0.3.0 fold moved that store into the mutex-guarded
// tracker. Run under `go test -race` to confirm no data race / crash remains.
func TestBuildProxyConcurrentMultiSessionNoRace(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := Config{Upstream: upstream.URL, Listen: ":0"}
	p, err := buildProxy(cfg, io.Discard, nil)
	if err != nil {
		t.Fatalf("buildProxy: %v", err)
	}
	front := httptest.NewServer(p)
	defer front.Close()

	// Distinct first-user message per goroutine -> distinct session id, so the
	// interceptor exercises the shared session store from many goroutines at once.
	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf(
				`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"session %d"}]}`,
				i)
			resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
			if err != nil {
				errs <- err
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errs <- fmt.Errorf("request %d status %d", i, resp.StatusCode)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent request failed: %v", err)
	}
}

// TestBuildProxyPinModeMetricsMatchBenchmark covers fix-pin-mode-metrics-observe-mutated:
// under --pin a mutated-but-reconcilable turn must report ~0 reprocessing, matching
// bench/benchmark.go (which feeds the reconciled array to the pinned tracker). Before
// the fix the proxy observed the raw mutated request and overstated reprocessing every
// turn, contradicting the benchmark and making pin look broken when it was working.
func TestBuildProxyPinModeMetricsMatchBenchmark(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	var human bytes.Buffer
	cfg := Config{Upstream: upstream.URL, Listen: ":0", Pin: true}
	p, err := buildProxy(cfg, &human, nil)
	if err != nil {
		t.Fatalf("buildProxy: %v", err)
	}
	front := httptest.NewServer(p)
	defer front.Close()

	// Turn 1 establishes canonical: system, user, assistant.
	first := `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"u1"},{"role":"assistant","content":"a1"}]}`
	// Turn 2 mutates the assistant message (a1 -> a1X) and appends a new user
	// message. Pin reconciles it to canonical + new tail; the tracker must observe
	// the reconciled array so the per-turn metrics report ~0 reprocessing.
	second := `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"u1"},{"role":"assistant","content":"a1X"},{"role":"user","content":"u2"}]}`

	post(t, front.URL, first)
	post(t, front.URL, second)

	lines := strings.Split(strings.TrimSpace(human.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected >=2 metric lines, got %d: %q", len(lines), human.String())
	}
	secondLine := lines[1]
	if strings.Contains(secondLine, "MUTATION") {
		t.Errorf("pin-mode turn 2 reported a mutation (should be reconciled clean): %q", secondLine)
	}
	if !strings.Contains(secondLine, "0 tokens reprocessed") {
		t.Errorf("pin-mode turn 2 should report ~0 reprocessed tokens, got: %q", secondLine)
	}
}

// TestBuildProxyNDJSONAppendsAcrossRestarts covers fix-ndjson-truncates-and-races
// at the sink level: openNDJSON opens the --ndjson file in append mode (v0.4.0),
// so re-opening the same path (a process restart) accumulates turns instead of
// truncating. Before the fix os.Create (O_TRUNC) wiped the prior log on every
// restart. The two proxies each write one turn through their own append-opened
// sink, mirroring what run() does across a restart.
func TestBuildProxyNDJSONAppendsAcrossRestarts(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	f, err := os.CreateTemp("", "cachepin-ndjson-*.jsonl")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	ndjson := f.Name()
	f.Close()
	defer os.Remove(ndjson)

	// "Restart 1": append-open the sink, build a proxy over it, send one turn.
	sink1, err := openNDJSON(ndjson)
	if err != nil {
		t.Fatalf("openNDJSON 1: %v", err)
	}
	cfg := Config{Upstream: upstream.URL, Listen: ":0"}
	p1, err := buildProxy(cfg, io.Discard, sink1)
	if err != nil {
		t.Fatalf("buildProxy p1: %v", err)
	}
	front1 := httptest.NewServer(p1)
	defer front1.Close()
	post(t, front1.URL, `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"restart1"}]}`)
	if err := sink1.Close(); err != nil {
		t.Fatalf("sink1 close: %v", err)
	}

	// "Restart 2": append-open the SAME file (a fresh process restart). Under
	// the old os.Create path this truncated the file; append keeps both turns.
	sink2, err := openNDJSON(ndjson)
	if err != nil {
		t.Fatalf("openNDJSON 2: %v", err)
	}
	p2, err := buildProxy(cfg, io.Discard, sink2)
	if err != nil {
		t.Fatalf("buildProxy p2: %v", err)
	}
	front2 := httptest.NewServer(p2)
	defer front2.Close()
	post(t, front2.URL, `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"restart2"}]}`)
	if err := sink2.Close(); err != nil {
		t.Fatalf("sink2 close: %v", err)
	}

	data, err := os.ReadFile(ndjson)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	body := string(data)
	// Both turns' NDJSON lines must be present (append, not truncate). The
	// per-turn record carries a session_id (a hash of system+first-user), not
	// the message content, so assert line count + two DISTINCT session ids.
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 appended NDJSON lines across restarts, got %d: %q", len(lines), body)
	}
	ids := make(map[string]struct{}, 2)
	for _, ln := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(ln), &rec); err != nil {
			t.Fatalf("appended NDJSON line not valid JSON: %q\nerr: %v", ln, err)
		}
		if sid, ok := rec["session_id"].(string); ok {
			ids[sid] = struct{}{}
		}
	}
	if len(ids) != 2 {
		t.Errorf("expected 2 distinct session_ids across restarts, got %d: %q", len(ids), body)
	}
}

// TestBuildProxyPinWarnsOnEvictedSession covers fix-pin-coverage-voided-by-eviction:
// under --pin with a small --max-sessions, a session evicted then returning has
// its canonical lost (tracker.Canonical returns nil), so pin.Reconcile is a
// no-op and the raw mutated request is forwarded. The v0.4.0 fix emits a WARN on
// the human line so the silent-green-while-reprocessing case is surfaced, and
// the NDJSON record carries pin_coverage_lost:true.
func TestBuildProxyPinWarnsOnEvictedSession(t *testing.T) {
	var bodies []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	var human bytes.Buffer
	cfg := Config{Upstream: upstream.URL, Listen: ":0", Pin: true, MaxSessions: 1}
	p, err := buildProxy(cfg, &human, nil)
	if err != nil {
		t.Fatalf("buildProxy: %v", err)
	}
	front := httptest.NewServer(p)
	defer front.Close()

	// Turn 1: establish canonical for session A (system + user u1 + assistant a1).
	post(t, front.URL, `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"u1"},{"role":"assistant","content":"a1"}]}`)
	// Turn 2: a different session B forces LRU eviction of A (cap = 1).
	post(t, front.URL, `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"B"}]}`)
	// Turn 3: session A returns MUTATED (a1 -> a1X) + new u2. Canonical was
	// evicted, so pin cannot reconcile and must forward the raw body with a WARN.
	post(t, front.URL, `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"u1"},{"role":"assistant","content":"a1X"},{"role":"user","content":"u2"}]}`)

	if len(bodies) != 3 {
		t.Fatalf("upstream saw %d requests, want 3", len(bodies))
	}
	// The raw mutated body must reach the upstream (pin did NOT restore a1).
	if !strings.Contains(bodies[2], "a1X") {
		t.Errorf("evicted-session turn 3 should forward the raw mutated body containing a1X; got %q", bodies[2])
	}
	if !strings.Contains(human.String(), "WARN: canonical evicted") {
		t.Errorf("evicted-session turn 3 should emit a pin-coverage WARN; human = %q", human.String())
	}
	if !strings.Contains(human.String(), "raise --max-sessions") {
		t.Errorf("WARN should name the remediation; human = %q", human.String())
	}
}

// TestStartupLogRedactsUpstreamCredentials is the end-to-end regression test
// for fix-upstream-credentials-leaked-in-error-and-log at main.go:105. The
// startup fmt.Printf prints to stdout, which is the same sink as the per-turn
// metrics log, so a raw --upstream with userinfo leaked the credential into
// any captured stdout. With the fix, the startup line renders the upstream via
// proxy.RedactedUpstream. The listen address uses an invalid port so
// http.ListenAndServe fails immediately and run() returns right after the
// startup line is printed (no real listener starts).
func TestStartupLogRedactsUpstreamCredentials(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	cfg := Config{
		Upstream: "http://user:secret@127.0.0.1:1",
		Listen:   "127.0.0.1:99999", // invalid port -> ListenAndServe fails immediately
	}
	_ = run(cfg) // expected to return a ListenAndServe error after printing startup
	w.Close()
	os.Stdout = old
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if strings.Contains(string(out), "secret") {
		t.Errorf("startup log leaked upstream password: %q", string(out))
	}
	if strings.Contains(string(out), "user:secret") {
		t.Errorf("startup log leaked upstream userinfo: %q", string(out))
	}
	// The redacted host must still be present so the startup line stays useful.
	if !strings.Contains(string(out), "127.0.0.1:1") {
		t.Errorf("startup log should still name the upstream host; got %q", string(out))
	}
}

// TestBuildProxyPinSameSessionConcurrentNoTurnLoss is the integration regression
// test for fix-same-session-pin-reconcile-race: the wired --pin interceptor must
// route through ReconcileAndObserve (one critical section) so two concurrent
// same-session --pin requests cannot drop a turn from the canonical history.
// Each goroutine appends a unique assistant turn to the SAME session; after all
// complete, one final request's reconciled upstream body must contain EVERY
// appended turn — turn loss from the pre-v0.5.0 Canonical->Reconcile->Observe
// TOCTOU would drop some. Run under `go test -race`.
func TestBuildProxyPinSameSessionConcurrentNoTurnLoss(t *testing.T) {
	var lastBody string
	var lastMu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		lastMu.Lock()
		lastBody = string(b)
		lastMu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := Config{Upstream: upstream.URL, Listen: ":0", Pin: true}
	p, err := buildProxy(cfg, io.Discard, nil)
	if err != nil {
		t.Fatalf("buildProxy: %v", err)
	}
	front := httptest.NewServer(p)
	defer front.Close()

	// Same system + first-user message => same session id. Each goroutine
	// appends a unique assistant turn to that one session concurrently.
	const n = 64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release all goroutines together to maximize interleaving
			body := fmt.Sprintf(
				`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"u"},{"role":"assistant","content":"turn-%d"}]}`,
				i)
			resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
			if err != nil {
				t.Errorf("request %d: %v", i, err)
				return
			}
			resp.Body.Close()
		}(i)
	}
	close(start)
	wg.Wait()

	// One final request: its reconciled body = canonical-so-far + "FINAL", so
	// it exposes the full accumulated canonical. Every appended turn must appear
	// (quote-delimited so turn-1 does not match inside turn-10/turn-11).
	post(t, front.URL, `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"u"},{"role":"assistant","content":"FINAL"}]}`)

	lastMu.Lock()
	final := lastBody
	lastMu.Unlock()
	for i := 0; i < n; i++ {
		want := fmt.Sprintf("\"turn-%d\"", i)
		if !strings.Contains(final, want) {
			t.Errorf("turn %s lost from canonical (same-session pin race); final upstream body: %s", want, final)
		}
	}
	if !strings.Contains(final, "\"FINAL\"") {
		t.Errorf("final turn missing from upstream body: %s", final)
	}
}
