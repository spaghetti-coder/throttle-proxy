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

// Handler routes configured endpoints through the dispatcher and forwards every
// other request straight to an upstream.
type Handler struct {
	cfg  *config.Config
	disp *dispatcher.Dispatcher

	// passthroughs holds one reverse proxy per upstream, in cfg.Upstreams order.
	passthroughs []*httputil.ReverseProxy

	rrCounter atomic.Int64
}

// NewHandler returns a Handler for cfg. Throttled endpoints require disp.Run to be
// running; without it they get 503.
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

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if config.MatchesEndpoints(r.URL.Path, h.cfg.Endpoints) {
		h.serveThrottled(w, r)
	} else {
		h.servePassthrough(w, r)
	}
}

// serveThrottled blocks until the dispatcher reports a result, then relays it.
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

// servePassthrough forwards the request to the next upstream, round-robin.
func (h *Handler) servePassthrough(w http.ResponseWriter, r *http.Request) {
	// Add returns the post-increment value, hence -1 for a 0-based index.
	idx := int(h.rrCounter.Add(1)-1) % len(h.passthroughs)
	h.passthroughs[idx].ServeHTTP(w, r)
}
