package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dyudin0821/bestregards/internal/events"

	"github.com/dyudin0821/bestregards/internal/user"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	s, err := Open(ctx, Config{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestOpenIsRepeatable(t *testing.T) {
	openTestStore(t)
	openTestStore(t) // migrations must be a no-op the second time
}

func TestUpsertKeepsFirstSourceAndRefreshesLastSeen(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const id = 9_000_000_001
	t.Cleanup(func() { s.pool.Exec(ctx, "DELETE FROM users WHERE telegram_id = $1", id) })

	first, err := s.UpsertUser(ctx, id, "tt_launch")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.UpsertUser(ctx, id, "other_campaign")
	if err != nil {
		t.Fatal(err)
	}
	if second.Source != "tt_launch" {
		t.Errorf("source = %q, want the first one", second.Source)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) || second.LastSeenAt.Before(first.LastSeenAt) {
		t.Errorf("timestamps not maintained: %+v -> %+v", first, second)
	}
	if second.Banned {
		t.Error("new users must not be banned")
	}
}

func TestGetUserNotFound(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.GetUser(context.Background(), 9_000_000_404); !errors.Is(err, user.ErrNotFound) {
		t.Fatalf("err = %v, want user.ErrNotFound", err)
	}
}

// freshDatabase creates an empty database next to the test one and returns its URL.
func freshDatabase(t *testing.T) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	suffix := make([]byte, 4)
	rand.Read(suffix)
	name := "bestregards_test_" + hex.EncodeToString(suffix)

	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		admin.Close(ctx)
	})
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// Several replicas start at once on rollout; all of them must come up and migrate exactly once.
func TestConcurrentOpenMigratesOnce(t *testing.T) {
	url := freshDatabase(t)
	const replicas = 8

	errs := make(chan error, replicas)
	var wg sync.WaitGroup
	for range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			s, err := Open(ctx, Config{URL: url})
			if err == nil {
				s.Close()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("replica failed to start: %v", err)
		}
	}
}

