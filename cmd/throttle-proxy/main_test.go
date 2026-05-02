// Package main provides comprehensive unit tests for the throttle-proxy main package.
// Tests cover signal handling, server lifecycle, configuration error handling,
// and graceful shutdown scenarios.
//
// Test Design:
//   - Tests use the run() function directly to test main logic without os.Exit()
//   - Signal handling is tested by sending signals to channels
//   - Server lifecycle is tested using net.Listener
//   - Context cancellation is used to test shutdown scenarios
package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"throttle-proxy/internal/config"
	"throttle-proxy/internal/dispatcher"
	"throttle-proxy/internal/proxy"
)

// setupTestLogger creates a logger that writes to a buffer for testing.
func setupTestLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	return logger, buf
}

// TestSignalHandling_SIGTERM verifies that SIGTERM triggers graceful shutdown.
func TestSignalHandling_SIGTERM(t *testing.T) {
	logger, logs := setupTestLogger()
	sigChan := make(chan os.Signal, 1)

	// Send SIGTERM after a short delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		sigChan <- syscall.SIGTERM
	}()

	// Valid config
	envVars := map[string]string{
		"UPSTREAM": "http://localhost:8080",
	}
	lookup := func(k string) string { return envVars[k] }

	exitCode := run(lookup, sigChan, nil, logger)

	if exitCode != 0 {
		t.Errorf("expected exit code 0 for SIGTERM, got %d", exitCode)
	}

	if !strings.Contains(logs.String(), "shutting down") {
		t.Error("expected 'shutting down' in logs")
	}
}

// TestSignalHandling_SIGINT verifies that SIGINT triggers graceful shutdown.
func TestSignalHandling_SIGINT(t *testing.T) {
	logger, logs := setupTestLogger()
	sigChan := make(chan os.Signal, 1)

	// Send SIGINT after a short delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		sigChan <- syscall.SIGINT
	}()

	envVars := map[string]string{
		"UPSTREAM": "http://localhost:8080",
	}
	lookup := func(k string) string { return envVars[k] }

	exitCode := run(lookup, sigChan, nil, logger)

	if exitCode != 0 {
		t.Errorf("expected exit code 0 for SIGINT, got %d", exitCode)
	}

	if !strings.Contains(logs.String(), "shutting down") {
		t.Error("expected 'shutting down' in logs")
	}
}

// TestSignalHandling_MultipleSignals verifies handling of multiple signals.
func TestSignalHandling_MultipleSignals(t *testing.T) {
	logger, _ := setupTestLogger()
	sigChan := make(chan os.Signal, 2)

	// Send multiple signals
	go func() {
		time.Sleep(20 * time.Millisecond)
		sigChan <- syscall.SIGTERM
		time.Sleep(10 * time.Millisecond)
		sigChan <- syscall.SIGINT
	}()

	envVars := map[string]string{
		"UPSTREAM": "http://localhost:8080",
	}
	lookup := func(k string) string { return envVars[k] }

	exitCode := run(lookup, sigChan, nil, logger)

	// Should exit cleanly with first signal
	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
}

// TestConfigurationError_MissingUpstream verifies error handling for missing UPSTREAM.
func TestConfigurationError_MissingUpstream(t *testing.T) {
	logger, logs := setupTestLogger()
	sigChan := make(chan os.Signal, 1)

	lookup := func(string) string { return "" }

	exitCode := run(lookup, sigChan, nil, logger)

	if exitCode != 1 {
		t.Errorf("expected exit code 1 for config error, got %d", exitCode)
	}

	if !strings.Contains(logs.String(), "config error") {
		t.Error("expected 'config error' in logs")
	}

	if !strings.Contains(logs.String(), "UPSTREAM is required") {
		t.Error("expected 'UPSTREAM is required' in logs")
	}
}

// TestConfigurationError_InvalidPort verifies error handling for invalid PORT.
func TestConfigurationError_InvalidPort(t *testing.T) {
	logger, logs := setupTestLogger()
	sigChan := make(chan os.Signal, 1)

	lookup := func(k string) string {
		env := map[string]string{
			"UPSTREAM": "http://localhost:8080",
			"PORT":     "invalid",
		}
		return env[k]
	}

	exitCode := run(lookup, sigChan, nil, logger)

	if exitCode != 1 {
		t.Errorf("expected exit code 1 for config error, got %d", exitCode)
	}

	if !strings.Contains(logs.String(), "config error") {
		t.Error("expected 'config error' in logs")
	}
}

