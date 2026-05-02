// Package proxy provides HTTP proxy functionality for throttle-proxy.
package proxy

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"sync/atomic"

	"throttle-proxy/internal/config"
	"throttle-proxy/internal/dispatcher"
	"throttle-proxy/internal/xforwarded"
)

// Handler routes HTTP requests to either the dispatcher for throttled endpoints
// or directly to upstream servers for passthrough endpoints.
//
// The Handler maintains two request paths:
//   - Throttled: Requests matching configured endpoints are queued and processed
//     sequentially by the dispatcher to prevent upstream rate limiting.
//   - Passthrough: Non-matching requests are forwarded directly using round-robin
//     selection across upstream servers.
type Handler struct {
	// cfg is the proxy configuration including endpoints and upstream URLs.
	cfg *config.Config

	// disp is the request dispatcher that manages throttled request queues.
	// Used for endpoints that require rate limiting protection.
	disp *dispatcher.Dispatcher

	// passthroughs contains pre-configured reverse proxies for each upstream.
	// Used for direct forwarding of non-throttled requests.
	// Index 0 corresponds to cfg.Upstreams[0], etc.
	passthroughs []*httputil.ReverseProxy

	// rrCounter is an atomic counter for round-robin upstream selection.
	// Incremented for each passthrough request to distribute load evenly.
	rrCounter atomic.Int64
}

// NewHandler creates a new HTTP handler with the given configuration and dispatcher.
//
// Creates both throttled and passthrough proxies:
//   - Throttled proxy: Uses the dispatcher for rate-limited endpoints
//   - Passthrough proxies: Creates a reverse proxy for each upstream server
//     that forwards requests directly with X-Forwarded-For header support
//
// The passthrough proxies are pre-configured with URL rewriting and header
// forwarding logic to ensure proper request routing to upstream servers.
func NewHandler(cfg *config.Config, disp *dispatcher.Dispatcher) *Handler {
	passthroughs := make([]*httputil.ReverseProxy, len(cfg.Upstreams))
	for i, u := range cfg.Upstreams {
		target := u
		passthroughs[i] = &httputil.ReverseProxy{
			Rewrite: func(req *httputil.ProxyRequest) {
				req.SetURL(target)
				req.Out.Host = target.Host
				xforwarded.SetXForwardedFor(req.Out, req.In)
			},
		}
	}
	return &Handler{cfg: cfg, disp: disp, passthroughs: passthroughs}
}

// ServeHTTP implements http.Handler, routing requests to throttled or passthrough handling.
//
// Routing decision:
//   - If the request path matches any configured endpoint (checked via
//     config.MatchesEndpoints), the request is throttled via serveThrottled.
//   - Otherwise, the request is forwarded directly via servePassthrough.
//
// Throttled requests are queued and processed sequentially to prevent
// overwhelming upstream servers. Passthrough requests are forwarded immediately
// using round-robin load balancing across available upstreams.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if config.MatchesEndpoints(r.URL.Path, h.cfg.Endpoints) {
		h.serveThrottled(w, r)
	} else {
		h.servePassthrough(w, r)
	}
}

// serveThrottled handles requests to rate-limited endpoints.
//
// Full flow:
//  1. Enqueue request: Send the request to the dispatcher's queue. This blocks
//     until the dispatcher accepts the request (queue has space).
//  2. Wait for result: Block on the result channel until the dispatcher
//     completes the upstream request.
//  3. Handle errors: If the upstream request failed, write an error response
//     with the appropriate status code.
//  4. Write response: Copy headers from the upstream response, then write
//     the status code and body to the client.
//
// The dispatcher ensures sequential processing of throttled requests,
// preventing concurrent requests to rate-limited endpoints.
func (h *Handler) serveThrottled(w http.ResponseWriter, r *http.Request) {
	res := <-h.disp.Enqueue(r)

	if res.Err != nil {
		http.Error(w, res.Err.Error(), res.StatusCode)
		return
	}

	hdr := w.Header()
	for k, vs := range res.Header {
		for _, v := range vs {
			hdr.Add(k, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	if _, err := w.Write(res.Body); err != nil {
		slog.Warn("failed to write response body", "err", err)
	}
}

// servePassthrough handles requests to non-throttled endpoints.
//
// Performs round-robin selection of upstream servers for load balancing:
//  1. Atomically increment rrCounter to get the next sequence number.
//  2. Convert to 0-based index using modulo operation with the number of upstreams.
//     The -1 adjustment is needed because Add returns the new value (1, 2, 3...)
//     but we need 0-based indexing for the passthroughs slice.
//  3. Forward the request to the selected upstream via its reverse proxy.
//
// The atomic counter ensures thread-safe round-robin distribution across
// all upstream servers without needing explicit synchronization.
func (h *Handler) servePassthrough(w http.ResponseWriter, r *http.Request) {
	// Atomic increment returns the new value (starting from 0, so first call returns 1).
	// Subtract 1 to get 0-based index, then modulo to cycle through upstreams.
	idx := int(h.rrCounter.Add(1)-1) % len(h.passthroughs)
	h.passthroughs[idx].ServeHTTP(w, r)
}
