package translate

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/dyudin0821/bestregards/internal/events"
	"github.com/dyudin0821/bestregards/internal/limits"
	"github.com/dyudin0821/bestregards/internal/pairs"
	"github.com/dyudin0821/bestregards/internal/user"
)

var (
	moscow = time.FixedZone("MSK", 3*3600)
	now    = time.Date(2026, 11, 15, 12, 0, 0, 0, moscow) // hour 12 -> cap is 13/24 of the ceiling
)

const ceiling = 24_000_000 // $24, so one hour is exactly $1

type fakeBooks struct {
	mu           sync.Mutex
	used         map[int64]int
	spent        int64
	guestSpent   int64
	refunds      int
	refundCtx    []error // ctx.Err() seen by each refund
	reserveErr   error
	adds         []int64
	reservations []int64
}

func newBooks() *fakeBooks { return &fakeBooks{used: map[int64]int{}} }

func (f *fakeBooks) ReserveQuota(_ context.Context, id int64, _ time.Time, limit int) (int, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reserveErr != nil {
		return 0, false, f.reserveErr
	}
	if f.used[id] >= limit {
		return 0, false, nil
	}
	f.used[id]++
	return f.used[id], true, nil
}

func (f *fakeBooks) RefundQuota(ctx context.Context, id int64, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refunds++
	f.refundCtx = append(f.refundCtx, ctx.Err())
	f.used[id]--
	return nil
}

func (f *fakeBooks) QuotaUsed(_ context.Context, id int64, _ time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.used[id], nil
}

func (f *fakeBooks) BudgetSpent(context.Context, time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spent, nil
}

func (f *fakeBooks) ReserveBudget(_ context.Context, _ time.Time, c Claim) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.spent+c.Micro > c.Cap || (c.Guest && f.guestSpent+c.Micro > c.GuestCap) {
		return false, nil
	}
	f.spent += c.Micro
	if c.Guest {
		f.guestSpent += c.Micro
	}
	f.reservations = append(f.reservations, c.Micro)
	return true, nil
}

func (f *fakeBooks) AddBudgetSpend(_ context.Context, _ time.Time, delta int64, guest bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spent += delta
	if guest {
		f.guestSpent += delta
	}
	f.adds = append(f.adds, delta)
	return nil
}

type fakeAI struct {
	completion Completion
	err        error
	calls      int
	got        Request
	onCall     func()
}

func (f *fakeAI) Complete(_ context.Context, r Request) (Completion, error) {
	f.calls++
	f.got = r
	if f.onCall != nil {
		f.onCall()
	}
	return f.completion, f.err
}

type env struct {
	svc   *Service
	books *fakeBooks
	ai    *fakeAI
}

