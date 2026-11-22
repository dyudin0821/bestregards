package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dyudin0821/bestregards/internal/admin"
	"github.com/dyudin0821/bestregards/internal/store/db"
)

var _ admin.Reader = (*Store)(nil)

// Activity counts what people did in [from, to).
func (s *Store) Activity(ctx context.Context, from, to time.Time) (admin.Activity, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	done, err := s.q.ActivityDone(ctx, db.ActivityDoneParams{At: from, At_2: to})
	if err != nil {
		return admin.Activity{}, err
	}
	a := admin.Activity{Pair: done.PairN, AI: done.AiN, Copied: done.CopiedN, People: done.UsersN}
	counts, err := s.q.CountEventsByType(ctx, db.CountEventsByTypeParams{At: from, At_2: to})
	if err != nil {
		return admin.Activity{}, err
	}
	for _, c := range counts {
		switch c.Type {
		case "quota_exhausted":
			a.QuotaStops = c.N
		case "budget_exhausted":
			a.BudgetStops = c.N
		}
	}
	return a, nil
}

const (
	settingFreeStopped = "free_ai_stopped"
	settingGuestsOff   = "guests_off"
)

// FreeStopped reports whether AI translations are switched off.
func (s *Store) FreeStopped(ctx context.Context) (bool, error) {
	return s.flag(ctx, settingFreeStopped)
}

// SetFreeStopped switches AI translations off or on.
func (s *Store) SetFreeStopped(ctx context.Context, stopped bool) error {
	return s.setFlag(ctx, settingFreeStopped, stopped)
}

// GuestsOff reports whether new guest accounts are switched off.
func (s *Store) GuestsOff(ctx context.Context) (bool, error) { return s.flag(ctx, settingGuestsOff) }

// SetGuestsOff stops or resumes handing out guest accounts.
func (s *Store) SetGuestsOff(ctx context.Context, off bool) error {
	return s.setFlag(ctx, settingGuestsOff, off)
}

// flag reads a switch; one that was never set is off.
func (s *Store) flag(ctx context.Context, key string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	v, err := s.q.GetSetting(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return v == "1", err
}

func (s *Store) setFlag(ctx context.Context, key string, on bool) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	v := "0"
	if on {
		v = "1"
	}
	return s.q.PutSetting(ctx, db.PutSettingParams{Key: key, Value: v})
}
