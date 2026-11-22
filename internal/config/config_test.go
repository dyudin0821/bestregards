package config

import (
	"strings"
	"testing"
	"time"
)

// vars returns an environment with every required setting, plus overrides given as name, value pairs.
// An empty value removes the variable.
func vars(kv ...string) func(string) string {
	m := map[string]string{
		"DATABASE_URL": "postgres://x", "BOT_TOKEN": "1:abc",
		"SESSION_SECRET": strings.Repeat("k", 32), "EVENTS_PEPPER": strings.Repeat("p", 32),
	}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

func mustLoad(t *testing.T, kv ...string) Config {
	t.Helper()
	c, err := FromEnv(vars(kv...))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDefaults(t *testing.T) {
	c := mustLoad(t)
	if c.Server.Addr != ":8080" || c.Server.TrustProxy || c.Database.MaxConns != 10 || c.Events.Retention != 365*24*time.Hour {
		t.Errorf("server/db/events: %+v %+v %+v", c.Server, c.Database, c.Events)
	}
	if c.AI.Key != "" || c.AI.BaseURL != "https://openrouter.ai/api/v1" || c.AI.Model != "deepseek/deepseek-v4-flash" ||
		c.AI.FreeBudgetUSD != 10 {
		t.Errorf("ai: %+v", c.AI)
	}
	if c.QuotaLocation.String() != "Europe/Moscow" || c.Auth.GuestsEnabled() || c.Telegram.WebhookSecret != "" || c.Metrics.Addr != "" || c.Tracing {
		t.Errorf("everything optional must be off: %+v", c)
	}
}

func TestEverythingWrongIsReportedAtOnce(t *testing.T) {
	_, err := FromEnv(func(k string) string { return map[string]string{"PORT": "80 80", "SESSION_SECRET": "short"}[k] })
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"DATABASE_URL", "BOT_TOKEN", "SESSION_SECRET", "EVENTS_PEPPER", "PORT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q is not mentioned in: %v", want, err)
		}
	}
}

