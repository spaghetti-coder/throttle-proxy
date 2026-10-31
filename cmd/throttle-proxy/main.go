// Package main runs the throttle-proxy HTTP server.
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

const (
	// defaultReadHeaderTimeout bounds slowloris-style header stalls.
	defaultReadHeaderTimeout = 10 * time.Second
	defaultReadTimeout       = 30 * time.Second
	defaultIdleTimeout       = 120 * time.Second
)

const shutdownTimeout = 30 * time.Second

// run starts the server and blocks until a signal or a server error, then returns
// the process exit code.
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
