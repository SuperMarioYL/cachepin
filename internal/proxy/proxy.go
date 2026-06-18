// Package proxy is CachePin's transparent, OpenAI-compatible reverse proxy
// (milestone m1). It forwards every request — including streaming
// /v1/chat/completions responses delivered over Server-Sent Events — to the
// upstream model server unchanged, so a coding-agent harness pointed at
// CachePin behaves exactly as if it were talking to the upstream directly.
//
// Later milestones observe traffic (m2 tracking) and rewrite mutated requests
// (m3 --pin reconciliation) by installing an Interceptor; when none is set the
// proxy is fully transparent and never buffers a request or response body.
package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

// Interceptor observes, and may rewrite, a buffered chat-completions request
// body before it is forwarded upstream. It receives the request path and the
// raw body bytes and returns the body to forward. Returning the input unchanged
// keeps the proxy transparent.
//
// Milestone m2 installs an interceptor that only reads the body (returning it
// untouched) to track the canonical session history; milestone m3 installs one
// that rewrites mutated requests back to append-only form. Milestone m1 leaves
// it nil, so request bodies stream straight through without being buffered.
type Interceptor func(path string, body []byte) ([]byte, error)

// Proxy is a single-upstream reverse proxy for an OpenAI-compatible server.
type Proxy struct {
	upstream *url.URL
	rp       *httputil.ReverseProxy

	// Intercept, when non-nil, is invoked with the buffered body of POSTs to
	// the chat-completions endpoint. It is the integration seam for the
	// tracking (m2) and pin (m3) milestones; leave nil for transparent
	// pass-through.
	Intercept Interceptor
}

// New builds a Proxy that forwards to upstream, which must be an absolute URL
// such as http://localhost:8080.
func New(upstream string) (*Proxy, error) {
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("proxy: parse upstream %q: %w", upstream, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("proxy: upstream must be an absolute URL like http://localhost:8080, got %q", upstream)
	}

	rp := httputil.NewSingleHostReverseProxy(u)
	// FlushInterval -1 flushes every write to the client immediately, so SSE
	// token streams are relayed chunk-by-chunk instead of being buffered until
	// the upstream response completes.
	rp.FlushInterval = -1

	// Preserve the default director (which joins the upstream and request
	// paths and sets scheme/host) but also rewrite the outbound Host header to
	// the upstream so servers that vhost on Host route correctly.
	director := rp.Director
	rp.Director = func(req *http.Request) {
		director(req)
		req.Host = u.Host
	}

	p := &Proxy{upstream: u, rp: rp}
	rp.ErrorHandler = p.errorHandler
	return p, nil
}

// ServeHTTP implements http.Handler. When an Interceptor is set and the request
// is a POST to the chat-completions endpoint, the body is buffered, passed
// through the interceptor, and the (possibly rewritten) body is forwarded; all
// other traffic streams through untouched.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.Intercept != nil && r.Method == http.MethodPost && isChatCompletionsPath(r.URL.Path) {
		body, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			p.errorHandler(w, r, fmt.Errorf("read request body: %w", err))
			return
		}

		out, err := p.Intercept(r.URL.Path, body)
		if err != nil {
			http.Error(w, "cachepin: intercept request: "+err.Error(), http.StatusInternalServerError)
			return
		}

		r.Body = io.NopCloser(bytes.NewReader(out))
		r.ContentLength = int64(len(out))
		r.Header.Set("Content-Length", strconv.Itoa(len(out)))
	}

	p.rp.ServeHTTP(w, r)
}

// errorHandler reports an unreachable or failing upstream as a 502 rather than
// letting the proxy hang or emit an empty response.
func (p *Proxy) errorHandler(w http.ResponseWriter, _ *http.Request, err error) {
	http.Error(w, fmt.Sprintf("cachepin: upstream %s error: %v", p.upstream, err), http.StatusBadGateway)
}

// isChatCompletionsPath reports whether path is the OpenAI chat-completions
// endpoint (e.g. /v1/chat/completions, or a vendor-prefixed variant).
func isChatCompletionsPath(path string) bool {
	return strings.HasSuffix(path, "/chat/completions")
}
