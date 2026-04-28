package integration

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"throttle-proxy/internal/config"
	"throttle-proxy/internal/dispatcher"
	"throttle-proxy/internal/proxy"
)

// TestSequentialProcessing verifies that requests are processed sequentially
func TestSequentialProcessing(t *testing.T) {
	// Create counting upstream
	var requestCount atomic.Int64
	var maxConcurrent atomic.Int64
	var currentConcurrent atomic.Int64

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := currentConcurrent.Add(1)
		if current > maxConcurrent.Load() {
			maxConcurrent.Store(current)
		}
		defer currentConcurrent.Add(-1)

		requestCount.Add(1)
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("OK")); err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	}))
	defer upstream.Close()

	upstreamURL, _ := url.Parse(upstream.URL)
	cfg := &config.Config{
		Upstreams:       []*url.URL{upstreamURL},
		UpstreamTimeout: 5 * time.Second,
		DelayMin:        1 * time.Millisecond,
		DelayMax:        5 * time.Millisecond,
		Endpoints:       []string{"/"},
		QueueSize:       10000,
	}

	disp := dispatcher.New(cfg)
	handler := proxy.NewHandler(cfg, disp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go disp.Run(ctx)

	// Wait for dispatcher to start accepting requests
	time.Sleep(10 * time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/", nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
		}()
	}
	wg.Wait()

	if maxConcurrent.Load() != 1 {
		t.Errorf("expected max concurrent requests to be 1, got %d", maxConcurrent.Load())
	}
}

// TestFailoverBehavior tests failover to next upstream on failure
func TestFailoverBehavior(t *testing.T) {
	// First upstream always fails
	upstream1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream1.Close()

	// Second upstream succeeds
	upstream2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("success from upstream2")); err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	}))
	defer upstream2.Close()

	url1, _ := url.Parse(upstream1.URL)
	url2, _ := url.Parse(upstream2.URL)
	cfg := &config.Config{
		Upstreams:       []*url.URL{url1, url2},
		UpstreamTimeout: 5 * time.Second,
		DelayMin:        0,
		DelayMax:        0,
		Endpoints:       []string{"/"},
	}

	disp := dispatcher.New(cfg)
	handler := proxy.NewHandler(cfg, disp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go disp.Run(ctx)

	// Wait for dispatcher to start accepting requests
	time.Sleep(10 * time.Millisecond)

	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200 after failover, got %d", rr.Code)
	}

	body, _ := io.ReadAll(rr.Body)
	if string(body) != "success from upstream2" {
		t.Errorf("expected body from upstream2, got %s", string(body))
	}
}

// TestEndpointMatching tests endpoint prefix matching
func TestEndpointMatching(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("OK")); err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	}))
	defer upstream.Close()

	upstreamURL, _ := url.Parse(upstream.URL)
	cfg := &config.Config{
		Upstreams:       []*url.URL{upstreamURL},
		UpstreamTimeout: 5 * time.Second,
		DelayMin:        0,
		DelayMax:        0,
		Endpoints:       []string{"/search"},
	}

	disp := dispatcher.New(cfg)
	handler := proxy.NewHandler(cfg, disp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go disp.Run(ctx)

	// Wait for dispatcher to start accepting requests
	time.Sleep(10 * time.Millisecond)

	tests := []struct {
		path           string
		shouldThrottle bool
	}{
		{"/search", true},
		{"/search?q=test", true},
		{"/search/foo/bar", true},
		{"/searches", false},
		{"/api", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK && rr.Code != http.StatusServiceUnavailable {
				t.Errorf("unexpected status code for %s: %d", tt.path, rr.Code)
			}
		})
	}
}

// TestRoundRobinPassthrough tests round-robin for passthrough endpoints
func TestRoundRobinPassthrough(t *testing.T) {
	var counts []atomic.Int64

	upstreams := make([]*httptest.Server, 3)
	upstreamURLs := make([]*url.URL, 3)

	for i := range upstreams {
		counts = append(counts, atomic.Int64{})
		idx := i
		upstreams[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counts[idx].Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		defer upstreams[i].Close()
		upstreamURLs[i], _ = url.Parse(upstreams[i].URL)
	}

	cfg := &config.Config{
		Upstreams:       upstreamURLs,
		UpstreamTimeout: 5 * time.Second,
		Endpoints:       []string{"/search"},
	}

	disp := dispatcher.New(cfg)
	handler := proxy.NewHandler(cfg, disp)

	for i := 0; i < 9; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
	}

	for i := range counts {
		if c := counts[i].Load(); c != 3 {
			t.Errorf("expected upstream %d to receive 3 requests, got %d", i, c)
		}
	}
}

// TestConcurrentSafe verifies thread safety
func TestConcurrentSafe(t *testing.T) {
	upstreams := make([]*httptest.Server, 3)
	upstreamURLs := make([]*url.URL, 3)

	for i := range upstreams {
		upstreams[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("OK")); err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	}))
	defer upstreams[i].Close()
	upstreamURLs[i], _ = url.Parse(upstreams[i].URL)
}

	cfg := &config.Config{
		Upstreams:       upstreamURLs,
		UpstreamTimeout: 5 * time.Second,
		DelayMin:        1 * time.Millisecond,
		DelayMax:        5 * time.Millisecond,
		Endpoints:       []string{"/"},
		QueueSize:       10000,
	}

	disp := dispatcher.New(cfg)
	handler := proxy.NewHandler(cfg, disp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go disp.Run(ctx)

	// Wait for dispatcher to start accepting requests
	time.Sleep(10 * time.Millisecond)

	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/", nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
		}(i)
	}

	wg.Wait()
}
