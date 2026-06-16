// Package dispatcher queues requests and dispatches them to upstream servers.
package dispatcher

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"throttle-proxy/internal/config"
	"throttle-proxy/internal/upstream"
	"throttle-proxy/internal/xforwarded"
)

var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Proxy-Connection",
	"TE",
	"Trailers",
	"Transfer-Encoding",
	"Upgrade",
}

const (
	statusQueueFull          = http.StatusServiceUnavailable
	statusShuttingDown       = http.StatusServiceUnavailable
	statusMaxWaitExceeded    = http.StatusServiceUnavailable
	statusAllUpstreamsFailed = http.StatusBadGateway
)

// Result holds an upstream response to forward to the client.
type Result struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	Err        error
}

type proxyRequest struct {
	r          *http.Request
	bodyBytes  []byte
	resultChan chan Result
	enqueuedAt time.Time
	maxWait    time.Duration
	ctx        context.Context
}

// Dispatcher serializes requests to upstreams using earliest-deadline-first scheduling.
type Dispatcher struct {
	cfg     *config.Config
	states  []*upstream.State
	queue   chan *proxyRequest
	client  *http.Client
	rng     *rand.Rand
	running atomic.Bool
}

// New returns a Dispatcher configured with cfg.
func New(cfg *config.Config) *Dispatcher {
	states := make([]*upstream.State, len(cfg.Upstreams))
	for i, u := range cfg.Upstreams {
		states[i] = upstream.NewState(u, cfg)
	}

	client := &http.Client{
		Timeout: cfg.UpstreamTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return &Dispatcher{
		cfg:    cfg,
		states: states,
		queue:  make(chan *proxyRequest, cfg.QueueSize),
		client: client,
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Enqueue reads the request body and adds the request to the dispatch queue.
// The returned channel receives exactly one Result when the request completes.
func (d *Dispatcher) Enqueue(r *http.Request) <-chan Result {
	var bodyBytes []byte
	if r.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			slog.Warn("read body failed", "method", r.Method, "path", r.URL.Path, "client", r.RemoteAddr, "err", err)
			ch := make(chan Result, 1)
			ch <- Result{StatusCode: http.StatusBadRequest, Err: err}
			return ch
		}
	}

	pr := &proxyRequest{
		r:          r,
		bodyBytes:  bodyBytes,
		resultChan: make(chan Result, 1),
		enqueuedAt: time.Now(),
		maxWait:    d.cfg.MaxWait,
		ctx:        r.Context(),
	}
	if pr.ctx.Err() != nil {
		slog.Warn("client disconnected", "method", r.Method, "path", r.URL.Path, "client", r.RemoteAddr)
		pr.resultChan <- Result{StatusCode: http.StatusServiceUnavailable, Err: fmt.Errorf("client disconnected")}
		return pr.resultChan
	}
	if !d.running.Load() {
		pr.resultChan <- Result{StatusCode: statusShuttingDown, Err: fmt.Errorf("dispatcher stopped")}
		return pr.resultChan
	}
	select {
	case d.queue <- pr:
	default:
		slog.Warn("queue full", "method", pr.r.Method, "path", pr.r.URL.Path, "client", pr.r.RemoteAddr)
		pr.resultChan <- Result{StatusCode: statusQueueFull, Err: fmt.Errorf("queue full")}
	}
	return pr.resultChan
}

// Run processes queued requests until ctx is cancelled.
// Start exactly one goroutine running this method.
func (d *Dispatcher) Run(ctx context.Context) {
	d.running.Store(true)
	defer d.running.Store(false)

	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case pr := <-d.queue:
					slog.Warn("dispatcher shutting down, request dropped", "method", pr.r.Method, "path", pr.r.URL.Path, "client", pr.r.RemoteAddr)
					pr.resultChan <- Result{StatusCode: statusShuttingDown, Err: fmt.Errorf("dispatcher shutting down")}
				default:
					return
				}
			}
		case pr := <-d.queue:
			d.dispatch(ctx, pr)
		}
	}
}

