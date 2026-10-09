// Command federation-pusher polls federation_outbox_events for pending
// "user.upsert" events written by the FederationOutboxMixin ent hook
// (backend/ent/schema/mixins/federation_outbox.go) and delivers them to an
// overseas sub2api-plus deployment's admin API. It is an independent process
// deployed alongside the mainland server, connecting to the same database;
// it never runs inside the main server's request path.
//
// Config is read from environment variables (defaults noted below); it
// reuses the server's DATABASE_* / TIMEZONE settings via config.LoadForBootstrap
// so it points at the same mainland database:
//
//	FEDERATION_OVERSEAS_BASE_URL  (required) e.g. https://api.example.com
//	FEDERATION_ADMIN_EMAIL        (required) admin login for the overseas API
//	FEDERATION_ADMIN_PASSWORD     (required)
//	FEDERATION_POLL_INTERVAL      default 5s
//	FEDERATION_BATCH_SIZE         default 20
//	FEDERATION_MAX_ATTEMPTS       default 8 (row marked failed after this many tries)
//	FEDERATION_HTTP_TIMEOUT       default 20s
//	FEDERATION_DELIVERED_RETENTION default 168h; delivered rows older than
//	                              this are deleted (at most hourly); 0 disables
//
// Subcommand `federation-pusher backfill-users [--dry-run]` queues a
// balance.snapshot for every existing user, plus a user.upsert (with
// password_hash) for non-admins, and exits; the running pusher then delivers
// them. It needs
// only the database settings, not the FEDERATION_OVERSEAS_* ones.
//
// Known limitation: created overseas mirror accounts get a random password
// the operator never sees, because this change doesn't decide an overseas
// login/password strategy — see openspec/changes/federation-outbox-sync/design.md.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	dbent "github.com/LuckyKuang/sub2api-plus/ent"
	_ "github.com/LuckyKuang/sub2api-plus/ent/runtime"
	"github.com/LuckyKuang/sub2api-plus/internal/config"
	"github.com/LuckyKuang/sub2api-plus/internal/repository"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if len(os.Args) > 1 && os.Args[1] == "backfill-users" {
		os.Exit(runBackfill(logger, os.Args[2:]))
	}

	cfg, err := loadPusherConfig()
	if err != nil {
		logger.Error("federation-pusher: config", "error", err)
		os.Exit(1)
	}

	client, err := openMainlandDB()
	if err != nil {
		logger.Error("federation-pusher: init db", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := client.Close(); err != nil {
			logger.Error("federation-pusher: close db", "error", err)
		}
	}()

	overseas := newOverseasClient(cfg, logger)
	p := &pusher{db: client, overseas: overseas, cfg: cfg, logger: logger}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	logger.Info("federation-pusher: started",
		"overseas_base_url", cfg.OverseasBaseURL,
		"poll_interval", cfg.PollInterval,
		"batch_size", cfg.BatchSize,
		"delivered_retention", cfg.DeliveredRetention)

	for {
		if err := p.runOnce(ctx); err != nil {
			logger.Error("federation-pusher: run once", "error", err)
		}
		select {
		case <-ctx.Done():
			logger.Info("federation-pusher: shutting down")
			return
		case <-ticker.C:
		}
	}
}

// openMainlandDB connects to this deployment's database using the main
// server's DATABASE_*/TIMEZONE settings.
func openMainlandDB() (*dbent.Client, error) {
	appCfg, err := config.LoadForBootstrap()
	if err != nil {
		return nil, err
	}
	client, _, err := repository.InitEnt(appCfg)
	return client, err
}

func runBackfill(logger *slog.Logger, args []string) int {
	fs := flag.NewFlagSet("backfill-users", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "only report how many users would be queued")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	client, err := openMainlandDB()
	if err != nil {
		logger.Error("federation-pusher backfill-users: init db", "error", err)
		return 1
	}
	defer func() { _ = client.Close() }()

	n, err := backfillUsers(context.Background(), client, *dryRun)
	if err != nil {
		logger.Error("federation-pusher backfill-users: failed", "queued_before_error", n, "error", err)
		return 1
	}
	logger.Info("federation-pusher backfill-users: done", "users", n, "dry_run", *dryRun)
	return 0
}
