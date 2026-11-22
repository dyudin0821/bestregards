package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dyudin0821/bestregards/internal/user"
)

type fakeReader struct {
	Reader     // unused methods panic, which would show a test touching more than it should
	from, to   time.Time
	spent      int64
	guestSpent int64
	guestsOff  bool
	stopped    bool
	users      map[int64]user.User
}

func (f *fakeReader) Activity(_ context.Context, from, to time.Time) (Activity, error) {
	f.from, f.to = from, to
	return Activity{Pair: 3, AI: 1, Copied: 1}, nil
}
func (f *fakeReader) BudgetSpent(context.Context, time.Time) (int64, error) { return f.spent, nil }
func (f *fakeReader) GuestBudgetSpent(context.Context, time.Time) (int64, error) {
	return f.guestSpent, nil
}
func (f *fakeReader) GuestsOff(context.Context) (bool, error)   { return f.guestsOff, nil }
func (f *fakeReader) FreeStopped(context.Context) (bool, error) { return f.stopped, nil }
func (f *fakeReader) GetUser(_ context.Context, id int64) (user.User, error) {
	u, ok := f.users[id]
	if !ok {
		return user.User{}, user.ErrNotFound
	}
	return u, nil
}
func (f *fakeReader) QuotaUsed(context.Context, int64, time.Time) (int, error) { return 2, nil }

func newService(f *fakeReader, hour int) *Service {
	moscow := time.FixedZone("MSK", 3*3600)
	now := time.Date(2026, 11, 15, hour, 30, 0, 0, moscow)
	return New(f, Config{CeilingMicro: 24_000_000, Location: moscow, Now: func() time.Time { return now }})
}

func TestSummaryCoversTheQuotaDayInItsOwnZone(t *testing.T) {
	f := &fakeReader{spent: 300_000}
	s, err := newService(f, 1).Summary(context.Background()) // 01:30 in Moscow is still the previous day in UTC
	if err != nil {
		t.Fatal(err)
	}
	if f.from.Hour() != 0 || f.from.Day() != 15 || f.to.Sub(f.from) != 24*time.Hour || f.from.Location().String() != "MSK" {
		t.Fatalf("window %v .. %v", f.from, f.to)
	}
	if s.Done() != 4 || s.CopiedPercent() != 25 || s.SpentMicro != 300_000 {
		t.Fatalf("%+v", s)
	}
	if (Activity{}).CopiedPercent() != 0 {
		t.Fatal("no translations must not divide by zero")
	}
}

func TestBudgetIsExhaustedWhenSpentReachesTheHourlyCap(t *testing.T) {
	f := &fakeReader{spent: 3_000_000}
	b, err := newService(f, 2).Budget(context.Background()) // hour 2: 3 of 24 hours of $24 = $3
	if err != nil {
		t.Fatal(err)
	}
	if b.AllowedMicro != 3_000_000 || !b.Exhausted() || b.ResetsAt.Day() != 16 {
		t.Fatalf("%+v", b)
	}
	f.spent = 2_999_999
	if b, _ = newService(f, 2).Budget(context.Background()); b.Exhausted() {
		t.Fatal("one micro-dollar below the cap is not exhausted")
	}
}

func TestBudgetReportsTheGuestsShareAndTheirSwitch(t *testing.T) {
	f := &fakeReader{spent: 2_000_000, guestSpent: 900_000, guestsOff: true}
	b, err := newService(f, 5).Budget(context.Background()) // hour 5: 6 of 24 hours of $24 = $6
	if err != nil {
		t.Fatal(err)
	}
	if !b.GuestsOff || b.GuestSpentMicro != 900_000 || b.GuestAllowedMicro != 2_400_000 { // 40% of $6
		t.Fatalf("%+v", b)
	}
}

func TestBudgetReportsTheManualStop(t *testing.T) {
	f := &fakeReader{stopped: true}
	b, err := newService(f, 5).Budget(context.Background())
	if err != nil || !b.FreeStopped {
		t.Fatalf("%+v %v", b, err)
	}
}

func TestUserCard(t *testing.T) {
	f := &fakeReader{users: map[int64]user.User{42: {ID: 42, Source: "tt_x"}, -3: {ID: -3}}}
	svc := newService(f, 12)
	c, err := svc.User(context.Background(), 42)
	if err != nil || c.FreeUsed != 2 || c.FreeLimit != 5 {
		t.Fatalf("%+v %v", c, err)
	}
	if g, err := svc.User(context.Background(), -3); err != nil || g.FreeLimit != 3 {
		t.Fatalf("a guest has the smaller allowance: %+v %v", g, err)
	}
	if _, err := svc.User(context.Background(), 43); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
}

func TestUserCardMarksAnUnlimitedOperator(t *testing.T) {
	f := &fakeReader{users: map[int64]user.User{42: {ID: 42}}}
	svc := New(f, Config{Unlimited: map[int64]bool{42: true}})
	if c, err := svc.User(context.Background(), 42); err != nil || !c.Unlimited {
		t.Fatalf("%+v %v", c, err)
	}
}
