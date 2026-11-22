// Package server wires the HTTP API and the embedded web frontend.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/dyudin0821/bestregards/internal/auth"
	"github.com/dyudin0821/bestregards/internal/events"
	"github.com/dyudin0821/bestregards/internal/metrics"
	"github.com/dyudin0821/bestregards/internal/pairs"
	"github.com/dyudin0821/bestregards/internal/telemetry"
	"github.com/dyudin0821/bestregards/internal/translate"
	"github.com/dyudin0821/bestregards/internal/user"
)

// Pinger reports whether the database is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Options configures a Server. Now may be nil.
type Options struct {
	Catalog    *pairs.Catalog
	Auth       *auth.Service
	Translate  *translate.Service
	Events     events.Recorder // optional
	Bot        http.Handler    // optional: the Telegram webhook, mounted at POST /bot/webhook
	Ready      Pinger
	TrustProxy bool
	Web        fs.FS
	Logger     *slog.Logger
	Now        func() time.Time
	Metrics    *metrics.Metrics // optional: traffic, latency and error metrics for Prometheus
	// TracerProvider makes the spans of incoming requests; nil means the global one, which does
	// nothing until telemetry.Setup installs a real one.
	TracerProvider trace.TracerProvider

	// Guests lets anonymous website visitors in through POST /api/auth/guest. TurnstileSiteKey is
	// the public key the page needs to show the challenge that guards it; empty means no challenge.
	Guests           bool
	TurnstileSiteKey string
}

// Server is the HTTP handler of the service.
type Server struct {
	Options
	authLimiter  *ipLimiter
	aiLimiter    *ipLimiter
	evLimiter    *ipLimiter
	meLimiter    *ipLimiter
	guestLimiter *ipLimiter
	readiness    readyCache
	mux          *http.ServeMux
	handler      http.Handler
}

// readyCache answers /readyz from one database ping per interval, so a flood of probes cannot take
// connections from real traffic.
type readyCache struct {
	mu sync.Mutex
	at time.Time
	ok bool
}

const readyTTL = 2 * time.Second

// New builds the handler tree.
func New(o Options) *Server {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Events == nil {
		o.Events = events.Discard
	}
	s := &Server{Options: o, authLimiter: newIPLimiter(30, 10), aiLimiter: newIPLimiter(30, 10), evLimiter: newIPLimiter(60, 20), meLimiter: newIPLimiter(120, 30), guestLimiter: newIPLimiter(10, 5), mux: http.NewServeMux()}

	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	s.mux.HandleFunc("GET /readyz", s.ready)
	s.mux.HandleFunc("GET /api/pairs", s.listPairs)
	s.mux.HandleFunc("GET /api/config", s.publicConfig)
	s.mux.HandleFunc("POST /api/auth/telegram", s.authTelegram)
	s.mux.HandleFunc("POST /api/auth/guest", s.authGuest)
	s.mux.HandleFunc("GET /api/me", s.me)
	s.mux.HandleFunc("POST /api/translate", s.translate)
	s.mux.HandleFunc("POST /api/events", s.clientEvents)
	if s.Bot != nil {
		s.mux.Handle("POST /bot/webhook", s.Bot)
	}
	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "Не найдено.")
	})
	s.mountWeb()

	traceOpts := []otelhttp.Option{
		// Callers come from the internet: their trace context starts a linked trace, not ours.
		otelhttp.WithPublicEndpointFn(func(*http.Request) bool { return true }),
		otelhttp.WithFilter(func(r *http.Request) bool { return r.URL.Path != "/healthz" && r.URL.Path != "/readyz" }),
	}
	if o.TracerProvider != nil {
		traceOpts = append(traceOpts, otelhttp.WithTracerProvider(o.TracerProvider))
	}
	s.handler = otelhttp.NewHandler(http.HandlerFunc(s.serve), "http.server", traceOpts...)
	return s
}

