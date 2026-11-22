package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/dyudin0821/bestregards/internal/auth"
	"github.com/dyudin0821/bestregards/internal/events"
	"github.com/dyudin0821/bestregards/internal/limits"
	"github.com/dyudin0821/bestregards/internal/pairs"
	"github.com/dyudin0821/bestregards/internal/session"
	"github.com/dyudin0821/bestregards/internal/tgauth"
	"github.com/dyudin0821/bestregards/internal/translate"
	"github.com/dyudin0821/bestregards/internal/user"
)

const botToken = "123456:TEST-TOKEN"

var fixedNow = time.Unix(1_800_000_000, 0)

type fakeUsers struct {
	users     map[int64]user.User
	pingErr   error
	guestsOff bool
}

func (f *fakeUsers) GuestsOff(context.Context) (bool, error) { return f.guestsOff, nil }

func (f *fakeUsers) UpsertUser(_ context.Context, id int64, source string) (user.User, error) {
	u, ok := f.users[id]
	if !ok {
		u = user.User{ID: id, Source: source, CreatedAt: fixedNow}
		f.users[id] = u
	}
	return u, nil
}

func (f *fakeUsers) CreateGuest(context.Context) (user.User, error) {
	u := user.User{ID: -int64(len(f.users) + 1), CreatedAt: fixedNow}
	f.users[u.ID] = u
	return u, nil
}

func (f *fakeUsers) GetUser(_ context.Context, id int64) (user.User, error) {
	if u, ok := f.users[id]; ok {
		return u, nil
	}
	return user.User{}, user.ErrNotFound
}

func (f *fakeUsers) Ping(context.Context) error { return f.pingErr }

// fakeBooks keeps quota and budget in memory.
type fakeBooks struct {
	used  map[int64]int
	spent int64
}

func (f *fakeBooks) ReserveQuota(_ context.Context, id int64, _ time.Time, limit int) (int, bool, error) {
	if f.used[id] >= limit {
		return 0, false, nil
	}
	f.used[id]++
	return f.used[id], true, nil
}
func (f *fakeBooks) RefundQuota(_ context.Context, id int64, _ time.Time) error {
	f.used[id]--
	return nil
}
func (f *fakeBooks) QuotaUsed(_ context.Context, id int64, _ time.Time) (int, error) {
	return f.used[id], nil
}
func (f *fakeBooks) ReserveBudget(_ context.Context, _ time.Time, c translate.Claim) (bool, error) {
	if f.spent+c.Micro > c.Cap {
		return false, nil
	}
	f.spent += c.Micro
	return true, nil
}
func (f *fakeBooks) AddBudgetSpend(_ context.Context, _ time.Time, micro int64, _ bool) error {
	f.spent += micro
	return nil
}

type fakeAI struct {
	err  error
	text string
}

func (f *fakeAI) Complete(context.Context, translate.Request) (translate.Completion, error) {
	return translate.Completion{Text: f.text, Usage: translate.Usage{InputTokens: 100, OutputTokens: 50}}, f.err
}

type fixtures struct {
	users  *fakeUsers
	books  *fakeBooks
	ai     *fakeAI
	events *eventSpy
}

// eventSpy collects recorded events.
type eventSpy struct{ got []events.Event }

func (e *eventSpy) Record(ev events.Event) { e.got = append(e.got, ev) }

func newTestServer(t *testing.T) (*Server, *fakeUsers) {
	s, f := newTestServerWith(t, true)
	return s, f.users
}

