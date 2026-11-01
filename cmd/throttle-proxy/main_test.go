package main

import (
	"bytes"
	"log/slog"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

// setupTestLogger creates a logger that writes to a buffer for testing.
func setupTestLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	return logger, buf
}

func envLookup(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

// testListener opens a free port on loopback.
func testListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// signalLater delivers sig once, after delay, so run() reaches its select first.
func signalLater(sig os.Signal, delay time.Duration) <-chan os.Signal {
	ch := make(chan os.Signal, 1)
	go func() {
		time.Sleep(delay)
		ch <- sig
	}()
	return ch
}

func TestRunExitsCleanly(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"valid config", map[string]string{"UPSTREAM": "http://localhost:8080"}},
		{"multiple upstreams", map[string]string{"UPSTREAM": "http://localhost:8080, http://localhost:8081"}},
		{"https upstream", map[string]string{"UPSTREAM": "https://example.com"}},
		{"custom port and endpoints", map[string]string{
			"UPSTREAM":  "http://localhost:8080",
			"ENDPOINTS": "/api/v1/search, /api/v2/search",
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, logs := setupTestLogger()

			exitCode := run(envLookup(tt.env), signalLater(syscall.SIGTERM, 50*time.Millisecond), testListener(t), logger)

			if exitCode != 0 {
				t.Errorf("expected exit code 0, got %d (logs: %s)", exitCode, logs.String())
			}
			for _, want := range []string{"starting throttle-proxy", "upstreams", "stopped"} {
				if !bytes.Contains(logs.Bytes(), []byte(want)) {
					t.Errorf("expected %q in logs, got %s", want, logs.String())
				}
			}
		})
	}
}

func TestSignalHandling(t *testing.T) {
	for _, sig := range []os.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			logger, logs := setupTestLogger()

			exitCode := run(envLookup(map[string]string{"UPSTREAM": "http://localhost:8080"}), signalLater(sig, 50*time.Millisecond), nil, logger)

			if exitCode != 0 {
				t.Errorf("expected exit code 0 for %v, got %d", sig, exitCode)
			}
			if !bytes.Contains(logs.Bytes(), []byte("shutting down")) {
				t.Error("expected 'shutting down' in logs")
			}
		})
	}
}

// TestGracefulShutdownFast verifies shutdown does not burn the whole shutdownTimeout.
func TestGracefulShutdownFast(t *testing.T) {
	logger, _ := setupTestLogger()

	start := time.Now()
	exitCode := run(envLookup(map[string]string{"UPSTREAM": "http://localhost:8080"}),
		signalLater(syscall.SIGTERM, 50*time.Millisecond), testListener(t), logger)
	elapsed := time.Since(start)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
	if elapsed >= shutdownTimeout {
		t.Errorf("shutdown took %v, expected less than %v", elapsed, shutdownTimeout)
	}
}

// TestConfigurationError_MissingUpstream verifies error handling for missing UPSTREAM.
func TestConfigurationError_MissingUpstream(t *testing.T) {
	logger, logs := setupTestLogger()

	exitCode := run(envLookup(nil), make(chan os.Signal, 1), nil, logger)

	if exitCode != 1 {
		t.Errorf("expected exit code 1 for config error, got %d", exitCode)
	}
	for _, want := range []string{"config error", "UPSTREAM is required"} {
		if !bytes.Contains(logs.Bytes(), []byte(want)) {
			t.Errorf("expected %q in logs, got %s", want, logs.String())
		}
	}
}

// TestConfigurationError_InvalidPort verifies error handling for invalid PORT.
func TestConfigurationError_InvalidPort(t *testing.T) {
	logger, logs := setupTestLogger()

	lookup := envLookup(map[string]string{"UPSTREAM": "http://localhost:8080", "PORT": "invalid"})
	exitCode := run(lookup, make(chan os.Signal, 1), nil, logger)

	if exitCode != 1 {
		t.Errorf("expected exit code 1 for config error, got %d", exitCode)
	}
	if !bytes.Contains(logs.Bytes(), []byte("config error")) {
		t.Error("expected 'config error' in logs")
	}
}

// TestServerErrorFromListener verifies a failed Serve sets exit code 1: the listener is
// closed under the server, so Serve returns before any signal arrives.
func TestServerErrorFromListener(t *testing.T) {
	logger, logs := setupTestLogger()

	ln := testListener(t)
	exitDone := make(chan int, 1)
	go func() {
		exitDone <- run(envLookup(map[string]string{"UPSTREAM": "http://localhost:8080"}), make(chan os.Signal, 1), ln, logger)
	}()

	time.Sleep(50 * time.Millisecond)
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	select {
	case exitCode := <-exitDone:
		if exitCode != 1 {
			t.Errorf("expected exit code 1 for server error, got %d", exitCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after server error")
	}

	if !bytes.Contains(logs.Bytes(), []byte("server error")) {
		t.Error("expected 'server error' in logs")
	}
}

// TestNilGetenv covers the nil getenv and nil logger branches.
func TestNilGetenv(t *testing.T) {
	t.Setenv("UPSTREAM", "http://localhost:8080")

	exitCode := run(nil, signalLater(syscall.SIGTERM, 50*time.Millisecond), nil, nil)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
}