// ServeHTTP traces the request, then serves it.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// serve applies the security headers, recovery, metrics and access log to every request.
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Content-Security-Policy", csp)
	h.Set("Strict-Transport-Security", "max-age=31536000")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		h.Set("Cache-Control", "no-store")
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	}
	span := trace.SpanFromContext(r.Context())
	requestID := telemetry.NewRequestID(span.SpanContext())
	h.Set("X-Request-Id", requestID) // a user can quote it to support
	r = r.WithContext(telemetry.WithRequestID(r.Context(), requestID))

	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	start := s.Now()
	defer func() {
		p := recover()
		if p != nil && p != http.ErrAbortHandler {
			s.Logger.ErrorContext(r.Context(), "panic", "path", r.URL.Path, "panic", p, "stack", string(debug.Stack()))
			span.RecordError(fmt.Errorf("panic: %v", p))
			if !rec.wrote {
				writeError(rec, http.StatusInternalServerError, "Что-то пошло не так.")
			}
			rec.status = http.StatusInternalServerError // even when the handler had begun a 2xx: it is broken
		}
		route := routeOf(r)
		span.SetName(route)
		span.SetAttributes(attribute.String("http.route", route))
		var traceID string
		if sc := span.SpanContext(); sc.IsSampled() {
			traceID = sc.TraceID().String()
		}
		took := s.Now().Sub(start)
		s.Metrics.Request(route, rec.status, took, traceID)
		if r.URL.Path != "/healthz" {
			level := slog.LevelInfo
			if rec.status >= 500 {
				level = slog.LevelError // server errors must stand out in the log stream
			}
			s.Logger.Log(r.Context(), level, "request", "method", r.Method, "route", route, "path", r.URL.Path, "status", rec.status, "ms", took.Milliseconds())
		}
		if p == http.ErrAbortHandler {
			panic(p) // net/http aborts the response quietly; recovering would hide that from it
		}
	}()
	s.mux.ServeHTTP(rec, r)
}

// routeOf is the mux pattern that served r, which keeps metric labels and span names few.
func routeOf(r *http.Request) string {
	switch {
	case r.Pattern == "":
		return "unmatched"
	case strings.HasPrefix(r.Pattern, "/"):
		return r.Method + " " + r.Pattern // a pattern without a method, like the /api/ catch-all
	default:
		return r.Pattern
	}
}

// allow applies the flood guard l to the caller of r and counts a refusal.
func (s *Server) allow(l *ipLimiter, r *http.Request) bool {
	if l.allow(clientIP(r, s.TrustProxy), s.Now()) {
		return true
	}
	s.Metrics.RateLimited(routeOf(r))
	return false
}

// Telegram loads its SDK from telegram.org and embeds the app in its own web client. Cloudflare
// serves the Turnstile script and the frame of its challenge.
const csp = "default-src 'self'; script-src 'self' https://telegram.org https://challenges.cloudflare.com; style-src 'self'; form-action 'none'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-src https://challenges.cloudflare.com; base-uri 'none'; object-src 'none'; " +
	"frame-ancestors 'self' https://web.telegram.org"

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool // the status line is gone: it can no longer be changed
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, code >= 200
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer (flush, deadlines).
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	c := &s.readiness
	c.mu.Lock()
	if now := s.Now(); now.Sub(c.at) >= readyTTL {
		// The probe's own cancellation must not turn a healthy database into a cached 503 for everyone.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
		err := s.Ready.Ping(ctx)
		cancel()
		if err != nil {
			s.Logger.ErrorContext(r.Context(), "readiness", "err", err)
		}
		c.ok, c.at = err == nil, now
	}
	ok := c.ok
	c.mu.Unlock()
	if !ok {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) listPairs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, s.Catalog)
}

type authRequest struct {
	InitData string `json:"initData"`
}

type authResponse struct {
	Token     string `json:"token"`
	ExpiresIn int    `json:"expiresIn"`
	User      struct {
		ID        int64  `json:"id"`
		FirstName string `json:"firstName"`
	} `json:"user"`
}

func (s *Server) authTelegram(w http.ResponseWriter, r *http.Request) {
	if !s.allow(s.authLimiter, r) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "Слишком много попыток. Подождите минуту.")
		return
	}
	var req authRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.InitData == "" {
		writeError(w, http.StatusBadRequest, "Откройте приложение из Telegram.")
		return
	}
	session, err := s.Auth.Login(r.Context(), req.InitData)
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		s.Logger.WarnContext(r.Context(), "auth rejected", "err", err)
		writeError(w, http.StatusUnauthorized, "Не удалось подтвердить вход через Telegram. Откройте приложение заново.")
		return
	case errors.Is(err, auth.ErrBanned):
		writeError(w, http.StatusForbidden, "Доступ закрыт.")
		return
	case err != nil:
		s.Logger.ErrorContext(r.Context(), "login", "err", err)
		writeError(w, http.StatusInternalServerError, "Не получилось войти. Попробуйте ещё раз.")
		return
	}
	var resp authResponse
	resp.Token = session.Token
	resp.ExpiresIn = int(session.ExpiresIn.Seconds())
	resp.User.ID, resp.User.FirstName = session.UserID, session.FirstName
	writeJSON(w, http.StatusOK, resp)
}

