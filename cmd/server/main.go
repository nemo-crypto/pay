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

	"github.com/nemo-crypto/pay/internal/config"
	"github.com/nemo-crypto/pay/internal/httpapi"
	"github.com/nemo-crypto/pay/internal/onramp"
	"github.com/nemo-crypto/pay/internal/payment"
	"github.com/nemo-crypto/pay/internal/service"
	"github.com/nemo-crypto/pay/internal/store"
	"github.com/nemo-crypto/pay/web"
)

func main() {
	if e := run(); e != nil {
		slog.Error("server stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	cfg, e := config.Load()
	if e != nil {
		return e
	}
	db, e := store.Open(cfg.Database)
	if e != nil {
		return e
	}
	defer db.DB.Close()
	providers := map[string]payment.Provider{}
	if cfg.Mode == "demo" {
		for _, g := range []string{"stripe", "allscale"} {
			var verifier payment.Provider = &payment.Stripe{WebhookSecret: httpapi.DemoStripeSecret}
			if g == "allscale" {
				verifier = &payment.Allscale{Key: httpapi.DemoAllscaleKey, Secret: httpapi.DemoAllscaleSecret}
			}
			p := &payment.Demo{Gateway: g, PublicURL: cfg.PublicURL, DB: db.DB, Verifier: verifier}
			if e = p.Init(); e != nil {
				return e
			}
			providers[g] = p
		}
	} else {
		if cfg.StripeKey != "" {
			providers["stripe"] = &payment.Stripe{Key: cfg.StripeKey, WebhookSecret: cfg.StripeWebhookSecret, BaseURL: "https://api.stripe.com", HTTP: payment.Client(), Live: cfg.StripeLive}
		}
		if cfg.AllscaleKey != "" {
			if _, e = service.Quote(1000, cfg.FXRate); e != nil {
				return e
			}
			providers["allscale"] = &payment.Allscale{Key: cfg.AllscaleKey, Secret: cfg.AllscaleSecret, BaseURL: "https://openapi.allscale.io", HTTP: payment.Client(), SignedResponses: cfg.AllscaleSigned}
		}
	}
	svc := &service.Service{Store: db, Providers: providers, Config: cfg}
	ramps, e := onramp.New(db.DB, cfg.Mode, cfg.PublicURL)
	if e != nil {
		return e
	}
	api := httpapi.New(svc)
	api.Onramp = ramps
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()
		svc.Tick(ctx)
		ramps.Tick(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				svc.Tick(ctx)
				ramps.Tick(ctx)
			}
		}
	}()
	server := &http.Server{Addr: cfg.Addr, Handler: api.Handler(web.Handler()), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	slog.Info("payment server ready", "mode", cfg.Mode, "address", cfg.Addr)
	e = server.ListenAndServe()
	stop()
	<-done
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
