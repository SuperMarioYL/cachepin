package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewRejectsRelativeUpstream(t *testing.T) {
	for _, bad := range []string{"", "localhost:8080", "/v1"} {
		if _, err := New(bad); err == nil {
			t.Errorf("New(%q) = nil error, want error for non-absolute upstream", bad)
		}
	}
	if _, err := New("http://localhost:8080"); err != nil {
		t.Fatalf("New(valid upstream) returned error: %v", err)
	}
}

// TestTransparentPassthrough checks that method, path, query, headers, and body
// are forwarded unchanged and the upstream status and response body/headers come
// back intact.
func TestTransparentPassthrough(t *testing.T) {
	var (
		gotMethod, gotPath, gotQuery, gotHeader, gotBody string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		gotHeader = r.Header.Get("X-Harness")
		gotBody = string(body)
		w.Header().Set("X-Upstream", "ok")
		w.WriteHeader(http.StatusTeapot)
		io.WriteString(w, "pong")
	}))
	defer upstream.Close()

	p, err := New(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(p)
	defer front.Close()

	req, _ := http.NewRequest(http.MethodPost, front.URL+"/v1/chat/completions?stream=true", strings.NewReader(`{"model":"x"}`))
	req.Header.Set("X-Harness", "claude-code")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if gotMethod != http.MethodPost {
		t.Errorf("upstream method = %q, want POST", gotMethod)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("upstream path = %q, want /v1/chat/completions", gotPath)
	}
	if gotQuery != "stream=true" {
		t.Errorf("upstream query = %q, want stream=true", gotQuery)
	}
	if gotHeader != "claude-code" {
		t.Errorf("upstream X-Harness = %q, want claude-code", gotHeader)
	}
	if gotBody != `{"model":"x"}` {
		t.Errorf("upstream body = %q, want forwarded verbatim", gotBody)
	}
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("client status = %d, want %d", resp.StatusCode, http.StatusTeapot)
	}
	if resp.Header.Get("X-Upstream") != "ok" {
		t.Errorf("client missing upstream response header X-Upstream")
	}
	if string(respBody) != "pong" {
		t.Errorf("client body = %q, want pong", respBody)
	}
}

// TestSSEStreamingFlushesIncrementally proves the proxy relays SSE chunks as
// they are produced rather than buffering the whole response. The upstream
// sends one chunk, then blocks until the client confirms it has received that
// chunk before sending the second; if the proxy buffered, this would deadlock.
func TestSSEStreamingFlushesIncrementally(t *testing.T) {
	firstSent := make(chan struct{})
	clientGotFirst := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("upstream ResponseWriter is not an http.Flusher")
			return
		}
		io.WriteString(w, "data: one\n\n")
		flusher.Flush()
		close(firstSent)

		<-clientGotFirst // only send the rest after the client has the first chunk
		io.WriteString(w, "data: two\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	p, err := New(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(p)
	defer front.Close()

	resp, err := http.Get(front.URL + "/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	done := make(chan error, 1)
	var assembled strings.Builder
	go func() {
		first := make([]byte, len("data: one\n\n"))
		if _, err := io.ReadFull(resp.Body, first); err != nil {
			done <- err
			return
		}
		assembled.Write(first)
		close(clientGotFirst) // unblock the upstream's second write
		rest, err := io.ReadAll(resp.Body)
		assembled.Write(rest)
		done <- err
	}()

	select {
	case <-firstSent:
		// upstream flushed the first chunk; the streaming relay is working
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first SSE chunk — proxy is buffering the stream")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("reading streamed response: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out reading full stream")
	}

	if got := assembled.String(); got != "data: one\n\ndata: two\n\n" {
		t.Errorf("assembled stream = %q, want both SSE chunks intact", got)
	}
}

// TestInterceptorRewritesBody verifies the m2/m3 integration seam: a non-nil
// Interceptor receives the chat-completions body and its rewritten output is
// what reaches the upstream (with a corrected Content-Length).
func TestInterceptorRewritesBody(t *testing.T) {
	var gotBody string
	var gotLen int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		gotLen = r.ContentLength
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	var seenPath, seenBody string
	p.Intercept = func(path string, body []byte) ([]byte, error) {
		seenPath, seenBody = path, string(body)
		return []byte(`{"model":"rewritten"}`), nil
	}
	front := httptest.NewServer(p)
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"orig"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if seenPath != "/v1/chat/completions" {
		t.Errorf("interceptor path = %q", seenPath)
	}
	if seenBody != `{"model":"orig"}` {
		t.Errorf("interceptor saw body = %q, want original", seenBody)
	}
	if gotBody != `{"model":"rewritten"}` {
		t.Errorf("upstream body = %q, want rewritten", gotBody)
	}
	if want := int64(len(`{"model":"rewritten"}`)); gotLen != want {
		t.Errorf("upstream Content-Length = %d, want %d", gotLen, want)
	}
}

// TestInterceptorSkippedForNonChatPaths ensures the body buffering only kicks in
// for the chat-completions endpoint; other paths stream through untouched even
// when an interceptor is installed.
func TestInterceptorSkippedForNonChatPaths(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	p.Intercept = func(path string, body []byte) ([]byte, error) {
		called = true
		return body, nil
	}
	front := httptest.NewServer(p)
	defer front.Close()

	resp, err := http.Get(front.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if called {
		t.Error("interceptor was invoked for /v1/models, want only chat-completions")
	}
}

func TestErrorHandlerReturnsBadGatewayOnUnreachableUpstream(t *testing.T) {
	// Port 1 has no listener, so the upstream connection fails.
	p, err := New("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(p)
	defer front.Close()

	resp, err := http.Get(front.URL + "/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want %d for unreachable upstream", resp.StatusCode, http.StatusBadGateway)
	}
}

func TestIsChatCompletionsPath(t *testing.T) {
	cases := map[string]bool{
		"/v1/chat/completions":        true,
		"/openai/v1/chat/completions": true,
		"/v1/models":                  false,
		"/v1/completions":             false,
		"/":                           false,
	}
	for path, want := range cases {
		if got := isChatCompletionsPath(path); got != want {
			t.Errorf("isChatCompletionsPath(%q) = %v, want %v", path, got, want)
		}
	}
}