// publicConfig tells the page what it needs before anyone is signed in.
func (s *Server) publicConfig(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, map[string]any{"guests": s.Guests, "turnstileSiteKey": s.TurnstileSiteKey})
}

type guestRequest struct {
	Challenge string `json:"challenge"`
}

// authGuest hands an anonymous website visitor a session. The challenge (Turnstile) is what keeps
// scripts from minting accounts; the IP limiter only slows them down.
func (s *Server) authGuest(w http.ResponseWriter, r *http.Request) {
	if !s.Guests {
		writeCoded(w, http.StatusNotFound, "guests_disabled", "Вход без Telegram выключен.", nil)
		return
	}
	if !s.allow(s.guestLimiter, r) {
		w.Header().Set("Retry-After", "60")
		writeCoded(w, http.StatusTooManyRequests, "rate_limited", "Слишком много попыток. Подождите минуту.", nil)
		return
	}
	var req guestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCoded(w, http.StatusBadRequest, "invalid", "Не удалось разобрать запрос.", nil)
		return
	}
	var ip string // the real address: Turnstile needs it, the limiter's /64 key would be malformed
	if addr, ok := clientAddr(r, s.TrustProxy); ok {
		ip = addr.String()
	}
	session, err := s.Auth.Guest(r.Context(), req.Challenge, ip)
	switch {
	case errors.Is(err, auth.ErrGuestsDisabled):
		writeCoded(w, http.StatusServiceUnavailable, "guests_disabled", "Вход без Telegram сейчас закрыт. Готовые примеры работают, или откройте бота.", nil)
		return
	case errors.Is(err, auth.ErrChallengeFailed):
		writeCoded(w, http.StatusForbidden, "challenge_failed", "Не удалось подтвердить, что вы не робот. Обновите страницу.", nil)
		return
	case err != nil:
		s.Logger.ErrorContext(r.Context(), "guest login", "err", err)
		writeError(w, http.StatusInternalServerError, "Не получилось войти. Попробуйте ещё раз.")
		return
	}
	var resp authResponse
	resp.Token = session.Token
	resp.ExpiresIn = int(session.ExpiresIn.Seconds())
	resp.User.ID = session.UserID
	writeJSON(w, http.StatusOK, resp)
}

// bearerUser resolves the session token of the request. On failure it has already answered.
func (s *Server) bearerUser(w http.ResponseWriter, r *http.Request) (user.User, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		writeError(w, http.StatusUnauthorized, "Нужен вход.")
		return user.User{}, false
	}
	u, err := s.Auth.Authenticate(r.Context(), token)
	switch {
	case errors.Is(err, auth.ErrSessionInvalid):
		writeError(w, http.StatusUnauthorized, "Сессия недействительна. Обновите страницу.")
	case errors.Is(err, auth.ErrBanned):
		writeError(w, http.StatusForbidden, "Доступ закрыт.")
	case err != nil:
		s.Logger.ErrorContext(r.Context(), "authenticate", "err", err)
		writeError(w, http.StatusInternalServerError, "Что-то пошло не так.")
	default:
		return u, true
	}
	return user.User{}, false
}

type quotaJSON struct {
	Limit     int       `json:"limit"`
	Used      int       `json:"used"`
	Remaining int       `json:"remaining"`
	ResetsAt  time.Time `json:"resetsAt"`
}

func toQuotaJSON(q translate.Quota) quotaJSON {
	return quotaJSON{Limit: q.Limit, Used: q.Used, Remaining: q.Remaining(), ResetsAt: q.ResetsAt}
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	if !s.allow(s.meLimiter, r) {
		w.Header().Set("Retry-After", "60")
		writeCoded(w, http.StatusTooManyRequests, "rate_limited", "Слишком много запросов. Подождите минуту.", nil)
		return
	}
	u, ok := s.bearerUser(w, r)
	if !ok {
		return
	}
	quota, err := s.Translate.Status(r.Context(), u)
	if err != nil {
		s.Logger.ErrorContext(r.Context(), "quota status", "err", err)
		writeError(w, http.StatusInternalServerError, "Что-то пошло не так.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": u.ID, "createdAt": u.CreatedAt, "aiEnabled": s.Translate.AIEnabled(), "quota": toQuotaJSON(quota),
		"guest": u.Guest(),
	})
}

