package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/dyudin0821/bestregards/internal/events"
)

func postEvents(s *Server, auth, body string) int {
	if auth == "" {
		return do(s, "POST", "/api/events", body).Code
	}
	return do(s, "POST", "/api/events", body, "Authorization", auth).Code
}

const copiedAI = `{"type":"copied","kind":"ai","mode":"hr","level":2,"resultId":"abcdef0123456789"}`

func TestClientEventsFromAnAnonymousVisitor(t *testing.T) {
	s, f := newTestServerWith(t, true)
	body := `{"events":[{"type":"translate_done","kind":"pair","mode":"boss","level":3,"resultId":"0123456789abcdef"},` + copiedAI + `]}`
	if code := postEvents(s, "", body); code != http.StatusNoContent {
		t.Fatalf("status %d", code)
	}
	if len(f.events.got) != 2 {
		t.Fatalf("events: %+v", f.events.got)
	}
	for _, ev := range f.events.got {
		if ev.Source != events.SourceWeb || ev.UserID != 0 || ev.Campaign != "" {
			t.Errorf("an anonymous event must carry no user: %+v", ev)
		}
	}
	if ev := f.events.got[0]; ev.Type != events.TranslateDone || ev.Kind != events.KindPair || ev.Mode != "boss" || ev.Level != 3 {
		t.Errorf("first event: %+v", ev)
	}
}

func TestClientEventsFromALoggedInUserCarryUserAndCampaign(t *testing.T) {
	s, f := newTestServerWith(t, true)
	auth := bearer(t, s, 42) // initData in the tests starts every user from tt_launch
	if code := postEvents(s, auth, `{"events":[`+copiedAI+`]}`); code != http.StatusNoContent {
		t.Fatalf("status %d", code)
	}
	ev := f.events.got[0]
	if ev.UserID != 42 || ev.Campaign != "tt_launch" || ev.Source != events.SourceMiniApp {
		t.Fatalf("event %+v", ev)
	}
	// A broken session must not fail the request: the event is recorded as anonymous.
	f.events.got = nil
	if code := postEvents(s, "Bearer garbage", `{"events":[`+copiedAI+`]}`); code != http.StatusNoContent {
		t.Fatalf("status %d", code)
	}
	if ev := f.events.got[0]; ev.UserID != 0 || ev.Source != events.SourceWeb {
		t.Fatalf("event %+v", ev)
	}
}

func TestClientEventsAreValidatedAndNothingIsStoredFromABadBatch(t *testing.T) {
	s, f := newTestServerWith(t, true)
	good := copiedAI
	cases := map[string]string{
		"not json":                    `{`,
		"empty list":                  `{"events":[]}`,
		"unknown type":                `{"events":[{"type":"quota_exhausted","kind":"ai","mode":"hr","level":1,"resultId":"abcdef0123456789"}]}`,
		"server-only type":            `{"events":[{"type":"budget_exhausted","kind":"","mode":"hr","level":1,"resultId":"abcdef0123456789"}]}`,
		"AI done is the server's job": `{"events":[{"type":"translate_done","kind":"ai","mode":"hr","level":1,"resultId":"abcdef0123456789"}]}`,
		"bad kind":                    `{"events":[{"type":"copied","kind":"x","mode":"hr","level":1,"resultId":"abcdef0123456789"}]}`,
		"bad mode":                    `{"events":[{"type":"copied","kind":"ai","mode":"zzz","level":1,"resultId":"abcdef0123456789"}]}`,
		"bad level":                   `{"events":[{"type":"copied","kind":"ai","mode":"hr","level":9,"resultId":"abcdef0123456789"}]}`,
		"bad result id":               `{"events":[{"type":"copied","kind":"ai","mode":"hr","level":1,"resultId":"BAD id"}]}`,
		"one bad among good":          `{"events":[` + good + `,{"type":"copied","kind":"ai","mode":"hr","level":1,"resultId":""}]}`,
		"too many":                    `{"events":[` + strings.TrimSuffix(strings.Repeat(good+",", 11), ",") + `]}`,
		"oversized":                   `{"events":[` + good + `],"pad":"` + strings.Repeat("x", 9000) + `"}`,
	}
	for name, body := range cases {
		if code := postEvents(s, "", body); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, code)
		}
	}
	if len(f.events.got) != 0 {
		t.Fatalf("a rejected request must record nothing, got %+v", f.events.got)
	}
}

func TestClientEventsAreRateLimitedPerIP(t *testing.T) {
	s, _ := newTestServerWith(t, true)
	limited := 0
	for range 40 {
		if postEvents(s, "", `{"events":[`+copiedAI+`]}`) == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("expected some requests to be rate limited")
	}
}

func TestTranslateResponseCarriesTheResultIDOfTheRecordedEvent(t *testing.T) {
	s, f := newTestServerWith(t, true)
	auth := bearer(t, s, 42)
	rec := postTranslate(s, auth, ownText)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(f.events.got) != 1 {
		t.Fatalf("events: %+v", f.events.got)
	}
	ev := f.events.got[0]
	if !strings.Contains(rec.Body.String(), `"resultId":"`+ev.ResultID+`"`) || ev.Type != events.TranslateDone || ev.Kind != events.KindAI || ev.Source != events.SourceMiniApp {
		t.Fatalf("event %+v does not match the response %s", ev, rec.Body)
	}
}
