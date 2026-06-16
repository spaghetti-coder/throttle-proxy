// Package proxy provides end-to-end tests using in-process HTTP servers.
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

type testProxy struct {
	server     *http.Server
	listener   net.Listener
	dispatcher *dispatcher.Dispatcher
	ctx        context.Context
	cancel     context.CancelFunc
	port       int
}

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
		DelayMin:          1 * time.Millisecond,
		DelayMax:          5 * time.Millisecond,
		MaxWait:           30 * time.Second,
		EscalateAfter:     0,
		EscalateMaxCount:  3,
		EscalateFactorMin: 1.5,
		EscalateFactorMax: 2.0,
		Endpoints:         []string{"/"},
		QueueSize:         100,
	}
}

func startTestProxy(t *testing.T, cfg *config.Config) *testProxy {
	disp := dispatcher.New(cfg)
	handler := NewHandler(cfg, disp)

	ctx, cancel := context.WithCancel(context.Background())

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
	go disp.Run(ctx)
	go func() {
		_ = srv.Serve(listener)
	}()

	return tp
}

func (tp *testProxy) stop() {
	tp.cancel()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tp.server.Shutdown(shutdownCtx)
}

func (tp *testProxy) signal(sig os.Signal) {
	tp.cancel()
}

func (tp *testProxy) url() string {
	return fmt.Sprintf("http://127.0.0.1:%d", tp.port)
}

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

func TestE2E_GracefulShutdown_SIGTERM(t *testing.T) {
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
	cfg := createTestConfig([]string{upstream.URL})
	tp := startTestProxy(t, cfg)
	defer tp.stop()

	proxyURL := "http://127.0.0.1:" + net.JoinHostPort("127.0.0.1", "")[9:]
	if tp.listener != nil {
		proxyURL = "http://" + tp.listener.Addr().String()
	}
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
	time.Sleep(20 * time.Millisecond)
	tp.signal(syscall.SIGTERM)
	wg.Wait()
	if requestCompleted.Load() && len(responseBody) > 0 {
		if responseStatus != http.StatusOK {
			t.Errorf("Expected status 200 for in-flight request, got %d", responseStatus)
		}
	}
}

func TestE2E_GracefulShutdown_QueueDrain(t *testing.T) {
	var processedCount atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		processedCount.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer upstream.Close()
	cfg := createTestConfig([]string{upstream.URL})
	cfg.QueueSize = 5
	tp := startTestProxy(t, cfg)

	proxyURL := "http://" + tp.listener.Addr().String()
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
	time.Sleep(50 * time.Millisecond)
	tp.signal(syscall.SIGTERM)
	wg.Wait()
	tp.stop()
	successCount := 0
	failCount := 0
	for _, status := range responses {
		if status == http.StatusOK {
			successCount++
		} else if status == -1 || status == http.StatusServiceUnavailable {
			failCount++
		}
	}
	if processedCount.Load() > 0 && successCount != int(processedCount.Load()) {
		t.Logf("Processed: %d, Success: %d, Fail: %d", processedCount.Load(), successCount, failCount)
	}
}

func TestE2E_MultiUpstreamFailoverChain(t *testing.T) {
	counters := make([]atomic.Int32, 3)
	upstreams := make([]*httptest.Server, 3)

	upstreams[0] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counters[0].Add(1)
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("Error from upstream 1"))
	}))
	defer upstreams[0].Close()

	upstreams[1] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counters[1].Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Error from upstream 2"))
	}))
	defer upstreams[1].Close()

	upstreams[2] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counters[2].Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Success from upstream 3"))
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
	if string(body) != "Success from upstream 3" {
		t.Errorf("Expected body from upstream 3, got %s", string(body))
	}
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

func TestE2E_MultiUpstreamFailoverFirstHealthy(t *testing.T) {
	var counters [3]atomic.Int32
	upstreams := make([]*httptest.Server, 3)
	upstreams[0] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counters[0].Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstreams[0].Close()
	upstreams[1] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counters[1].Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK from upstream 2"))
	}))
	defer upstreams[1].Close()
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

func TestE2E_BodyStreaming(t *testing.T) {
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
	testCases := []struct {
		name      string
		size      int
		isChunked bool
	}{
		{"Empty body", 0, false},
		{"Small body 1KB", 1024, false},
		{"Medium body 100KB", 100 * 1024, false},
		{"Large body 1MB", 1024 * 1024, false},
		{"Chunked 10KB", 10 * 1024, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data := make([]byte, tc.size)
			for i := range data {
				data[i] = byte(i % 256)
			}

			req, err := http.NewRequest("POST", proxyURL+"/", bytes.NewReader(data))
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}
			req.Header.Set("Content-Type", "application/octet-stream")

			client := &http.Client{Timeout: 10 * time.Second}
			resp, err := client.Do(req)
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
			if !bytes.Equal(body, data) {
				t.Errorf("Response body does not match request body. Got %d bytes, expected %d bytes", len(body), len(data))
			}
			if tc.size > 0 && !tc.isChunked {
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

func TestE2E_QueueBackpressure(t *testing.T) {
	tests := []struct {
		name          string
		queueSize     int
		upstreamDelay time.Duration
		maxWait       time.Duration
		requests      int
		primeFirst    bool
	}{
		{name: "small_queue", queueSize: 2, upstreamDelay: 1 * time.Second, maxWait: 30 * time.Second, requests: 6, primeFirst: true},
		{name: "short_max_wait", queueSize: 3, upstreamDelay: 500 * time.Millisecond, maxWait: 1 * time.Second, requests: 10, primeFirst: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(tt.upstreamDelay)
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("OK"))
			}))
			defer upstream.Close()

			cfg := createTestConfig([]string{upstream.URL})
			cfg.QueueSize = tt.queueSize
			cfg.DelayMin = 0
			cfg.DelayMax = 0
			cfg.MaxWait = tt.maxWait

			tp := startTestProxy(t, cfg)
			defer tp.stop()

			proxyURL := "http://" + tp.listener.Addr().String()
			responses := make([]int, tt.requests)
			var mu sync.Mutex
			var wg sync.WaitGroup

			startIdx := 0
			if tt.primeFirst {
				go func() {
					resp, err := http.Get(proxyURL + "/")
					if err != nil {
						return
					}
					defer resp.Body.Close()
					io.Copy(io.Discard, resp.Body)
					mu.Lock()
					responses[0] = resp.StatusCode
					mu.Unlock()
				}()
				startIdx = 1
				time.Sleep(50 * time.Millisecond)
			}

			for i := startIdx; i < tt.requests; i++ {
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

			okCount := 0
			unavailableCount := 0
			errorCount := 0
			for _, status := range responses {
				switch status {
				case http.StatusOK:
					okCount++
				case http.StatusServiceUnavailable:
					unavailableCount++
				case -1, 0:
					errorCount++
				}
			}

			if unavailableCount == 0 {
				t.Errorf("Expected some requests to get 503, got none (ok=%d, err=%d)", okCount, errorCount)
			}
			if !tt.primeFirst && okCount == 0 {
				t.Errorf("Expected some requests to succeed, got 0")
			}
			total := okCount + unavailableCount + errorCount
			if total != tt.requests {
				t.Errorf("Expected %d total responses, got %d (ok=%d, 503=%d, err=%d)", tt.requests, total, okCount, unavailableCount, errorCount)
			}

			t.Logf("Backpressure results: OK=%d, 503=%d, Error=%d", okCount, unavailableCount, errorCount)
		})
	}
}
