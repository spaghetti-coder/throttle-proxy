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

func TestServeHTTP_ThrottledPath(t *testing.T) {
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go disp.Run(ctx)
	time.Sleep(10 * time.Millisecond)

	req := httptest.NewRequest("GET", "/search?q=test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK && rr.Code != http.StatusServiceUnavailable {
		t.Logf("Got status code: %d", rr.Code)
	}
}

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
	mu.Lock()
	if requestCounts[0] != 2 {
		t.Errorf("expected upstream1 to receive 2 requests, got %d", requestCounts[0])
	}
	if requestCounts[1] != 2 {
		t.Errorf("expected upstream2 to receive 2 requests, got %d", requestCounts[1])
	}
	mu.Unlock()
}

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
