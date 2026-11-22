package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ctxPinger fails when its context is already cancelled, like pgx does.
type ctxPinger struct{ sawCancelled bool }

func (p *ctxPinger) Ping(ctx context.Context) error {
	if ctx.Err() != nil {
		p.sawCancelled = true
		return ctx.Err()
	}
	return nil
}

func TestReadinessIsNotPoisonedByAProbeThatHangsUp(t *testing.T) {
	s, _ := newTestServer(t)
	p := &ctxPinger{}
	s.Ready = p

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the prober that triggered the refresh is already gone
	req := httptest.NewRequest("GET", "/readyz", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if p.sawCancelled {
		t.Fatal("the database ping must not inherit the caller's cancellation")
	}
	if got := do(s, "GET", "/readyz", ""); got.Code != http.StatusOK {
		t.Fatalf("the next probe must see a healthy database: %d", got.Code)
	}
}