func setup(t *testing.T, mutate ...func(*Config)) env {
	t.Helper()
	catalog, err := pairs.Load()
	if err != nil {
		t.Fatal(err)
	}
	e := env{books: newBooks(), ai: &fakeAI{completion: Completion{Text: "Готово.", Usage: Usage{InputTokens: 1000, OutputTokens: 500}}}}
	cfg := Config{
		Pairs: catalog, Quotas: e.books, Budget: e.books, AI: e.ai,
		Prices:           limits.Prices{InputPerMtok: 0.15, OutputPerMtok: 0.60},
		DailyBudgetMicro: ceiling, Location: moscow,
		Now: func() time.Time { return now }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, m := range mutate {
		m(&cfg)
	}
	e.svc, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

var (
	alice   = user.User{ID: 1, CreatedAt: now.AddDate(0, 0, -10)}
	own     = Request{Text: "Это не мой баг.", Mode: "colleagues", Level: 2}
	someone = Request{Text: "не успеваю", Mode: "boss", Level: 3}
)

func TestReadyPairIsFreeAndSkipsAI(t *testing.T) {
	e := setup(t)
	p := e.svc.cfg.Pairs.Pairs[0]

	got, err := e.svc.Translate(context.Background(), alice, Request{Text: "  " + p.Input + " ", Mode: p.Mode, Level: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourcePair || got.Text != p.Output || got.Quota.Used != 0 || e.ai.calls != 0 || len(e.books.adds) != 0 {
		t.Fatalf("pair must be free: %+v ai=%d spend=%v", got, e.ai.calls, e.books.adds)
	}
	// Asking for another variant is not a pair lookup any more: it needs AI and quota.
	got, err = e.svc.Translate(context.Background(), alice, Request{Text: p.Input, Mode: p.Mode, Level: 1, Previous: p.Output})
	if err != nil || got.Source != SourceAI || got.Quota.Used != 1 {
		t.Fatalf("alternative: %+v %v", got, err)
	}
}

func TestAITranslationConsumesQuotaAndRecordsCost(t *testing.T) {
	e := setup(t)
	got, err := e.svc.Translate(context.Background(), alice, own)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceAI || got.Text != "Готово." || got.Quota.Limit != 5 || got.Quota.Used != 1 || got.Quota.Remaining() != 4 {
		t.Fatalf("unexpected result %+v", got)
	}
	if !got.Quota.ResetsAt.Equal(time.Date(2026, 11, 16, 0, 0, 0, 0, moscow)) {
		t.Errorf("ResetsAt = %v", got.Quota.ResetsAt)
	}
	// 1000 input at $0.15/Mtok + 500 output at $0.60/Mtok = 450 micro-dollars.
	if e.books.spent != 450 || e.ai.got.Text != own.Text || e.ai.got.Level != 2 {
		t.Fatalf("spent=%d request=%+v", e.books.spent, e.ai.got)
	}
}

func TestMissingUsageIsChargedByEstimate(t *testing.T) {
	e := setup(t)
	e.ai.completion = Completion{Text: "Готово."}
	if _, err := e.svc.Translate(context.Background(), alice, own); err != nil {
		t.Fatal(err)
	}
	if e.books.spent <= 0 {
		t.Fatal("a completion without usage must still cost something")
	}
}

func TestQuotaExhausted(t *testing.T) {
	e := setup(t)
	e.books.used[alice.ID] = limits.FreeDaily

	_, err := e.svc.Translate(context.Background(), alice, own)
	var q *QuotaExhaustedError
	if !errors.Is(err, ErrQuotaExhausted) || !errors.As(err, &q) {
		t.Fatalf("err = %v", err)
	}
	if q.Quota.Limit != 5 || q.Quota.Remaining() != 0 || q.Quota.ResetsAt.IsZero() {
		t.Errorf("quota in error = %+v", q.Quota)
	}
	if e.ai.calls != 0 || e.books.refunds != 0 {
		t.Errorf("nothing to call or refund: calls=%d refunds=%d", e.ai.calls, e.books.refunds)
	}
}

func TestUnlimitedUserIsNotStoppedByTheQuotaButIsByTheCeiling(t *testing.T) {
	e := setup(t, func(c *Config) { c.Unlimited = map[int64]bool{alice.ID: true} })
	e.books.used[alice.ID] = limits.FreeDaily

	got, err := e.svc.Translate(context.Background(), alice, own)
	if err != nil {
		t.Fatalf("an unlimited user past the daily quota: %v", err)
	}
	if !got.Quota.Unlimited || got.Quota.Used != limits.FreeDaily+1 {
		t.Errorf("the translation is still counted: %+v", got.Quota)
	}
	if q, err := e.svc.Status(context.Background(), alice); err != nil || !q.Unlimited {
		t.Errorf("status: %+v %v", q, err)
	}

	e.books.spent = ceiling
	if _, err := e.svc.Translate(context.Background(), alice, own); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("the spending ceiling still applies: err = %v", err)
	}
}

func TestAdUserGetsSmallerFirstDayQuota(t *testing.T) {
	e := setup(t)
	fromAd := user.User{ID: 9, Source: "tt_launch", CreatedAt: now.Add(-time.Hour)}
	for i := range limits.AdFirstDay {
		if _, err := e.svc.Translate(context.Background(), fromAd, own); err != nil {
			t.Fatalf("translation %d: %v", i+1, err)
		}
	}
	if _, err := e.svc.Translate(context.Background(), fromAd, own); !errors.Is(err, ErrQuotaExhausted) {
		t.Fatalf("4th translation: err = %v, want quota exhausted", err)
	}
}

func TestFailuresGiveTheQuotaBack(t *testing.T) {
	cases := map[string]struct {
		mutate func(e env)
		want   error
		paid   bool // the provider was asked, so its input tokens are still charged
	}{
		"budget exhausted": {func(e env) { e.books.spent = ceiling }, ErrBudgetExhausted, false},
		"ai down":          {func(e env) { e.ai.err = errors.New("502") }, ErrUnavailable, true},
	}
	for name, c := range cases {
		e := setup(t)
		c.mutate(e)
		before := e.books.spent

		_, err := e.svc.Translate(context.Background(), alice, own)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
		if e.books.used[alice.ID] != 0 || e.books.refunds != 1 {
			t.Errorf("%s: quota must be given back: used=%d refunds=%d", name, e.books.used[alice.ID], e.books.refunds)
		}
		wantSpent := before
		if c.paid {
			wantSpent += e.svc.inputCost(own) // the provider may have billed what it received
		}
		if e.books.spent != wantSpent {
			t.Errorf("%s: spent %d, want %d: the reservation must be released, keeping only what the provider may have billed", name, e.books.spent, wantSpent)
		}
	}
}

func TestClientLeavingAfterTheProviderWasAskedCostsTheUnitAndTheInput(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	e.ai.onCall = cancel
	e.ai.err = context.Canceled

	if _, err := e.svc.Translate(ctx, alice, own); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if e.books.refunds != 0 || e.books.used[alice.ID] != 1 {
		t.Fatalf("a dropped request must not be a free call: refunds=%d used=%d", e.books.refunds, e.books.used[alice.ID])
	}
	if want := e.svc.inputCost(own); e.books.spent != want {
		t.Fatalf("spent %d, want the input cost %d: the provider may bill a call that was dropped", e.books.spent, want)
	}
	for _, d := range e.books.adds {
		if d == 0 {
			t.Error("the release must be booked, and on a live context")
		}
	}
}

func TestBudgetCapGrowsHourByHour(t *testing.T) {
	// At 12:00 the cap is 13 hours' worth = $13. A call is admitted when its worst case still fits.
	cap := int64(13_000_000)
	e := setup(t)
	worst := e.svc.worstCase(own)
	e.books.spent = cap - worst
	if _, err := e.svc.Translate(context.Background(), alice, own); err != nil {
		t.Fatalf("worst case exactly fits: %v", err)
	}
	e = setup(t)
	e.books.spent = cap - worst + 1
	if _, err := e.svc.Translate(context.Background(), alice, own); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("worst case does not fit: err = %v", err)
	}
}

func TestGuestsHaveTheirOwnShareOfTheCeiling(t *testing.T) {
	guest := user.User{ID: -1, CreatedAt: now}
	e := setup(t)
	worst := e.svc.worstCase(own)
	guestCap := limits.GuestBudgetCap(ceiling, now, moscow)

	// The guests' share is nearly used: a guest whose worst case no longer fits is refused...
	e.books.guestSpent = guestCap - worst + 1
	e.books.spent = e.books.guestSpent
	if _, err := e.svc.Translate(context.Background(), guest, own); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("a guest past their share must be refused: %v", err)
	}
	if e.books.used[guest.ID] != 0 {
		t.Fatalf("the guest's quota must be given back: %d", e.books.used[guest.ID])
	}
	// ...while a Telegram user, who answers only to the whole ceiling, is served.
	if _, err := e.svc.Translate(context.Background(), alice, own); err != nil {
		t.Fatalf("Telegram users must not be blocked by the guests' share: %v", err)
	}
	if e.books.guestSpent != guestCap-worst+1 {
		t.Fatalf("a Telegram user's spend must not count as a guest's: %d", e.books.guestSpent)
	}

	// A guest's finished call settles both counters, and a failed one releases both.
	e = setup(t)
	if _, err := e.svc.Translate(context.Background(), guest, own); err != nil {
		t.Fatal(err)
	}
	if want := e.svc.cfg.Prices.CostMicro(1000, 500); e.books.guestSpent != want || e.books.spent != want {
		t.Fatalf("guest %d, total %d, want %d in both", e.books.guestSpent, e.books.spent, want)
	}
	e = setup(t)
	e.ai.err = errors.New("502")
	e.svc.Translate(context.Background(), guest, own)
	if want := e.svc.inputCost(own); e.books.guestSpent != want || e.books.spent != want {
		t.Fatalf("after a failure guest %d, total %d, want only the input cost %d in both", e.books.guestSpent, e.books.spent, want)
	}
}

func TestFinishedCallReplacesItsReservationWithTheRealCost(t *testing.T) {
	e := setup(t)
	if _, err := e.svc.Translate(context.Background(), alice, own); err != nil {
		t.Fatal(err)
	}
	// The fake provider reports 1000 input and 500 output tokens.
	if want := e.svc.cfg.Prices.CostMicro(1000, 500); e.books.spent != want {
		t.Fatalf("spent %d, want the real cost %d, not the worst case %d", e.books.spent, want, e.svc.worstCase(own))
	}
}

// gatedAI holds every call until released, and counts how many got through.
type gatedAI struct {
	mu      sync.Mutex
	reached int
	gate    chan struct{}
}

func (g *gatedAI) Complete(context.Context, Request) (Completion, error) {
	g.mu.Lock()
	g.reached++
	g.mu.Unlock()
	<-g.gate
	return Completion{Text: "Готово.", Usage: Usage{InputTokens: 1000, OutputTokens: 500}}, nil
}

// The ceiling must hold when many requests arrive at once: money is claimed before the provider is
// asked, so requests that find no room never reach it, however many are in flight.
func TestReservationHoldsTheCeilingAgainstParallelRequests(t *testing.T) {
	ai := &gatedAI{gate: make(chan struct{})}
	e := setup(t, func(cfg *Config) { cfg.AI = ai })
	room := int64(3)
	e.books.spent = 13_000_000 - room*e.svc.worstCase(own) // exactly three calls fit
	var users []user.User
	for i := 0; i < 20; i++ {
		users = append(users, user.User{ID: int64(100 + i), CreatedAt: now.Add(-48 * time.Hour)})
	}

	var wg sync.WaitGroup
	exhausted := make(chan struct{}, len(users))
	for _, u := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.svc.Translate(context.Background(), u, own); errors.Is(err, ErrBudgetExhausted) {
				exhausted <- struct{}{}
			}
		}()
	}
	for len(exhausted) < len(users)-int(room) { // wait until everyone who will be refused has been
		time.Sleep(time.Millisecond)
	}
	ai.mu.Lock()
	reached := ai.reached
	ai.mu.Unlock()
	close(ai.gate)
	wg.Wait()
	if int64(reached) != room {
		t.Fatalf("%d requests reached the provider, want %d", reached, room)
	}
	if e.books.spent > 13_000_000 {
		t.Fatalf("spent %d passed the cap", e.books.spent)
	}
}

