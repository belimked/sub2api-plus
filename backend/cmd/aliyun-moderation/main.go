// Command aliyun-moderation exposes an OpenAI-compatible POST /v1/moderations
// backed by Aliyun AI Guardrails (TextModerationPlus, default service
// query_security_check_pro), so sub2api-plus Content Moderation can use it by
// pointing its base URL here. It runs as a sidecar next to the server and has
// no database access. See README.md in this directory.
//
// Config (environment):
//
//	MODERATION_API_KEY             (required) bearer token sub2api sends
//	ALIYUN_ACCESS_KEY_ID           (required) RAM user with AliyunYundunGreenWebFullAccess
//	ALIYUN_ACCESS_KEY_SECRET       (required)
//	ALIYUN_GREEN_REGION            default cn-shanghai (overseas: ap-southeast-1)
//	ALIYUN_GREEN_ENDPOINT          default https://green-cip.<region>.aliyuncs.com
//	ALIYUN_GREEN_SERVICE           default query_security_check_pro
//	ALIYUN_GREEN_MIN_RISK_LEVEL    low|medium|high, default high
//	ALIYUN_GREEN_ATTACK_MIN_LEVEL  low|medium|high|off, default high
//	ALIYUN_GREEN_TIMEOUT           default 3s per Aliyun call
//	ALIYUN_GREEN_MAX_CHUNKS        default 3 (2000-char chunks checked per request)
//	MODERATION_LISTEN              default :8090
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := loadAdapterConfig()
	if err != nil {
		logger.Error("aliyun-moderation: config", "error", err)
		os.Exit(1)
	}

	s := &server{cfg: cfg, green: newGreenClient(cfg), logger: logger}
	httpServer := &http.Server{Addr: cfg.Listen, Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	logger.Info("aliyun-moderation: started", "listen", cfg.Listen, "endpoint", cfg.Endpoint, "service", cfg.Service)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("aliyun-moderation: serve", "error", err)
		os.Exit(1)
	}
}