// dispatch forwards pr to the earliest available upstream, retrying on 5xx/timeouts.
func (d *Dispatcher) dispatch(ctx context.Context, pr *proxyRequest) {
	select {
	case <-ctx.Done():
		slog.Warn("dispatcher shutting down", "method", pr.r.Method, "path", pr.r.URL.Path, "client", pr.r.RemoteAddr)
		pr.resultChan <- Result{StatusCode: statusShuttingDown, Err: fmt.Errorf("dispatcher shutting down")}
		return
	default:
	}

	if pr.maxWait > 0 && time.Since(pr.enqueuedAt) >= pr.maxWait {
		slog.Warn("max wait exceeded", "method", pr.r.Method, "path", pr.r.URL.Path, "client", pr.r.RemoteAddr, "max_wait", pr.maxWait)
		pr.resultChan <- Result{StatusCode: statusMaxWaitExceeded, Err: fmt.Errorf("max wait exceeded")}
		return
	}

	type candidate struct {
		state *upstream.State
		ts    time.Time
	}
	candidates := make([]candidate, len(d.states))
	for i, s := range d.states {
		candidates[i] = candidate{s, s.NextMinTs()}
	}
	sort.Slice(candidates, func(a, b int) bool {
		return candidates[a].ts.Before(candidates[b].ts)
	})

	now := time.Now()
	for _, c := range candidates {
		if c.ts.After(now) {
			timer := time.NewTimer(c.ts.Sub(now))
			select {
			case <-ctx.Done():
				timer.Stop()
				slog.Warn("dispatcher shutting down", "method", pr.r.Method, "path", pr.r.URL.Path, "client", pr.r.RemoteAddr)
				pr.resultChan <- Result{StatusCode: statusShuttingDown, Err: ctx.Err()}
				return
			case <-pr.ctx.Done():
				timer.Stop()
				slog.Warn("client disconnected", "method", pr.r.Method, "path", pr.r.URL.Path, "client", pr.r.RemoteAddr)
				pr.resultChan <- Result{StatusCode: http.StatusServiceUnavailable, Err: fmt.Errorf("client disconnected")}
				return
			case <-timer.C:
			}
			timer.Stop()
		}

		if pr.ctx.Err() != nil {
			slog.Warn("client disconnected", "method", pr.r.Method, "path", pr.r.URL.Path, "client", pr.r.RemoteAddr)
			pr.resultChan <- Result{StatusCode: http.StatusServiceUnavailable, Err: fmt.Errorf("client disconnected")}
			return
		}

		res, err := d.fireRequest(ctx, pr, c.state)
		c.state.UpdateAfterRequest(time.Now(), d.rng)

		if err != nil {
			slog.Warn("upstream request failed", "method", pr.r.Method, "path", pr.r.URL.Path, "client", pr.r.RemoteAddr, "upstream", c.state.URL.String(), "err", err)
			now = time.Now()
			continue
		}
		if res.StatusCode >= 500 {
			slog.Warn("upstream error", "method", pr.r.Method, "path", pr.r.URL.Path, "client", pr.r.RemoteAddr, "upstream", c.state.URL.String(), "status", res.StatusCode)
			now = time.Now()
			continue
		}
		if res.StatusCode == http.StatusTooManyRequests || res.StatusCode == http.StatusForbidden {
			slog.Warn("upstream rate limited or forbidden", "method", pr.r.Method, "path", pr.r.URL.Path, "client", pr.r.RemoteAddr, "upstream", c.state.URL.String(), "status", res.StatusCode)
		}
		pr.resultChan <- res
		return
	}

	slog.Error("all upstreams failed", "method", pr.r.Method, "path", pr.r.URL.Path, "client", pr.r.RemoteAddr)
	pr.resultChan <- Result{StatusCode: statusAllUpstreamsFailed, Err: fmt.Errorf("all upstreams failed")}
}

func (d *Dispatcher) fireRequest(ctx context.Context, pr *proxyRequest, state *upstream.State) (Result, error) {
	targetURL := *state.URL
	targetURL.Path = pr.r.URL.Path
	targetURL.RawPath = pr.r.URL.RawPath
	targetURL.RawQuery = pr.r.URL.RawQuery
	targetURL.Fragment = pr.r.URL.Fragment

	var body io.Reader
	if len(pr.bodyBytes) > 0 {
		body = bytes.NewReader(pr.bodyBytes)
	}

	outReq, err := http.NewRequestWithContext(ctx, pr.r.Method, targetURL.String(), body)
	if err != nil {
		return Result{}, err
	}
	outReq.Host = state.URL.Host

	copyHeaders(outReq.Header, pr.r.Header)
	xforwarded.SetXForwardedFor(outReq, pr.r)

	resp, err := d.client.Do(outReq)
	if err != nil {
		return Result{}, err
	}

	respBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return Result{}, err
	}

	h := make(http.Header)
	copyHeaders(h, resp.Header)
	if len(resp.TransferEncoding) == 0 {
		h.Set("Content-Length", strconv.Itoa(len(respBody)))
	}

	return Result{StatusCode: resp.StatusCode, Header: h, Body: respBody}, nil
}

func copyHeaders(dst, src http.Header) {
	skip := make(map[string]bool, len(hopByHopHeaders))
	for _, h := range hopByHopHeaders {
		skip[strings.ToLower(h)] = true
	}
	for _, v := range src["Connection"] {
		for _, name := range strings.Split(v, ",") {
			skip[strings.ToLower(strings.TrimSpace(name))] = true
		}
	}

	for k, vs := range src {
		if skip[strings.ToLower(k)] {
			continue
		}
		dst[k] = append(dst[k], vs...)
	}
}
