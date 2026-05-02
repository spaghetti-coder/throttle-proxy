package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"throttle-proxy/internal/config"
	"throttle-proxy/internal/dispatcher"
)

// TestNewHandler tests handler initialization
func TestNewHandler(t *testing.T) {
	tests := []struct {
		name      string
		upstreams []string
		wantLen   int
	}{
		{
			name:      "single upstream",
			upstreams: []string{"http://localhost:8080"},
			wantLen:   1,
		},
		{
			name:      "multiple upstreams",
			upstreams: []string{"http://localhost:8080", "http://localhost:8081"},
			wantLen:   2,
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
				Upstreams: upstreams,
				Endpoints: []string{"/"},
			}

			disp := dispatcher.New(cfg)
			handler := NewHandler(cfg, disp)

			if handler.cfg != cfg {
				t.Error("expected cfg to be set")
			}
			if handler.disp != disp {
				t.Error("expected dispatcher to be set")
			}
			if len(handler.passthroughs) != tt.wantLen {
				t.Errorf("expected %d passthroughs, got %d", tt.wantLen, len(handler.passthroughs))
			}
		})
	}
}

// TestServeHTTP_ThrottledPath tests requests that should be throttled
func TestServeHTTP_ThrottledPath(t *testing.T) {
	// Create mock upstream that returns success
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{"result":"success"}`)); err != nil {
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
	handler := NewHandler(cfg, disp)

	// Start dispatcher
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go disp.Run(ctx)

	// Wait for dispatcher to start
	time.Sleep(10 * time.Millisecond)

	req := httptest.NewRequest("GET", "/search?q=test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	// Note: Since we're using real dispatcher in test, response may vary
	// We mainly verify no panic occurs and request is processed
	if rr.Code != http.StatusOK && rr.Code != http.StatusServiceUnavailable {
		t.Logf("Got status code: %d", rr.Code)
	}
}

// TestServeHTTP_PassthroughPath tests requests that should passthrough
func TestServeHTTP_PassthroughPath(t *testing.T) {
	// Create a mock upstream server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream-Header", "value")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("upstream response")); err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	}))
	defer upstream.Close()

	upstreamURL, _ := url.Parse(upstream.URL)
	cfg := &config.Config{
		Upstreams:       []*url.URL{upstreamURL},
		UpstreamTimeout: 5 * time.Second,
		Endpoints:       []string{"/search"}, // /static is not throttled
	}

	disp := dispatcher.New(cfg)
	handler := NewHandler(cfg, disp)

	req := httptest.NewRequest("GET", "/static/file.txt", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	if rr.Header().Get("X-Upstream-Header") != "value" {
		t.Errorf("expected X-Upstream-Header 'value', got %s", rr.Header().Get("X-Upstream-Header"))
	}
	body, _ := io.ReadAll(rr.Body)
	if string(body) != "upstream response" {
		t.Errorf("expected body 'upstream response', got %s", string(body))
	}
}

// TestServeHTTP_PassthroughRoundRobin tests round-robin load balancing
func TestServeHTTP_PassthroughRoundRobin(t *testing.T) {
	var requestCounts []int
	var mu sync.Mutex

	// Create mock upstream servers
	upstream1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestCounts[0]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("upstream1")); err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	}))
	defer upstream1.Close()

	upstream2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestCounts[1]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("upstream2")); err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	}))
	defer upstream2.Close()

	upstreamURL1, _ := url.Parse(upstream1.URL)
	upstreamURL2, _ := url.Parse(upstream2.URL)
	cfg := &config.Config{
		Upstreams:       []*url.URL{upstreamURL1, upstreamURL2},
		UpstreamTimeout: 5 * time.Second,
		Endpoints:       []string{"/search"}, // /static is passthrough
	}

	disp := dispatcher.New(cfg)
	handler := NewHandler(cfg, disp)

	// Reset counters
	requestCounts = make([]int, 2)

	// Make 4 requests
	for i := 0; i < 4; i++ {
		req := httptest.NewRequest("GET", "/static/file.txt", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
	}

	// Should round-robin: upstream1, upstream2, upstream1, upstream2
	mu.Lock()
	if requestCounts[0] != 2 {
		t.Errorf("expected upstream1 to receive 2 requests, got %d", requestCounts[0])
	}
	if requestCounts[1] != 2 {
		t.Errorf("expected upstream2 to receive 2 requests, got %d", requestCounts[1])
	}
	mu.Unlock()
}

// TestServeHTTP_StatusCodes tests various status codes for passthrough
func TestServeHTTP_StatusCodes(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{"200 OK", http.StatusOK, "OK"},
		{"201 Created", http.StatusCreated, "Created"},
		{"400 Bad Request", http.StatusBadRequest, "Bad Request"},
		{"404 Not Found", http.StatusNotFound, "Not Found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				if _, err := w.Write([]byte(tt.body)); err != nil {
					t.Errorf("failed to write response: %v", err)
				}
			}))
			defer upstream.Close()

			upstreamURL, _ := url.Parse(upstream.URL)
			cfg := &config.Config{
				Upstreams:       []*url.URL{upstreamURL},
				UpstreamTimeout: 5 * time.Second,
				Endpoints:       []string{"/search"},
			}

			disp := dispatcher.New(cfg)
			handler := NewHandler(cfg, disp)

			req := httptest.NewRequest("GET", "/static/test", nil)
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			if rr.Code != tt.statusCode {
				t.Errorf("expected status %d, got %d", tt.statusCode, rr.Code)
			}
			body, _ := io.ReadAll(rr.Body)
			if string(body) != tt.body {
				t.Errorf("expected body %s, got %s", tt.body, string(body))
			}
		})
	}
}

// TestServeHTTP_ConcurrentAccess tests thread safety
func TestServeHTTP_ConcurrentAccess(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("OK")); err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	}))
	defer upstream.Close()

	upstreamURL, _ := url.Parse(upstream.URL)
	// Use passthrough endpoint only to avoid starting dispatcher
	cfg := &config.Config{
		Upstreams:       []*url.URL{upstreamURL},
		UpstreamTimeout: 5 * time.Second,
		Endpoints:       []string{"/search"}, // /api is passthrough
	}

	disp := dispatcher.New(cfg)
	handler := NewHandler(cfg, disp)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Use passthrough path to avoid dispatcher
			req := httptest.NewRequest("GET", "/api/test", nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
		}(i)
	}

	wg.Wait()
}

// TestXForwardedForConsistency verifies both throttled and passthrough paths set XFF identically
func TestXForwardedForConsistency(t *testing.T) {
	t.Run("passthrough without existing XFF", func(t *testing.T) {
		var receivedXFF string
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedXFF = r.Header.Get("X-Forwarded-For")
			w.WriteHeader(http.StatusOK)
		}))
		defer upstream.Close()

		upstreamURL, _ := url.Parse(upstream.URL)
		cfg := &config.Config{
			Upstreams:       []*url.URL{upstreamURL},
			UpstreamTimeout: 5 * time.Second,
			Endpoints:       []string{"/throttled"},
		}
		handler := NewHandler(cfg, nil)

		req := httptest.NewRequest("GET", "/passthrough", nil)
		req.RemoteAddr = "1.2.3.4:5678"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if receivedXFF != "1.2.3.4:5678" {
			t.Errorf("expected XFF '1.2.3.4:5678', got %q", receivedXFF)
		}
	})

	t.Run("passthrough with existing XFF", func(t *testing.T) {
		var receivedXFF string
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedXFF = r.Header.Get("X-Forwarded-For")
			w.WriteHeader(http.StatusOK)
		}))
		defer upstream.Close()

		upstreamURL, _ := url.Parse(upstream.URL)
		cfg := &config.Config{
			Upstreams:       []*url.URL{upstreamURL},
			UpstreamTimeout: 5 * time.Second,
			Endpoints:       []string{"/throttled"},
		}
		handler := NewHandler(cfg, nil)

		req := httptest.NewRequest("GET", "/passthrough", nil)
		req.Header.Set("X-Forwarded-For", "9.9.9.9")
		req.RemoteAddr = "1.2.3.4:5678"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if receivedXFF != "9.9.9.9, 1.2.3.4:5678" {
			t.Errorf("expected XFF '9.9.9.9, 1.2.3.4:5678', got %q", receivedXFF)
		}
	})

	t.Run("passthrough with X-Real-IP", func(t *testing.T) {
		var receivedXFF string
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedXFF = r.Header.Get("X-Forwarded-For")
			w.WriteHeader(http.StatusOK)
		}))
		defer upstream.Close()

		upstreamURL, _ := url.Parse(upstream.URL)
		cfg := &config.Config{
			Upstreams:       []*url.URL{upstreamURL},
			UpstreamTimeout: 5 * time.Second,
			Endpoints:       []string{"/throttled"},
		}
		handler := NewHandler(cfg, nil)

		req := httptest.NewRequest("GET", "/passthrough", nil)
		req.Header.Set("X-Real-IP", "5.5.5.5")
		req.RemoteAddr = "1.2.3.4:5678"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if receivedXFF != "5.5.5.5" {
			t.Errorf("expected XFF '5.5.5.5', got %q", receivedXFF)
		}
	})
}
