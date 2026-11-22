package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dyudin0821/bestregards/internal/config"
	"github.com/dyudin0821/bestregards/internal/store"
)

const purgeTimeout = 10 * time.Minute

// Purge deletes events, guests and old quota counters past their retention. Run it daily (a
// Kubernetes CronJob); it needs only the database, not the whole service configuration. A failure
// of one step does not skip the others: all run, and their errors are returned together.
func Purge(ctx context.Context, cfg config.Purge, log *slog.Logger) error {
	ctx, cancel := context.WithTimeout(ctx, purgeTimeout)
	defer cancel()
	db, err := store.Open(ctx, store.Config{URL: cfg.DatabaseURL, MaxConns: 3})
	if err != nil {
		return err
	}
	defer db.Close()
	now := time.Now()
	var errs []error
	removed, err := db.PurgeEvents(ctx, now.Add(-cfg.EventsRetention))
	log.Info("events purged", "removed", removed, "retentionDays", int(cfg.EventsRetention.Hours()/24))
	if err != nil {
		errs = append(errs, fmt.Errorf("purge events: %w", err))
	}
	guests, err := db.PurgeGuests(ctx, now.Add(-cfg.GuestRetention))
	log.Info("guests purged", "removed", guests, "retentionDays", int(cfg.GuestRetention.Hours()/24))
	if err != nil {
		errs = append(errs, fmt.Errorf("purge guests: %w", err))
	}
	quota, err := db.PurgeQuota(ctx, now.Add(-cfg.QuotaRetention))
	log.Info("quota counters purged", "removed", quota, "retentionDays", int(cfg.QuotaRetention.Hours()/24))
	if err != nil {
		errs = append(errs, fmt.Errorf("purge quota: %w", err))
	}
	return errors.Join(errs...)
}
