package proxy

import (
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

// TestServeHTTP_PassthroughPath tests requests that should passthrough
func TestServeHTTP_PassthroughPath(t *testing.T) {
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

	requestCounts = make([]int, 2)

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
			req := httptest.NewRequest("GET", "/api/test", nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
		}(i)
	}

	wg.Wait()
}

// TestServeHTTP_PassthroughXForwardedFor checks the passthrough path builds the client
// chain; the shared helper itself is covered in internal/xforwarded.
func TestServeHTTP_PassthroughXForwardedFor(t *testing.T) {
	tests := []struct {
		name   string
		header map[string]string
		want   string
	}{
		{"remote addr", nil, "1.2.3.4:5678"},
		{"extends existing chain", map[string]string{"X-Forwarded-For": "9.9.9.9"}, "9.9.9.9, 1.2.3.4:5678"},
		{"x-real-ip wins", map[string]string{"X-Real-IP": "5.5.5.5"}, "5.5.5.5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			for k, v := range tt.header {
				req.Header.Set(k, v)
			}
			handler.ServeHTTP(httptest.NewRecorder(), req)

			if receivedXFF != tt.want {
				t.Errorf("expected XFF %q, got %q", tt.want, receivedXFF)
			}
		})
	}
}
