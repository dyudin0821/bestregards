package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dyudin0821/bestregards/internal/auth"
)

// fakeChallenge accepts only the answer "human".
type fakeChallenge struct {
	err error
	ip  string
}

func (f *fakeChallenge) Check(_ context.Context, response, ip string) error {
	f.ip = ip
	if f.err != nil {
		return f.err
	}
	if response != "human" {
		return auth.ErrChallengeFailed
	}
	return nil
}

func guestServer(t *testing.T) (*Server, fixtures, *fakeChallenge) {
	t.Helper()
	s, f := newTestServerWith(t, true)
	c := &fakeChallenge{}
	s.Auth.RequireChallenge(c)
	s.Guests, s.TurnstileSiteKey = true, "site-key"
	return s, f, c
}

func guestLogin(s *Server, answer string) *httpResult {
	rec := do(s, "POST", "/api/auth/guest", `{"challenge":"`+answer+`"}`)
	return &httpResult{rec.Code, rec.Body.String()}
}

type httpResult struct {
	Code int
	Body string
}

func TestGuestGetsASessionThatWorks(t *testing.T) {
	s, f, c := guestServer(t)
	rec := do(s, "POST", "/api/auth/guest", `{"challenge":"human"}`, "X-Forwarded-For", "203.0.113.7")
	var resp authResponse
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &resp) != nil || resp.User.ID >= 0 || resp.Token == "" {
		t.Fatalf("guest login: %d %s", rec.Code, rec.Body)
	}
	if c.ip == "" {
		t.Error("the challenge must be told the visitor's address")
	}
	header := "Bearer " + resp.Token

	me := do(s, "GET", "/api/me", "", "Authorization", header)
	if me.Code != 200 || !strings.Contains(me.Body.String(), `"guest":true`) || !strings.Contains(me.Body.String(), `"limit":3`) {
		t.Fatalf("a guest is announced with the smaller allowance: %d %s", me.Code, me.Body)
	}

	for i := 1; i <= 3; i++ {
		if rec := postTranslate(s, header, ownText); rec.Code != 200 {
			t.Fatalf("guest translation %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	rec = postTranslate(s, header, ownText)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "quota_exhausted") {
		t.Fatalf("the fourth must stop at the guest allowance: %d %s", rec.Code, rec.Body)
	}
	for _, ev := range f.events.got {
		if ev.Source != "web" {
			t.Errorf("a guest's events belong to the web channel: %+v", ev)
		}
	}
}

func TestGuestLoginIsRefusedWithoutTheChallenge(t *testing.T) {
	s, f, c := guestServer(t)
	before := len(f.users.users)
	for name, body := range map[string]string{"wrong answer": `{"challenge":"robot"}`, "no answer": `{}`} {
		rec := do(s, "POST", "/api/auth/guest", body)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "challenge_failed") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if len(f.users.users) != before {
		t.Fatal("a failed challenge must not create an account")
	}

	c.err = errors.New("cloudflare is down")
	if rec := do(s, "POST", "/api/auth/guest", `{"challenge":"human"}`); rec.Code != http.StatusInternalServerError {
		t.Errorf("a challenge that cannot be checked fails closed: %d", rec.Code)
	}
	if rec := do(s, "POST", "/api/auth/guest", "{"); rec.Code != http.StatusBadRequest {
		t.Errorf("garbage body: %d", rec.Code)
	}
}

func TestChallengeGetsTheRealIPv6AddressNotTheLimiterKey(t *testing.T) {
	s, _, c := guestServer(t)
	req := httptest.NewRequest("POST", "/api/auth/guest", strings.NewReader(`{"challenge":"human"}`))
	req.RemoteAddr = "[2001:db8:1:2:aaaa::1]:4000"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 200 || c.ip != "2001:db8:1:2:aaaa::1" {
		t.Fatalf("status %d, challenge saw %q", rec.Code, c.ip)
	}
}

func TestGuestsAreOffUntilEnabled(t *testing.T) {
	s, _ := newTestServer(t)
	if got := guestLogin(s, "human"); got.Code != http.StatusNotFound || !strings.Contains(got.Body, "guests_disabled") {
		t.Fatalf("%+v", got)
	}
	cfg := do(s, "GET", "/api/config", "")
	if cfg.Code != 200 || !strings.Contains(cfg.Body.String(), `"guests":false`) {
		t.Fatalf("config: %d %s", cfg.Code, cfg.Body)
	}
}

func TestAnOperatorCanClosePublicSignInWithoutLockingOutExistingGuests(t *testing.T) {
	s, f, _ := guestServer(t)
	first := do(s, "POST", "/api/auth/guest", `{"challenge":"human"}`)
	var resp authResponse
	if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &resp) != nil {
		t.Fatalf("guest login: %d %s", first.Code, first.Body)
	}

	f.users.guestsOff = true
	before := len(f.users.users)
	rec := do(s, "POST", "/api/auth/guest", `{"challenge":"human"}`)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "guests_disabled") {
		t.Fatalf("new guests are refused with a code the page understands: %d %s", rec.Code, rec.Body)
	}
	if len(f.users.users) != before {
		t.Fatal("no account may be created while guests are off")
	}
	if me := do(s, "GET", "/api/me", "", "Authorization", "Bearer "+resp.Token); me.Code != 200 {
		t.Fatalf("a guest who is already in keeps their session: %d", me.Code)
	}
}

func TestPublicConfigTellsThePageHowToLetGuestsIn(t *testing.T) {
	s, _, _ := guestServer(t)
	cfg := do(s, "GET", "/api/config", "")
	if !strings.Contains(cfg.Body.String(), `"guests":true`) || !strings.Contains(cfg.Body.String(), `"turnstileSiteKey":"site-key"`) {
		t.Fatalf("config: %d %s", cfg.Code, cfg.Body)
	}
}

func TestGuestLoginIsRateLimitedPerIP(t *testing.T) {
	s, _, _ := guestServer(t)
	limited := 0
	for range 20 {
		if guestLogin(s, "human").Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("minting guests must be rate limited per IP")
	}
}

func TestChallengeSourcesAreAllowedByTheCSP(t *testing.T) {
	s, _ := newTestServer(t)
	csp := do(s, "GET", "/api/pairs", "").Header().Get("Content-Security-Policy")
	for _, want := range []string{"script-src 'self' https://telegram.org https://challenges.cloudflare.com", "frame-src https://challenges.cloudflare.com"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q: %s", want, csp)
		}
	}
}
