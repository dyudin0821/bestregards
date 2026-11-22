// Package admin is the operator's view of the service: today's numbers, spending against the
// ceiling and a single user's card, plus the one switch an operator may flip. It defines the
// ports it needs and knows nothing about Telegram or SQL.
package admin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dyudin0821/bestregards/internal/limits"
	"github.com/dyudin0821/bestregards/internal/user"
)

// ErrNotFound means there is no such user.
var ErrNotFound = errors.New("not found")

// Activity is what people did in a time window.
type Activity struct {
	Pair, AI    int64 // translations shown: ready-made pairs and AI
	Copied      int64 // of those, how many were copied
	People      int64 // distinct people who translated, guests included
	QuotaStops  int64 // requests refused because the daily quota was used up
	BudgetStops int64 // requests refused because the daily ceiling was reached
}

// Done is every translation shown.
func (a Activity) Done() int64 { return a.Pair + a.AI }

// CopiedPercent is the headline metric: what share of translations was copied.
func (a Activity) CopiedPercent() float64 {
	if a.Done() == 0 {
		return 0
	}
	return 100 * float64(a.Copied) / float64(a.Done())
}

// Summary is one quota day: what people did and what the AI cost.
type Summary struct {
	Activity
	Since      time.Time
	SpentMicro int64 // AI cost of the day
}

// Budget is AI spending against the ceiling.
type Budget struct {
	FreeStopped       bool // an admin switched AI translations off
	GuestsOff         bool // an admin stopped handing out guest accounts
	SpentMicro        int64
	AllowedMicro      int64 // the most that may have been spent by this hour
	GuestSpentMicro   int64 // the part of SpentMicro spent by guests
	GuestAllowedMicro int64 // the most guests may have spent by this hour
	CeilingMicro      int64 // the whole day
	ResetsAt          time.Time
}

// Exhausted reports whether AI translations are stopped for everyone right now.
func (b Budget) Exhausted() bool { return b.SpentMicro >= b.AllowedMicro }

// Card is what an admin sees about one user.
type Card struct {
	User      user.User
	FreeUsed  int
	FreeLimit int
}

// Reader is the read side of the store.
type Reader interface {
	Activity(ctx context.Context, from, to time.Time) (Activity, error)
	BudgetSpent(ctx context.Context, day time.Time) (int64, error)
	GuestBudgetSpent(ctx context.Context, day time.Time) (int64, error)
	GetUser(ctx context.Context, id int64) (user.User, error) // user.ErrNotFound
	QuotaUsed(ctx context.Context, userID int64, day time.Time) (int, error)

	// The switches the panel flips.
	FreeStopped(ctx context.Context) (bool, error)
	SetFreeStopped(ctx context.Context, stopped bool) error
	GuestsOff(ctx context.Context) (bool, error)
	SetGuestsOff(ctx context.Context, off bool) error
}

// Config holds what the panel needs to interpret "today" and the ceiling.
type Config struct {
	CeilingMicro int64
	Location     *time.Location
	Now          func() time.Time
}

// Service implements the panel's use cases.
type Service struct {
	r   Reader
	cfg Config
}

// New wires the service. Location and Now default to UTC and the wall clock.
func New(r Reader, cfg Config) *Service {
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{r: r, cfg: cfg}
}

// Location is the zone in which the panel shows times and in which "today" is counted.
func (s *Service) Location() *time.Location { return s.cfg.Location }

// dayStart is midnight of the current quota day in the configured zone.
func (s *Service) dayStart(now time.Time) time.Time {
	y, m, d := now.In(s.cfg.Location).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, s.cfg.Location)
}

// Summary reports the quota day so far.
func (s *Service) Summary(ctx context.Context) (Summary, error) {
	return s.DaySummary(ctx, s.dayStart(s.cfg.Now()))
}

// DaySummary reports the quota day that starts at from, a midnight in the configured zone.
func (s *Service) DaySummary(ctx context.Context, from time.Time) (Summary, error) {
	a, err := s.r.Activity(ctx, from, from.AddDate(0, 0, 1))
	if err != nil {
		return Summary{}, fmt.Errorf("activity: %w", err)
	}
	spent, err := s.r.BudgetSpent(ctx, limits.Day(from, s.cfg.Location))
	if err != nil {
		return Summary{}, fmt.Errorf("spend: %w", err)
	}
	return Summary{Activity: a, Since: from, SpentMicro: spent}, nil
}

// Budget reports today's AI spending.
func (s *Service) Budget(ctx context.Context) (Budget, error) {
	now := s.cfg.Now()
	spent, err := s.r.BudgetSpent(ctx, limits.Day(now, s.cfg.Location))
	if err != nil {
		return Budget{}, fmt.Errorf("budget spent: %w", err)
	}
	guestSpent, err := s.r.GuestBudgetSpent(ctx, limits.Day(now, s.cfg.Location))
	if err != nil {
		return Budget{}, fmt.Errorf("guest budget spent: %w", err)
	}
	stopped, err := s.r.FreeStopped(ctx)
	if err != nil {
		return Budget{}, fmt.Errorf("free stopped: %w", err)
	}
	guestsOff, err := s.r.GuestsOff(ctx)
	if err != nil {
		return Budget{}, fmt.Errorf("guests off: %w", err)
	}
	return Budget{
		FreeStopped:       stopped,
		GuestsOff:         guestsOff,
		SpentMicro:        spent,
		AllowedMicro:      limits.BudgetCap(s.cfg.CeilingMicro, now, s.cfg.Location),
		GuestSpentMicro:   guestSpent,
		GuestAllowedMicro: limits.GuestBudgetCap(s.cfg.CeilingMicro, now, s.cfg.Location),
		CeilingMicro:      s.cfg.CeilingMicro,
		ResetsAt:          limits.ResetsAt(now, s.cfg.Location),
	}, nil
}

// User assembles a user's card. It returns ErrNotFound for an id nobody has used.
func (s *Service) User(ctx context.Context, id int64) (Card, error) {
	u, err := s.r.GetUser(ctx, id)
	if errors.Is(err, user.ErrNotFound) {
		return Card{}, ErrNotFound
	}
	if err != nil {
		return Card{}, fmt.Errorf("user: %w", err)
	}
	now := s.cfg.Now()
	used, err := s.r.QuotaUsed(ctx, id, limits.Day(now, s.cfg.Location))
	if err != nil {
		return Card{}, fmt.Errorf("quota: %w", err)
	}
	return Card{User: u, FreeUsed: used, FreeLimit: limits.DailyLimit(u, now, s.cfg.Location)}, nil
}

// FreeStopped reports whether an admin has switched AI translations off.
func (s *Service) FreeStopped(ctx context.Context) (bool, error) { return s.r.FreeStopped(ctx) }

// SetFreeStopped switches AI translations off or on for everyone. Ready-made pairs are not affected.
func (s *Service) SetFreeStopped(ctx context.Context, stopped bool) error {
	return s.r.SetFreeStopped(ctx, stopped)
}

// GuestsOff reports whether an admin has stopped handing out guest accounts.
func (s *Service) GuestsOff(ctx context.Context) (bool, error) { return s.r.GuestsOff(ctx) }

// SetGuestsOff stops or resumes new guest accounts. Guests who already have a session keep it.
func (s *Service) SetGuestsOff(ctx context.Context, off bool) error {
	return s.r.SetGuestsOff(ctx, off)
}
