package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"
)

// MockResponse represents a response from the mock upstream
type MockResponse struct {
	StatusCode int                 `json:"status_code"`
	Headers    map[string][]string `json:"headers"`
	Body       string              `json:"body"`
}

// NewEchoHandler creates a handler that echoes request info back
func NewEchoHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"method":      r.Method,
			"path":        r.URL.Path,
			"query":       r.URL.RawQuery,
			"headers":     r.Header,
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	}
}

// NewDelayHandler creates a handler that delays before responding
func NewDelayHandler(delay time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.Header().Set("X-Delay", delay.String())
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("delayed response"))
	}
}

// NewErrorHandler creates a handler that returns an error status
func NewErrorHandler(statusCode int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Error", "true")
		w.WriteHeader(statusCode)
		w.Write([]byte(http.StatusText(statusCode)))
	}
}

// NewCountingHandler creates a handler that tracks request count
func NewCountingHandler() (http.HandlerFunc, *atomic.Int64) {
	var count atomic.Int64
	return func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.Header().Set("X-Request-Count", "1")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}, &count
}

// NewSequentialHandler creates a handler that returns different responses
func NewSequentialHandler(responses []int) http.HandlerFunc {
	var index atomic.Int64
	return func(w http.ResponseWriter, r *http.Request) {
		idx := int(index.Add(1)) - 1
		if idx < len(responses) {
			w.WriteHeader(responses[idx])
			w.Write([]byte(http.StatusText(responses[idx])))
		} else {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		}
	}
}

// NewMockUpstream creates a test HTTP server with the given handler
func NewMockUpstream(handler http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(handler)
}

// NewMultipleMockUpstreams creates multiple test servers
func NewMultipleMockUpstreams(count int, handlerFactory func(int) http.HandlerFunc) []*httptest.Server {
	servers := make([]*httptest.Server, count)
	for i := 0; i < count; i++ {
		servers[i] = httptest.NewServer(handlerFactory(i))
	}
	return servers
}

// CloseUpstreams closes multiple test servers
func CloseUpstreams(servers []*httptest.Server) {
	for _, s := range servers {
		s.Close()
	}
}
