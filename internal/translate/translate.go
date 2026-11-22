// Package translate holds the translation use case: ready-made pairs are free, a user's own
// text is translated by AI against a daily free quota and a global daily spending ceiling.
// It defines the ports it needs and knows nothing about HTTP, SQL or the AI provider's API.
package translate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dyudin0821/bestregards/internal/events"
	"github.com/dyudin0821/bestregards/internal/limits"
	"github.com/dyudin0821/bestregards/internal/pairs"
	"github.com/dyudin0821/bestregards/internal/user"
)

// MaxTextRunes is the longest original text accepted.
const MaxTextRunes = 6000

// maxPreviousRunes bounds the earlier translation echoed back to the model for "another variant".
const maxPreviousRunes = 12000

// DefaultMaxOutputTokens is the worst-case completion size assumed when none is configured.
const DefaultMaxOutputTokens = 1800

// Levels: 1 = "почти человек", 2 = "корпоративный сотрудник", 3 = "HR-директор 3000".
const (
	MinLevel = 1
	MaxLevel = 3
)

// Errors the delivery layer maps to responses. Refunds have already happened when they are returned.
var (
	ErrAIDisabled      = errors.New("ai is not configured")
	ErrQuotaExhausted  = errors.New("free quota exhausted")
	ErrBudgetExhausted = errors.New("free budget exhausted")
	ErrFreeStopped     = errors.New("free translations are switched off")
	ErrUnavailable     = errors.New("ai is unavailable")
)

// ValidationError describes bad input in words safe to show to the user.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// QuotaExhaustedError carries the quota state so the caller can say when it renews.
type QuotaExhaustedError struct{ Quota Quota }

func (e *QuotaExhaustedError) Error() string { return ErrQuotaExhausted.Error() }
func (e *QuotaExhaustedError) Unwrap() error { return ErrQuotaExhausted }

// Request is what the user wants translated. A non-empty Previous asks for another variant.
type Request struct {
	Text     string
	Mode     string
	Level    int
	Previous string
	Channel  events.Source // where the request came from, for the metric only
}

// Source tells where a translation came from.
type Source string

const (
	SourcePair Source = "pair"
	SourceAI   Source = "ai"
)

// Quota is the user's free allowance for the current day.
type Quota struct {
	Limit    int
	Used     int
	ResetsAt time.Time
}

// Remaining is how many free translations are left today.
func (q Quota) Remaining() int { return max(q.Limit-q.Used, 0) }

// Result is a finished translation and the user's quota after it.
type Result struct {
	ID     string // ties a later "copied" event to this translation
	Text   string
	Source Source
	Quota  Quota
}

// Usage is the token count the provider reports for one completion.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Completion is the provider's answer.
type Completion struct {
	Text  string
	Usage Usage
}

// Ports, implemented by outer layers.

// Completer produces the AI translation.
type Completer interface {
	Complete(ctx context.Context, req Request) (Completion, error)
}

// Quotas keeps the per-user, per-day counters. Reserve must be atomic: concurrent requests of
// one user can never get more than limit reservations in a day.
type Quotas interface {
	ReserveQuota(ctx context.Context, userID int64, day time.Time, limit int) (used int, ok bool, err error)
	RefundQuota(ctx context.Context, userID int64, day time.Time) error
	QuotaUsed(ctx context.Context, userID int64, day time.Time) (int, error)
}

// Claim is one reservation against the daily ceiling.
type Claim struct {
	Micro    int64
	Cap      int64 // the most all free translations together may have spent by now
	Guest    bool  // the claimant is an anonymous guest, whose spend is counted apart
	GuestCap int64 // for a guest: the most all guests together may have spent by now
}

// Budget keeps the global spending of free translations, in millionths of a dollar per day, and
// the part of it spent by guests.
type Budget interface {
	// ReserveBudget atomically adds c.Micro to the day's spending unless that would take it past
	// c.Cap or, for a guest, take the guests' spending past c.GuestCap, and reports whether it did.
	// The reservation is what makes the ceiling hold under concurrency: money is claimed before
	// the provider is asked, not booked after it answered.
	ReserveBudget(ctx context.Context, day time.Time, c Claim) (bool, error)
	// AddBudgetSpend adjusts the day's spending by delta (and the guests' too when guest is set);
	// a negative delta releases the part of a reservation that was not used.
	AddBudgetSpend(ctx context.Context, day time.Time, delta int64, guest bool) error
}

// FreeSwitch lets an operator switch free AI translations off for everyone. Optional.
type FreeSwitch interface {
	FreeStopped(ctx context.Context) (bool, error)
}

// Config wires the use case. AI may be nil (translation of own text is then reported as disabled).
type Config struct {
	Pairs            *pairs.Catalog
	Quotas           Quotas
	Budget           Budget
	Switch           FreeSwitch
	MaxOutputTokens  int // the most one completion can produce; 0 means DefaultMaxOutputTokens
	AI               Completer
	Events           events.Recorder // optional
	Prices           limits.Prices
	DailyBudgetMicro int64
	Location         *time.Location
	Now              func() time.Time
	Logger           *slog.Logger
}

