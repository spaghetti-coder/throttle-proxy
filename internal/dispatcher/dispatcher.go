// Package dispatcher handles request queuing and dispatching to upstream servers.
package dispatcher

import (
	"bytes"
	"context"
	"fmt"
	"io"
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

// hopByHopHeaders lists headers that must be removed when proxying requests,
// as per RFC 2616 Section 13.5.1. These headers are hop-by-hop headers and
// should not be forwarded between clients and origins across proxies.
//
// Categories of hop-by-hop headers:
//   - Connection management: Connection, Keep-Alive, Proxy-Connection
//   - Proxy authentication: Proxy-Authenticate, Proxy-Authorization
//   - Protocol upgrades: Upgrade, TE
//   - Transfer encoding: Transfer-Encoding, Trailer (note: Trailers header, not Trailer)
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

// HTTP status codes used by the dispatcher.
// Using named constants improves readability and maintainability.
const (
	// statusQueueFull is returned when the request queue is at capacity.
	statusQueueFull = http.StatusServiceUnavailable

	// statusShuttingDown is returned when the dispatcher is stopping
	// and cannot accept new requests.
	statusShuttingDown = http.StatusServiceUnavailable

	// statusMaxWaitExceeded is returned when a request has been waiting
	// in the queue longer than its configured max wait time.
	statusMaxWaitExceeded = http.StatusServiceUnavailable

	// statusAllUpstreamsFailed is returned when all upstream servers
	// fail to respond or return 5xx errors.
	statusAllUpstreamsFailed = http.StatusBadGateway
)

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
	running atomic.Bool
}

// New creates a new Dispatcher with the given configuration.
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

// Enqueue reads the request body and places the request into the dispatch queue.
// The returned channel receives exactly one Result when the request completes.
func (d *Dispatcher) Enqueue(r *http.Request) <-chan Result {
	var bodyBytes []byte
	if r.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(r.Body)
		_ = r.Body.Close()
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
		pr.resultChan <- Result{StatusCode: statusShuttingDown, Err: fmt.Errorf("dispatcher stopped")}
		return pr.resultChan
	}
	select {
	case d.queue <- pr:
	default:
		pr.resultChan <- Result{StatusCode: statusQueueFull, Err: fmt.Errorf("queue full")}
	}
	return pr.resultChan
}

// Run is the single dispatcher goroutine. It must be started exactly once.
func (d *Dispatcher) Run(ctx context.Context) {
	d.running.Store(true)
	defer func() {
		// Drain any remaining queued requests after shutdown.
		d.running.Store(false)
	}()
	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case pr := <-d.queue:
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

// dispatch implements Earliest Deadline First (EDF) scheduling for request dispatch.
//
// Algorithm overview:
//  1. For each upstream, get the next available timestamp (deadline)
//  2. Sort upstreams by deadline (earliest first)
//  3. Try each upstream in order: wait until available, then send request
//  4. If the request succeeds (status < 500), return the response
//  5. If all upstreams fail, return 502 Bad Gateway
//
// This ensures fair scheduling across all upstreams while respecting rate limits.
func (d *Dispatcher) dispatch(ctx context.Context, pr *proxyRequest) {
	select {
	case <-ctx.Done():
		pr.resultChan <- Result{StatusCode: statusShuttingDown, Err: fmt.Errorf("dispatcher shutting down")}
		return
	default:
	}

	// Check if request has exceeded its maximum wait time in the queue.
	// If maxWait is configured and exceeded, fail fast with 503.
	if pr.maxWait > 0 && time.Since(pr.enqueuedAt) >= pr.maxWait {
		pr.resultChan <- Result{StatusCode: statusMaxWaitExceeded, Err: fmt.Errorf("max wait exceeded")}
		return
	}

	// Build list of upstream candidates with their next available timestamps.
	// The EDF algorithm selects the upstream with the earliest deadline.
	type candidate struct {
		state *upstream.State
		ts    time.Time
	}
	candidates := make([]candidate, len(d.states))
	for i, s := range d.states {
		candidates[i] = candidate{s, s.NextMinTs()}
	}
	// Sort by deadline (earliest first) - this is the core EDF scheduling decision.
	sort.Slice(candidates, func(a, b int) bool {
		return candidates[a].ts.Before(candidates[b].ts)
	})

	// Try each upstream in EDF order, waiting as needed and failing over on errors.
	now := time.Now()
	for _, c := range candidates {
		// Wait until this upstream becomes available (respects rate limiting).
		// This timer enforces the delay between requests to avoid rate limits.
		if c.ts.After(now) {
			timer := time.NewTimer(c.ts.Sub(now))
			select {
			case <-ctx.Done():
				timer.Stop()
				pr.resultChan <- Result{StatusCode: statusShuttingDown, Err: ctx.Err()}
				return
			case <-timer.C:
			}
			timer.Stop()
		}

		// Send request to this upstream and update its timing state.
		res, err := d.fireRequest(ctx, pr, c.state)
		c.state.UpdateAfterRequest(time.Now(), d.rng)

		// Success: status < 500 means the upstream handled the request.
		// Return the result to complete dispatch for this request.
		if err == nil && res.StatusCode < 500 {
			pr.resultChan <- res
			return
		}

		// Failure: move to next candidate. Update time for subsequent wait calculations.
		now = time.Now()
	}

	// All upstreams failed - return 502 Bad Gateway as per HTTP specification.
	// This indicates the proxy cannot get a valid response from upstream.
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
