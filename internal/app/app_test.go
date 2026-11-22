package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dyudin0821/bestregards/internal/config"
	"github.com/dyudin0821/bestregards/internal/events"
	"github.com/dyudin0821/bestregards/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func baseEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":   os.Getenv("TEST_DATABASE_URL"),
		"BOT_TOKEN":      "123456:TEST",
		"SESSION_SECRET": strings.Repeat("s", 32),
		"EVENTS_PEPPER":  strings.Repeat("p", 32),
	}
}

func load(t *testing.T, env map[string]string) config.Config {
	t.Helper()
	cfg, err := config.FromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// fakeTelegram records the Bot API methods it is asked to run.
func fakeTelegram(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var methods []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
	}))
	t.Cleanup(ts.Close)
	return ts, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), methods...)
	}
}

// start runs Serve on free ports and returns the public and metrics base URLs and a stop function
// that reports what Serve returned.
func start(t *testing.T, env map[string]string, tweak ...func(*config.Config)) (public, metricsURL string, stop func() error) {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	pub, met := freeAddr(t), freeAddr(t)
	env["METRICS_ADDR"] = met
	cfg := load(t, env)
	cfg.Server.Addr = pub
	for _, f := range tweak {
		f(&cfg)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, cfg, quiet) }()

	deadline := time.Now().Add(15 * time.Second)
	for {
		if resp, err := http.Get("http://" + pub + "/healthz"); err == nil {
			resp.Body.Close()
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Serve returned early: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("the service did not come up")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop = func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(20 * time.Second):
			t.Fatal("Serve did not stop after its context was cancelled")
			return nil
		}
	}
	t.Cleanup(func() { cancel() })
	return "http://" + pub, "http://" + met, stop
}

func status(t *testing.T, method, url string, header ...string) int {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader("{}"))
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestServeWiresThePublicServerBotAndMetricsAndStopsCleanly(t *testing.T) {
	tg, _ := fakeTelegram(t)
	env := baseEnv()
	env["WEBHOOK_SECRET"] = strings.Repeat("w", 32)
	env["PUBLIC_URL"] = "https://bestregards.example"
	env["ADMIN_IDS"] = "777"
	env["METRICS_TOKEN"] = strings.Repeat("m", 20)
	public, metricsURL, stop := start(t, env, func(c *config.Config) { c.Telegram.APIURL = tg.URL })

	if got := status(t, "GET", public+"/api/pairs"); got != 200 {
		t.Errorf("pairs: %d", got)
	}
	if got := status(t, "GET", public+"/app"); got != 200 {
		t.Errorf("the embedded app: %d", got)
	}
	if got := status(t, "GET", public+"/api/me"); got != 401 {
		t.Errorf("an API call without a session: %d", got)
	}
	if got := status(t, "POST", public+"/bot/webhook"); got != 403 {
		t.Errorf("the bot is on, so the webhook exists and demands its secret: %d", got)
	}
	if got := status(t, "GET", public+"/metrics"); got != 404 {
		t.Errorf("metrics must never be on the public port: %d", got)
	}
	if got := status(t, "GET", metricsURL+"/metrics"); got != 401 {
		t.Errorf("metrics without the token: %d", got)
	}
	if got := status(t, "GET", metricsURL+"/metrics", "Authorization", "Bearer "+env["METRICS_TOKEN"]); got != 200 {
		t.Errorf("metrics with the token: %d", got)
	}
	if err := stop(); err != nil {
		t.Fatalf("a clean shutdown must not be an error: %v", err)
	}
}