// Service implements the translation use cases. The wiring is private so that what New validated
// cannot be swapped afterwards.
type Service struct {
	cfg Config
}

// New validates the wiring and returns the service.
func New(c Config) (*Service, error) {
	switch {
	case c.Pairs == nil || c.Quotas == nil || c.Budget == nil:
		return nil, errors.New("translate: pairs, quotas and budget are required")
	case c.Location == nil:
		return nil, errors.New("translate: location is required")
	case c.AI != nil && c.DailyBudgetMicro <= 0:
		return nil, errors.New("translate: a positive daily budget is required when AI is enabled")
	case c.AI != nil && c.Prices.InputPerMtok <= 0 && c.Prices.OutputPerMtok <= 0:
		return nil, errors.New("translate: token prices are required when AI is enabled")
	}
	if c.MaxOutputTokens <= 0 {
		c.MaxOutputTokens = DefaultMaxOutputTokens
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Events == nil {
		c.Events = events.Discard
	}
	return &Service{cfg: c}, nil
}

// AIEnabled reports whether translation of a user's own text is available.
func (s *Service) AIEnabled() bool { return s.cfg.AI != nil }

// Status returns the user's free quota for today.
func (s *Service) Status(ctx context.Context, u user.User) (Quota, error) {
	now := s.cfg.Now()
	used, err := s.cfg.Quotas.QuotaUsed(ctx, u.ID, limits.Day(now, s.cfg.Location))
	if err != nil {
		return Quota{}, fmt.Errorf("read quota: %w", err)
	}
	return s.quota(u, now, used), nil
}

func (s *Service) quota(u user.User, now time.Time, used int) Quota {
	return Quota{Limit: limits.DailyLimit(u, now, s.cfg.Location), Used: used, ResetsAt: limits.ResetsAt(now, s.cfg.Location)}
}

// Translate returns a ready-made pair for free when the text matches one, otherwise it translates
// with AI. Anything that fails after the quota was reserved gives the reservation back.
func (s *Service) Translate(ctx context.Context, u user.User, req Request) (Result, error) {
	req, err := s.validate(req)
	if err != nil {
		return Result{}, err
	}
	now := s.cfg.Now()
	day := limits.Day(now, s.cfg.Location)

	if req.Previous == "" {
		if p, ok := s.cfg.Pairs.Find(req.Mode, req.Text); ok {
			used, err := s.cfg.Quotas.QuotaUsed(ctx, u.ID, day)
			if err != nil {
				return Result{}, fmt.Errorf("read quota: %w", err)
			}
			id := events.NewResultID()
			s.record(u, req, events.TranslateDone, events.KindPair, id)
			return Result{ID: id, Text: p.Output, Source: SourcePair, Quota: s.quota(u, now, used)}, nil
		}
	}
	if s.cfg.AI == nil {
		return Result{}, ErrAIDisabled
	}

	fund, err := s.fund(ctx, u, req, now, day)
	if err != nil {
		return Result{}, err
	}
	settled, attempted := false, false
	defer func() {
		if !settled {
			s.unfund(ctx, u.ID, day, fund, req, attempted)
		}
	}()

	attempted = true
	c, err := s.cfg.AI.Complete(ctx, req)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	settled = true
	s.settle(ctx, day, fund, s.actualCost(req, c))

	id := events.NewResultID()
	s.record(u, req, events.TranslateDone, events.KindAI, id)
	return Result{ID: id, Text: c.Text, Source: SourceAI, Quota: s.quota(u, now, fund.used)}, nil
}

// funding is what pays for one AI translation.
type funding struct {
	used     int   // translations used today, including this one
	reserved int64 // money claimed against the ceiling before the provider is asked
	guest    bool  // the money was claimed as a guest's, so it is adjusted in the guests' books too
}

// fund reserves what pays for a translation: one unit of the daily allowance and room under the
// daily spending ceiling.
func (s *Service) fund(ctx context.Context, u user.User, req Request, now, day time.Time) (funding, error) {
	limit := limits.DailyLimit(u, now, s.cfg.Location)
	stopped := false
	if s.cfg.Switch != nil {
		var err error
		if stopped, err = s.cfg.Switch.FreeStopped(ctx); err != nil {
			return funding{}, fmt.Errorf("read free switch: %w", err)
		}
	}
	ceilingHit := false
	if !stopped {
		reserved, ok, err := s.cfg.Quotas.ReserveQuota(ctx, u.ID, day, limit)
		if err != nil {
			return funding{}, fmt.Errorf("reserve quota: %w", err)
		}
		if ok {
			worst := s.worstCase(req)
			claimed, err := s.cfg.Budget.ReserveBudget(ctx, day, Claim{
				Micro:    worst,
				Cap:      limits.BudgetCap(s.cfg.DailyBudgetMicro, now, s.cfg.Location),
				Guest:    u.Guest(),
				GuestCap: limits.GuestBudgetCap(s.cfg.DailyBudgetMicro, now, s.cfg.Location),
			})
			if err != nil {
				s.refund(ctx, u.ID, day)
				return funding{}, fmt.Errorf("reserve budget: %w", err)
			}
			if claimed {
				return funding{used: reserved, reserved: worst, guest: u.Guest()}, nil
			}
			s.refund(ctx, u.ID, day)
			ceilingHit = true
		}
	}

	if stopped {
		return funding{}, ErrFreeStopped
	}
	if ceilingHit {
		s.record(u, req, events.BudgetExhausted, events.KindNone, "")
		return funding{}, ErrBudgetExhausted
	}
	s.record(u, req, events.QuotaExhausted, events.KindNone, "")
	return funding{}, &QuotaExhaustedError{Quota: s.quota(u, now, limit)}
}

// unfund undoes what fund reserved after a translation that did not happen.
//
// When the provider was asked, it may have billed for what it received, so the input tokens are
// charged anyway, and if the caller gave up while it worked the unit is not handed back: otherwise
// opening a request and dropping it would be a free, unmetered call.
func (s *Service) unfund(ctx context.Context, userID int64, day time.Time, f funding, req Request, attempted bool) {
	dctx, cancel := detached(ctx)
	defer cancel()
	var input int64
	if attempted {
		input = s.inputCost(req)
	}
	callerLeft := attempted && ctx.Err() != nil
	if release := f.reserved - input; release > 0 {
		if err := s.cfg.Budget.AddBudgetSpend(dctx, day, -release, f.guest); err != nil {
			s.cfg.Logger.ErrorContext(dctx, "release budget", "err", err)
		}
	}
	if !callerLeft {
		s.refund(ctx, userID, day)
	}
}

// record reports a server-side event. It never blocks and never fails the translation.
func (s *Service) record(u user.User, req Request, t events.Type, kind events.Kind, resultID string) {
	source := req.Channel
	if source == "" {
		source = events.SourceMiniApp
	}
	s.cfg.Events.Record(events.Event{
		Type: t, Kind: kind, Mode: req.Mode, Level: req.Level, Source: source,
		ResultID: resultID, Campaign: u.Source, UserID: u.ID,
	})
}

func (s *Service) validate(req Request) (Request, error) {
	req.Text = strings.TrimSpace(req.Text)
	switch {
	case req.Text == "":
		return req, &ValidationError{"Сначала напишите, что вы на самом деле думаете."}
	case utf8.RuneCountInString(req.Text) > MaxTextRunes:
		return req, &ValidationError{fmt.Sprintf("Сообщение слишком длинное. Максимум — %d символов.", MaxTextRunes)}
	case !s.knownMode(req.Mode) || req.Level < MinLevel || req.Level > MaxLevel:
		return req, &ValidationError{"Проверьте режим и уровень корпоративности."}
	}
	if utf8.RuneCountInString(req.Previous) > maxPreviousRunes {
		req.Previous = string([]rune(req.Previous)[:maxPreviousRunes])
	}
	return req, nil
}

func (s *Service) knownMode(id string) bool {
	for _, m := range s.cfg.Pairs.Modes {
		if m.ID == id {
			return true
		}
	}
	return false
}

// The request context may already be cancelled (the client left); the books must still balance.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func (s *Service) refund(ctx context.Context, userID int64, day time.Time) {
	ctx, cancel := detached(ctx)
	defer cancel()
	if err := s.cfg.Quotas.RefundQuota(ctx, userID, day); err != nil {
		s.cfg.Logger.ErrorContext(ctx, "refund quota", "user", userID, "err", err)
	}
}

