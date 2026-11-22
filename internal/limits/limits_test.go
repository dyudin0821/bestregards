package limits

import (
	"testing"
	"time"

	"github.com/dyudin0821/bestregards/internal/user"
)

var moscow = time.FixedZone("MSK", 3*3600)

func at(day, hour, min int) time.Time { return time.Date(2026, 11, day, hour, min, 0, 0, moscow) }

func TestDayUsesTheGivenLocation(t *testing.T) {
	// 22:30 UTC on the 14th is already the 15th in Moscow.
	now := time.Date(2026, 11, 14, 22, 30, 0, 0, time.UTC)
	if got, want := Day(now, moscow), time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("Day = %v, want %v", got, want)
	}
	if got, want := ResetsAt(now, moscow), at(16, 0, 0); !got.Equal(want) {
		t.Fatalf("ResetsAt = %v, want %v", got, want)
	}
}

func TestDailyLimit(t *testing.T) {
	now := at(15, 12, 0)
	cases := map[string]struct {
		u    user.User
		want int
	}{
		"organic user":              {user.User{Source: "", CreatedAt: now}, FreeDaily},
		"ad user, first day":        {user.User{Source: "tt_launch", CreatedAt: at(15, 1, 0)}, AdFirstDay},
		"ad user, later day":        {user.User{Source: "tt_launch", CreatedAt: at(14, 23, 59)}, FreeDaily},
		"other campaign, first day": {user.User{Source: "landing", CreatedAt: now}, FreeDaily},
		"guest":                     {user.User{ID: -7, CreatedAt: now}, GuestDaily},
		"guest from an ad":          {user.User{ID: -7, Source: "tt_launch", CreatedAt: now}, GuestDaily},
	}
	for name, c := range cases {
		if got := DailyLimit(c.u, now, moscow); got != c.want {
			t.Errorf("%s: limit = %d, want %d", name, got, c.want)
		}
	}
}

func TestGuestBudgetCapIsAShareOfTheHourlyCapAndNeverMoreThanIt(t *testing.T) {
	const ceiling = 10_000_000
	for h := range 24 {
		now := at(15, h, 0)
		whole, guests := BudgetCap(ceiling, now, moscow), GuestBudgetCap(ceiling, now, moscow)
		if guests > whole || guests != int64(float64(whole)*GuestBudgetShare+0.5) {
			t.Fatalf("hour %d: guests %d of %d", h, guests, whole)
		}
	}
	if GuestBudgetShare <= 0 || GuestBudgetShare >= 1 {
		t.Fatalf("the share must leave room for everyone else: %v", GuestBudgetShare)
	}
}

func TestBudgetCapSpreadsTheCeilingAcrossTheDay(t *testing.T) {
	const ceiling = 10_000_000 // $10
	cases := []struct {
		now  time.Time
		want int64
	}{
		{at(15, 0, 0), ceiling / 24},
		{at(15, 11, 59), ceiling * 12 / 24},
		{at(15, 23, 59), ceiling},
	}
	for _, c := range cases {
		if got := BudgetCap(ceiling, c.now, moscow); got != c.want {
			t.Errorf("%s: cap = %d, want %d", c.now.Format("15:04"), got, c.want)
		}
	}
	for h := 1; h < 24; h++ {
		if BudgetCap(ceiling, at(15, h, 0), moscow) < BudgetCap(ceiling, at(15, h-1, 0), moscow) {
			t.Fatalf("cap must never shrink during the day (hour %d)", h)
		}
	}
}

func TestCostMicroRoundsUp(t *testing.T) {
	p := Prices{InputPerMtok: 0.15, OutputPerMtok: 0.60}
	// 1000 input tokens at $0.15/Mtok = $0.00015 = 150 micro-dollars; 500 output at $0.60 = 300.
	if got := p.CostMicro(1000, 500); got != 450 {
		t.Fatalf("cost = %d, want 450", got)
	}
	if got := p.CostMicro(1, 0); got != 1 {
		t.Fatalf("a fraction of a micro-dollar must round up, got %d", got)
	}
	if got := p.CostMicro(0, 0); got != 0 {
		t.Fatalf("no tokens cost nothing, got %d", got)
	}
}
