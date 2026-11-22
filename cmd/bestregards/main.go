// Command bestregards runs the service and its two maintenance jobs. It only chooses what to run:
// parsing configuration and wiring live in packages config and app.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/dyudin0821/bestregards/internal/app"
	"github.com/dyudin0821/bestregards/internal/config"
	"github.com/dyudin0821/bestregards/internal/telemetry"
)

func main() {
	log := slog.New(telemetry.LogHandler(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, log, os.Args[1:])
	stop()
	if err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger, args []string) error {
	switch {
	case len(args) == 0:
		cfg, err := config.FromEnv(os.Getenv)
		if err != nil {
			return err
		}
		return app.Serve(ctx, cfg, log)
	case len(args) == 1 && args[0] == "purge":
		cfg, err := config.PurgeFromEnv(os.Getenv)
		if err != nil {
			return err
		}
		return app.Purge(ctx, cfg, log)
	case len(args) == 1 && args[0] == "setup-webhook":
		cfg, err := config.FromEnv(os.Getenv)
		if err != nil {
			return err
		}
		return app.SetupWebhook(ctx, cfg, log)
	}
	return errors.New("usage: bestregards [purge|setup-webhook]")
}
