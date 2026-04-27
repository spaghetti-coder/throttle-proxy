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

// TestEnqueue tests request enqueueing
func TestEnqueue(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{
		Upstreams:       []*url.URL{u},
		UpstreamTimeout: 5 * time.Second,
		QueueSize:       1,
	}
	d := New(cfg)

	// Create test request
	body := []byte("test body")
	req := httptest.NewRequest("POST", "/test", bytes.NewReader(body))

	resultChan := d.Enqueue(req)

	if resultChan == nil {
		t.Error("expected non-nil result channel")
	}

	// Verify request was queued
	select {
	case <-resultChan:
		t.Error("expected channel to be empty initially")
	default:
		// Expected - channel should be empty
	}
}

// TestEnqueue_FullQueue tests that Enqueue returns 503 when the queue is at capacity
func TestEnqueue_FullQueue(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{
		Upstreams:       []*url.URL{u},
		UpstreamTimeout: 5 * time.Second,
		QueueSize:       1,
	}
	d := New(cfg)

	// Fill the queue without consuming it
	req1 := httptest.NewRequest("GET", "/test1", nil)
	_ = d.Enqueue(req1)

	// Since Run isn't started, a blocking Enqueue would deadlock.
	// An immediate 503 should be returned for the second request.
	req2 := httptest.NewRequest("GET", "/test2", nil)
	done := make(chan struct{})
	var result2 Result
	go func() {
		resultChan2 := d.Enqueue(req2)
		result2 = <-resultChan2
		close(done)
	}()

	select {
	case <-done:
		// Expected: Enqueue should have returned immediately with a 503.
		if result2.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected status %d for full queue, got %d", http.StatusServiceUnavailable, result2.StatusCode)
		}
		if result2.Err == nil || result2.Err.Error() != "queue full" {
			t.Fatalf("expected 'queue full' error, got %v", result2.Err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Enqueue blocked on full queue — deadlock detected")
	}
}

// TestRun_DrainsQueueOnShutdown verifies queued requests receive 503 during shutdown
func TestRun_DrainsQueuedRequestsOnShutdown(t *testing.T) {
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

	// Enqueue a request that will be picked up and block in fireRequest
	req1 := httptest.NewRequest("GET", "/test", nil)
	resultChan1 := d.Enqueue(req1)
	// Give Run time to start dispatching req1
	time.Sleep(50 * time.Millisecond)

	// Enqueue another request that should remain in the queue buffer
	req2 := httptest.NewRequest("GET", "/test", nil)
	resultChan2 := d.Enqueue(req2)

	// Cancel context: Run should drain the remaining queued request(s)
	cancel()

	// req1's dispatch will ultimately fail (context cancelled)
	select {
	case result := <-resultChan1:
		if result.Err == nil {
			t.Fatalf("expected error for req1 after cancel, got none")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("req1 did not complete after cancel")
	}

	// req2 must be drained with a 503 instead of blocking forever
	select {
	case result := <-resultChan2:
		if result.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 for drained queued request, got %d", result.StatusCode)
		}
		if result.Err == nil || result.Err.Error() != "dispatcher shutting down" {
			t.Fatalf("expected shutdown error, got %v", result.Err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued request was not drained on shutdown — deadlock")
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

	// Give Run time to start
	time.Sleep(10 * time.Millisecond)

	// Stop dispatcher
	cancel()

	// Give Run time to exit and close done channel
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

	// Create a request with a body that returns an error on read
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

// TestEnqueue_WithoutBody tests request without body
func TestEnqueue_WithoutBody(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{
		Upstreams:       []*url.URL{u},
		UpstreamTimeout: 5 * time.Second,
		QueueSize:       1,
	}
	d := New(cfg)

	req := httptest.NewRequest("GET", "/test", nil)

	resultChan := d.Enqueue(req)

	if resultChan == nil {
		t.Error("expected non-nil result channel")
	}
}

// TestCopyHeaders_HopByHopExcluded tests hop-by-hop headers are excluded
func TestCopyHeaders_HopByHopExcluded(t *testing.T) {
	src := http.Header{
		"Content-Type":       []string{"application/json"},
		"Connection":         []string{"close"},
		"Keep-Alive":         []string{"timeout=5"},
		"Proxy-Authenticate": []string{"Basic"},
		"X-Custom-Header":    []string{"value"},
	}

	dst := make(http.Header)
	copyHeaders(dst, src)

	// These should be present
	if dst.Get("Content-Type") != "application/json" {
		t.Error("expected Content-Type to be copied")
	}
	if dst.Get("X-Custom-Header") != "value" {
		t.Error("expected X-Custom-Header to be copied")
	}

	// These should be excluded
	if dst.Get("Connection") != "" {
		t.Error("expected Connection to be excluded")
	}
	if dst.Get("Keep-Alive") != "" {
		t.Error("expected Keep-Alive to be excluded")
	}
	if dst.Get("Proxy-Authenticate") != "" {
		t.Error("expected Proxy-Authenticate to be excluded")
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

	// Connection header itself should be excluded
	if dst.Get("Connection") != "" {
		t.Error("expected Connection header to be excluded")
	}

	// X-Custom should also be excluded since it's in Connection
	if dst.Get("X-Custom") != "" {
		t.Error("expected X-Custom to be excluded (listed in Connection)")
	}

	// Content-Type should be present
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

// TestSetXForwardedFor_AppendsExisting verifies X-Forwarded-For is extended
func TestSetXForwardedFor_AppendsExisting(t *testing.T) {
	src := httptest.NewRequest("GET", "/test", nil)
	src.Header.Set("X-Forwarded-For", "1.2.3.4")
	src.RemoteAddr = "5.6.7.8:1234"

	out := httptest.NewRequest("GET", "/test", nil)
	setXForwardedFor(out, src)

	if got := out.Header.Get("X-Forwarded-For"); got != "1.2.3.4, 5.6.7.8:1234" {
		t.Fatalf("expected XFF to append, got %q", got)
	}
}

// TestSetXForwardedFor_UsesRealIPWhenXFFMissing verifies fallback to X-Real-IP
func TestSetXForwardedFor_UsesRealIPWhenXFFMissing(t *testing.T) {
	src := httptest.NewRequest("GET", "/test", nil)
	src.Header.Set("X-Real-IP", "1.2.3.4")
	src.RemoteAddr = "5.6.7.8:1234"

	out := httptest.NewRequest("GET", "/test", nil)
	setXForwardedFor(out, src)

	if got := out.Header.Get("X-Forwarded-For"); got != "1.2.3.4" {
		t.Fatalf("expected XFF to use X-Real-IP, got %q", got)
	}
}

// TestSetXForwardedFor_UsesRemoteAddrFallback verifies fallback to RemoteAddr
func TestSetXForwardedFor_UsesRemoteAddrFallback(t *testing.T) {
	src := httptest.NewRequest("GET", "/test", nil)
	src.RemoteAddr = "5.6.7.8:1234"

	out := httptest.NewRequest("GET", "/test", nil)
	setXForwardedFor(out, src)

	if got := out.Header.Get("X-Forwarded-For"); got != "5.6.7.8:1234" {
		t.Fatalf("expected XFF to fall back to RemoteAddr, got %q", got)
	}
}

// TestFireRequest_Success tests successful request to upstream
func TestFireRequest_Success(t *testing.T) {
	// Create mock upstream server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
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
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	u, _ := url.Parse(upstream.URL)
	cfg := &config.Config{
		Upstreams:       []*url.URL{u},
		UpstreamTimeout: 5 * time.Second,
	}
	d := New(cfg)

	// Create proxy request
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Custom", "value")

	body, _ := io.ReadAll(req.Body)
	pr := &proxyRequest{
		r:          req,
		bodyBytes:  body,
		resultChan: make(chan Result, 1),
		enqueuedAt: time.Now(),
		maxWait:    0,
	}

	// Get state
	state := d.states[0]

	// Fire request
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
	// Create mock upstream server
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

	// Create proxy request with body
	req := httptest.NewRequest("POST", "/test", bytes.NewReader([]byte("test body")))

	pr := &proxyRequest{
		r:          req,
		bodyBytes:  []byte("test body"),
		resultChan: make(chan Result, 1),
		enqueuedAt: time.Now(),
		maxWait:    0,
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
	// Create mock upstream server that returns 500
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
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

	// Create request with short max wait
	req := httptest.NewRequest("GET", "/test", nil)
	pr := &proxyRequest{
		r:          req,
		resultChan: make(chan Result, 1),
		enqueuedAt: time.Now().Add(-3 * time.Second), // Enqueued 3 seconds ago
		maxWait:    1 * time.Second,                  // But max wait is 1 second
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

// TestDispatch_ContextCancellation tests context cancellation
func TestDispatch_ContextCancellation(t *testing.T) {
	// Create a test server that will block
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until context is cancelled
		<-r.Context().Done()
	}))
	defer upstream.Close()

	u, _ := url.Parse(upstream.URL)
	cfg := &config.Config{
		Upstreams:       []*url.URL{u},
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
	}

	// Create context that we'll cancel
	ctx, cancel := context.WithCancel(context.Background())

	// Start dispatch in goroutine
	done := make(chan bool)
	go func() {
		d.dispatch(ctx, pr)
		done <- true
	}()

	// Cancel context after short delay
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Wait for dispatch to complete
	<-done

	result := <-pr.resultChan
	// Context cancellation during request returns 502 from fireRequest
	if result.StatusCode != http.StatusBadGateway {
		t.Logf("Got status code %d with error: %v", result.StatusCode, result.Err)
	}
}
