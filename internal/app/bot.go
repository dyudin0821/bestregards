package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/dyudin0821/bestregards/internal/admin"
	"github.com/dyudin0821/bestregards/internal/bot"
	"github.com/dyudin0821/bestregards/internal/config"
	"github.com/dyudin0821/bestregards/internal/server"
	"github.com/dyudin0821/bestregards/internal/store"
)

const webhookTimeout = 30 * time.Second

// enableBot mounts the Telegram webhook: the /start button and the operator's /admin panel.
func enableBot(opts *server.Options, cfg config.Config, db *store.Store, log *slog.Logger) error {
	tg := cfg.Telegram
	client, err := bot.NewClient(tg.BotToken, tg.APIURL)
	if err != nil {
		return err
	}
	admins := adminSet(tg.AdminIDs)
	if len(admins) == 0 {
		log.Warn("ADMIN_IDS is empty: the /admin panel is available to nobody")
	}
	opts.Bot = &bot.Handler{
		Secret:   tg.WebhookSecret,
		Telegram: client,
		Admins:   admins,
		Admin: admin.New(db, admin.Config{
			CeilingMicro: int64(cfg.AI.FreeBudgetUSD * 1_000_000), Location: cfg.QuotaLocation,
			Unlimited: admins,
		}),
		AppURL: tg.PublicURL + "/app",
		Log:    log,
	}
	return nil
}

// SetupWebhook points the bot at this service. Run it once after a deploy that changes PUBLIC_URL
// or WEBHOOK_SECRET; it is safe to repeat.
func SetupWebhook(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	tg := cfg.Telegram
	if tg.WebhookSecret == "" {
		return errors.New("WEBHOOK_SECRET is empty: the bot webhook is off, there is nothing to register")
	}
	client, err := bot.NewClient(tg.BotToken, tg.APIURL)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, webhookTimeout)
	defer cancel()
	url := tg.PublicURL + "/bot/webhook"
	if err := client.Setup(ctx, url, tg.WebhookSecret); err != nil {
		return err
	}
	log.Info("webhook registered", "url", url)
	return nil
}

// adminSet turns ADMIN_IDS into a lookup: who may use /admin and who has no daily quota.
func adminSet(ids []int64) map[int64]bool {
	set := make(map[int64]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}
