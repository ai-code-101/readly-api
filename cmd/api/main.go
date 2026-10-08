// Command api runs the Readly REST API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ai-code-101/readly-api/internal/config"
	"github.com/ai-code-101/readly-api/internal/db"
	"github.com/ai-code-101/readly-api/internal/httpapi"
	"github.com/ai-code-101/readly-api/internal/sms"
	"github.com/ai-code-101/readly-api/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	var sender sms.Sender = sms.LogSender{Log: log}
	if cfg.SMSMode == "live" {
		sender = &sms.Client{URL: cfg.SMSURL, Channel: cfg.SMSChannel, OrganizationID: cfg.SMSOrgID, Token: cfg.SMSToken}
	} else {
		log.Warn("SMS_MODE=log: OTP codes are printed here instead of being sent by SMS")
	}

	api := httpapi.New(store.New(pool), httpapi.Options{
		AdminToken:           cfg.AdminToken,
		AllowedOrigins:       cfg.AllowedOrigins,
		MaxEPUBBytes:         cfg.MaxEPUBBytes,
		MaxImageBytes:        cfg.MaxImageBytes,
		Logger:               log,
		SMS:                  sender,
		OTPSecret:            cfg.OTPSecret,
		SubscriptionHours:    cfg.SubscriptionHours,
		SubscriptionPriceKES: cfg.SubscriptionPriceKES,
		CookieSecure:         cfg.CookieSecure,
	})
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("readly api listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
