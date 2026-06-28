package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
