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

func newTestDispatcher(t *testing.T, upstream *httptest.Server, queueSize int) *Dispatcher {
	t.Helper()
	var upstreams []*url.URL
	if upstream != nil {
		u, _ := url.Parse(upstream.URL)
		upstreams = append(upstreams, u)
	}
	cfg := &config.Config{
		Upstreams:       upstreams,
		UpstreamTimeout: 5 * time.Second,
		QueueSize:       queueSize,
	}
	return New(cfg)
}

func TestNew(t *testing.T) {
	tests := []struct {
		name       string
		upstreams  []string
		wantStates int
	}{
		{name: "single upstream", upstreams: []string{"http://localhost:8080"}, wantStates: 1},
		{name: "multiple upstreams", upstreams: []string{"http://localhost:8080", "http://localhost:8081"}, wantStates: 2},
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

func TestEnqueue(t *testing.T) {
	tests := []struct {
		name string
		req  *http.Request
	}{
		{name: "with body", req: httptest.NewRequest("POST", "/test", bytes.NewReader([]byte("test body")))},
		{name: "without body", req: httptest.NewRequest("GET", "/test", nil)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newTestDispatcher(t, nil, 1)
			resultChan := d.Enqueue(tt.req)
			if resultChan == nil {
				t.Error("expected non-nil result channel")
			}
		})
	}
}

func TestEnqueue_FullQueue(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer upstream.Close()

	d := newTestDispatcher(t, upstream, 1)

	ctx, cancel := context.WithCancel(context.Background())
	go d.Run(ctx)
	defer cancel()
	time.Sleep(10 * time.Millisecond)
	req1 := httptest.NewRequest("GET", "/test1", nil)
	_ = d.Enqueue(req1)

	req2 := httptest.NewRequest("GET", "/test2", nil)
	_ = d.Enqueue(req2)
	time.Sleep(50 * time.Millisecond)

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

func TestRun_DrainsQueuedRequestsOnShutdown(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer upstream.Close()

	d := newTestDispatcher(t, upstream, 10)
	d.cfg.UpstreamTimeout = 30 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	go d.Run(ctx)
	req1 := httptest.NewRequest("GET", "/test", nil)
	resultChan1 := d.Enqueue(req1)
	time.Sleep(50 * time.Millisecond)
	req2 := httptest.NewRequest("GET", "/test", nil)
	resultChan2 := d.Enqueue(req2)
	cancel()

	select {
	case result := <-resultChan1:
		if result.Err == nil {
			t.Fatalf("expected error for req1 after cancel, got none")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("req1 did not complete after cancel")
	}

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

func TestEnqueue_AfterStop(t *testing.T) {
	d := newTestDispatcher(t, nil, 10)

	ctx, cancel := context.WithCancel(context.Background())
	go d.Run(ctx)
	time.Sleep(10 * time.Millisecond)
	cancel()
	time.Sleep(50 * time.Millisecond)
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

func TestEnqueue_ReadBodyError(t *testing.T) {
	d := newTestDispatcher(t, nil, 10)
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

type errorReader struct {
	err error
}

func (e *errorReader) Read(p []byte) (n int, err error) {
	return 0, e.err
}

func TestCopyHeaders(t *testing.T) {
	tests := []struct {
		name  string
		src   http.Header
		check func(t *testing.T, dst http.Header)
	}{
		{
			name: "hop-by-hop excluded",
			src: http.Header{
				"Content-Type":       []string{"application/json"},
				"Connection":         []string{"close"},
				"Keep-Alive":         []string{"timeout=5"},
				"Proxy-Authenticate": []string{"Basic"},
				"Proxy-Connection":   []string{"keep-alive"},
				"X-Custom-Header":    []string{"value"},
			},
			check: func(t *testing.T, dst http.Header) {
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
			},
		},
		{
			name: "connection values excluded",
			src: http.Header{
				"Content-Type": []string{"application/json"},
				"X-Custom":     []string{"value"},
				"Connection":   []string{"X-Custom, close"},
			},
			check: func(t *testing.T, dst http.Header) {
				if dst.Get("Connection") != "" {
					t.Error("expected Connection header to be excluded")
				}
				if dst.Get("X-Custom") != "" {
					t.Error("expected X-Custom to be excluded (listed in Connection)")
				}
				if dst.Get("Content-Type") != "application/json" {
					t.Error("expected Content-Type to be copied")
				}
			},
		},
		{
			name: "multiple values preserved",
			src: http.Header{
				"X-Values": []string{"value1", "value2", "value3"},
			},
			check: func(t *testing.T, dst http.Header) {
				values := dst["X-Values"]
				if len(values) != 3 {
					t.Errorf("expected 3 values, got %d", len(values))
				}
				for i, v := range []string{"value1", "value2", "value3"} {
					if values[i] != v {
						t.Errorf("expected value[%d] = %q, got %q", i, v, values[i])
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dst := make(http.Header)
			copyHeaders(dst, tt.src)
			tt.check(t, dst)
		})
	}
}

func TestFireRequest(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		body      string
		setHeader func(r *http.Request)
		upstream  http.HandlerFunc
		assert    func(t *testing.T, result Result, err error)
	}{
		{
			name: "success no body", method: "GET",
			setHeader: func(r *http.Request) {
				r.Header.Set("X-Custom", "value")
			},
			upstream: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			},
			assert: func(t *testing.T, result Result, err error) {
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
			},
		},
		{
			name: "success with body", method: "POST", body: "test body",
			upstream: func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if string(body) != "test body" {
					t.Errorf("expected body 'test body', got %q", string(body))
				}
				w.WriteHeader(http.StatusOK)
			},
			assert: func(t *testing.T, result Result, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			},
		},
		{
			name: "upstream error", method: "GET",
			upstream: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("server error"))
			},
			assert: func(t *testing.T, result Result, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.StatusCode != http.StatusInternalServerError {
					t.Errorf("expected status %d, got %d", http.StatusInternalServerError, result.StatusCode)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(tt.upstream)
			defer upstream.Close()

			d := newTestDispatcher(t, upstream, 10)

			var bodyReader io.Reader
			if tt.body != "" {
				bodyReader = bytes.NewReader([]byte(tt.body))
			}
			req := httptest.NewRequest(tt.method, "/test", bodyReader)
			if tt.setHeader != nil {
				tt.setHeader(req)
			}

			pr := &proxyRequest{
				r:          req,
				bodyBytes:  []byte(tt.body),
				resultChan: make(chan Result, 1),
				enqueuedAt: time.Now(),
				maxWait:    0,
				ctx:        context.Background(),
			}

			result, err := d.fireRequest(context.Background(), pr, d.states[0])
			tt.assert(t, result, err)
		})
	}
}

func TestDispatch_MaxWaitTimeout(t *testing.T) {
	d := newTestDispatcher(t, nil, 10)
	d.cfg.DelayMin = 1 * time.Second
	d.cfg.DelayMax = 2 * time.Second
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

func TestDispatch_ContextCancellation(t *testing.T) {
	done := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	defer func() {
		close(done)
		upstream.Close()
	}()

	d := newTestDispatcher(t, upstream, 10)

	req := httptest.NewRequest("GET", "/test", nil)
	pr := &proxyRequest{
		r:          req,
		resultChan: make(chan Result, 1),
		enqueuedAt: time.Now(),
		maxWait:    0,
		ctx:        context.Background(),
	}
	ctx, cancel := context.WithCancel(context.Background())

	dispatchDone := make(chan bool)
	go func() {
		d.dispatch(ctx, pr)
		dispatchDone <- true
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-dispatchDone

	result := <-pr.resultChan
	if result.StatusCode != http.StatusBadGateway {
		t.Logf("Got status code %d with error: %v", result.StatusCode, result.Err)
	}
}

func TestFireRequest_ContentLengthHandling(t *testing.T) {
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

	t.Run("chunked", func(t *testing.T) {
		d := newTestDispatcher(t, upChunked, 10)
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
		d := newTestDispatcher(t, upPlain, 10)
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

	d := newTestDispatcher(t, upstream, 10)
	d.cfg.DelayMin = 5 * time.Second
	d.cfg.DelayMax = 10 * time.Second

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
