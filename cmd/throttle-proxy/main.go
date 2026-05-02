// Package main implements the throttle-proxy command-line tool.
//
// Program Flow:
//  1. Load configuration from environment variables
//  2. Create request dispatcher and HTTP handler
//  3. Start HTTP server with timeouts and signal handling
//  4. Handle graceful shutdown on SIGINT/SIGTERM signals
//
// The proxy serializes incoming requests to prevent upstream rate limiting
// and IP bans. It implements a queue-based dispatch system where concurrent
// requests to the same upstream endpoint are queued and processed sequentially.
//
// Graceful shutdown:
//   - Stop accepting new connections
//   - Wait for active requests to complete (up to 30s timeout)
//   - Exit cleanly or with error code on failure
//
// Environment variables: See internal/config/config.go for full list.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"throttle-proxy/internal/config"
	"throttle-proxy/internal/dispatcher"
	"throttle-proxy/internal/proxy"
)

// Server timeout constants for http.Server configuration.
const (
	// Time allowed to read request headers (prevents slowloris attacks)
	defaultReadHeaderTimeout = 10 * time.Second
	// Time allowed to read the entire request including body
	defaultReadTimeout = 30 * time.Second
	// Time between requests on a keep-alive connection
	defaultIdleTimeout = 120 * time.Second
)

// Graceful shutdown timeout for active connections.
const shutdownTimeout = 30 * time.Second

// run contains the main application logic and returns an exit code.
// This function is separate from main() to enable testing.
func run(
	getenv func(string) string,
	sigChan <-chan os.Signal,
	listener net.Listener,
	logger *slog.Logger,
) int {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		}))
	}

	var lookup func(string) string
	if getenv != nil {
		lookup = getenv
	} else {
		lookup = os.Getenv
	}

	cfg, err := config.Load(lookup)
	if err != nil {
		logger.Error("config error", "err", err)
		return 1
	}

	upstreamURLs := make([]string, len(cfg.Upstreams))
	for i, u := range cfg.Upstreams {
		upstreamURLs[i] = u.String()
	}
	logger.Info("starting throttle-proxy",
		"port", cfg.Port,
		"upstreams", upstreamURLs,
		"upstream_timeout", cfg.UpstreamTimeout,
		"delay_min", cfg.DelayMin,
		"delay_max", cfg.DelayMax,
		"max_wait", cfg.MaxWait,
		"escalate_after", cfg.EscalateAfter,
		"escalate_max_count", cfg.EscalateMaxCount,
		"endpoints", cfg.Endpoints,
		"queue_size", cfg.QueueSize,
	)

	disp := dispatcher.New(cfg)
	handler := proxy.NewHandler(cfg, disp)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           handler,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		ReadTimeout:       defaultReadTimeout,
		IdleTimeout:       defaultIdleTimeout,
		// WriteTimeout: 0 allows streaming and large responses; queue wait is separately controlled by MAX_WAIT.
	}

	ctx, cancel := context.WithCancel(context.Background())

	go disp.Run(ctx)

	serverErr := make(chan error, 1)
	go func() {
		if listener != nil {
			logger.Info("listening", "addr", listener.Addr().String())
			serverErr <- srv.Serve(listener)
		} else {
			logger.Info("listening", "addr", srv.Addr)
			serverErr <- srv.ListenAndServe()
		}
	}()

	var exitCode int
	select {
	case sig := <-sigChan:
		logger.Info("shutting down", "signal", sig.String())
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "err", err)
			exitCode = 1
		}
	}

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "err", err)
		if exitCode == 0 {
			exitCode = 1
		}
	}

	logger.Info("stopped")

	return exitCode
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	exitCode := run(nil, sigChan, nil, nil)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