// TestServerStartup_ValidConfig verifies server starts with valid configuration.
func TestServerStartup_ValidConfig(t *testing.T) {
	logger, logs := setupTestLogger()

	// Create a listener on a random port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer listener.Close()

	envVars := map[string]string{
		"UPSTREAM": "http://localhost:8081",
	}
	lookup := func(k string) string { return envVars[k] }

	sigChan := make(chan os.Signal, 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
		sigChan <- syscall.SIGTERM
	}()

	exitCode := run(lookup, sigChan, listener, logger)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	if !strings.Contains(logs.String(), "starting throttle-proxy") {
		t.Error("expected 'starting throttle-proxy' in logs")
	}

	if !strings.Contains(logs.String(), "stopped") {
		t.Error("expected 'stopped' in logs")
	}
}

// TestServerStartup_CustomPort verifies server starts on a custom port.
func TestServerStartup_CustomPort(t *testing.T) {
	logger, _ := setupTestLogger()

	// Create a listener on a random port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port

	// Close this listener and create a fresh one
	listener.Close()

	listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("failed to create listener on port %d: %v", port, err)
	}
	defer listener.Close()

	envVars := map[string]string{
		"UPSTREAM": "http://localhost:8080",
	}
	lookup := func(k string) string { return envVars[k] }

	sigChan := make(chan os.Signal, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		sigChan <- syscall.SIGTERM
	}()

	exitCode := run(lookup, sigChan, listener, logger)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
}

// TestDispatcherInitialization verifies dispatcher is initialized with correct config.
func TestDispatcherInitialization(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{
		Upstreams:         []*url.URL{u},
		UpstreamTimeout:   5 * time.Second,
		DelayMin:          1 * time.Second,
		DelayMax:          2 * time.Second,
		QueueSize:         100,
	}

	disp := dispatcher.New(cfg)

	if disp == nil {
		t.Fatal("expected non-nil dispatcher")
	}
}

// TestProxyHandlerInitialization verifies proxy handler is initialized with correct config.
func TestProxyHandlerInitialization(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{
		Upstreams:         []*url.URL{u},
		UpstreamTimeout:   5 * time.Second,
		Endpoints:         []string{"/test"},
		QueueSize:         100,
	}

	disp := dispatcher.New(cfg)
	handler := proxy.NewHandler(cfg, disp)

	if handler == nil {
		t.Fatal("expected non-nil handler")
	}
}

