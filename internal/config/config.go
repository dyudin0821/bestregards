// Package config reads service settings from the environment.
package config

import (
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // QUOTA_TZ must resolve in minimal images that have no zoneinfo database
)

// Fixed policy. These are product decisions, not deployment settings, so they are not read from
// the environment.
const (
	// InitDataMaxAge is how old Telegram's launch data may be and still log a person in.
	InitDataMaxAge = time.Hour
	// SessionTTL is how long a Telegram user's session token lives.
	SessionTTL = 12 * time.Hour
	// GuestTTL is how long a guest's session lives. The token is the guest's only identity, so a
	// long one keeps a returning visitor from getting a fresh allowance every day.
	GuestTTL = 30 * 24 * time.Hour
	// GuestRetention is how long a guest row stays; it outlives the session by two weeks.
	GuestRetention = GuestTTL + 14*24*time.Hour
	// QuotaRetention is how long a day's per-user quota counter stays. Only today's counter is ever
	// read; older ones are kept briefly for looking into complaints.
	QuotaRetention = 30 * 24 * time.Hour
)

const (
	defaultPort        = 8080
	defaultRetention   = 365 // days
	defaultDBMaxConns  = 10
	minDBMaxConns      = 3    // migrations hold one connection for the lock and run on another
	defaultFreeBudget  = 10.0 // US dollars per day
	defaultQuotaZone   = "Europe/Moscow"
	defaultAIBaseURL   = "https://openrouter.ai/api/v1"
	defaultAIModel     = "deepseek/deepseek-v4-flash"
	minSecretLen       = 32
	minMetricsTokenLen = 16
)

// Config holds every setting the service needs at runtime.
type Config struct {
	Server   Server
	Database Database
	Telegram Telegram
	Auth     Auth
	Events   Events
	AI       AI
	Metrics  Metrics

	// QuotaLocation is the time zone in which a day, and so a daily quota, begins and ends.
	QuotaLocation *time.Location
	// Tracing exports OpenTelemetry spans; it is on when OTEL_EXPORTER_OTLP_ENDPOINT (or
	// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT) is set. The other OTEL_* variables are read by the SDK.
	Tracing bool
}

// Server is the public HTTP listener.
type Server struct {
	Addr       string
	TrustProxy bool // take the client address from X-Forwarded-For; only behind a proxy you control
}

// Database is the PostgreSQL connection.
type Database struct {
	URL      string
	MaxConns int32 // per process; replicas x MaxConns must stay below PostgreSQL max_connections
}

// Telegram is the bot. An empty WebhookSecret turns the webhook off (no /start button, no /admin).
type Telegram struct {
	BotToken      string
	WebhookSecret string
	PublicURL     string  // https origin of the service; the webhook is PublicURL/bot/webhook
	AdminIDs      []int64 // who may use /admin
	// APIURL replaces the Bot API address. It is not an environment setting: tests point it at a
	// fake server. Empty means the real Bot API.
	APIURL string
}

// Auth decides who may use the service.
type Auth struct {
	SessionSecret []byte
	// Guests are anonymous visitors of the website. They are let in only after a Cloudflare
	// Turnstile challenge, unless GuestsWithoutChallenge is set for local work.
	TurnstileSecret        string
	TurnstileSiteKey       string // public; the page needs it to render the challenge
	GuestsWithoutChallenge bool
}

// GuestsEnabled reports whether anonymous website visitors are let in.
func (a Auth) GuestsEnabled() bool { return a.TurnstileSecret != "" || a.GuestsWithoutChallenge }

// Events are the anonymous usage events.
type Events struct {
	Pepper    []byte        // keys the hash that stands in for a user id
	Retention time.Duration // how long `bestregards purge` keeps them
}

// AI is the provider of translations. An empty Key turns AI off; ready-made pairs keep working.
type AI struct {
	Key            string
	BaseURL        string
	Model          string
	PriceInputUSD  float64 // dollars per million input tokens
	PriceOutputUSD float64 // dollars per million output tokens
	FreeBudgetUSD  float64 // daily ceiling for everyone's translations together
}

// Metrics is the Prometheus listener. It is separate from the public one so the metrics never
// share its port. An empty Addr turns it off.
type Metrics struct {
	Addr  string
	Token string // when set, must be sent as a Bearer token
}

var webhookSecretRe = regexp.MustCompile(`^[A-Za-z0-9_-]{32,256}$`)

