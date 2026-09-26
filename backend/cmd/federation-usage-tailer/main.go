// Command federation-usage-tailer tails the local usage_log table (an
// overseas sub2api-plus deployment's own usage, append-only) and pushes cost
// deductions to the mainland deployment's admin API, in strict usage_log.id
// order, never skipping a failed row -- see
// openspec/changes/federation-usage-tailer/proposal.md for why. Deploy this
// on the overseas side, connected to the overseas database; the pusher
// (backend/cmd/federation-pusher) is its mirror image on the mainland side.
//
// Config is read from environment variables. It reuses the local server's
// DATABASE_*/TIMEZONE settings via config.LoadForBootstrap to read this
// deployment's own usage_log:
//
//	FEDERATION_MAINLAND_BASE_URL  (required) e.g. https://mainland.example.com
//	FEDERATION_ADMIN_EMAIL        (required) admin login on the mainland API
//	FEDERATION_ADMIN_PASSWORD     (required)
//	FEDERATION_POLL_INTERVAL      default 5s
//	FEDERATION_BATCH_SIZE         default 50
//	FEDERATION_HTTP_TIMEOUT       default 20s
//	FEDERATION_CURSOR_SCOPE       default "overseas_to_mainland"
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/LuckyKuang/sub2api-plus/ent/runtime"
	"github.com/LuckyKuang/sub2api-plus/internal/config"
	"github.com/LuckyKuang/sub2api-plus/internal/repository"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := loadTailerConfig()
	if err != nil {
		logger.Error("federation-usage-tailer: config", "error", err)
		os.Exit(1)
	}

	appCfg, err := config.LoadForBootstrap()
	if err != nil {
		logger.Error("federation-usage-tailer: load app config", "error", err)
		os.Exit(1)
	}

	client, _, err := repository.InitEnt(appCfg)
	if err != nil {
		logger.Error("federation-usage-tailer: init db", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := client.Close(); err != nil {
			logger.Error("federation-usage-tailer: close db", "error", err)
		}
	}()

	mainland := newMainlandClient(cfg)
	t := &tailer{db: client, mainland: mainland, cfg: cfg, logger: logger}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	logger.Info("federation-usage-tailer: started",
		"mainland_base_url", cfg.MainlandBaseURL,
		"poll_interval", cfg.PollInterval,
		"batch_size", cfg.BatchSize,
		"cursor_scope", cfg.CursorScope)

	for {
		if err := t.runOnce(ctx); err != nil {
			logger.Error("federation-usage-tailer: run once", "error", err)
		}
		select {
		case <-ctx.Done():
			logger.Info("federation-usage-tailer: shutting down")
			return
		case <-ticker.C:
		}
	}
}
