// Package app is the composition root: it turns a parsed configuration into a running service by
// wiring the use cases to their adapters. It holds no rules of its own; those live in the inner
// packages. Keeping the wiring here, and not in package main, makes it testable.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"time"

	"go.opentelemetry.io/otel"

	"github.com/dyudin0821/bestregards/internal/ai"
	"github.com/dyudin0821/bestregards/internal/auth"
	"github.com/dyudin0821/bestregards/internal/config"
	"github.com/dyudin0821/bestregards/internal/events"
	"github.com/dyudin0821/bestregards/internal/limits"
	"github.com/dyudin0821/bestregards/internal/metrics"
	"github.com/dyudin0821/bestregards/internal/pairs"
	"github.com/dyudin0821/bestregards/internal/server"
	"github.com/dyudin0821/bestregards/internal/session"
	"github.com/dyudin0821/bestregards/internal/store"
	"github.com/dyudin0821/bestregards/internal/telemetry"
	"github.com/dyudin0821/bestregards/internal/tgauth"
	"github.com/dyudin0821/bestregards/internal/translate"
	"github.com/dyudin0821/bestregards/internal/turnstile"
	"github.com/dyudin0821/bestregards/web"
)

const (
	connectTimeout  = 30 * time.Second
	flushTimeout    = 5 * time.Second
	shutdownTimeout = 30 * time.Second // longer than an AI call (25 s), so requests in flight settle their books
)

// Serve runs the service until ctx is cancelled, then shuts it down in order: stop accepting
// requests, flush queued events, close the database, flush the spans.
func Serve(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	var outbound *http.Client // nil: each adapter's default client
	if cfg.Tracing {
		shutdown, err := telemetry.Setup(ctx, log)
		if err != nil {
			return err
		}
		defer flushSpans(shutdown, log)
		outbound = &http.Client{Transport: telemetry.Transport(http.DefaultTransport)}
		log.Info("tracing enabled")
	}
	catalog, err := pairs.Load()
	if err != nil {
		return err
	}
	sessions, err := session.New(cfg.Auth.SessionSecret, config.SessionTTL)
	if err != nil {
		return err
	}
	sessions.WithGuestTTL(config.GuestTTL)
	dist, err := web.Dist()
	if err != nil {
		return err
	}

	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	db, err := store.Open(connectCtx, store.Config{URL: cfg.Database.URL, MaxConns: cfg.Database.MaxConns, Tracing: cfg.Tracing})
	if err != nil {
		return err
	}
	defer db.Close()

	recorder := events.NewAsync(db, events.AsyncConfig{Pepper: cfg.Events.Pepper, Logger: log})
	defer flushEvents(recorder, log) // deferred after db.Close, so it runs first: the pool is still open

	translator, err := newTranslator(cfg, db, recorder, catalog, outbound, log)
	if err != nil {
		return err
	}

	var meter *metrics.Metrics
	if cfg.Metrics.Addr != "" {
		meter = metrics.New(metrics.Sources{EventsDropped: recorder.Dropped})
	}
	opts := server.Options{
		Metrics: meter, Catalog: catalog, Translate: translator, Events: recorder, Ready: db, TrustProxy: cfg.Server.TrustProxy, Web: dist, Logger: log,
		Auth: auth.New(db, tgauth.Verifier{BotToken: cfg.Telegram.BotToken, MaxAge: config.InitDataMaxAge}, sessions, nil),
	}
	if cfg.Auth.GuestsEnabled() {
		opts.Guests, opts.TurnstileSiteKey = true, cfg.Auth.TurnstileSiteKey
		if cfg.Auth.TurnstileSecret != "" {
			opts.Auth.RequireChallenge(&turnstile.Verifier{Secret: cfg.Auth.TurnstileSecret, HTTP: outbound})
		} else {
			log.Warn("GUESTS_WITHOUT_CHALLENGE is set: scripts can mint guest accounts freely; the daily spending ceiling is the only limit")
		}
	} else {
		log.Warn("TURNSTILE_SECRET is not set: guests are off, the website works only inside Telegram")
	}
	if cfg.Telegram.WebhookSecret != "" {
		if err := enableBot(&opts, cfg, db, log); err != nil {
			return err
		}
	} else {
		log.Warn("WEBHOOK_SECRET is not set: the bot webhook (/start and /admin) is off")
	}
	return listen(ctx, cfg, server.New(opts), meter, log)
}

func flushSpans(shutdown func(context.Context) error, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		log.Error("flush spans on shutdown", "err", err)
	}
}

func flushEvents(recorder *events.Async, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	if err := recorder.Close(ctx); err != nil {
		log.Error("flush events on shutdown", "err", err)
	}
	if n := recorder.Dropped(); n > 0 {
		log.Warn("events were dropped", "count", n)
	}
}

// newTranslator wires the translation use case. Without an AI key only ready-made pairs work.
func newTranslator(cfg config.Config, db *store.Store, rec events.Recorder, catalog *pairs.Catalog, client *http.Client, log *slog.Logger) (*translate.Service, error) {
	tc := translate.Config{
		Switch: db, Events: rec, Pairs: catalog, Quotas: db, Budget: db,
		Location: cfg.QuotaLocation, Logger: log,
		Prices:           limits.Prices{InputPerMtok: cfg.AI.PriceInputUSD, OutputPerMtok: cfg.AI.PriceOutputUSD},
		DailyBudgetMicro: int64(cfg.AI.FreeBudgetUSD * 1_000_000), MaxOutputTokens: ai.MaxOutputTokens,
		Unlimited: adminSet(cfg.Telegram.AdminIDs),
	}
	if cfg.AI.Key == "" {
		log.Warn("AI_API_KEY is not set: only ready-made pairs can be translated")
	} else {
		provider, err := ai.New(ai.Config{BaseURL: cfg.AI.BaseURL, APIKey: cfg.AI.Key, Model: cfg.AI.Model, HTTP: client})
		if err != nil {
			return nil, err
		}
		tc.AI = provider
		log.Info("ai enabled", "model", cfg.AI.Model, "freeBudgetUSD", cfg.AI.FreeBudgetUSD)
	}
	if cfg.Tracing {
		traceTranslate(&tc, otel.GetTracerProvider(), cfg.AI.Model)
	}
	return translate.New(tc)
}

// loopbackOnly reports whether addr listens on a loopback address only. An empty host, 0.0.0.0 and
// [::] all mean every interface; so does anything that is not a loopback address or "localhost".
func loopbackOnly(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// listen serves the public handler and, when configured, the metrics, until ctx is cancelled.
func listen(ctx context.Context, cfg config.Config, handler http.Handler, meter *metrics.Metrics, log *slog.Logger) error {
	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      45 * time.Second, // longer than the AI call timeout
		IdleTimeout:       60 * time.Second,
	}
	errc := make(chan error, 2)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", cfg.Server.Addr)

	var metricsSrv *http.Server
	if meter != nil {
		m := cfg.Metrics
		metricsSrv = meter.Server(m.Addr, m.Token)
		go func() {
			if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("metrics server: %w", err)
			}
		}()
		log.Info("metrics listening", "addr", m.Addr, "auth", m.Token != "")
		if m.Token == "" && !loopbackOnly(m.Addr) {
			log.Warn("metrics are served on a non-loopback address without a token: restrict the port with a network policy or set METRICS_TOKEN")
		}
	}

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if metricsSrv != nil {
		if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
			log.Warn("metrics server shutdown", "err", err)
		}
	}
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
