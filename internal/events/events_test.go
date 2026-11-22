package events

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSink struct {
	mu      sync.Mutex
	batches [][]Row
	err     error
	block   chan struct{}
}

func (f *fakeSink) InsertEvents(_ context.Context, rows []Row) error {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.batches = append(f.batches, append([]Row(nil), rows...))
	return nil
}

func (f *fakeSink) all() []Row {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Row
	for _, b := range f.batches {
		out = append(out, b...)
	}
	return out
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func newAsync(s Sink, mutate ...func(*AsyncConfig)) *Async {
	c := AsyncConfig{Pepper: []byte("pepper-for-tests-pepper-for-tests"), Logger: quiet, FlushEvery: 10 * time.Millisecond}
	for _, m := range mutate {
		m(&c)
	}
	return NewAsync(s, c)
}

func closeAsync(t *testing.T, a *Async) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestEventsAreWrittenHashedAndTrimmed(t *testing.T) {
	sink := &fakeSink{}
	a := newAsync(sink)
	long := strings.Repeat("я", 200)
	a.Record(Event{Type: Copied, Kind: KindAI, Mode: "hr", Level: 2, Source: SourceMiniApp, ResultID: "abcd1234", Campaign: long, UserID: 42})
	a.Record(Event{Type: TranslateDone, Kind: KindPair, Source: SourceWeb})
	closeAsync(t, a)

	rows := sink.all()
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].UserHash == "" || rows[0].UserHash == "42" || len(rows[0].UserHash) != 32 {
		t.Errorf("user must be a hash, got %q", rows[0].UserHash)
	}
	if got := []rune(rows[0].Campaign); len(got) != MaxCampaignLen {
		t.Errorf("campaign length %d, want %d", len(got), MaxCampaignLen)
	}
	if rows[1].UserHash != "" {
		t.Errorf("an anonymous event must have no user hash, got %q", rows[1].UserHash)
	}
	if rows[0].At.IsZero() {
		t.Error("events need a timestamp")
	}
}

func TestUserHashDependsOnThePepper(t *testing.T) {
	a, b := UserHash([]byte("pepper-one-pepper-one-pepper-one"), 7), UserHash([]byte("pepper-two-pepper-two-pepper-two"), 7)
	if a == b || a == "" {
		t.Fatalf("hashes must differ per pepper: %q %q", a, b)
	}
	if UserHash([]byte("pepper-one-pepper-one-pepper-one"), 7) != a {
		t.Fatal("the hash must be stable")
	}
	if UserHash([]byte("x"), 0) != "" {
		t.Fatal("anonymous visitors have no hash")
	}
}

func TestRecordNeverBlocksWhenTheQueueIsFull(t *testing.T) {
	sink := &fakeSink{block: make(chan struct{})}
	a := newAsync(sink, func(c *AsyncConfig) { c.Queue = 2; c.Batch = 1 })

	done := make(chan struct{})
	go func() {
		for range 50 {
			a.Record(Event{Type: Copied, Source: SourceWeb})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Record blocked while the sink was stuck")
	}
	if a.Dropped() == 0 {
		t.Error("overflow must be counted")
	}
	close(sink.block)
	closeAsync(t, a)
}

func TestSinkFailureDoesNotBreakLaterEvents(t *testing.T) {
	sink := &fakeSink{err: errors.New("db down")}
	a := newAsync(sink)
	a.Record(Event{Type: Copied, Source: SourceWeb})
	time.Sleep(60 * time.Millisecond)
	sink.mu.Lock()
	sink.err = nil
	sink.mu.Unlock()
	a.Record(Event{Type: TranslateDone, Source: SourceWeb})
	closeAsync(t, a)

	rows := sink.all()
	if len(rows) != 1 || rows[0].Type != TranslateDone {
		t.Fatalf("rows after recovery: %+v", rows)
	}
	if a.Dropped() != 1 {
		t.Errorf("the lost event must be counted, dropped = %d", a.Dropped())
	}
}

func TestCloseFlushesAndLaterRecordsAreSafe(t *testing.T) {
	sink := &fakeSink{}
	a := newAsync(sink, func(c *AsyncConfig) { c.FlushEvery = time.Hour })
	for range 5 {
		a.Record(Event{Type: Copied, Source: SourceWeb})
	}
	closeAsync(t, a)
	if len(sink.all()) != 5 {
		t.Fatalf("Close must flush the queue, wrote %d", len(sink.all()))
	}
	a.Record(Event{Type: Copied, Source: SourceWeb}) // must not panic
	closeAsync(t, a)                                 // closing twice is fine
}

func TestCloseGivesUpWhenTheSinkIsStuck(t *testing.T) {
	sink := &fakeSink{block: make(chan struct{})}
	a := newAsync(sink, func(c *AsyncConfig) { c.Batch = 1 })
	a.Record(Event{Type: Copied, Source: SourceWeb})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := a.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	close(sink.block)
}

func TestResultIDs(t *testing.T) {
	if id := NewResultID(); !ValidResultID(id) {
		t.Fatalf("generated id %q is not valid", id)
	}
	for _, bad := range []string{"", "short", "UPPERCASE12345", "has space 1234", strings.Repeat("a", 33), "ключ-ключ-ключ"} {
		if ValidResultID(bad) {
			t.Errorf("%q must be invalid", bad)
		}
	}
	if a, b := NewResultID(), NewResultID(); a == b {
		t.Error("ids must be random")
	}
}
