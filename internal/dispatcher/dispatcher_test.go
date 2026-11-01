package dispatcher

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"throttle-proxy/internal/config"
)

// TestNew tests dispatcher initialization
func TestNew(t *testing.T) {
	tests := []struct {
		name       string
		upstreams  []string
		wantStates int
	}{
		{
			name:       "single upstream",
			upstreams:  []string{"http://localhost:8080"},
			wantStates: 1,
		},
		{
			name:       "multiple upstreams",
			upstreams:  []string{"http://localhost:8080", "http://localhost:8081"},
			wantStates: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstreams := make([]*url.URL, len(tt.upstreams))
			for i, u := range tt.upstreams {
				parsed, _ := url.Parse(u)
				upstreams[i] = parsed
			}

			cfg := &config.Config{
				Upstreams:       upstreams,
				UpstreamTimeout: 5 * time.Second,
			}

			d := New(cfg)

			if len(d.states) != tt.wantStates {
				t.Errorf("expected %d states, got %d", tt.wantStates, len(d.states))
			}
			if d.queue == nil {
				t.Error("expected queue to be initialized")
			}
			if d.client == nil {
				t.Error("expected client to be initialized")
			}
		})
	}
}

// TestEnqueue_FullQueue tests that Enqueue returns 503 when the queue is at capacity
func TestEnqueue_FullQueue(t *testing.T) {
	// Upstream blocks until cancel so Run cannot drain the queue.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer upstream.Close()

	u, _ := url.Parse(upstream.URL)
	cfg := &config.Config{
		Upstreams:       []*url.URL{u},
		UpstreamTimeout: 5 * time.Second,
		QueueSize:       1,
	}
	d := New(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	go d.Run(ctx)
	defer cancel()

	time.Sleep(10 * time.Millisecond)

	// Fill the queue: req1 will be picked up by Run and block in fireRequest
	req1 := httptest.NewRequest("GET", "/test1", nil)
	_ = d.Enqueue(req1)

	req2 := httptest.NewRequest("GET", "/test2", nil)
	_ = d.Enqueue(req2)

	time.Sleep(50 * time.Millisecond)

	// req3 should trigger queue full — immediate 503
	req3 := httptest.NewRequest("GET", "/test3", nil)
	done := make(chan struct{})
	var result3 Result
	go func() {
		resultChan3 := d.Enqueue(req3)
		result3 = <-resultChan3
		close(done)
	}()

	select {
	case <-done:
		if result3.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected status %d for full queue, got %d", http.StatusServiceUnavailable, result3.StatusCode)
		}
		if result3.Err == nil || result3.Err.Error() != "queue full" {
			t.Fatalf("expected 'queue full' error, got %v", result3.Err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Enqueue blocked on full queue — deadlock detected")
	}
}

// TestRun_ResolvesQueuedRequestsOnShutdown pins the shutdown contract: every request
// already queued when the context is cancelled still gets a result instead of blocking
// forever. Requests may be answered by dispatch or by the shutdown drain, either way an
// error result is required.
func TestRun_ResolvesQueuedRequestsOnShutdown(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer upstream.Close()

	u, _ := url.Parse(upstream.URL)
	cfg := &config.Config{
		Upstreams:       []*url.URL{u},
		UpstreamTimeout: 30 * time.Second,
		QueueSize:       10,
	}
	d := New(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	go d.Run(ctx)

	// The first request parks Run inside fireRequest; the rest queue up behind it.
	const queued = 8
	chans := []<-chan Result{d.Enqueue(httptest.NewRequest("GET", "/test", nil))}
	time.Sleep(50 * time.Millisecond)
	for i := 0; i < queued; i++ {
		chans = append(chans, d.Enqueue(httptest.NewRequest("GET", "/test", nil)))
	}

	cancel()

	for i, ch := range chans {
		select {
		case res := <-ch:
			if res.Err == nil {
				t.Errorf("request %d after shutdown: expected error, got status %d", i, res.StatusCode)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("request %d never resolved after shutdown", i)
		}
	}
}

// TestEnqueue_AfterStop verifies Enqueue returns 503 after dispatcher has stopped
func TestEnqueue_AfterStop(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{
		Upstreams:       []*url.URL{u},
		UpstreamTimeout: 5 * time.Second,
		QueueSize:       10,
	}
	d := New(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	go d.Run(ctx)

	time.Sleep(10 * time.Millisecond)

	cancel()

	time.Sleep(50 * time.Millisecond)

	// Enqueue after stop — must not block
	req := httptest.NewRequest("GET", "/test", nil)
	done := make(chan struct{})
	var result Result
	go func() {
		ch := d.Enqueue(req)
		result = <-ch
		close(done)
	}()

	select {
	case <-done:
		if result.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected status 503 after stop, got %d", result.StatusCode)
		}
		if result.Err == nil || result.Err.Error() != "dispatcher stopped" {
			t.Fatalf("expected 'dispatcher stopped' error, got %v", result.Err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Enqueue blocked after dispatcher stopped — deadlock")
	}
}

// TestEnqueue_ReadBodyError verifies Enqueue returns 400 when body read fails
func TestEnqueue_ReadBodyError(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{
		Upstreams:       []*url.URL{u},
		UpstreamTimeout: 5 * time.Second,
		QueueSize:       10,
	}
	d := New(cfg)

	req := httptest.NewRequest("POST", "/test", &errorReader{err: fmt.Errorf("read failed")})

	resultChan := d.Enqueue(req)
	result := <-resultChan

	if result.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status 400 when body read fails, got %d", result.StatusCode)
	}
	if result.Err == nil || result.Err.Error() != "read failed" {
		t.Fatalf("expected body read error, got %v", result.Err)
	}
}

// errorReader is an io.Reader that always returns an error
type errorReader struct {
	err error
}

func (e *errorReader) Read(p []byte) (n int, err error) {
	return 0, e.err
}

// TestCopyHeaders_HopByHopExcluded tests hop-by-hop headers are excluded
func TestCopyHeaders_HopByHopExcluded(t *testing.T) {
	src := http.Header{
		"Content-Type":       []string{"application/json"},
		"Connection":         []string{"close"},
		"Keep-Alive":         []string{"timeout=5"},
		"Proxy-Authenticate": []string{"Basic"},
		"Proxy-Connection":   []string{"keep-alive"},
		"X-Custom-Header":    []string{"value"},
	}

	dst := make(http.Header)
	copyHeaders(dst, src)

	if dst.Get("Content-Type") != "application/json" {
		t.Error("expected Content-Type to be copied")
	}
	if dst.Get("X-Custom-Header") != "value" {
		t.Error("expected X-Custom-Header to be copied")
	}

	if dst.Get("Connection") != "" {
		t.Error("expected Connection to be excluded")
	}
	if dst.Get("Keep-Alive") != "" {
		t.Error("expected Keep-Alive to be excluded")
	}
	if dst.Get("Proxy-Authenticate") != "" {
		t.Error("expected Proxy-Authenticate to be excluded")
	}
	if dst.Get("Proxy-Connection") != "" {
		t.Error("expected Proxy-Connection to be excluded")
	}
}

// TestCopyHeaders_ConnectionHeaderValues tests Connection header values exclusion
func TestCopyHeaders_ConnectionHeaderValues(t *testing.T) {
	src := http.Header{
		"Content-Type": []string{"application/json"},
		"X-Custom":     []string{"value"},
		"Connection":   []string{"X-Custom, close"},
	}

	dst := make(http.Header)
	copyHeaders(dst, src)

	if dst.Get("Connection") != "" {
		t.Error("expected Connection header to be excluded")
	}

	// X-Custom should also be excluded since it's in Connection
	if dst.Get("X-Custom") != "" {
		t.Error("expected X-Custom to be excluded (listed in Connection)")
	}

	if dst.Get("Content-Type") != "application/json" {
		t.Error("expected Content-Type to be copied")
	}
}

// TestCopyHeaders_MultipleValues tests multiple header values preservation
func TestCopyHeaders_MultipleValues(t *testing.T) {
	src := http.Header{
		"X-Values": []string{"value1", "value2", "value3"},
	}

	dst := make(http.Header)
	copyHeaders(dst, src)

	values := dst["X-Values"]
	if len(values) != 3 {
		t.Errorf("expected 3 values, got %d", len(values))
	}
	for i, v := range []string{"value1", "value2", "value3"} {
		if values[i] != v {
			t.Errorf("expected value[%d] = %q, got %q", i, v, values[i])
		}
	}
}

// TestFireRequest_Success tests successful request to upstream
func TestFireRequest_Success(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected method GET, got %s", r.Method)
		}
		if r.URL.Path != "/test" {
			t.Errorf("expected path /test, got %s", r.URL.Path)
		}
		if r.Header.Get("X-Custom") != "value" {
			t.Errorf("expected X-Custom header, got %s", r.Header.Get("X-Custom"))
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{"status":"ok"}`)); err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	}))
	defer upstream.Close()

	u, _ := url.Parse(upstream.URL)
	cfg := &config.Config{
		Upstreams:       []*url.URL{u},
		UpstreamTimeout: 5 * time.Second,
	}
	d := New(cfg)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Custom", "value")

	body, _ := io.ReadAll(req.Body)
	pr := &proxyRequest{
		r:          req,
		bodyBytes:  body,
		resultChan: make(chan Result, 1),
		enqueuedAt: time.Now(),
		maxWait:    0,
		ctx:        context.Background(),
	}

	state := d.states[0]

	ctx := context.Background()
	result, err := d.fireRequest(ctx, pr, state)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StatusCode != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, result.StatusCode)
	}
	if string(result.Body) != `{"status":"ok"}` {
		t.Errorf("expected body %q, got %q", `{"status":"ok"}`, string(result.Body))
	}
	if result.Header.Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", result.Header.Get("Content-Type"))
	}
}

// TestFireRequest_WithBody tests request body forwarding
func TestFireRequest_WithBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != "test body" {
			t.Errorf("expected body 'test body', got %q", string(body))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	u, _ := url.Parse(upstream.URL)
	cfg := &config.Config{
		Upstreams:       []*url.URL{u},
		UpstreamTimeout: 5 * time.Second,
	}
	d := New(cfg)

	req := httptest.NewRequest("POST", "/test", bytes.NewReader([]byte("test body")))

	pr := &proxyRequest{
		r:          req,
		bodyBytes:  []byte("test body"),
		resultChan: make(chan Result, 1),
		enqueuedAt: time.Now(),
		maxWait:    0,
		ctx:        context.Background(),
	}

	state := d.states[0]
	ctx := context.Background()
	_, err := d.fireRequest(ctx, pr, state)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestFireRequest_UpstreamError tests handling of upstream errors
func TestFireRequest_UpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		if _, err := w.Write([]byte("server error")); err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	}))
	defer upstream.Close()

	u, _ := url.Parse(upstream.URL)
	cfg := &config.Config{
		Upstreams:       []*url.URL{u},
		UpstreamTimeout: 5 * time.Second,
	}
	d := New(cfg)

	req := httptest.NewRequest("GET", "/test", nil)
	pr := &proxyRequest{
		r:          req,
		resultChan: make(chan Result, 1),
		enqueuedAt: time.Now(),
		maxWait:    0,
		ctx:        context.Background(),
	}

	state := d.states[0]
	ctx := context.Background()
	result, err := d.fireRequest(ctx, pr, state)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 500 is still returned as a result, not an error
	if result.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", http.StatusInternalServerError, result.StatusCode)
	}
}

// TestDispatch_MaxWaitTimeout tests max wait timeout handling
func TestDispatch_MaxWaitTimeout(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{
		Upstreams:       []*url.URL{u},
		UpstreamTimeout: 5 * time.Second,
		DelayMin:        1 * time.Second,
		DelayMax:        2 * time.Second,
	}
	d := New(cfg)

	req := httptest.NewRequest("GET", "/test", nil)
	pr := &proxyRequest{
		r:          req,
		resultChan: make(chan Result, 1),
		enqueuedAt: time.Now().Add(-3 * time.Second),
		maxWait:    1 * time.Second,
		ctx:        context.Background(),
	}

	ctx := context.Background()
	d.dispatch(ctx, pr)

	result := <-pr.resultChan
	if result.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected status %d for max wait exceeded, got %d", http.StatusServiceUnavailable, result.StatusCode)
	}
	if result.Err == nil || result.Err.Error() != "max wait exceeded" {
		t.Errorf("expected 'max wait exceeded' error, got %v", result.Err)
	}
}

// TestResult_ContentLength tests that Content-Length is not set for chunked responses
func TestResult_ContentLength(t *testing.T) {
	upChunked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Transfer-Encoding", "chunked")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("OK")); err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	}))
	defer upChunked.Close()

	upPlain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("OK")); err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	}))
	defer upPlain.Close()

	u1, _ := url.Parse(upChunked.URL)
	u2, _ := url.Parse(upPlain.URL)

	t.Run("chunked", func(t *testing.T) {
		cfg := &config.Config{
			Upstreams:       []*url.URL{u1},
			UpstreamTimeout: 5 * time.Second,
			DelayMin:        0,
			DelayMax:        0,
		}
		d := New(cfg)
		req := httptest.NewRequest("GET", "/test", nil)
		pr := &proxyRequest{
			r:          req,
			resultChan: make(chan Result, 1),
			enqueuedAt: time.Now(),
			maxWait:    0,
			ctx:        context.Background(),
		}
		ctx := context.Background()
		d.dispatch(ctx, pr)
		res := <-pr.resultChan
		if cl := res.Header.Get("Content-Length"); cl != "" {
			t.Errorf("expected no Content-Length for chunked response, got %q", cl)
		}
	})

	t.Run("plain", func(t *testing.T) {
		cfg := &config.Config{
			Upstreams:       []*url.URL{u2},
			UpstreamTimeout: 5 * time.Second,
			DelayMin:        0,
			DelayMax:        0,
		}
		d := New(cfg)
		req := httptest.NewRequest("GET", "/test", nil)
		pr := &proxyRequest{
			r:          req,
			resultChan: make(chan Result, 1),
			enqueuedAt: time.Now(),
			maxWait:    0,
			ctx:        context.Background(),
		}
		ctx := context.Background()
		d.dispatch(ctx, pr)
		res := <-pr.resultChan
		if cl := res.Header.Get("Content-Length"); cl != "2" {
			t.Errorf("expected Content-Length 2, got %q", cl)
		}
	})
}

func TestDispatch_ClientDisconnectDuringWait(t *testing.T) {
	blockDone := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blockDone
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	u, _ := url.Parse(upstream.URL)
	cfg := &config.Config{
		Upstreams:       []*url.URL{u},
		UpstreamTimeout: 5 * time.Second,
		DelayMin:        5 * time.Second,
		DelayMax:        10 * time.Second,
	}
	d := New(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	time.Sleep(10 * time.Millisecond)

	firstReq := httptest.NewRequest("GET", "/test", nil)
	firstResult := d.Enqueue(firstReq)

	time.Sleep(50 * time.Millisecond)

	reqCtx, reqCancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/test", nil)
	req = req.WithContext(reqCtx)

	resultChan := d.Enqueue(req)

	time.Sleep(50 * time.Millisecond)
	reqCancel()

	close(blockDone)

	select {
	case <-firstResult:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not complete")
	}

	select {
	case result := <-resultChan:
		if result.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("expected status 503 for client disconnect, got %d", result.StatusCode)
		}
		if result.Err == nil {
			t.Error("expected error for client disconnect")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch did not complete after client disconnect")
	}
}
