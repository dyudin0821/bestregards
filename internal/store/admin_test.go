package store

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dyudin0821/bestregards/internal/admin"
	"github.com/dyudin0821/bestregards/internal/events"
	"github.com/dyudin0821/bestregards/internal/translate"
)

// The window is far in the past so rows from other tests and from real use never fall into it.
var adminDay = time.Date(2001, 3, 4, 0, 0, 0, 0, time.UTC)

func TestAdminActivityCountsTranslationsCopiesAndRefusals(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const prefix = "admtest1"
	t.Cleanup(func() {
		s.pool.Exec(ctx, "DELETE FROM events WHERE at >= $1 AND at < $2", adminDay, adminDay.AddDate(0, 0, 1))
	})
	at := adminDay.Add(12 * time.Hour)

	pair := eventRow(events.TranslateDone, prefix+"0001", at)
	pair.Kind = events.KindPair
	pair.UserHash = ""
	other := eventRow(events.TranslateDone, prefix+"0003", at)
	other.UserHash = "cafebabecafebabecafebabecafebabe"
	quota := eventRow(events.QuotaExhausted, "", at)
	quota.Kind = events.KindNone
	outside := eventRow(events.TranslateDone, prefix+"0009", adminDay.Add(-time.Hour))

	err := s.InsertEvents(ctx, []events.Row{
		pair, eventRow(events.TranslateDone, prefix+"0002", at), other, outside,
		eventRow(events.Copied, prefix+"0001", at), eventRow(events.Copied, prefix+"0002", at),
		quota, quota,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Activity(ctx, adminDay, adminDay.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	want := admin.Activity{Pair: 1, AI: 2, Copied: 2, People: 2, QuotaStops: 2}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if pct := got.CopiedPercent(); pct < 66 || pct > 67 {
		t.Fatalf("copied percent %.1f", pct)
	}
}

func TestFreeSwitchSurvivesAndDefaultsToOn(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	t.Cleanup(func() { s.pool.Exec(ctx, "DELETE FROM settings WHERE key = $1", settingFreeStopped) })
	s.pool.Exec(ctx, "DELETE FROM settings WHERE key = $1", settingFreeStopped)

	if stopped, err := s.FreeStopped(ctx); err != nil || stopped {
		t.Fatalf("default must be running: %v %v", stopped, err)
	}
	if err := s.SetFreeStopped(ctx, true); err != nil {
		t.Fatal(err)
	}
	if stopped, _ := s.FreeStopped(ctx); !stopped {
		t.Fatal("stop did not stick")
	}
	if err := s.SetFreeStopped(ctx, false); err != nil {
		t.Fatal(err)
	}
	if stopped, _ := s.FreeStopped(ctx); stopped {
		t.Fatal("resume did not stick")
	}
}

func TestReserveBudgetNeverPassesTheCapUnderConcurrency(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	day := time.Date(2001, 7, 8, 0, 0, 0, 0, time.UTC)
	t.Cleanup(func() { s.pool.Exec(ctx, "DELETE FROM budget_usage WHERE day = $1", day) })

	const cap, each, workers = 1000, 100, 30 // room for ten reservations
	var wg sync.WaitGroup
	var granted atomic.Int32
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.ReserveBudget(ctx, day, translate.Claim{Micro: each, Cap: cap})
			if err != nil {
				t.Error(err)
			} else if ok {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()
	spent, _ := s.BudgetSpent(ctx, day)
	if granted.Load() != 10 || spent != 1000 {
		t.Fatalf("granted %d, spent %d: want exactly ten reservations and the cap reached, never passed", granted.Load(), spent)
	}

	// Releasing makes room again; an oversized first reservation is refused even on a fresh day.
	if err := s.AddBudgetSpend(ctx, day, -300, false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ReserveBudget(ctx, day, translate.Claim{Micro: 250, Cap: cap}); !ok {
		t.Fatal("released money must be reservable again")
	}
	fresh := day.AddDate(0, 0, 1)
	t.Cleanup(func() { s.pool.Exec(ctx, "DELETE FROM budget_usage WHERE day = $1", fresh) })
	if ok, err := s.ReserveBudget(ctx, fresh, translate.Claim{Micro: cap + 1, Cap: cap}); ok || err != nil {
		t.Fatalf("a reservation larger than the cap must be refused on an empty day: %v %v", ok, err)
	}
}

func TestGuestsCannotPassTheirShareAndOthersAreStillServed(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	day := time.Date(2001, 9, 10, 0, 0, 0, 0, time.UTC)
	t.Cleanup(func() { s.pool.Exec(ctx, "DELETE FROM budget_usage WHERE day = $1", day) })

	const cap, guestCap, each, workers = 1000, 400, 50, 40 // guests have room for eight of them
	var wg sync.WaitGroup
	var granted atomic.Int32
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.ReserveBudget(ctx, day, translate.Claim{Micro: each, Cap: cap, Guest: true, GuestCap: guestCap})
			if err != nil {
				t.Error(err)
			} else if ok {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()
	guest, _ := s.GuestBudgetSpent(ctx, day)
	if granted.Load() != 8 || guest != guestCap {
		t.Fatalf("granted %d, guest spend %d: want exactly eight and the guest cap, never past it", granted.Load(), guest)
	}

	// The guests' share is used up, the whole ceiling is not: a Telegram user is still served.
	if ok, err := s.ReserveBudget(ctx, day, translate.Claim{Micro: each, Cap: cap}); err != nil || !ok {
		t.Fatalf("a Telegram user must not be blocked by the guests' share: %v %v", ok, err)
	}
	if ok, _ := s.ReserveBudget(ctx, day, translate.Claim{Micro: each, Cap: cap, Guest: true, GuestCap: guestCap}); ok {
		t.Fatal("a guest must stay refused once their share is used")
	}
	if total, _ := s.BudgetSpent(ctx, day); total != guestCap+each {
		t.Fatalf("total %d: the guests' spend counts in the total, the Telegram user's does not count as a guest's", total)
	}

	// Releasing a guest's reservation gives the room back to guests, and only theirs.
	if err := s.AddBudgetSpend(ctx, day, -each, true); err != nil {
		t.Fatal(err)
	}
	if guest, _ := s.GuestBudgetSpent(ctx, day); guest != guestCap-each {
		t.Fatalf("guest spend after the release: %d", guest)
	}
	if err := s.AddBudgetSpend(ctx, day, -each, false); err != nil {
		t.Fatal(err)
	}
	if guest, _ := s.GuestBudgetSpent(ctx, day); guest != guestCap-each {
		t.Fatalf("a Telegram user's release must not touch the guests' share: %d", guest)
	}
	// A reservation larger than the guests' share is refused even on an empty day.
	fresh := day.AddDate(0, 0, 1)
	t.Cleanup(func() { s.pool.Exec(ctx, "DELETE FROM budget_usage WHERE day = $1", fresh) })
	if ok, err := s.ReserveBudget(ctx, fresh, translate.Claim{Micro: guestCap + 1, Cap: cap, Guest: true, GuestCap: guestCap}); ok || err != nil {
		t.Fatalf("the first insert of a day must respect the guest cap too: %v %v", ok, err)
	}
}

func TestGuestsOffSwitchDefaultsToOnAndSurvives(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	t.Cleanup(func() { s.pool.Exec(ctx, "DELETE FROM settings WHERE key = $1", settingGuestsOff) })
	s.pool.Exec(ctx, "DELETE FROM settings WHERE key = $1", settingGuestsOff)

	if off, err := s.GuestsOff(ctx); err != nil || off {
		t.Fatalf("guests are let in by default: %v %v", off, err)
	}
	if err := s.SetGuestsOff(ctx, true); err != nil {
		t.Fatal(err)
	}
	if off, _ := s.GuestsOff(ctx); !off {
		t.Fatal("the switch did not stick")
	}
	if stopped, _ := s.FreeStopped(ctx); stopped {
		t.Fatal("the two switches are independent")
	}
	if err := s.SetGuestsOff(ctx, false); err != nil {
		t.Fatal(err)
	}
	if off, _ := s.GuestsOff(ctx); off {
		t.Fatal("resume did not stick")
	}
}
