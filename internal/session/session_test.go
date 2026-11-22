package session

import (
	"strings"
	"testing"
	"time"
)

var (
	secret = []byte(strings.Repeat("s", 32))
	now    = time.Unix(1_800_000_000, 0)
)

func TestRoundTrip(t *testing.T) {
	s, err := New(secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Verify(s.Issue(42, now), now.Add(time.Minute))
	if err != nil || id != 42 {
		t.Fatalf("id=%d err=%v", id, err)
	}
}

func TestVerifyRejects(t *testing.T) {
	s, _ := New(secret, time.Hour)
	other, _ := New([]byte(strings.Repeat("o", 32)), time.Hour)
	body, _, _ := strings.Cut(s.Issue(42, now), ".")

	cases := map[string]struct {
		token string
		at    time.Time
	}{
		"expired":      {s.Issue(42, now), now.Add(time.Hour)},
		"other secret": {other.Issue(42, now), now},
		"forged sig":   {body + "." + strings.Repeat("A", 43), now},
		"no dot":       {"abc", now},
		"empty":        {"", now},
	}
	for name, c := range cases {
		if _, err := s.Verify(c.token, c.at); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestNewRejectsShortSecret(t *testing.T) {
	if _, err := New([]byte("short"), time.Hour); err == nil {
		t.Fatal("expected error")
	}
}

func TestGuestTokensLiveLonger(t *testing.T) {
	s, _ := New(secret, time.Hour)
	s.WithGuestTTL(48 * time.Hour)
	later := now.Add(24 * time.Hour)
	if _, err := s.Verify(s.Issue(-5, now), later); err != nil {
		t.Fatalf("guest token must outlive the regular TTL: %v", err)
	}
	if _, err := s.Verify(s.Issue(42, now), later); err == nil {
		t.Fatal("a Telegram user's token must keep the regular TTL")
	}
}
