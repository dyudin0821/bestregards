package turnstile

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dyudin0821/bestregards/internal/auth"
)

func fake(t *testing.T, body string, status int) (*Verifier, *http.Request) {
	t.Helper()
	var got http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		got = *r
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Verifier{Secret: "s3cret", URL: srv.URL}, &got
}

func TestCheckAcceptsASolvedChallenge(t *testing.T) {
	v, got := fake(t, `{"success":true}`, 200)
	if err := v.Check(context.Background(), "token", "203.0.113.9"); err != nil {
		t.Fatal(err)
	}
	if got.PostForm.Get("secret") != "s3cret" || got.PostForm.Get("response") != "token" || got.PostForm.Get("remoteip") != "203.0.113.9" {
		t.Fatalf("form sent: %v", got.PostForm)
	}
}

func TestCheckRejects(t *testing.T) {
	cases := map[string]struct {
		body     string
		status   int
		response string
		failed   bool // the visitor failed, as opposed to a fault on our side
	}{
		"wrong answer":   {`{"success":false,"error-codes":["invalid-input-response"]}`, 200, "token", true},
		"no answer":      {`{"success":true}`, 200, "", true},
		"oversized":      {`{"success":true}`, 200, string(make([]byte, maxResponse+1)), true},
		"our secret bad": {`{"success":false,"error-codes":["invalid-input-secret"]}`, 200, "token", false},
		"server error":   {`oops`, 500, "token", false},
		"not json":       {`<html>`, 200, "token", false},
	}
	for name, c := range cases {
		v, _ := fake(t, c.body, c.status)
		err := v.Check(context.Background(), c.response, "")
		if err == nil {
			t.Errorf("%s: want an error", name)
		} else if errors.Is(err, auth.ErrChallengeFailed) != c.failed {
			t.Errorf("%s: ErrChallengeFailed = %v, want %v (%v)", name, !c.failed, c.failed, err)
		}
	}
}

func TestCheckFailsClosedWhenCloudflareIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	err := (&Verifier{Secret: "x", URL: url}).Check(context.Background(), "token", "")
	if err == nil || errors.Is(err, auth.ErrChallengeFailed) {
		t.Fatalf("err = %v, want a non-challenge error", err)
	}
}
