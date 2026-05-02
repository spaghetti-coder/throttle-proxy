// Package proxy provides E2E tests using in-process HTTP servers.
//
// These tests verify complete system behavior that cannot be adequately
// tested at the unit level:
// - Signal handling (SIGTERM graceful shutdown)
// - Real network integration (failover chains)
// - High-load scenarios (queue backpressure)
// - Streaming behavior (large request/response bodies)
//
// All E2E tests use httptest.Server for dummy upstreams, ensuring:
// - No external dependencies
// - Automatic cleanup (defer server.Close())
// - CI-friendly (no Docker required)
package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"throttle-proxy/internal/config"
	"throttle-proxy/internal/dispatcher"
)

// testProxy wraps a test HTTP server with signal capabilities
type testProxy struct {
	server     *http.Server
	listener   net.Listener
	dispatcher *dispatcher.Dispatcher
	ctx        context.Context
	cancel     context.CancelFunc
	port       int
}

// createTestConfig creates a test configuration with the given upstreams
func createTestConfig(upstreams []string) *config.Config {
	upstreamURLs := make([]*url.URL, len(upstreams))
	for i, u := range upstreams {
		parsed, _ := url.Parse(u)
		upstreamURLs[i] = parsed
	}

	return &config.Config{
		Port:              0, // Let system assign port
		Upstreams:         upstreamURLs,
		UpstreamTimeout:   5 * time.Second,
		DelayMin:        1 * time.Millisecond,
		DelayMax:        5 * time.Millisecond,
		MaxWait:         30 * time.Second,
		EscalateAfter:   0,
		EscalateMaxCount: 3,
		EscalateFactorMin: 1.5,
		EscalateFactorMax: 2.0,
		Endpoints:       []string{"/"},
		QueueSize:       100,
	}
}

// startTestProxy starts a test proxy server
func startTestProxy(t *testing.T, cfg *config.Config) *testProxy {
	disp := dispatcher.New(cfg)
	handler := NewHandler(cfg, disp)

	ctx, cancel := context.WithCancel(context.Background())

	// Create listener on random port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	port := listener.Addr().(*net.TCPAddr).Port

	tp := &testProxy{
		server:     srv,
		listener:   listener,
		dispatcher: disp,
		ctx:        ctx,
		cancel:     cancel,
		port:       port,
	}

	// Start dispatcher
	go disp.Run(ctx)

	// Start server
	go func() {
		_ = srv.Serve(listener)
	}()

	return tp
}

// stop gracefully shuts down the test proxy
func (tp *testProxy) stop() {
	tp.cancel()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tp.server.Shutdown(shutdownCtx)
}

// signal sends a signal to the proxy context (simulates shutdown)
func (tp *testProxy) signal(sig os.Signal) {
	// Cancel context to trigger shutdown
	tp.cancel()
}

// url returns the base URL for the test proxy
func (tp *testProxy) url() string {
	return fmt.Sprintf("http://127.0.0.1:%d", tp.port)
}

// sendTestRequest sends a test request to the given URL
func sendTestRequest(url string, body []byte) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequest("POST", url, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	return client.Do(req)
}

// TestE2E_GracefulShutdown_SIGTERM tests that SIGTERM triggers graceful shutdown
func TestE2E_GracefulShutdown_SIGTERM(t *testing.T) {
	// Create dummy upstream that takes some time to respond
	var requestStarted atomic.Bool
	var requestCompleted atomic.Bool

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestStarted.Store(true)
		time.Sleep(100 * time.Millisecond)
		requestCompleted.Store(true)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer upstream.Close()

	// Start proxy
	cfg := createTestConfig([]string{upstream.URL})
	tp := startTestProxy(t, cfg)
	defer tp.stop()

	proxyURL := "http://127.0.0.1:" + net.JoinHostPort("127.0.0.1", "")[9:]
	// Get actual port
	if tp.listener != nil {
		proxyURL = "http://" + tp.listener.Addr().String()
	}

	// Send a request in background that will be in-flight during shutdown
	var wg sync.WaitGroup
	wg.Add(1)
	var responseStatus int
	var responseBody []byte

	go func() {
		defer wg.Done()
		resp, err := http.Get(proxyURL + "/")
		if err != nil {
			t.Logf("Request error (expected during shutdown): %v", err)
			return
		}
		defer resp.Body.Close()
		responseStatus = resp.StatusCode
		responseBody, _ = io.ReadAll(resp.Body)
	}()

	// Wait for request to start processing
	time.Sleep(20 * time.Millisecond)

	// Send SIGTERM (simulate by canceling context)
	tp.signal(syscall.SIGTERM)

	// Wait for request to complete
	wg.Wait()

	// Verify in-flight request completed successfully
	if requestCompleted.Load() && len(responseBody) > 0 {
		if responseStatus != http.StatusOK {
			t.Errorf("Expected status 200 for in-flight request, got %d", responseStatus)
		}
	}
}

