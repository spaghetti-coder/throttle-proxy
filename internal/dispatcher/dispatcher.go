package dispatcher

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"throttle-proxy/internal/config"
	"throttle-proxy/internal/upstream"
)

var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"TE",
	"Trailers",
	"Transfer-Encoding",
	"Upgrade",
}

// Result holds the proxied response to be forwarded to the client.
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
}

// Dispatcher serializes requests to upstreams using Earliest Deadline First scheduling.
type Dispatcher struct {
	cfg     *config.Config
	states  []*upstream.State
	queue   chan *proxyRequest
	client  *http.Client
	rng     *rand.Rand
	done    chan struct{}
	running atomic.Bool
}

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

	queueSize := cfg.QueueSize
	if queueSize < 1 {
		queueSize = 1
	}

	return &Dispatcher{
		cfg:    cfg,
		states: states,
		queue:  make(chan *proxyRequest, queueSize),
		client: client,
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
		done:   make(chan struct{}),
	}
}

// Enqueue reads the request body and places the request into the dispatch queue.
// The returned channel receives exactly one Result when the request completes.
func (d *Dispatcher) Enqueue(r *http.Request) <-chan Result {
	var bodyBytes []byte
	if r.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
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
	}
	if !d.running.Load() {
		pr.resultChan <- Result{StatusCode: http.StatusServiceUnavailable, Err: fmt.Errorf("dispatcher stopped")}
		return pr.resultChan
	}
	select {
	case d.queue <- pr:
	default:
		pr.resultChan <- Result{StatusCode: http.StatusServiceUnavailable, Err: fmt.Errorf("queue full")}
	}
	return pr.resultChan
}

// Run is the single dispatcher goroutine. It must be started exactly once.
func (d *Dispatcher) Run(ctx context.Context) {
	d.running.Store(true)
	defer func() {
		// Drain any remaining queued requests after shutdown.
		d.running.Store(false)
		close(d.done)
	}()
	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case pr := <-d.queue:
					pr.resultChan <- Result{StatusCode: http.StatusServiceUnavailable, Err: fmt.Errorf("dispatcher shutting down")}
				default:
					return
				}
			}
		case pr := <-d.queue:
			d.dispatch(ctx, pr)
		}
	}
}

func (d *Dispatcher) dispatch(ctx context.Context, pr *proxyRequest) {
	select {
	case <-ctx.Done():
		pr.resultChan <- Result{StatusCode: http.StatusServiceUnavailable, Err: fmt.Errorf("dispatcher shutting down")}
		return
	default:
	}

	if pr.maxWait > 0 && time.Since(pr.enqueuedAt) >= pr.maxWait {
		pr.resultChan <- Result{StatusCode: http.StatusServiceUnavailable, Err: fmt.Errorf("max wait exceeded")}
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
				pr.resultChan <- Result{StatusCode: http.StatusServiceUnavailable, Err: ctx.Err()}
				return
			case <-timer.C:
			}
			timer.Stop()
		}

		res, err := d.fireRequest(ctx, pr, c.state)
		c.state.UpdateAfterRequest(time.Now(), d.rng)

		if err == nil && res.StatusCode < 500 {
			pr.resultChan <- res
			return
		}

		now = time.Now()
	}

	pr.resultChan <- Result{StatusCode: http.StatusBadGateway, Err: fmt.Errorf("all upstreams failed")}
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
	setXForwardedFor(outReq, pr.r)

	resp, err := d.client.Do(outReq)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, err
	}

	h := make(http.Header)
	copyHeaders(h, resp.Header)
	h.Set("Content-Length", fmt.Sprintf("%d", len(respBody)))

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

func setXForwardedFor(outReq *http.Request, inReq *http.Request) {
	clientIP := inReq.RemoteAddr
	if xri := inReq.Header.Get("X-Real-IP"); xri != "" {
		clientIP = xri
	}

	if xff := inReq.Header.Get("X-Forwarded-For"); xff != "" {
		outReq.Header.Set("X-Forwarded-For", xff+", "+clientIP)
	} else {
		outReq.Header.Set("X-Forwarded-For", clientIP)
	}
}