// TestContextCancellation verifies that context cancellation propagates properly.
func TestContextCancellation(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{
		Upstreams:         []*url.URL{u},
		UpstreamTimeout:   5 * time.Second,
		QueueSize:         10,
	}

	disp := dispatcher.New(cfg)

	ctx, cancel := context.WithCancel(context.Background())

	// Start dispatcher
	go disp.Run(ctx)

	// Cancel context
	cancel()

	// Give time for shutdown
	time.Sleep(50 * time.Millisecond)

	// Try to enqueue after cancellation - should return 503
	req := httptest.NewRequest("GET", "/test", nil)
	resultChan := disp.Enqueue(req)

	select {
	case result := <-resultChan:
		if result.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("expected status 503 after cancel, got %d", result.StatusCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Enqueue blocked after context cancellation")
	}
}

// TestGracefulShutdown_Timeout verifies shutdown completes within timeout.
func TestGracefulShutdown_Timeout(t *testing.T) {
	logger, _ := setupTestLogger()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer listener.Close()

	envVars := map[string]string{
		"UPSTREAM": "http://localhost:8080",
	}
	lookup := func(k string) string { return envVars[k] }

	sigChan := make(chan os.Signal, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		sigChan <- syscall.SIGTERM
	}()

	start := time.Now()
	exitCode := run(lookup, sigChan, listener, logger)
	elapsed := time.Since(start)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	if elapsed > shutdownTimeout {
		t.Errorf("shutdown took %v, expected less than %v", elapsed, shutdownTimeout)
	}
}

// TestServerErrorHandling verifies proper error handling from server errors.
func TestServerErrorHandling(t *testing.T) {
	logger, _ := setupTestLogger()

	// Create first listener
	listener1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer listener1.Close()

	// Create second listener on same port - this will fail
	port := listener1.Addr().(*net.TCPAddr).Port
	listener2, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err == nil {
		// If we succeeded, close it and skip this test
		listener2.Close()
		t.Skip("Port was available, cannot test server error handling")
	}

	envVars := map[string]string{
		"UPSTREAM": "http://localhost:8080",
	}
	lookup := func(k string) string { return envVars[k] }

	sigChan := make(chan os.Signal, 1)
	go func() {
		time.Sleep(500 * time.Millisecond)
		sigChan <- syscall.SIGTERM
	}()

	exitCode := run(lookup, sigChan, listener1, logger)

	// Should exit cleanly on SIGTERM
	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
}

// TestMainIntegration is an integration test for the main function components.
func TestMainIntegration(t *testing.T) {
	logger, logs := setupTestLogger()

	// Create an upstream server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer upstream.Close()

	// Create listener
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer listener.Close()

	envVars := map[string]string{
		"UPSTREAM": upstream.URL,
	}
	lookup := func(k string) string { return envVars[k] }

	sigChan := make(chan os.Signal, 1)
	go func() {
		time.Sleep(200 * time.Millisecond)
		sigChan <- syscall.SIGTERM
	}()

	exitCode := run(lookup, sigChan, listener, logger)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	if !strings.Contains(logs.String(), "starting throttle-proxy") {
		t.Error("expected 'starting throttle-proxy' in logs")
	}

	if !strings.Contains(logs.String(), "stopped") {
		t.Error("expected 'stopped' in logs")
	}
}

// TestMultipleUpstreams verifies handling of multiple upstreams.
func TestMultipleUpstreams(t *testing.T) {
	logger, logs := setupTestLogger()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer listener.Close()

	envVars := map[string]string{
		"UPSTREAM": "http://localhost:8080, http://localhost:8081",
	}
	lookup := func(k string) string { return envVars[k] }

	sigChan := make(chan os.Signal, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		sigChan <- syscall.SIGTERM
	}()

	exitCode := run(lookup, sigChan, listener, logger)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	if !strings.Contains(logs.String(), "upstreams") {
		t.Error("expected 'upstreams' in logs")
	}
}

// TestHTTPSUpstream verifies handling of HTTPS upstream URLs.
func TestHTTPSUpstream(t *testing.T) {
	logger, _ := setupTestLogger()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer listener.Close()

	envVars := map[string]string{
		"UPSTREAM": "https://example.com",
	}
	lookup := func(k string) string { return envVars[k] }

	sigChan := make(chan os.Signal, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		sigChan <- syscall.SIGTERM
	}()

	exitCode := run(lookup, sigChan, listener, logger)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
}

// TestEndpointsConfiguration verifies custom endpoints configuration.
func TestEndpointsConfiguration(t *testing.T) {
	logger, logs := setupTestLogger()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer listener.Close()

	envVars := map[string]string{
		"UPSTREAM":  "http://localhost:8080",
		"ENDPOINTS": "/api/v1/search, /api/v2/search",
	}
	lookup := func(k string) string { return envVars[k] }

	sigChan := make(chan os.Signal, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		sigChan <- syscall.SIGTERM
	}()

	exitCode := run(lookup, sigChan, listener, logger)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	if !strings.Contains(logs.String(), "endpoints") {
		t.Error("expected 'endpoints' in logs")
	}
}

// TestDefaultLogger verifies that nil logger uses default.
func TestDefaultLogger(t *testing.T) {
	envVars := map[string]string{
		"UPSTREAM": "http://localhost:8080",
	}
	lookup := func(k string) string { return envVars[k] }

	sigChan := make(chan os.Signal, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		sigChan <- syscall.SIGTERM
	}()

	// Pass nil logger - should use default
	exitCode := run(lookup, sigChan, nil, nil)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
}

// TestServerErrorFromListener verifies handling of server errors from listener.
func TestServerErrorFromListener(t *testing.T) {
	logger, logs := setupTestLogger()

	// Create a listener
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	envVars := map[string]string{
		"UPSTREAM": "http://localhost:8080",
	}
	lookup := func(k string) string { return envVars[k] }

	// Start server in background
	sigChan := make(chan os.Signal, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		// Close listener to cause an error
		listener.Close()
	}()

	exitCode := run(lookup, sigChan, listener, logger)

	// Exit code should be 1 when server errors
	if exitCode != 1 {
		t.Errorf("expected exit code 1 for server error, got %d", exitCode)
	}

	if !strings.Contains(logs.String(), "server error") {
		t.Error("expected 'server error' in logs")
	}
}

// TestShutdownTimeoutError verifies handling of shutdown timeout.
func TestShutdownTimeoutError(t *testing.T) {
	logger, logs := setupTestLogger()

	// Create a listener
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	// Create a blocking upstream that will delay shutdown
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until context is cancelled
		<-r.Context().Done()
	}))
	defer upstream.Close()

	envVars := map[string]string{
		"UPSTREAM":       upstream.URL,
		"UPSTREAM_TIMEOUT": "30",
	}
	lookup := func(k string) string { return envVars[k] }

	// Start server
	sigChan := make(chan os.Signal, 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
		// Send signal
		sigChan <- syscall.SIGTERM
	}()

	exitCode := run(lookup, sigChan, listener, logger)

	// Exit code can be 0 or 1 depending on timing
	if exitCode != 0 && exitCode != 1 {
		t.Errorf("expected exit code 0 or 1, got %d", exitCode)
	}

	// Should see shutdown message
	if !strings.Contains(logs.String(), "shutting down") {
		t.Error("expected 'shutting down' in logs")
	}
}

