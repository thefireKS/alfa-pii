// Command pii-service runs the personal-data protection HTTP service.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"alfa-hackathon.local/pii/internal/app"
	"alfa-hackathon.local/pii/internal/config"
	"alfa-hackathon.local/pii/internal/httpapi"
	"alfa-hackathon.local/pii/internal/masker"
	"alfa-hackathon.local/pii/internal/recognizer"
	"alfa-hackathon.local/pii/internal/store"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err.Error())
		os.Exit(1)
	}

	// Wire dependencies explicitly at the entry point.
	rec := recognizer.EmailRecognizer{}
	m := masker.New(cfg.MarkerPrefix)
	st := store.NewMemory(store.Limits{
		MaxEntries: cfg.StoreMaxEntries,
		MaxBytes:   cfg.StoreMaxBytes,
		TTL:        cfg.StoreTTL,
	})
	svc := app.New([]app.Recognizer{rec}, st, m)

	ready := func() bool { return true }
	h := httpapi.NewHandler(svc, ready, cfg.MaxActiveRequests, cfg.MaxBodyBytes)

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      h.Routes(),
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.ListenAddr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err.Error())
			os.Exit(1)
		}
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err.Error())
			os.Exit(1)
		}
	}
	logger.Info("stopped")
}