type translateRequest struct {
	Text     string `json:"text"`
	Mode     string `json:"mode"`
	Level    int    `json:"level"`
	Previous string `json:"previous"`
}

type translateResponse struct {
	ResultID string    `json:"resultId"`
	Text     string    `json:"text"`
	Source   string    `json:"source"`
	Quota    quotaJSON `json:"quota"`
}

func (s *Server) translate(w http.ResponseWriter, r *http.Request) {
	if !s.allow(s.aiLimiter, r) {
		w.Header().Set("Retry-After", "60")
		writeCoded(w, http.StatusTooManyRequests, "rate_limited", "Слишком много запросов. Подождите минуту.", nil)
		return
	}
	u, ok := s.bearerUser(w, r)
	if !ok {
		return
	}
	var req translateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCoded(w, http.StatusBadRequest, "invalid", "Не удалось разобрать запрос.", nil)
		return
	}

	res, err := s.Translate.Translate(r.Context(), u, translate.Request{
		Text: req.Text, Mode: req.Mode, Level: req.Level, Previous: req.Previous, Channel: channelOf(u),
	})
	var (
		invalid *translate.ValidationError
		quota   *translate.QuotaExhaustedError
		outcome string
	)
	switch {
	case err == nil:
		outcome = string(res.Source)
		writeJSON(w, http.StatusOK, translateResponse{ResultID: res.ID, Text: res.Text, Source: string(res.Source), Quota: toQuotaJSON(res.Quota)})
	case errors.As(err, &invalid):
		outcome = "invalid"
		writeCoded(w, http.StatusBadRequest, outcome, invalid.Message, nil)
	case errors.As(err, &quota):
		outcome = "quota_exhausted"
		q := toQuotaJSON(quota.Quota)
		writeCoded(w, http.StatusTooManyRequests, outcome, "На сегодня синергия закончилась.", &q)
	case errors.Is(err, translate.ErrBudgetExhausted):
		outcome = "budget_exhausted"
		writeCoded(w, http.StatusServiceUnavailable, outcome, "AI-переводы на сегодня закончились у всех. Квота не потрачена, загляните позже.", nil)
	case errors.Is(err, translate.ErrFreeStopped):
		outcome = "free_stopped"
		writeCoded(w, http.StatusServiceUnavailable, outcome, "AI-переводы временно отключены. Готовые примеры работают.", nil)
	case errors.Is(err, translate.ErrAIDisabled):
		outcome = "ai_disabled"
		writeCoded(w, http.StatusServiceUnavailable, outcome, "Перевод своих текстов пока не подключён. Готовые примеры работают.", nil)
	case errors.Is(err, translate.ErrUnavailable):
		outcome = "unavailable"
		s.Metrics.AIError()
		s.Logger.ErrorContext(r.Context(), "ai unavailable", "err", err)
		writeCoded(w, http.StatusBadGateway, outcome, "Сервис перевода не ответил. Квота не потрачена, текст сохранён: попробуйте ещё раз.", nil)
	default:
		outcome = "internal"
		s.Logger.ErrorContext(r.Context(), "translate", "err", err)
		writeCoded(w, http.StatusInternalServerError, outcome, "Что-то пошло не так. Квота не потрачена.", nil)
	}
	// The request span tells which way the use case went; the text itself never goes into a trace.
	// Mode and level come from the client and are recorded only when they are valid, so a caller
	// cannot fill the trace backend with its own values.
	mode, level := "invalid", 0
	for _, m := range s.Catalog.Modes {
		if m.ID == req.Mode {
			mode = m.ID
		}
	}
	if req.Level >= translate.MinLevel && req.Level <= translate.MaxLevel {
		level = req.Level
	}
	trace.SpanFromContext(r.Context()).SetAttributes(
		attribute.String("translate.outcome", outcome), attribute.String("translate.mode", mode),
		attribute.Int("translate.level", level), attribute.Bool("translate.another_variant", req.Previous != ""),
	)
}

// channelOf names where a user comes from, for the usage metric.
func channelOf(u user.User) events.Source {
	if u.Guest() {
		return events.SourceWeb
	}
	return events.SourceMiniApp
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // the status is already sent; a failed write means the client left
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// writeCoded adds a stable machine-readable code next to the message.
func writeCoded(w http.ResponseWriter, status int, code, message string, quota *quotaJSON) {
	body := map[string]any{"error": message, "code": code}
	if quota != nil {
		body["quota"] = quota
	}
	writeJSON(w, status, body)
}