func newTestServerWith(t *testing.T, aiOn bool) (*Server, fixtures) {
	t.Helper()
	catalog, err := pairs.Load()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := session.New([]byte(strings.Repeat("k", 32)), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	users := &fakeUsers{users: map[int64]user.User{}}
	f := fixtures{users: users, books: &fakeBooks{used: map[int64]int{}}, ai: &fakeAI{text: "Переведено."}, events: &eventSpy{}}
	now := func() time.Time { return fixedNow }
	tcfg := translate.Config{
		Pairs: catalog, Quotas: f.books, Budget: f.books, Location: time.UTC, Now: now, Events: f.events,
		Prices: limits.Prices{InputPerMtok: 1, OutputPerMtok: 1}, DailyBudgetMicro: 24_000_000,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if aiOn {
		tcfg.AI = f.ai
	}
	translator, err := translate.New(tcfg)
	if err != nil {
		t.Fatal(err)
	}
	web := fstest.MapFS{
		"landing.html":    {Data: []byte("<h1>landing</h1>")},
		"privacy.html":    {Data: []byte("<h1>privacy</h1>")},
		"index.html":      {Data: []byte("<h1>app</h1>")},
		"assets/app.js":   {Data: []byte("console.log(1)")},
		"favicon.svg":     {Data: []byte("<svg/>")},
		"assets/sub/x.js": {Data: []byte("x")},
	}
	s := New(Options{
		Catalog: catalog, Translate: translator, Events: f.events, Ready: users, Web: web,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: now,
		Auth: auth.New(users, tgauth.Verifier{BotToken: botToken, MaxAge: time.Hour}, signer, now),
	})
	return s, f
}

func do(s *Server, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func initData(userID int64) string {
	return tgauth.Sign(map[string]string{
		"auth_date":   strconv.FormatInt(fixedNow.Add(-time.Minute).Unix(), 10),
		"start_param": "tt_launch",
		"user":        `{"id":` + strconv.FormatInt(userID, 10) + `,"first_name":"Анна"}`,
	}, botToken)
}

func authBody(raw string) string {
	b, _ := json.Marshal(authRequest{InitData: raw})
	return string(b)
}

func TestPairsEndpointServesCatalog(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(s, "GET", "/api/pairs", "")
	var got pairs.Catalog
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || len(got.Pairs) != 40 || len(got.Modes) != 4 {
		t.Fatalf("code=%d pairs=%d modes=%d", rec.Code, len(got.Pairs), len(got.Modes))
	}
}

func TestAuthIssuesWorkingSession(t *testing.T) {
	s, users := newTestServer(t)
	rec := do(s, "POST", "/api/auth/telegram", authBody(initData(42)))
	if rec.Code != 200 {
		t.Fatalf("auth: %d %s", rec.Code, rec.Body)
	}
	var resp authResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.User.ID != 42 || resp.User.FirstName != "Анна" || resp.Token == "" || resp.ExpiresIn != 3600 {
		t.Fatalf("unexpected response %+v", resp)
	}
	if users.users[42].Source != "tt_launch" {
		t.Errorf("source not recorded: %+v", users.users[42])
	}

	me := do(s, "GET", "/api/me", "", "Authorization", "Bearer "+resp.Token)
	if me.Code != 200 || !strings.Contains(me.Body.String(), `"id":42`) {
		t.Fatalf("me: %d %s", me.Code, me.Body)
	}
}

func TestAuthRejects(t *testing.T) {
	s, users := newTestServer(t)
	users.users[7] = user.User{ID: 7, Banned: true}
	forged := strings.Replace(initData(42), "42", "43", 1)

	cases := []struct {
		name, body string
		want       int
	}{
		{"empty body", "", 400},
		{"not json", "x", 400},
		{"missing initData", `{}`, 400},
		{"forged", authBody(forged), 401},
		{"banned", authBody(initData(7)), 403},
	}
	for _, c := range cases {
		if rec := do(s, "POST", "/api/auth/telegram", c.body); rec.Code != c.want {
			t.Errorf("%s: status %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body)
		}
	}
}

func TestAuthIsRateLimitedPerIP(t *testing.T) {
	s, _ := newTestServer(t)
	var limited int
	for range 15 {
		if do(s, "POST", "/api/auth/telegram", "x").Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("expected some requests to be rate limited")
	}
	req := httptest.NewRequest("POST", "/api/auth/telegram", strings.NewReader("x"))
	req.RemoteAddr = "203.0.113.9:1234"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code == http.StatusTooManyRequests {
		t.Fatal("another client must not be limited")
	}
}

func TestMeRejectsBadSessions(t *testing.T) {
	s, users := newTestServer(t)
	signer, _ := session.New([]byte(strings.Repeat("k", 32)), time.Hour)
	users.users[7] = user.User{ID: 7, Banned: true}
	expired := signer.Issue(42, fixedNow.Add(-2*time.Hour))

	cases := []struct {
		name, header string
		want         int
	}{
		{"no header", "", 401},
		{"not bearer", "Basic abc", 401},
		{"garbage", "Bearer nope", 401},
		{"expired", "Bearer " + expired, 401},
		{"unknown user", "Bearer " + signer.Issue(99, fixedNow), 401},
		{"banned user", "Bearer " + signer.Issue(7, fixedNow), 403},
	}
	for _, c := range cases {
		var rec *httptest.ResponseRecorder
		if c.header == "" {
			rec = do(s, "GET", "/api/me", "")
		} else {
			rec = do(s, "GET", "/api/me", "", "Authorization", c.header)
		}
		if rec.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, rec.Code, c.want)
		}
	}
}

func TestReadyReflectsDatabaseButPingsItOnlyOncePerInterval(t *testing.T) {
	s, users := newTestServer(t)
	clock := fixedNow
	s.Now = func() time.Time { return clock }
	if rec := do(s, "GET", "/readyz", ""); rec.Code != 200 {
		t.Fatalf("ready: %d", rec.Code)
	}
	users.pingErr = errors.New("down")
	if rec := do(s, "GET", "/readyz", ""); rec.Code != 200 {
		t.Fatalf("a probe within the interval must be answered from the last ping: %d", rec.Code)
	}
	clock = clock.Add(readyTTL)
	if rec := do(s, "GET", "/readyz", ""); rec.Code != 503 {
		t.Fatalf("not ready: %d", rec.Code)
	}
	if rec := do(s, "GET", "/healthz", ""); rec.Code != 200 {
		t.Fatalf("liveness must not depend on the database: %d", rec.Code)
	}
}

func TestWebRoutes(t *testing.T) {
	s, _ := newTestServer(t)
	cases := []struct {
		path string
		want int
		body string
	}{
		{"/", 200, "landing"},
		{"/app", 200, "app"},
		{"/privacy", 200, "privacy"},
		{"/privacy.html", 404, ""},
		{"/app/history", 200, "app"},
		{"/assets/app.js", 200, "console.log"},
		{"/favicon.svg", 200, "<svg/>"},
		{"/assets/", 404, ""},
		{"/assets/sub", 404, ""},
		{"/index.html", 404, ""},
		{"/landing.html", 404, ""},
		{"/missing", 404, ""},
		{"/api/unknown", 404, `"error"`},
	}
	for _, c := range cases {
		rec := do(s, "GET", c.path, "")
		if rec.Code != c.want || !strings.Contains(rec.Body.String(), c.body) {
			t.Errorf("%s: status %d body %q, want %d containing %q", c.path, rec.Code, rec.Body, c.want, c.body)
		}
	}
	if cc := do(s, "GET", "/assets/app.js", "").Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("assets Cache-Control = %q", cc)
	}
	if cc := do(s, "GET", "/app", "").Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("app shell Cache-Control = %q", cc)
	}
}

