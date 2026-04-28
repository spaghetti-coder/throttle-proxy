package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"throttle-proxy/internal/config"
	"throttle-proxy/internal/dispatcher"
	"throttle-proxy/internal/proxy"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}

	upstreamURLs := make([]string, len(cfg.Upstreams))
	for i, u := range cfg.Upstreams {
		upstreamURLs[i] = u.String()
	}
	slog.Info("starting throttle-proxy",
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
		Addr:        fmt.Sprintf(":%d", cfg.Port),
		Handler:     handler,
		ReadTimeout: 30 * time.Second,
		IdleTimeout: 120 * time.Second,
		// WriteTimeout: 0 allows streaming and large responses; queue wait is separately controlled by MAX_WAIT.
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go disp.Run(ctx)

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", srv.Addr)
		serverErr <- srv.ListenAndServe()
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigChan:
		slog.Info("shutting down", "signal", sig.String())
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "err", err)
	}

	slog.Info("stopped")
}
