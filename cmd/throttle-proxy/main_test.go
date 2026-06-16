package main

import (
	"bytes"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func setupTestLogger(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	return logger, buf
}

func TestSignalHandling(t *testing.T) {
	cases := []struct {
		name     string
		signals  []os.Signal
		wantExit int
		wantLog  string
	}{
		{"sigterm", []os.Signal{syscall.SIGTERM}, 0, "shutting down"},
		{"sigint", []os.Signal{syscall.SIGINT}, 0, "shutting down"},
		{"multiple", []os.Signal{syscall.SIGTERM, syscall.SIGINT}, 0, "shutting down"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger, logs := setupTestLogger(t)
			sigChan := make(chan os.Signal, len(tc.signals))
			lookup := func(k string) string {
				env := map[string]string{"UPSTREAM": "http://localhost:8080"}
				return env[k]
			}

			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("failed to create listener: %v", err)
			}
			defer listener.Close()

			go func() {
				time.Sleep(50 * time.Millisecond)
				for _, s := range tc.signals {
					sigChan <- s
					time.Sleep(10 * time.Millisecond)
				}
			}()

			exitCode := run(lookup, sigChan, listener, logger)
			if exitCode != tc.wantExit {
				t.Errorf("exit code = %d, want %d", exitCode, tc.wantExit)
			}

			logStr := logs.String()
			if !strings.Contains(logStr, tc.wantLog) {
				t.Fatalf("logs missing %q; got:\n%s", tc.wantLog, logStr)
			}
		})
	}
}

func TestConfigurationError(t *testing.T) {
	cases := []struct {
		name     string
		wantExit int
		wantLog  []string
		env      map[string]string
	}{
		{name: "missing_upstream", wantExit: 1, wantLog: []string{"config error", "UPSTREAM is required"}, env: map[string]string{}},
		{name: "invalid_port", wantExit: 1, wantLog: []string{"config error"},
			env: map[string]string{
				"UPSTREAM": "http://localhost:8080",
				"PORT":     "invalid",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger, logs := setupTestLogger(t)
			sigChan := make(chan os.Signal, 1)
			lookup := func(k string) string { return tc.env[k] }

			exitCode := run(lookup, sigChan, nil, logger)
			if exitCode != tc.wantExit {
				t.Errorf("exit code = %d, want %d", exitCode, tc.wantExit)
			}

			logStr := logs.String()
			for _, want := range tc.wantLog {
				if !strings.Contains(logStr, want) {
					t.Errorf("logs missing %q", want)
				}
			}
		})
	}
}

func TestServerStartup(t *testing.T) {
	upstreamOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer upstreamOK.Close()

	cases := []struct {
		name       string
		env        map[string]string
		useRealGet bool
		nilLogger  bool
		wantLog    string
	}{
		{name: "valid_config", env: map[string]string{"UPSTREAM": "http://localhost:8081"}, wantLog: "starting throttle-proxy"},
		{name: "real_upstream", env: map[string]string{"UPSTREAM": upstreamOK.URL}, wantLog: "stopped"},
		{name: "multiple_upstreams", env: map[string]string{"UPSTREAM": "http://localhost:8080, http://localhost:8081"}, wantLog: "upstreams"},
		{name: "https_upstream", env: map[string]string{"UPSTREAM": "https://example.com"}, wantLog: "upstreams"},
		{name: "endpoints_configuration", env: map[string]string{"UPSTREAM": "http://localhost:8080", "ENDPOINTS": "/api/v1/search, /api/v2/search"}, wantLog: "endpoints"},
		{name: "nil_logger_defaults", env: map[string]string{"UPSTREAM": "http://localhost:8080"}, nilLogger: true},
		{name: "nil_lookup_uses_osenv", env: map[string]string{"UPSTREAM": "http://localhost:8080"}, useRealGet: true, wantLog: "starting throttle-proxy"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logger *slog.Logger
			var logs *bytes.Buffer
			if !tc.nilLogger {
				logger, logs = setupTestLogger(t)
			}

			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("failed to create listener: %v", err)
			}
			defer listener.Close()

			lookup := func(k string) string { return tc.env[k] }
			if tc.useRealGet {
				for k, v := range tc.env {
					t.Setenv(k, v)
				}
				lookup = nil
			}

			sigChan := make(chan os.Signal, 1)
			go func() {
				time.Sleep(100 * time.Millisecond)
				sigChan <- syscall.SIGTERM
			}()

			exitCode := run(lookup, sigChan, listener, logger)
			if exitCode != 0 {
				t.Errorf("exit code = %d, want 0", exitCode)
			}

			if logs != nil && tc.wantLog != "" {
				if !strings.Contains(logs.String(), tc.wantLog) {
					t.Errorf("logs missing %q", tc.wantLog)
				}
			}
		})
	}
}