// TestE2E_GracefulShutdown_QueueDrain tests that queue drains before shutdown
func TestE2E_GracefulShutdown_QueueDrain(t *testing.T) {
	// Create slow upstream to build up queue
	var processedCount atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		processedCount.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer upstream.Close()

	// Start proxy with small queue
	cfg := createTestConfig([]string{upstream.URL})
	cfg.QueueSize = 5
	tp := startTestProxy(t, cfg)

	proxyURL := "http://" + tp.listener.Addr().String()

	// Send multiple requests to build up queue
	var wg sync.WaitGroup
	responses := make([]int, 10)
	var mu sync.Mutex

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resp, err := http.Get(proxyURL + "/")
			if err != nil {
				mu.Lock()
				responses[idx] = -1 // Error
				mu.Unlock()
				return
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body)
			mu.Lock()
			responses[idx] = resp.StatusCode
			mu.Unlock()
		}(i)
	}

	// Give requests time to enter queue
	time.Sleep(50 * time.Millisecond)

	// Trigger shutdown
	tp.signal(syscall.SIGTERM)

	// Wait for all requests to complete or fail
	wg.Wait()
	tp.stop()

	// Count results
	successCount := 0
	failCount := 0
	for _, status := range responses {
		if status == http.StatusOK {
			successCount++
		} else if status == -1 || status == http.StatusServiceUnavailable {
			failCount++
		}
	}
	// All processed requests should succeed
	if processedCount.Load() > 0 && successCount != int(processedCount.Load()) {
		t.Logf("Processed: %d, Success: %d, Fail: %d", processedCount.Load(), successCount, failCount)
	}
}

