// Command proxy es el composition root del proxy de descomposición atómica:
// junta configuración, adaptadores y dominio, y sirve la API HTTP.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ai-proxy-agent-harness/internal/adapters/httpapi"
	"ai-proxy-agent-harness/internal/adapters/sessionstore/md"
	"ai-proxy-agent-harness/internal/adapters/upstream"
	"ai-proxy-agent-harness/internal/application/service"
	"ai-proxy-agent-harness/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	store, err := md.New(cfg.SessionsDir, cfg.SessionTTL, cfg.MaxSessions, logger)
	if err != nil {
		logger.Error("initializing session store", "err", err)
		os.Exit(1)
	}
	client := upstream.New(cfg.UpstreamBaseURL, cfg.UpstreamAPIKey, cfg.RequestTimeout)
	svc := service.New(client, store, cfg.UpstreamModel, cfg.MaxDecompositionDepth, cfg.MaxToolRoundsPerPhase, logger)
	handler := httpapi.New(svc, cfg.UpstreamModel, cfg.ExposeReasoningContent, logger)

	httpServer := &http.Server{
		Addr:    cfg.Addr(),
		Handler: handler,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("proxy listening", "addr", cfg.Addr(), "model", cfg.UpstreamModel)
		errCh <- httpServer.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "err", err)
			os.Exit(1)
		}
	case sig := <-sigCh:
		logger.Info("shutting down", "signal", sig.String())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			logger.Error("graceful shutdown failed", "err", err)
			os.Exit(1)
		}
	}
	logger.Info("proxy stopped")
}