// FromEnv builds a Config from getenv (os.Getenv in production) and reports all problems at once.
func FromEnv(getenv func(string) string) (Config, error) {
	e := &env{get: getenv}
	var c Config

	c.Server = Server{
		Addr:       ":" + strconv.Itoa(e.integer("PORT", defaultPort, 1, 65535)),
		TrustProxy: e.flag("TRUST_PROXY"),
	}

	c.Database = Database{
		URL:      e.required("DATABASE_URL"),
		MaxConns: e.integer32("DB_MAX_CONNS", defaultDBMaxConns, minDBMaxConns, 1000),
	}
	c.Telegram = Telegram{
		BotToken:      e.token("BOT_TOKEN"),
		WebhookSecret: e.get("WEBHOOK_SECRET"),
		PublicURL:     strings.TrimRight(e.get("PUBLIC_URL"), "/"),
		AdminIDs:      e.telegramIDs("ADMIN_IDS"),
	}
	c.Auth = Auth{
		SessionSecret:          []byte(e.secret("SESSION_SECRET", minSecretLen, true)),
		TurnstileSecret:        e.get("TURNSTILE_SECRET"),
		TurnstileSiteKey:       e.get("TURNSTILE_SITE_KEY"),
		GuestsWithoutChallenge: e.flag("GUESTS_WITHOUT_CHALLENGE"),
	}
	c.Events = Events{
		Pepper:    []byte(e.secret("EVENTS_PEPPER", minSecretLen, true)),
		Retention: time.Duration(e.integer("EVENTS_RETENTION_DAYS", defaultRetention, 1, 36500)) * 24 * time.Hour,
	}
	c.AI = AI{
		Key:            e.get("AI_API_KEY"),
		BaseURL:        e.str("AI_BASE_URL", defaultAIBaseURL),
		Model:          e.str("AI_MODEL", defaultAIModel),
		PriceInputUSD:  e.number("AI_PRICE_INPUT_USD_PER_MTOK", 0),
		PriceOutputUSD: e.number("AI_PRICE_OUTPUT_USD_PER_MTOK", 0),
		FreeBudgetUSD:  e.number("FREE_BUDGET_USD_PER_DAY", defaultFreeBudget),
	}
	c.Metrics = Metrics{
		Addr:  e.get("METRICS_ADDR"),
		Token: e.secret("METRICS_TOKEN", minMetricsTokenLen, false),
	}
	c.Tracing = e.get("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || e.get("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""

	zone := e.str("QUOTA_TZ", defaultQuotaZone)
	loc, err := time.LoadLocation(zone)
	if err != nil {
		e.fail("QUOTA_TZ %q is not a known time zone", zone)
	}
	c.QuotaLocation = loc

	c.validate(e)
	return c, e.err()
}

// validate checks the settings that depend on each other.
func (c Config) validate(e *env) {
	if c.AI.Key != "" {
		if c.AI.PriceInputUSD <= 0 && c.AI.PriceOutputUSD <= 0 {
			e.fail("AI_PRICE_INPUT_USD_PER_MTOK and AI_PRICE_OUTPUT_USD_PER_MTOK are required with AI_API_KEY: the spending ceiling cannot work without prices")
		}
		if c.AI.FreeBudgetUSD <= 0 {
			e.fail("FREE_BUDGET_USD_PER_DAY must be positive with AI_API_KEY")
		}
	}
	if err := checkOutboundURL(c.AI.BaseURL); err != nil {
		e.fail("AI_BASE_URL: %w", err)
	}
	if c.Telegram.WebhookSecret != "" {
		if !webhookSecretRe.MatchString(c.Telegram.WebhookSecret) {
			e.fail("WEBHOOK_SECRET must be 32 to 256 characters from A-Z a-z 0-9 _ - (the set Telegram accepts)")
		}
		if u, err := url.Parse(c.Telegram.PublicURL); err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" {
			e.fail("PUBLIC_URL must be an https origin without a path (Telegram only calls https webhooks) when WEBHOOK_SECRET is set")
		}
	}
	if (c.Auth.TurnstileSecret == "") != (c.Auth.TurnstileSiteKey == "") {
		e.fail("TURNSTILE_SECRET and TURNSTILE_SITE_KEY must be set together")
	}

	// Every secret must be its own: one that doubles as another would turn a leak of one into a leak of both.
	secrets := []struct{ name, value string }{
		{"BOT_TOKEN", c.Telegram.BotToken}, {"SESSION_SECRET", string(c.Auth.SessionSecret)},
		{"EVENTS_PEPPER", string(c.Events.Pepper)}, {"WEBHOOK_SECRET", c.Telegram.WebhookSecret},
		{"METRICS_TOKEN", c.Metrics.Token}, {"TURNSTILE_SECRET", c.Auth.TurnstileSecret},
	}
	for i, a := range secrets {
		for _, b := range secrets[i+1:] {
			if a.value != "" && a.value == b.value {
				e.fail("%s and %s must not be the same value", a.name, b.name)
			}
		}
	}
}

// Purge is the little configuration the `purge` job needs. The job runs as a CronJob and must not
// require the secrets of the whole service just to delete old rows.
type Purge struct {
	DatabaseURL     string
	EventsRetention time.Duration
	GuestRetention  time.Duration
	QuotaRetention  time.Duration
}

// PurgeFromEnv reads the settings of the purge job.
func PurgeFromEnv(getenv func(string) string) (Purge, error) {
	e := &env{get: getenv}
	p := Purge{
		DatabaseURL:     e.required("DATABASE_URL"),
		EventsRetention: time.Duration(e.integer("EVENTS_RETENTION_DAYS", defaultRetention, 1, 36500)) * 24 * time.Hour,
		GuestRetention:  GuestRetention,
		QuotaRetention:  QuotaRetention,
	}
	return p, e.err()
}

// checkOutboundURL accepts https URLs, and plain http only for a loopback host, which tests and
// local tooling use. A key sent over plain http to a remote host would travel in clear.
func checkOutboundURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("must be an absolute URL")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if h := u.Hostname(); h == "localhost" || h == "127.0.0.1" || h == "::1" {
			return nil
		}
	}
	return errors.New("must use https (plain http is accepted only for localhost)")
}