func TestStorageErrorsAreNotReportedAsQuotaProblems(t *testing.T) {
	e := setup(t)
	boom := errors.New("db down")
	e.books.reserveErr = boom
	if _, err := e.svc.Translate(context.Background(), alice, own); !errors.Is(err, boom) || errors.Is(err, ErrQuotaExhausted) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidation(t *testing.T) {
	e := setup(t)
	long := make([]rune, MaxTextRunes+1)
	for i := range long {
		long[i] = 'я'
	}
	cases := map[string]Request{
		"empty":       {Text: "   ", Mode: "hr", Level: 1},
		"too long":    {Text: string(long), Mode: "hr", Level: 1},
		"bad mode":    {Text: "x", Mode: "bad", Level: 1},
		"level zero":  {Text: "x", Mode: "hr", Level: 0},
		"level large": {Text: "x", Mode: "hr", Level: 4},
	}
	for name, req := range cases {
		var v *ValidationError
		if _, err := e.svc.Translate(context.Background(), alice, req); !errors.As(err, &v) || v.Message == "" {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	exactly := string(long[:MaxTextRunes])
	if _, err := e.svc.Translate(context.Background(), alice, Request{Text: exactly, Mode: "hr", Level: 1}); err != nil {
		t.Errorf("a text of exactly %d characters must be accepted: %v", MaxTextRunes, err)
	}
}

func TestAIDisabled(t *testing.T) {
	e := setup(t, func(c *Config) { c.AI = nil })
	if e.svc.AIEnabled() {
		t.Fatal("AIEnabled must be false")
	}
	if _, err := e.svc.Translate(context.Background(), alice, someone); !errors.Is(err, ErrAIDisabled) {
		t.Fatalf("err = %v", err)
	}
	// Ready pairs keep working without AI.
	p := e.svc.cfg.Pairs.Pairs[0]
	if r, err := e.svc.Translate(context.Background(), alice, Request{Text: p.Input, Mode: p.Mode, Level: 2}); err != nil || r.Source != SourcePair {
		t.Fatalf("pair without AI: %+v %v", r, err)
	}
}

func TestStatus(t *testing.T) {
	e := setup(t)
	e.books.used[alice.ID] = 2
	q, err := e.svc.Status(context.Background(), alice)
	if err != nil || q.Limit != 5 || q.Used != 2 || q.Remaining() != 3 {
		t.Fatalf("%+v %v", q, err)
	}
}

func TestNewRejectsUnsafeWiring(t *testing.T) {
	catalog, _ := pairs.Load()
	books := newBooks()
	ok := Config{Pairs: catalog, Quotas: books, Budget: books, AI: &fakeAI{}, Location: moscow,
		Prices: limits.Prices{InputPerMtok: 1}, DailyBudgetMicro: 1}
	cases := map[string]func(*Config){
		"no budget with AI": func(c *Config) { c.DailyBudgetMicro = 0 },
		"no prices with AI": func(c *Config) { c.Prices = limits.Prices{} },
		"no location":       func(c *Config) { c.Location = nil },
		"no pairs":          func(c *Config) { c.Pairs = nil },
	}
	for name, m := range cases {
		c := ok
		m(&c)
		if _, err := New(c); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

type recorderSpy struct{ got []events.Event }

func (r *recorderSpy) Record(e events.Event) { r.got = append(r.got, e) }

func TestServerSideEvents(t *testing.T) {
	spy := &recorderSpy{}
	e := setup(t, func(c *Config) { c.Events = spy })
	ad := user.User{ID: 7, Source: "tt_launch", CreatedAt: now.AddDate(0, 0, -3)}
	ctx := context.Background()

	// AI translation: one translate_done with the id the user gets back.
	res, err := e.svc.Translate(ctx, ad, own)
	if err != nil {
		t.Fatal(err)
	}
	if len(spy.got) != 1 {
		t.Fatalf("events after AI: %+v", spy.got)
	}
	ev := spy.got[0]
	if ev.Type != events.TranslateDone || ev.Kind != events.KindAI || ev.ResultID != res.ID || !events.ValidResultID(res.ID) ||
		ev.UserID != 7 || ev.Campaign != "tt_launch" || ev.Mode != "colleagues" || ev.Level != 2 || ev.Source != events.SourceMiniApp {
		t.Fatalf("unexpected event %+v (result id %q)", ev, res.ID)
	}

	// A ready pair is reported as a pair translation with its own id.
	p := e.svc.cfg.Pairs.Pairs[0]
	pr, err := e.svc.Translate(ctx, ad, Request{Text: p.Input, Mode: p.Mode, Level: 1, Channel: events.SourceInline})
	if err != nil || pr.ID == "" || pr.ID == res.ID {
		t.Fatalf("pair result: %+v %v", pr, err)
	}
	if ev := spy.got[1]; ev.Kind != events.KindPair || ev.Source != events.SourceInline || ev.ResultID != pr.ID {
		t.Fatalf("pair event %+v", ev)
	}

	// Refusals are events too, without a result id.
	e.books.used[ad.ID] = limits.FreeDaily
	e.svc.Translate(ctx, ad, own)
	if ev := spy.got[2]; ev.Type != events.QuotaExhausted || ev.ResultID != "" {
		t.Fatalf("quota event %+v", ev)
	}
	e.books.used[ad.ID] = 0
	e.books.spent = ceiling
	e.svc.Translate(ctx, ad, own)
	if ev := spy.got[3]; ev.Type != events.BudgetExhausted {
		t.Fatalf("budget event %+v", ev)
	}
}

func TestFailedTranslationsAreNotReportedAsDone(t *testing.T) {
	spy := &recorderSpy{}
	e := setup(t, func(c *Config) { c.Events = spy })
	e.ai.err = errors.New("502")
	if _, err := e.svc.Translate(context.Background(), alice, own); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if len(spy.got) != 0 {
		t.Fatalf("a failed translation must not produce events: %+v", spy.got)
	}
}

type fakeSwitch struct {
	stopped bool
	err     error
}

func (f *fakeSwitch) FreeStopped(context.Context) (bool, error) { return f.stopped, f.err }

func TestSwitchedOffAITranslationsStillServePairs(t *testing.T) {
	sw := &fakeSwitch{stopped: true}
	e := setup(t, func(c *Config) { c.Switch = sw })
	ctx := context.Background()

	// Ready-made pairs do not care.
	catalog, _ := pairs.Load()
	p := catalog.Pairs[0]
	if r, err := e.svc.Translate(ctx, alice, Request{Text: p.Input, Mode: p.Mode, Level: 2}); err != nil || r.Source != SourcePair {
		t.Fatalf("pair: %+v %v", r, err)
	}
	// The user is told why, and nothing is reserved or recorded as a quota stop.
	if _, err := e.svc.Translate(ctx, alice, own); !errors.Is(err, ErrFreeStopped) {
		t.Fatalf("want ErrFreeStopped, got %v", err)
	}
	if e.books.used[alice.ID] != 0 || e.ai.calls != 0 {
		t.Fatalf("a stopped request must not reserve quota or call AI: used %d calls %d", e.books.used[alice.ID], e.ai.calls)
	}

	// Switching back restores the free allowance.
	sw.stopped = false
	if _, err := e.svc.Translate(ctx, alice, own); err != nil || e.books.used[alice.ID] != 1 {
		t.Fatalf("after resume: %v used %d", err, e.books.used[alice.ID])
	}
}

func TestSwitchReadFailureFailsClosedWithoutSpendingAnything(t *testing.T) {
	sw := &fakeSwitch{err: errors.New("db down")}
	e := setup(t, func(c *Config) { c.Switch = sw })
	if _, err := e.svc.Translate(context.Background(), alice, own); err == nil || errors.Is(err, ErrFreeStopped) {
		t.Fatalf("want a plain error, got %v", err)
	}
	if e.books.used[alice.ID] != 0 || e.ai.calls != 0 {
		t.Fatal("nothing may be reserved or called when the switch cannot be read")
	}
}

func TestExhaustedQuotaAndCeilingReportTheirOwnReasons(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	e.books.used[alice.ID] = limits.FreeDaily
	if _, err := e.svc.Translate(ctx, alice, own); !errors.Is(err, ErrQuotaExhausted) {
		t.Fatalf("quota: %v", err)
	}
	e.books.used[alice.ID] = 0
	e.books.spent = ceiling
	if _, err := e.svc.Translate(ctx, alice, own); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("ceiling: %v", err)
	}
	if e.books.used[alice.ID] != 0 {
		t.Fatalf("the reservation must be given back when the ceiling is hit: used=%d", e.books.used[alice.ID])
	}
}