// estimatedTokens is what a request is assumed to send: a generous guess of two characters per token.
func estimatedInputTokens(req Request) int {
	return utf8.RuneCountInString(req.Text+req.Previous)/2 + 400
}

// inputCost is the least a provider call costs once it has been made.
func (s *Service) inputCost(req Request) int64 {
	return s.cfg.Prices.CostMicro(estimatedInputTokens(req), 0)
}

// worstCase is the most one call can cost: the whole input and a full-length answer.
func (s *Service) worstCase(req Request) int64 {
	return s.cfg.Prices.CostMicro(estimatedInputTokens(req), s.cfg.MaxOutputTokens)
}

// actualCost prices a finished call from the usage the provider reported.
func (s *Service) actualCost(req Request, c Completion) int64 {
	in, out := c.Usage.InputTokens, c.Usage.OutputTokens
	if in == 0 && out == 0 {
		// The provider did not report usage; charge a generous estimate rather than nothing.
		in = estimatedInputTokens(req)
		out = utf8.RuneCountInString(c.Text)/2 + 50
	}
	return s.cfg.Prices.CostMicro(in, out)
}

// settle replaces the reservation with what the finished call really cost.
func (s *Service) settle(ctx context.Context, day time.Time, f funding, cost int64) {
	ctx, cancel := detached(ctx)
	defer cancel()
	if err := s.cfg.Budget.AddBudgetSpend(ctx, day, cost-f.reserved, f.guest); err != nil {
		s.cfg.Logger.ErrorContext(ctx, "record spend", "err", err)
	}
}