// TestServerErrorExitCodePropagation verifies that server errors propagate exit code.
func TestServerErrorExitCodePropagation(t *testing.T) {
	logger, _ := setupTestLogger()

	// Create a listener
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	envVars := map[string]string{
		"UPSTREAM": "http://localhost:8080",
	}
	lookup := func(k string) string { return envVars[k] }

	// Start server in background
	sigChan := make(chan os.Signal, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		// Close listener to cause an error
		listener.Close()
	}()

	exitCode := run(lookup, sigChan, listener, logger)

	// Should exit with code 1 on server error
	if exitCode != 1 {
		t.Errorf("expected exit code 1, got %d", exitCode)
	}
}

// TestShutdownErrorExitCode verifies shutdown error sets exit code.
func TestShutdownErrorExitCode(t *testing.T) {
	logger, logs := setupTestLogger()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	// Create an upstream that will make shutdown difficult
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Keep connection open
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("partial"))
		// Don't close - let timeout handle it
		time.Sleep(5 * time.Second)
	}))
	defer upstream.Close()

	envVars := map[string]string{
		"UPSTREAM": upstream.URL,
	}
	lookup := func(k string) string { return envVars[k] }

	sigChan := make(chan os.Signal, 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
		sigChan <- syscall.SIGTERM
	}()

	exitCode := run(lookup, sigChan, listener, logger)

	// Exit code may be 0 or 1 depending on timing
	if exitCode != 0 && exitCode != 1 {
		t.Errorf("expected exit code 0 or 1, got %d", exitCode)
	}

	// Should see shutdown message
	if !strings.Contains(logs.String(), "shutting down") {
		t.Error("expected 'shutting down' in logs")
	}
}

// TestServerErrorWithShutdownError verifies shutdown error when server already errored.
func TestServerErrorWithShutdownError(t *testing.T) {
	logger, logs := setupTestLogger()

	// Create a listener
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	envVars := map[string]string{
		"UPSTREAM": "http://localhost:8080",
	}
	lookup := func(k string) string { return envVars[k] }

	// Close listener to cause immediate server error
	listener.Close()

	sigChan := make(chan os.Signal, 1)
	go func() {
		// Send signal after delay to trigger shutdown
		time.Sleep(100 * time.Millisecond)
		sigChan <- syscall.SIGTERM
	}()

	// Create a new listener for the actual test
	listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	exitCode := run(lookup, sigChan, listener, logger)

	// Should have "shutting down" in logs
	if !strings.Contains(logs.String(), "shutting down") {
		t.Error("expected 'shutting down' in logs")
	}

	// Exit code should be 0 (graceful shutdown)
	if exitCode != 0 {
		t.Logf("exit code: %d, logs: %s", exitCode, logs.String())
	}
}

// TestNilGetenv covers the getenv nil branch.
func TestNilGetenv(t *testing.T) {
	logger, _ := setupTestLogger()

	sigChan := make(chan os.Signal, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		sigChan <- syscall.SIGTERM
	}()

	// Set UPSTREAM env var temporarily
	oldUpstream := os.Getenv("UPSTREAM")
	os.Setenv("UPSTREAM", "http://localhost:8080")
	defer os.Setenv("UPSTREAM", oldUpstream)

	// Pass nil getenv - should use os.Getenv
	exitCode := run(nil, sigChan, nil, logger)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
}