func TestStoreHonoursCancelledContext(t *testing.T) {
	s := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.GetUser(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// registeredUser creates a user and removes it (with its counters) afterwards.
func registeredUser(t *testing.T, s *Store, id int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.UpsertUser(ctx, id, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.pool.Exec(ctx, "DELETE FROM users WHERE telegram_id = $1", id) })
}

var testDay = time.Date(2031, 3, 9, 0, 0, 0, 0, time.UTC)

func TestReserveQuotaNeverExceedsTheLimitUnderConcurrency(t *testing.T) {
	s := openTestStore(t)
	const id, limit, attempts = 9_000_000_101, 5, 30
	registeredUser(t, s, id)
	ctx := context.Background()

	var granted atomic.Int32
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok, err := s.ReserveQuota(ctx, id, testDay, limit); err != nil {
				t.Error(err)
			} else if ok {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()

	if granted.Load() != limit {
		t.Fatalf("granted %d reservations, want exactly %d", granted.Load(), limit)
	}
	if used, err := s.QuotaUsed(ctx, id, testDay); err != nil || used != limit {
		t.Fatalf("used = %d, %v", used, err)
	}
}

func TestQuotaReserveRefundAndDays(t *testing.T) {
	s := openTestStore(t)
	const id = 9_000_000_102
	registeredUser(t, s, id)
	ctx := context.Background()
	nextDay := testDay.AddDate(0, 0, 1)

	if used, err := s.QuotaUsed(ctx, id, testDay); err != nil || used != 0 {
		t.Fatalf("fresh user: used=%d err=%v", used, err)
	}
	if used, ok, err := s.ReserveQuota(ctx, id, testDay, 2); err != nil || !ok || used != 1 {
		t.Fatalf("first: %d %v %v", used, ok, err)
	}
	if used, ok, _ := s.ReserveQuota(ctx, id, testDay, 2); !ok || used != 2 {
		t.Fatalf("second: %d %v", used, ok)
	}
	if _, ok, _ := s.ReserveQuota(ctx, id, testDay, 2); ok {
		t.Fatal("third reservation must be refused")
	}
	if err := s.RefundQuota(ctx, id, testDay); err != nil {
		t.Fatal(err)
	}
	if used, ok, _ := s.ReserveQuota(ctx, id, testDay, 2); !ok || used != 2 {
		t.Fatalf("after refund: %d %v", used, ok)
	}
	// A new day starts from zero and an earlier day is untouched.
	if used, ok, _ := s.ReserveQuota(ctx, id, nextDay, 2); !ok || used != 1 {
		t.Fatalf("next day: %d %v", used, ok)
	}
	if used, _ := s.QuotaUsed(ctx, id, testDay); used != 2 {
		t.Fatalf("earlier day changed: %d", used)
	}
	// Refunds never push the counter below zero.
	for range 4 {
		if err := s.RefundQuota(ctx, id, nextDay); err != nil {
			t.Fatal(err)
		}
	}
	if used, _ := s.QuotaUsed(ctx, id, nextDay); used != 0 {
		t.Fatalf("over-refunded: %d", used)
	}
	if _, ok, _ := s.ReserveQuota(ctx, id, testDay.AddDate(0, 0, 5), 0); ok {
		t.Fatal("a limit below one must never grant a reservation")
	}
}

func TestBudgetSpendAddsUpUnderConcurrency(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	day := time.Date(2031, 3, 10, 0, 0, 0, 0, time.UTC)
	t.Cleanup(func() { s.pool.Exec(ctx, "DELETE FROM budget_usage WHERE day = $1", day) })

	if spent, err := s.BudgetSpent(ctx, day); err != nil || spent != 0 {
		t.Fatalf("empty day: %d %v", spent, err)
	}
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.AddBudgetSpend(ctx, day, 250, false); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if spent, err := s.BudgetSpent(ctx, day); err != nil || spent != 10_000 {
		t.Fatalf("spent = %d, %v; want 10000", spent, err)
	}
}

func TestDeletingAUserRemovesTheirCounters(t *testing.T) {
	s := openTestStore(t)
	const id = 9_000_000_103
	ctx := context.Background()
	if _, err := s.UpsertUser(ctx, id, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReserveQuota(ctx, id, testDay, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, "DELETE FROM users WHERE telegram_id = $1", id); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM quota_usage WHERE telegram_id = $1", id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("counters left behind: %d %v", n, err)
	}
}

func TestGuestsGetDistinctNegativeIdsAndArePurgedByAge(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a, err := s.CreateGuest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateGuest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.pool.Exec(ctx, "DELETE FROM users WHERE telegram_id IN ($1, $2)", a.ID, b.ID) })
	if a.ID >= 0 || b.ID >= 0 || a.ID == b.ID || !a.Guest() {
		t.Fatalf("guest ids %d, %d", a.ID, b.ID)
	}
	got, err := s.GetUser(ctx, a.ID)
	if err != nil || got.ID != a.ID {
		t.Fatalf("a guest must be readable like any user: %+v %v", got, err)
	}

	if _, _, err := s.ReserveQuota(ctx, a.ID, testDay, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, "UPDATE users SET created_at = now() - interval '40 days' WHERE telegram_id = $1", a.ID); err != nil {
		t.Fatal(err)
	}
	real := int64(9_000_000_104)
	registeredUser(t, s, real)
	s.pool.Exec(ctx, "UPDATE users SET created_at = now() - interval '400 days' WHERE telegram_id = $1", real)

	if _, err := s.PurgeGuests(ctx, time.Now().Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetUser(ctx, a.ID); !errors.Is(err, user.ErrNotFound) {
		t.Fatalf("the old guest must be gone: %v", err)
	}
	if _, err := s.GetUser(ctx, b.ID); err != nil {
		t.Fatalf("a recent guest must stay: %v", err)
	}
	if _, err := s.GetUser(ctx, real); err != nil {
		t.Fatalf("a Telegram user must never be purged as a guest: %v", err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM quota_usage WHERE telegram_id = $1", a.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the purged guest's counters must go too: %d %v", n, err)
	}
}

func TestPurgeQuotaRemovesOnlyOldDays(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id := int64(9_000_000_105)
	registeredUser(t, s, id)
	old, recent := testDay.AddDate(0, 0, -40), testDay
	for _, day := range []time.Time{old, recent} {
		if _, _, err := s.ReserveQuota(ctx, id, day, 3); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PurgeQuota(ctx, testDay.AddDate(0, 0, -30)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.QuotaUsed(ctx, id, old); err != nil || n != 0 {
		t.Fatalf("the old day must be gone: %d %v", n, err)
	}
	if n, err := s.QuotaUsed(ctx, id, recent); err != nil || n != 1 {
		t.Fatalf("a recent day must stay: %d %v", n, err)
	}
}

func eventRow(typ events.Type, resultID string, at time.Time) events.Row {
	return events.Row{At: at, Type: typ, Kind: events.KindAI, Mode: "hr", Level: 2, Source: events.SourceMiniApp,
		ResultID: resultID, UserHash: "deadbeefdeadbeefdeadbeefdeadbeef", Campaign: "tt_launch"}
}

func countEvents(t *testing.T, s *Store, resultPrefix string) (total, copied int) {
	t.Helper()
	err := s.pool.QueryRow(context.Background(),
		"SELECT count(*), count(*) FILTER (WHERE type = 'copied') FROM events WHERE result_id LIKE $1", resultPrefix+"%").Scan(&total, &copied)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func TestInsertEventsStoresBatchesAndIgnoresRepeatedCopies(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const prefix = "evtestaa"
	t.Cleanup(func() { s.pool.Exec(ctx, "DELETE FROM events WHERE result_id LIKE $1", prefix+"%") })
	now := time.Now()

	err := s.InsertEvents(ctx, []events.Row{
		eventRow(events.TranslateDone, prefix+"0001", now),
		eventRow(events.Copied, prefix+"0001", now),
		eventRow(events.Copied, prefix+"0001", now), // pressed the button twice
		eventRow(events.Copied, prefix+"0002", now),
	})
	if err != nil {
		t.Fatal(err)
	}
	if total, copied := countEvents(t, s, prefix); total != 3 || copied != 2 {
		t.Fatalf("total=%d copied=%d, want 3 and 2", total, copied)
	}
	// Repeating the same batch later still adds nothing for the copies.
	if err := s.InsertEvents(ctx, []events.Row{eventRow(events.Copied, prefix+"0002", now)}); err != nil {
		t.Fatal(err)
	}
	if _, copied := countEvents(t, s, prefix); copied != 2 {
		t.Fatalf("copied = %d after a repeat", copied)
	}
	if err := s.InsertEvents(ctx, nil); err != nil {
		t.Fatalf("an empty batch is not an error: %v", err)
	}
}

func TestInsertEventsRejectsValuesThatBreakTheSchema(t *testing.T) {
	s := openTestStore(t)
	bad := eventRow(events.Type("anything"), "evtestbb0001", time.Now())
	if err := s.InsertEvents(context.Background(), []events.Row{bad}); err == nil {
		t.Fatal("an unknown type must be refused by the database")
	}
}

func TestPurgeEventsWorksInChunksAndKeepsWhatIsNewer(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const prefix = "evtestch"
	t.Cleanup(func() { s.pool.Exec(ctx, "DELETE FROM events WHERE result_id LIKE $1", prefix+"%") })
	now := time.Now()
	old := now.AddDate(-2, 0, 0)
	var rows []events.Row
	for i := range 25 {
		rows = append(rows, eventRow(events.TranslateDone, fmt.Sprintf("%s%04d", prefix, i), old.Add(time.Duration(i)*time.Second)))
	}
	rows = append(rows, eventRow(events.TranslateDone, prefix+"fresh", now.Add(-time.Hour)))
	if err := s.InsertEvents(ctx, rows); err != nil {
		t.Fatal(err)
	}

	removed, err := s.purgeEvents(ctx, now.AddDate(-1, 0, 0), 10) // three rounds: 10, 10, 5
	if err != nil {
		t.Fatal(err)
	}
	if removed < 25 {
		t.Fatalf("removed %d, want at least our 25 old rows", removed)
	}
	if total, _ := countEvents(t, s, prefix); total != 1 {
		t.Fatalf("left %d rows, want only the fresh one", total)
	}

	// A cancelled context stops between chunks and says how far it got.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.purgeEvents(cancelled, now, 10); err == nil {
		t.Fatal("a cancelled purge must report the cancellation")
	}
}

func TestAResultCanBeReportedDoneOnlyOnce(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const prefix = "evtestdo"
	t.Cleanup(func() { s.pool.Exec(ctx, "DELETE FROM events WHERE result_id LIKE $1", prefix+"%") })
	at := time.Now()
	row := eventRow(events.TranslateDone, prefix+"0001", at)
	if err := s.InsertEvents(ctx, []events.Row{row, row, row}); err != nil {
		t.Fatal(err)
	}
	if total, _ := countEvents(t, s, prefix); total != 1 {
		t.Fatalf("%d done-events for one result: a client could flood the table with repeats", total)
	}
}

func TestPurgeEventsRemovesOnlyOldRows(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const prefix = "evtestcc"
	t.Cleanup(func() { s.pool.Exec(ctx, "DELETE FROM events WHERE result_id LIKE $1", prefix+"%") })
	now := time.Now()
	old, fresh := now.AddDate(-2, 0, 0), now.Add(-time.Hour)
	if err := s.InsertEvents(ctx, []events.Row{
		eventRow(events.TranslateDone, prefix+"0001", old),
		eventRow(events.TranslateDone, prefix+"0002", fresh),
	}); err != nil {
		t.Fatal(err)
	}
	// Other rows of the shared test database may be old too, so only check ours.
	if _, err := s.PurgeEvents(ctx, now.AddDate(-1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if total, _ := countEvents(t, s, prefix); total != 1 {
		t.Fatalf("left %d rows, want only the fresh one", total)
	}
}
