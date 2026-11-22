// Package store is the PostgreSQL persistence layer.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/dyudin0821/bestregards/internal/auth"
	"github.com/dyudin0821/bestregards/internal/events"
	"github.com/dyudin0821/bestregards/internal/store/db"
	"github.com/dyudin0821/bestregards/internal/translate"
	"github.com/dyudin0821/bestregards/internal/user"
	"github.com/dyudin0821/bestregards/migrations"
)

// Store wraps a connection pool and the generated queries.
type Store struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

// Config describes the database connection.
type Config struct {
	URL      string
	MaxConns int32 // per process; replicas x MaxConns must stay below PostgreSQL max_connections. 0 = pgx default
	Tracing  bool  // a span per query, through the global OpenTelemetry tracer provider
}

// queryTimeout bounds every single query so a stuck database cannot hold a pooled connection
// for as long as the HTTP request context lives.
const queryTimeout = 5 * time.Second

// Open connects to PostgreSQL and applies pending migrations.
func Open(ctx context.Context, c Config) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(c.URL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if c.MaxConns > 0 {
		cfg.MaxConns = c.MaxConns
	}
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnLifetimeJitter = 5 * time.Minute // avoids recycling every connection at once
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = time.Minute
	cfg.PingTimeout = 3 * time.Second
	if c.Tracing {
		cfg.ConnConfig.Tracer = otelpgx.NewTracer() // statements carry placeholders, never the values
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool, q: db.New(pool)}, nil
}

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	provider, err := newProvider(sqlDB)
	if err != nil {
		return err
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

func newProvider(sqlDB *sql.DB) (*goose.Provider, error) {
	// Every replica migrates on start; the advisory lock makes them take turns instead of racing.
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(2, 30))
	if err != nil {
		return nil, fmt.Errorf("migrations locker: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS, goose.WithSessionLocker(locker))
	if err != nil {
		return nil, fmt.Errorf("migrations provider: %w", err)
	}
	return provider, nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// Ping reports whether the database is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

var (
	_ auth.Users       = (*Store)(nil)
	_ auth.GuestSwitch = (*Store)(nil)
	_ translate.Quotas = (*Store)(nil)
	_ translate.Budget = (*Store)(nil)
)

// UpsertUser registers the user on first launch (recording source) or refreshes last_seen_at.
func (s *Store) UpsertUser(ctx context.Context, id int64, source string) (user.User, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	row, err := s.q.UpsertUser(ctx, db.UpsertUserParams{TelegramID: id, Source: source})
	if err != nil {
		return user.User{}, err
	}
	return toUser(row), nil
}

// CreateGuest registers an anonymous website visitor.
func (s *Store) CreateGuest(ctx context.Context) (user.User, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	row, err := s.q.CreateGuest(ctx)
	if err != nil {
		return user.User{}, err
	}
	return toUser(row), nil
}

// GetUser returns the user or user.ErrNotFound.
func (s *Store) GetUser(ctx context.Context, id int64) (user.User, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	row, err := s.q.GetUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return user.User{}, user.ErrNotFound
	}
	if err != nil {
		return user.User{}, err
	}
	return toUser(row), nil
}

func toUser(r db.User) user.User {
	return user.User{ID: r.TelegramID, Source: r.Source, Banned: r.Banned, CreatedAt: r.CreatedAt, LastSeenAt: r.LastSeenAt}
}

// ReserveQuota atomically takes one free translation for the day. ok is false when the user
// already used limit of them.
func (s *Store) ReserveQuota(ctx context.Context, userID int64, day time.Time, limit int) (used int, ok bool, err error) {
	if limit < 1 {
		return 0, false, nil // the first insert of a day does not check the limit, so refuse here
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	n, err := s.q.ReserveQuota(ctx, db.ReserveQuotaParams{TelegramID: userID, Day: day, MaxUsed: int32(limit)})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return int(n), true, nil
}

// RefundQuota gives one reserved translation back. It never goes below zero.
func (s *Store) RefundQuota(ctx context.Context, userID int64, day time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	return s.q.RefundQuota(ctx, db.RefundQuotaParams{TelegramID: userID, Day: day})
}

// QuotaUsed returns how many free translations the user used on the day.
func (s *Store) QuotaUsed(ctx context.Context, userID int64, day time.Time) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	n, err := s.q.GetQuotaUsed(ctx, db.GetQuotaUsedParams{TelegramID: userID, Day: day})
	return int(n), err
}

// BudgetSpent returns the money spent on free translations on the day, in millionths of a dollar.
func (s *Store) BudgetSpent(ctx context.Context, day time.Time) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	return s.q.GetBudgetSpent(ctx, day)
}

// GuestBudgetSpent returns the part of the day's spending that guests are responsible for.
func (s *Store) GuestBudgetSpent(ctx context.Context, day time.Time) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	return s.q.GetGuestBudgetSpent(ctx, day)
}