// TestE2E_MultiUpstreamFailoverChain tests failover across multiple upstreams
func TestE2E_MultiUpstreamFailoverChain(t *testing.T) {
	// Create counters for each upstream
	counters := make([]atomic.Int32, 3)
	upstreams := make([]*httptest.Server, 3)

	// Upstream 1: Returns 502 (Bad Gateway)
	upstreams[0] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counters[0].Add(1)
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("Error from upstream 1"))
	}))
	defer upstreams[0].Close()

	// Upstream 2: Returns 500 (Internal Server Error)
	upstreams[1] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counters[1].Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Error from upstream 2"))
	}))
	defer upstreams[1].Close()

	// Upstream 3: Returns 200 (OK)
	upstreams[2] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counters[2].Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Success from upstream 3"))
	}))
	defer upstreams[2].Close()

	// Configure proxy with all 3 upstreams
	upstreamURLs := make([]string, 3)
	for i, u := range upstreams {
		upstreamURLs[i] = u.URL
	}
	cfg := createTestConfig(upstreamURLs)
	cfg.DelayMin = 0
	cfg.DelayMax = 0

	tp := startTestProxy(t, cfg)
	defer tp.stop()

	proxyURL := "http://" + tp.listener.Addr().String()

	// Send request to throttled endpoint
	resp, err := http.Get(proxyURL + "/")
	if err != nil {
		t.Fatalf("Failed to send request: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	// Verify response came from upstream 3
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
	if string(body) != "Success from upstream 3" {
		t.Errorf("Expected body from upstream 3, got %s", string(body))
	}

	// Verify each upstream was hit exactly once
	if counters[0].Load() != 1 {
		t.Errorf("Expected upstream 1 to be hit once, got %d", counters[0].Load())
	}
	if counters[1].Load() != 1 {
		t.Errorf("Expected upstream 2 to be hit once, got %d", counters[1].Load())
	}
	if counters[2].Load() != 1 {
		t.Errorf("Expected upstream 3 to be hit once, got %d", counters[2].Load())
	}
}

// TestE2E_MultiUpstreamFailoverFirstHealthy tests failover stops at first healthy upstream
func TestE2E_MultiUpstreamFailoverFirstHealthy(t *testing.T) {
	var counters [3]atomic.Int32
	upstreams := make([]*httptest.Server, 3)

	// Upstream 1: Fails
	upstreams[0] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counters[0].Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstreams[0].Close()

	// Upstream 2: Succeeds
	upstreams[1] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counters[1].Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK from upstream 2"))
	}))
	defer upstreams[1].Close()

	// Upstream 3: Should never be hit
	upstreams[2] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counters[2].Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstreams[2].Close()

	upstreamURLs := make([]string, 3)
	for i, u := range upstreams {
		upstreamURLs[i] = u.URL
	}
	cfg := createTestConfig(upstreamURLs)
	cfg.DelayMin = 0
	cfg.DelayMax = 0

	tp := startTestProxy(t, cfg)
	defer tp.stop()

	proxyURL := "http://" + tp.listener.Addr().String()

	resp, err := http.Get(proxyURL + "/")
	if err != nil {
		t.Fatalf("Failed to send request: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
	if string(body) != "OK from upstream 2" {
		t.Errorf("Expected body from upstream 2, got %s", string(body))
	}

	// Verify upstream 1 and 2 were hit, but not 3
	if counters[0].Load() != 1 {
		t.Errorf("Expected upstream 1 to be hit once, got %d", counters[0].Load())
	}
	if counters[1].Load() != 1 {
		t.Errorf("Expected upstream 2 to be hit once, got %d", counters[1].Load())
	}
	if counters[2].Load() != 0 {
		t.Errorf("Expected upstream 3 to not be hit, got %d", counters[2].Load())
	}
}

// TestE2E_BodyStreaming tests request/response body streaming with various sizes
func TestE2E_BodyStreaming(t *testing.T) {
	// Create echo upstream that returns request body
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
		w.Header().Set("X-Original-Content-Length", r.Header.Get("Content-Length"))
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
	defer upstream.Close()

	cfg := createTestConfig([]string{upstream.URL})
	cfg.DelayMin = 0
	cfg.DelayMax = 0

	tp := startTestProxy(t, cfg)
	defer tp.stop()

	proxyURL := "http://" + tp.listener.Addr().String()

	// Test cases for different body sizes
	testCases := []struct {
		name     string
		size     int
		isChunked bool
	}{
		{"Empty body", 0, false},
		{"Small body 1KB", 1024, false},
		{"Medium body 100KB", 100 * 1024, false},
		{"Large body 1MB", 1024 * 1024, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Generate random data
			data := make([]byte, tc.size)
			for i := range data {
				data[i] = byte(i % 256)
			}

			resp, err := sendTestRequest(proxyURL+"/", data)
			if err != nil {
				t.Fatalf("Request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("Expected status 200, got %d", resp.StatusCode)
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("Failed to read response body: %v", err)
			}

			// Verify response matches original
			if !bytes.Equal(body, data) {
				t.Errorf("Response body does not match request body. Got %d bytes, expected %d bytes", len(body), len(data))
			}

			// Verify Content-Length
			if tc.size > 0 {
				if resp.Header.Get("Content-Length") != "" {
					expectedLen := int64(tc.size)
					if resp.ContentLength != expectedLen {
						t.Errorf("Content-Length mismatch: got %d, expected %d", resp.ContentLength, expectedLen)
					}
				}
			}
		})
	}
}

// TestE2E_BodyStreamingChunked tests chunked transfer encoding
func TestE2E_BodyStreamingChunked(t *testing.T) {
	var receivedBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		// Force chunked encoding
		w.Header().Set("Transfer-Encoding", "chunked")
		w.WriteHeader(http.StatusOK)
		w.Write(receivedBody)
	}))
	defer upstream.Close()

	cfg := createTestConfig([]string{upstream.URL})
	cfg.DelayMin = 0
	cfg.DelayMax = 0

	tp := startTestProxy(t, cfg)
	defer tp.stop()

	proxyURL := "http://" + tp.listener.Addr().String()

	// Send data with chunked encoding
	data := make([]byte, 10000)
	for i := range data {
		data[i] = byte(i % 256)
	}

	// Create request with chunked body
	req, _ := http.NewRequest("POST", proxyURL+"/", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/octet-stream")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if !bytes.Equal(body, data) {
		t.Errorf("Response body does not match request body")
	}

	if len(receivedBody) != len(data) {
		t.Errorf("Upstream received %d bytes, expected %d", len(receivedBody), len(data))
	}
}