func TestServeWithoutTheBotHasNoWebhookAndNoGuests(t *testing.T) {
	public, _, stop := start(t, baseEnv())
	if got := status(t, "POST", public+"/bot/webhook"); got != 404 {
		t.Errorf("the bot is off, so there is no webhook: %d", got)
	}
	if got := status(t, "POST", public+"/api/auth/guest"); got != 404 {
		t.Errorf("without a challenge guests stay out: %d", got)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestServeLetsGuestsInAgainstTheRealStore(t *testing.T) {
	env := baseEnv()
	env["GUESTS_WITHOUT_CHALLENGE"] = "1"
	public, _, stop := start(t, env)

	resp, err := http.Post(public+"/api/auth/guest", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		Token string `json:"token"`
		User  struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	json.NewDecoder(resp.Body).Decode(&session)
	resp.Body.Close()
	if resp.StatusCode != 200 || session.Token == "" || session.User.ID >= 0 {
		t.Fatalf("guest login: %d %+v", resp.StatusCode, session)
	}
	if got := status(t, "GET", public+"/api/me", "Authorization", "Bearer "+session.Token); got != 200 {
		t.Errorf("the guest's session must work: %d", got)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeRemovesOnlyEventsAndGuestsOlderThanTheirRetention(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, store.Config{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	const prefix = "apptestpg"
	clean := func() {
		c, err := pgx.Connect(ctx, url) // its own connection: this runs after the test's deferred Close
		if err == nil {
			c.Exec(ctx, "DELETE FROM events WHERE result_id LIKE $1", prefix+"%")
			c.Close(ctx)
		}
	}
	clean() // rows left by an earlier, interrupted run
	t.Cleanup(clean)

	row := func(id string, age time.Duration) events.Row {
		return events.Row{At: time.Now().Add(-age), Type: events.TranslateDone, Kind: events.KindPair, Source: events.SourceMiniApp, ResultID: prefix + id}
	}
	day := 24 * time.Hour
	if err := db.InsertEvents(ctx, []events.Row{row("old", 40*day), row("recent", 5*day)}); err != nil {
		t.Fatal(err)
	}
	oldGuest, err := db.CreateGuest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	freshGuest, err := db.CreateGuest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if c, err := pgx.Connect(ctx, url); err == nil {
			c.Exec(ctx, "DELETE FROM users WHERE telegram_id IN ($1, $2)", oldGuest.ID, freshGuest.ID)
			c.Close(ctx)
		}
	})
	if _, err := conn.Exec(ctx, "UPDATE users SET created_at = now() - interval '90 days' WHERE telegram_id = $1", oldGuest.ID); err != nil {
		t.Fatal(err)
	}

	if err := Purge(ctx, config.Purge{DatabaseURL: url, EventsRetention: 30 * day, GuestRetention: 45 * day}, quiet); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetUser(ctx, oldGuest.ID); err == nil {
		t.Error("the old guest must be purged")
	}
	if _, err := db.GetUser(ctx, freshGuest.ID); err != nil {
		t.Errorf("a recent guest must stay: %v", err)
	}
	var left []string
	rows, _ := conn.Query(ctx, "SELECT result_id FROM events WHERE result_id LIKE $1", prefix+"%")
	for rows.Next() {
		var id string
		rows.Scan(&id)
		left = append(left, id)
	}
	rows.Close()
	if len(left) != 1 || left[0] != prefix+"recent" {
		t.Fatalf("left %v, want only the recent event", left)
	}
}

func TestSetupWebhookRegistersTheWebhookAndClearsCommandsAndRefusesWithoutTheBot(t *testing.T) {
	tg, methods := fakeTelegram(t)
	env := baseEnv()
	env["WEBHOOK_SECRET"] = strings.Repeat("w", 32)
	env["PUBLIC_URL"] = "https://bestregards.example"
	env["ADMIN_IDS"] = "777"
	cfg := load(t, env)
	cfg.Telegram.APIURL = tg.URL
	if err := SetupWebhook(context.Background(), cfg, quiet); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(methods(), ",")
	if !strings.Contains(got, "setWebhook") || !strings.Contains(got, "deleteMyCommands") {
		t.Fatalf("Bot API calls: %s", got)
	}

	if err := SetupWebhook(context.Background(), load(t, baseEnv()), quiet); err == nil {
		t.Fatal("without a webhook secret there is nothing to register")
	}
}
