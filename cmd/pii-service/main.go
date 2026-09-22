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
	"alfa-hackathon.local/pii/internal/metrics"
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

	// Build the recognizer registry and register config-driven regexp rules.
	// The whole configuration is validated before use: an invalid rule or an
	// unknown type rejects startup with no partial activation.
	reg := recognizer.NewRegistry()
	for _, rule := range cfg.RegexpRules {
		if err := reg.AddRegexpRule(rule.Type, rule.Priority, rule.Pattern, rule.MaxMatches); err != nil {
			logger.Error("invalid regexp rule", "error", err.Error())
			os.Exit(1)
		}
	}

	// The fixed /process scope protects every built-in type.
	processTypes := []recognizer.Type{
		recognizer.Email,
		recognizer.Phone,
		recognizer.INN,
		recognizer.Card,
		recognizer.Passport,
		recognizer.DepartmentCode,
		recognizer.DriverLicense,
		recognizer.PIN,
		recognizer.CVV,
		recognizer.FullName,
		recognizer.BirthDate,
		recognizer.BirthPlace,
		recognizer.Citizenship,
		recognizer.PassportAuthority,
		recognizer.PassportIssueDate,
		recognizer.Address,
		recognizer.CardHolderName,
	}

	consumers := make([]app.Consumer, 0, len(cfg.Consumers))
	secrets := make(map[string]string, len(cfg.Consumers))
	for _, c := range cfg.Consumers {
		// Validate that every configured type is known before the service
		// starts, so an unknown type never activates partially.
		if _, _, err := reg.Recognizers(c.Types); err != nil {
			logger.Error("invalid consumer types", "consumer", c.Name, "error", err.Error())
			os.Exit(1)
		}
		consumers = append(consumers, app.Consumer{
			Name:           c.Name,
			Enabled:        c.Enabled,
			Types:          c.Types,
			MaskingEnabled: c.MaskingEnabled,
			CanRestore:     c.CanRestore,
			MaskFormat:     c.MaskFormat,
		})
		secrets[c.Secret] = c.Name
	}

	m := masker.New(cfg.MarkerPrefix)
	met := metrics.New()
	// The unauthenticated /process endpoint uses its own store so it cannot
	// exhaust the capacity of the managed consumers' store.
	processStore := store.NewMemory(store.Limits{
		MaxEntries:      cfg.StoreMaxEntries,
		MaxBytes:        cfg.StoreMaxBytes,
		MaxRecordBytes:  cfg.StoreMaxRecordBytes,
		TTL:             cfg.StoreTTL,
		CreateWait:      cfg.StoreCreateWait,
		CleanupInterval: cfg.StoreCleanupInterval,
	})
	consumerStore := store.NewMemory(store.Limits{
		MaxEntries:      cfg.StoreMaxEntries,
		MaxBytes:        cfg.StoreMaxBytes,
		MaxRecordBytes:  cfg.StoreMaxRecordBytes,
		TTL:             cfg.StoreTTL,
		CreateWait:      cfg.StoreCreateWait,
		CleanupInterval: cfg.StoreCleanupInterval,
	})
	processStore.SetObserver(storeObserver{met})
	consumerStore.SetObserver(storeObserver{met})
	processStore.StartCleanup()
	consumerStore.StartCleanup()
	defer processStore.Stop()
	defer consumerStore.Stop()
	svc, err := app.NewManaged(reg, processTypes, consumers, processStore, consumerStore, m)
	if err != nil {
		logger.Error("build service", "error", err.Error())
		os.Exit(1)
	}

	ready := func() bool { return true }
	auth := httpapi.NewAuthenticator(secrets)
	h := httpapi.NewHandler(svc, auth, ready, cfg.MaxActiveRequests, cfg.MaxBodyBytes, logger, met)

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

// storeObserver forwards store lifecycle events to the metrics collectors.
type storeObserver struct {
	met *metrics.Metrics
}

func (o storeObserver) RecordAdded()   { o.met.StoreRecordAdded() }
func (o storeObserver) RecordRemoved() { o.met.StoreRecordRemoved() }
func (o storeObserver) BytesDelta(d int64) {
	o.met.StoreBytesDelta(d)
}
func (o storeObserver) TTLExpired() { o.met.StoreTTLExpired() }
func (o storeObserver) Failure(reason string) {
	o.met.StoreFailure(reason)
}