func TestGracefulShutdown(t *testing.T) {
	cases := []struct {
		name         string
		makeUpstream func() *httptest.Server
		env          map[string]string
		allowExitOne bool
	}{
		{
			name: "within_timeout",
			env:  map[string]string{"UPSTREAM": "http://localhost:8080"},
		},
		{
			name: "blocked_upstream",
			makeUpstream: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					<-r.Context().Done()
				}))
			},
			env: map[string]string{
				"UPSTREAM":         "",
				"UPSTREAM_TIMEOUT": "30",
			},
			allowExitOne: true,
		},
		{
			name: "partial_response",
			makeUpstream: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Length", "10")
					w.WriteHeader(http.StatusOK)
					w.Write([]byte("partial"))
					time.Sleep(5 * time.Second)
				}))
			},
			env: map[string]string{
				"UPSTREAM": "",
			},
			allowExitOne: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger, logs := setupTestLogger(t)

			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("failed to create listener: %v", err)
			}
			defer listener.Close()

			upstreamURL := tc.env["UPSTREAM"]
			if tc.makeUpstream != nil {
				upstream := tc.makeUpstream()
				defer upstream.Close()
				upstreamURL = upstream.URL
			}

			lookup := func(k string) string {
				if k == "UPSTREAM" {
					return upstreamURL
				}
				return tc.env[k]
			}

			sigChan := make(chan os.Signal, 1)
			go func() {
				time.Sleep(100 * time.Millisecond)
				sigChan <- syscall.SIGTERM
			}()

			start := time.Now()
			exitCode := run(lookup, sigChan, listener, logger)
			elapsed := time.Since(start)

			if tc.name == "within_timeout" {
				if exitCode != 0 {
					t.Errorf("exit code = %d, want 0", exitCode)
				}
				if elapsed > shutdownTimeout {
					t.Errorf("shutdown took %v, expected less than %v", elapsed, shutdownTimeout)
				}
			} else if tc.allowExitOne {
				if exitCode != 0 && exitCode != 1 {
					t.Errorf("exit code = %d, want 0 or 1", exitCode)
				}
			}

			if !strings.Contains(logs.String(), "shutting down") {
				t.Error("expected 'shutting down' in logs")
			}
		})
	}
}

func TestServerErrorFromListener(t *testing.T) {
	logger, logs := setupTestLogger(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	lookup := func(k string) string {
		env := map[string]string{"UPSTREAM": "http://localhost:8080"}
		return env[k]
	}

	sigChan := make(chan os.Signal, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		listener.Close()
	}()

	exitCode := run(lookup, sigChan, listener, logger)
	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1", exitCode)
	}

	if !strings.Contains(logs.String(), "server error") {
		t.Error("expected 'server error' in logs")
	}
}

func TestServerErrorWithShutdownError(t *testing.T) {
	logger, logs := setupTestLogger(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	lookup := func(k string) string {
		env := map[string]string{"UPSTREAM": "http://localhost:8080"}
		return env[k]
	}

	listener.Close()

	sigChan := make(chan os.Signal, 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
		sigChan <- syscall.SIGTERM
	}()

	listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	exitCode := run(lookup, sigChan, listener, logger)
	if !strings.Contains(logs.String(), "shutting down") {
		t.Error("expected 'shutting down' in logs")
	}
	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0; logs: %s", exitCode, logs.String())
	}
}
