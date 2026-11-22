package events

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Sink stores a batch of events.
type Sink interface {
	InsertEvents(ctx context.Context, rows []Row) error
}

// AsyncConfig tunes the queue. Zero values get sensible defaults.
type AsyncConfig struct {
	Pepper     []byte
	Queue      int           // events waiting for the writer; more are dropped
	Batch      int           // most rows per insert
	FlushEvery time.Duration // how long a partial batch waits
	WriteLimit time.Duration // timeout of one insert
	Now        func() time.Time
	Logger     *slog.Logger
}

// Async queues events and writes them in batches in the background. A full queue or a failing
// database costs events, never latency or errors for the user.
type Async struct {
	AsyncConfig
	sink Sink

	mu      sync.RWMutex
	closed  bool
	ch      chan Row
	done    chan struct{}
	dropped atomic.Int64
}

var _ Recorder = (*Async)(nil)

// NewAsync starts the writer goroutine. Call Close to flush it.
func NewAsync(sink Sink, c AsyncConfig) *Async {
	if c.Queue <= 0 {
		c.Queue = 2048
	}
	if c.Batch <= 0 {
		c.Batch = 100
	}
	if c.FlushEvery <= 0 {
		c.FlushEvery = 2 * time.Second
	}
	if c.WriteLimit <= 0 {
		c.WriteLimit = 5 * time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	a := &Async{AsyncConfig: c, sink: sink, ch: make(chan Row, c.Queue), done: make(chan struct{})}
	go a.run()
	return a
}

// Record implements Recorder.
func (a *Async) Record(e Event) {
	row := Row{
		At: a.Now(), Type: e.Type, Kind: e.Kind, Mode: e.Mode, Level: e.Level, Source: e.Source,
		ResultID: e.ResultID, UserHash: UserHash(a.Pepper, e.UserID), Campaign: TrimCampaign(e.Campaign),
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		a.dropped.Add(1)
		return
	}
	select {
	case a.ch <- row:
	default:
		if n := a.dropped.Add(1); n == 1 || n%1000 == 0 {
			a.Logger.Warn("event queue is full, dropping events", "dropped", n)
		}
	}
}

// Dropped is how many events were lost to a full queue or a closed recorder.
func (a *Async) Dropped() int64 { return a.dropped.Load() }

// Close stops accepting events and writes what is queued, waiting at most until ctx ends.
func (a *Async) Close(ctx context.Context) error {
	a.mu.Lock()
	if !a.closed {
		a.closed = true
		close(a.ch)
	}
	a.mu.Unlock()
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Async) run() {
	defer close(a.done)
	timer := time.NewTimer(a.FlushEvery)
	defer timer.Stop()
	batch := make([]Row, 0, a.Batch)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), a.WriteLimit)
		defer cancel()
		if err := a.sink.InsertEvents(ctx, batch); err != nil {
			// The batch is dropped: retrying a broken database would only grow the queue.
			a.dropped.Add(int64(len(batch)))
			a.Logger.Error("write events", "lost", len(batch), "err", err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case row, ok := <-a.ch:
			if !ok {
				flush()
				return
			}
			batch = append(batch, row)
			if len(batch) >= a.Batch {
				flush()
			}
		case <-timer.C:
			flush()
			timer.Reset(a.FlushEvery)
		}
	}
}