func TestSecurityHeaders(t *testing.T) {
	s, _ := newTestServer(t)
	h := do(s, "GET", "/api/pairs", "").Header()
	if !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'self' https://web.telegram.org") {
		t.Error("CSP must allow Telegram Web to embed the app")
	}
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing nosniff")
	}
	csp := h.Get("Content-Security-Policy")
	if strings.Contains(csp, "unsafe-inline") || !strings.Contains(csp, "form-action 'none'") {
		t.Errorf("CSP must forbid inline styles and form posts: %s", csp)
	}
	if !strings.HasPrefix(h.Get("Strict-Transport-Security"), "max-age=") || !strings.Contains(h.Get("Permissions-Policy"), "camera=()") {
		t.Errorf("missing HSTS or Permissions-Policy: %v", h)
	}
}

func TestMeIsRateLimited(t *testing.T) {
	s, _ := newTestServer(t)
	limited := false
	for range 200 {
		if do(s, "GET", "/api/me", "").Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("/api/me must be rate limited per IP")
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 198.51.100.4")
	if got := clientIP(r, false); got != "10.0.0.1" {
		t.Errorf("untrusted proxy: %s", got)
	}
	if got := clientIP(r, true); got != "198.51.100.4" {
		t.Errorf("trusted proxy must use the last hop, got %s", got)
	}
}

func TestClientIPKeysIPv6ByItsBlockAndIgnoresJunk(t *testing.T) {
	key := func(remote, xff string, trust bool) string {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return clientIP(r, trust)
	}
	a := key("[2001:db8:1:2:aaaa::1]:1", "", false)
	b := key("[2001:db8:1:2:bbbb::9]:1", "", false)
	if a != b || a != "2001:db8:1:2::/64" {
		t.Errorf("one /64 must be one bucket: %s vs %s", a, b)
	}
	if other := key("[2001:db8:1:3::1]:1", "", false); other == a {
		t.Error("a different /64 is a different bucket")
	}
	if got := key("[2001:db8::1]:1", "::ffff:198.51.100.7", true); got != "198.51.100.7" {
		t.Errorf("IPv4-mapped must collapse to IPv4: %s", got)
	}
	// A forged header that is not an address must not become a bucket of its own.
	if got := key("10.0.0.1:5", "x-"+strings.Repeat("a", 50), true); got != "10.0.0.1" {
		t.Errorf("junk header must fall back to the connection address: %s", got)
	}
	// A proxy that adds its own header line must not let the client pick its key through an earlier line.
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:5"
	r.Header.Add("X-Forwarded-For", "6.6.6.6")
	r.Header.Add("X-Forwarded-For", "203.0.113.9")
	if got := clientIP(r, true); got != "203.0.113.9" {
		t.Errorf("the last line must win: %s", got)
	}
}

func TestClientAddrKeepsTheWholeIPv6AddressForTurnstile(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "[2001:db8:1:2:aaaa::1]:1"
	addr, ok := clientAddr(r, false)
	if !ok || addr.String() != "2001:db8:1:2:aaaa::1" {
		t.Fatalf("the challenge needs a real address, not the limiter's /64 key: %v %v", addr, ok)
	}
}

func TestLimiterTableIsBoundedAndNewcomersAreStillServedWhenItIsFull(t *testing.T) {
	l := newIPLimiter(60, 1)
	now := fixedNow
	for i := 0; i < maxLimiterKeys+500; i++ {
		l.allow(fmt.Sprintf("k%d", i), now)
	}
	if len(l.clients) != maxLimiterKeys {
		t.Fatalf("table size %d, want the cap %d", len(l.clients), maxLimiterKeys)
	}
	// A flood of new keys must not push other newcomers onto one shared bucket.
	passed := 0
	for i := 0; i < 50; i++ {
		if l.allow(fmt.Sprintf("late%d", i), now) {
			passed++
		}
	}
	if passed != 50 || len(l.clients) != maxLimiterKeys {
		t.Fatalf("%d of 50 newcomers passed, table %d", passed, len(l.clients))
	}
	// Idle entries are dropped by a later sweep and the table accepts keys again.
	later := now.Add(l.ttl + 2*sweepInterval)
	if !l.allow("fresh", later) || len(l.clients) != 1 {
		t.Fatalf("after the sweep: %d keys", len(l.clients))
	}
}

func TestEvictionTakesTheLeastRecentlySeenOfTheSample(t *testing.T) {
	l := newIPLimiter(60, 1)
	l.clients["old"] = &client{limiter: nil, seen: fixedNow.Add(-time.Hour)}
	for i := 0; i < evictSample-1; i++ {
		l.clients[fmt.Sprintf("new%d", i)] = &client{seen: fixedNow}
	}
	l.evictOne() // the sample covers the whole table here
	if _, ok := l.clients["old"]; ok || len(l.clients) != evictSample-1 {
		t.Fatalf("the oldest entry must go: %d entries left", len(l.clients))
	}
}

// bearer logs a user in and returns the Authorization header value.
func bearer(t *testing.T, s *Server, userID int64) string {
	t.Helper()
	rec := do(s, "POST", "/api/auth/telegram", authBody(initData(userID)))
	var resp authResponse
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &resp) != nil {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	return "Bearer " + resp.Token
}

func postTranslate(s *Server, auth, body string) *httptest.ResponseRecorder {
	if auth == "" {
		return do(s, "POST", "/api/translate", body)
	}
	return do(s, "POST", "/api/translate", body, "Authorization", auth)
}

const ownText = `{"text":"Это не мой баг","mode":"colleagues","level":2}`

func TestTranslateEndToEnd(t *testing.T) {
	s, f := newTestServerWith(t, true)
	auth := bearer(t, s, 42)

	rec := postTranslate(s, auth, ownText)
	var got translateResponse
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got.Text != "Переведено." || got.Source != "ai" || got.Quota.Used != 1 || got.Quota.Remaining != 2 || got.Quota.Limit != 3 {
		t.Fatalf("unexpected response %+v", got) // user 42 comes from an ad (tt_launch) and is new: 3 on day one
	}
	if f.books.spent != 150 {
		t.Errorf("spend = %d, want 150 (100 in + 50 out at $1/Mtok)", f.books.spent)
	}

	me := do(s, "GET", "/api/me", "", "Authorization", auth)
	if !strings.Contains(me.Body.String(), `"aiEnabled":true`) || !strings.Contains(me.Body.String(), `"used":1`) {
		t.Errorf("/api/me: %s", me.Body)
	}

	p := s.Catalog.Pairs[0]
	pairBody, _ := json.Marshal(translateRequest{Text: p.Input, Mode: p.Mode, Level: 1})
	rec = postTranslate(s, auth, string(pairBody))
	json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || got.Source != "pair" || got.Text != p.Output || got.Quota.Used != 1 {
		t.Fatalf("ready pair must be free: %d %+v", rec.Code, got)
	}
}

func TestTranslateErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(f fixtures)
		aiOn     bool
		auth     string // "" = no header, "ok" = log in first
		body     string
		status   int
		code     string
		wantUsed int // quota used afterwards: failures must give the quota back
	}{
		{"no session", nil, true, "", ownText, 401, "", 0},
		{"garbage session", nil, true, "Bearer nope", ownText, 401, "", 0},
		{"not json", nil, true, "ok", "{", 400, "invalid", 0},
		{"empty text", nil, true, "ok", `{"text":"  ","mode":"hr","level":1}`, 400, "invalid", 0},
		{"bad level", nil, true, "ok", `{"text":"x","mode":"hr","level":9}`, 400, "invalid", 0},
		{"quota used up", func(f fixtures) { f.books.used[42] = 3 }, true, "ok", ownText, 429, "quota_exhausted", 3},
		{"budget used up", func(f fixtures) { f.books.spent = 24_000_000 }, true, "ok", ownText, 503, "budget_exhausted", 0},
		{"provider down", func(f fixtures) { f.ai.err = errors.New("502") }, true, "ok", ownText, 502, "unavailable", 0},
		{"ai not configured", nil, false, "ok", ownText, 503, "ai_disabled", 0},
	}
	for _, c := range cases {
		s, f := newTestServerWith(t, c.aiOn)
		if c.setup != nil {
			c.setup(f)
		}
		header := c.auth
		if header == "ok" {
			header = bearer(t, s, 42)
		}
		rec := postTranslate(s, header, c.body)
		var body struct {
			Code  string     `json:"code"`
			Error string     `json:"error"`
			Quota *quotaJSON `json:"quota"`
		}
		json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != c.status || body.Code != c.code || body.Error == "" {
			t.Errorf("%s: status %d code %q error %q, want %d %q", c.name, rec.Code, body.Code, body.Error, c.status, c.code)
		}
		if c.code == "quota_exhausted" && (body.Quota == nil || body.Quota.Remaining != 0 || body.Quota.ResetsAt.IsZero()) {
			t.Errorf("%s: quota missing from the error: %+v", c.name, body.Quota)
		}
		if f.books.used[42] != c.wantUsed {
			t.Errorf("%s: quota used = %d, want %d", c.name, f.books.used[42], c.wantUsed)
		}
	}
}

func TestTranslateIsRateLimitedPerIP(t *testing.T) {
	s, _ := newTestServerWith(t, true)
	var limited int
	for range 15 {
		if postTranslate(s, "", ownText).Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("expected some requests to be rate limited")
	}
}

func TestTranslateRejectsOversizedBodies(t *testing.T) {
	s, _ := newTestServerWith(t, true)
	auth := bearer(t, s, 42)
	huge := `{"text":"` + strings.Repeat("я", 20_000) + `","mode":"hr","level":1}`
	if rec := postTranslate(s, auth, huge); rec.Code != http.StatusBadRequest {
		t.Errorf("a body over the limit must be refused with 400, got %d", rec.Code)
	}
}