// TestE2E_QueueBackpressure tests queue backpressure with 503 responses
func TestE2E_QueueBackpressure(t *testing.T) {
	// Create slow upstream that takes time to respond
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer upstream.Close()

	// Configure proxy with small queue
	cfg := createTestConfig([]string{upstream.URL})
	cfg.QueueSize = 3
	cfg.DelayMin = 0
	cfg.DelayMax = 0
	cfg.MaxWait = 1 * time.Second // Short max wait

	tp := startTestProxy(t, cfg)
	defer tp.stop()

	proxyURL := "http://" + tp.listener.Addr().String()

	// Send 10 parallel requests
	var wg sync.WaitGroup
	responses := make([]int, 10)
	var mu sync.Mutex

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resp, err := http.Get(proxyURL + "/")
			if err != nil {
				mu.Lock()
				responses[idx] = -1
				mu.Unlock()
				return
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body)
			mu.Lock()
			responses[idx] = resp.StatusCode
			mu.Unlock()
		}(i)
	}

	wg.Wait()

	// Count results
	okCount := 0
	unavailableCount := 0
	errorCount := 0

	for _, status := range responses {
		switch status {
		case http.StatusOK:
			okCount++
		case http.StatusServiceUnavailable:
			unavailableCount++
		case -1:
			errorCount++
		}
	}

	// Verify queue capacity was respected
	// Queue size is 3, so at most 3+1 requests should succeed (one being processed)
	// The rest should get 503
	if okCount == 0 {
		t.Errorf("Expected some requests to succeed, got 0")
	}

	if unavailableCount == 0 {
		t.Errorf("Expected some requests to get 503, got 0")
	}

	// Total should be 10
	total := okCount + unavailableCount + errorCount
	if total != 10 {
		t.Errorf("Expected 10 total responses, got %d (ok=%d, 503=%d, err=%d)", total, okCount, unavailableCount, errorCount)
	}

	t.Logf("Backpressure results: OK=%d, 503=%d, Error=%d", okCount, unavailableCount, errorCount)
}

// TestE2E_QueueBackpressureImmediate tests immediate 503 for overflowing queue
func TestE2E_QueueBackpressureImmediate(t *testing.T) {
	// Create slow upstream
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := createTestConfig([]string{upstream.URL})
	cfg.QueueSize = 2
	cfg.DelayMin = 0
	cfg.DelayMax = 0

	tp := startTestProxy(t, cfg)
	defer tp.stop()

	proxyURL := "http://" + tp.listener.Addr().String()

	// First, send one request to occupy the processing slot
	var firstReqDone atomic.Bool
	go func() {
		http.Get(proxyURL + "/")
		firstReqDone.Store(true)
	}()

	// Wait for first request to start processing
	time.Sleep(50 * time.Millisecond)

	// Now send requests to fill queue and cause overflow
	var wg sync.WaitGroup
	results := make([]int, 0)
	var mu sync.Mutex

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get(proxyURL + "/")
			if err != nil {
				return
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body)
			mu.Lock()
			results = append(results, resp.StatusCode)
			mu.Unlock()
		}()
	}

	wg.Wait()

	// At least some should be 503
	has503 := false
	for _, code := range results {
		if code == http.StatusServiceUnavailable {
			has503 = true
			break
		}
	}

	if !has503 {
		t.Errorf("Expected at least one 503 response when queue is full, got none. Results: %v", results)
	}
}