func TestNumbersAreBounded(t *testing.T) {
	if c := mustLoad(t, "PORT", "9090", "DB_MAX_CONNS", "25", "EVENTS_RETENTION_DAYS", "30"); c.Server.Addr != ":9090" || c.Database.MaxConns != 25 || c.Events.Retention != 30*24*time.Hour {
		t.Fatalf("%+v", c)
	}
	for name, kv := range map[string][]string{
		"port zero":      {"PORT", "0"},
		"port too big":   {"PORT", "70000"},
		"port junk":      {"PORT", "http"},
		"conns zero":     {"DB_MAX_CONNS", "0"},
		"conns too few":  {"DB_MAX_CONNS", "2"},
		"conns huge":     {"DB_MAX_CONNS", "99999999999"},
		"retention zero": {"EVENTS_RETENTION_DAYS", "0"},
		"retention junk": {"EVENTS_RETENTION_DAYS", "soon"},
		"zone":           {"QUOTA_TZ", "Mars/Base"},
	} {
		if _, err := FromEnv(vars(kv...)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestAI(t *testing.T) {
	if _, err := FromEnv(vars("AI_API_KEY", "sk")); err == nil || !strings.Contains(err.Error(), "AI_PRICE") {
		t.Fatalf("a key without prices makes the ceiling meaningless: %v", err)
	}
	c := mustLoad(t, "AI_API_KEY", "sk", "AI_PRICE_INPUT_USD_PER_MTOK", "0.15", "AI_PRICE_OUTPUT_USD_PER_MTOK", "0.6",
		"FREE_BUDGET_USD_PER_DAY", "7.5", "QUOTA_TZ", "UTC", "AI_MODEL", "other/model")
	if c.AI.PriceInputUSD != 0.15 || c.AI.PriceOutputUSD != 0.6 || c.AI.FreeBudgetUSD != 7.5 || c.AI.Model != "other/model" ||
		c.QuotaLocation != time.UTC {
		t.Fatalf("%+v", c)
	}
	for name, kv := range map[string][]string{
		"bad price":   {"AI_PRICE_INPUT_USD_PER_MTOK", "cheap"},
		"negative":    {"FREE_BUDGET_USD_PER_DAY", "-1"},
		"NaN":         {"FREE_BUDGET_USD_PER_DAY", "NaN"},
		"zero budget": {"AI_API_KEY", "sk", "AI_PRICE_INPUT_USD_PER_MTOK", "1", "FREE_BUDGET_USD_PER_DAY", "0"},
		"http url":    {"AI_BASE_URL", "http://ai.example/v1"},
		"relative":    {"AI_BASE_URL", "ai.example"},
	} {
		if _, err := FromEnv(vars(kv...)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	mustLoad(t, "AI_BASE_URL", "http://127.0.0.1:9") // loopback http is for tests and local tooling
}

func TestBotWebhook(t *testing.T) {
	hook := func(kv ...string) []string {
		return append([]string{"WEBHOOK_SECRET", strings.Repeat("w", 32), "PUBLIC_URL", "https://bestregards.example/", "ADMIN_IDS", "1, 22"}, kv...)
	}
	c := mustLoad(t, hook()...)
	if c.Telegram.PublicURL != "https://bestregards.example" || len(c.Telegram.AdminIDs) != 2 || c.Telegram.AdminIDs[1] != 22 {
		t.Fatalf("%+v", c.Telegram)
	}
	if c := mustLoad(t, hook("ADMIN_IDS", "")...); len(c.Telegram.AdminIDs) != 0 {
		t.Fatalf("admins are optional: %+v", c.Telegram)
	}
	for name, kv := range map[string][]string{
		"short secret":    hook("WEBHOOK_SECRET", "short"),
		"bad secret char": hook("WEBHOOK_SECRET", strings.Repeat("w", 31)+"!"),
		"http url":        hook("PUBLIC_URL", "http://bestregards.example"),
		"url with path":   hook("PUBLIC_URL", "https://bestregards.example/app"),
		"no url":          hook("PUBLIC_URL", ""),
		"junk admin":      hook("ADMIN_IDS", "1,me"),
		"negative admin":  hook("ADMIN_IDS", "-5"),
	} {
		if _, err := FromEnv(vars(kv...)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestGuests(t *testing.T) {
	c := mustLoad(t, "TURNSTILE_SECRET", "0x4AAAA-secret", "TURNSTILE_SITE_KEY", "0x4AAAA-site")
	if !c.Auth.GuestsEnabled() || c.Auth.TurnstileSiteKey != "0x4AAAA-site" {
		t.Fatalf("%+v", c.Auth)
	}
	if !mustLoad(t, "GUESTS_WITHOUT_CHALLENGE", "1").Auth.GuestsEnabled() {
		t.Fatal("the explicit local opt-out must let guests in")
	}
	for name, kv := range map[string][]string{
		"secret without site key": {"TURNSTILE_SECRET", "0x4AAAA-secret"},
		"site key without secret": {"TURNSTILE_SITE_KEY", "0x4AAAA-site"},
	} {
		if _, err := FromEnv(vars(kv...)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestMetrics(t *testing.T) {
	if c := mustLoad(t, "METRICS_ADDR", ":9090", "METRICS_TOKEN", strings.Repeat("m", 16)); c.Metrics.Addr != ":9090" || c.Metrics.Token == "" {
		t.Fatalf("%+v", c.Metrics)
	}
	if _, err := FromEnv(vars("METRICS_TOKEN", "short")); err == nil {
		t.Error("a short metrics token must be refused")
	}
}

func TestEverySecretIsItsOwn(t *testing.T) {
	same := strings.Repeat("s", 32)
	for name, kv := range map[string][]string{
		"session = pepper":    {"SESSION_SECRET", same, "EVENTS_PEPPER", same},
		"bot token = session": {"BOT_TOKEN", same, "SESSION_SECRET", same},
		"metrics = session":   {"METRICS_TOKEN", strings.Repeat("k", 32)},
		"turnstile = session": {"TURNSTILE_SECRET", strings.Repeat("k", 32), "TURNSTILE_SITE_KEY", "site"},
	} {
		if _, err := FromEnv(vars(kv...)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestBotTokenWithStrayWhitespaceIsRefusedAtStartup(t *testing.T) {
	for name, token := range map[string]string{"newline": "1:abc\n", "space": "1:a bc", "tab": "1:abc\t", "carriage return": "1:abc\r"} {
		if _, err := FromEnv(vars("BOT_TOKEN", token)); err == nil || !strings.Contains(err.Error(), "BOT_TOKEN") {
			t.Errorf("%s: a token that would leak through an HTTP error must be refused: %v", name, err)
		}
	}
}

func TestTracingFollowsTheOTelEndpoint(t *testing.T) {
	for _, name := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"} {
		if !mustLoad(t, name, "http://collector:4318").Tracing {
			t.Errorf("%s must turn tracing on", name)
		}
	}
}

func TestPurgeNeedsOnlyTheDatabaseAndTheRetention(t *testing.T) {
	get := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	p, err := PurgeFromEnv(get(map[string]string{"DATABASE_URL": "postgres://x"}))
	if err != nil || p.DatabaseURL != "postgres://x" || p.EventsRetention != 365*24*time.Hour || p.GuestRetention != GuestRetention || p.QuotaRetention != QuotaRetention {
		t.Fatalf("defaults without any secret: %+v %v", p, err)
	}
	p, err = PurgeFromEnv(get(map[string]string{"DATABASE_URL": "postgres://x", "EVENTS_RETENTION_DAYS": "30"}))
	if err != nil || p.EventsRetention != 30*24*time.Hour {
		t.Fatalf("30 days: %+v %v", p, err)
	}
	for name, m := range map[string]map[string]string{
		"no database": {},
		"zero days":   {"DATABASE_URL": "x", "EVENTS_RETENTION_DAYS": "0"},
		"junk days":   {"DATABASE_URL": "x", "EVENTS_RETENTION_DAYS": "soon"},
	} {
		if _, err := PurgeFromEnv(get(m)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestAGuestRowOutlivesItsSession(t *testing.T) {
	if GuestRetention <= GuestTTL {
		t.Fatal("a guest row purged before its session expires would hand the visitor a new allowance")
	}
}