// ReserveBudget adds c.Micro to the day's spending unless that would take it past c.Cap or, for a
// guest, take the guests' share past c.GuestCap. Concurrent reservations are serialized by the
// row, so together they can never pass either cap.
func (s *Store) ReserveBudget(ctx context.Context, day time.Time, c translate.Claim) (bool, error) {
	if c.Micro > c.Cap || (c.Guest && c.Micro > c.GuestCap) {
		return false, nil // the first insert of a day does not check the caps, so refuse here
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	_, err := s.q.ReserveBudget(ctx, db.ReserveBudgetParams{Day: day, Micro: c.Micro, Guest: c.Guest, Cap: c.Cap, GuestCap: c.GuestCap})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// AddBudgetSpend adjusts the day's spending by delta, which is negative when part of a reservation
// is released; concurrent adjustments all count. A guest's delta moves the guests' share as well.
func (s *Store) AddBudgetSpend(ctx context.Context, day time.Time, delta int64, guest bool) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	if delta < 0 {
		return s.q.ReleaseBudget(ctx, db.ReleaseBudgetParams{Day: day, Micro: -delta, Guest: guest})
	}
	return s.q.AddBudgetSpend(ctx, db.AddBudgetSpendParams{Day: day, Micro: delta, Guest: guest})
}

// InsertEvents stores a batch of events in one round trip. A repeated "copied" for the same
// result is ignored by the database.
func (s *Store) InsertEvents(ctx context.Context, rows []events.Row) error {
	if len(rows) == 0 {
		return nil
	}
	params := make([]db.InsertEventParams, len(rows))
	for i, r := range rows {
		params[i] = db.InsertEventParams{
			At: r.At, Type: string(r.Type), Kind: string(r.Kind), Mode: r.Mode, Level: int16(r.Level),
			Source: string(r.Source), ResultID: r.ResultID, UserHash: r.UserHash, Campaign: r.Campaign,
		}
	}
	var firstErr error
	results := s.q.InsertEvent(ctx, params)
	results.Exec(func(_ int, err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	})
	if err := results.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// PurgeGuests deletes guests created before the given time, together with their quota rows.
func (s *Store) PurgeGuests(ctx context.Context, before time.Time) (int64, error) {
	return s.q.PurgeGuests(ctx, before)
}

// PurgeQuota deletes the quota counters of days before the given one.
func (s *Store) PurgeQuota(ctx context.Context, before time.Time) (int64, error) {
	return s.q.PurgeQuota(ctx, before)
}

// purgeChunk is how many events one DELETE removes, so no single statement runs for long.
const purgeChunk = 10000

// PurgeEvents deletes events older than before, in chunks, and returns how many it removed. When
// it fails or ctx ends, the chunks already deleted stay deleted and the count says how many.
func (s *Store) PurgeEvents(ctx context.Context, before time.Time) (int64, error) {
	return s.purgeEvents(ctx, before, purgeChunk)
}

func (s *Store) purgeEvents(ctx context.Context, before time.Time, chunk int32) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := s.q.PurgeEventsChunk(ctx, db.PurgeEventsChunkParams{Before: before, Chunk: chunk})
		total += n
		if err != nil {
			return total, err
		}
		if n < int64(chunk) {
			return total, nil
		}
	}
}
