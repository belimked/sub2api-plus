package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type tailerConfig struct {
	MainlandBaseURL string
	AdminEmail      string
	AdminPassword   string
	PollInterval    time.Duration
	BatchSize       int
	HTTPTimeout     time.Duration
	CursorScope     string
}

func loadTailerConfig() (tailerConfig, error) {
	cfg := tailerConfig{
		MainlandBaseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("FEDERATION_MAINLAND_BASE_URL")), "/"),
		AdminEmail:      strings.TrimSpace(os.Getenv("FEDERATION_ADMIN_EMAIL")),
		AdminPassword:   os.Getenv("FEDERATION_ADMIN_PASSWORD"),
		PollInterval:    5 * time.Second,
		BatchSize:       50,
		HTTPTimeout:     20 * time.Second,
		CursorScope:     "overseas_to_mainland",
	}

	if cfg.MainlandBaseURL == "" {
		return cfg, fmt.Errorf("FEDERATION_MAINLAND_BASE_URL is required")
	}
	if cfg.AdminEmail == "" || cfg.AdminPassword == "" {
		return cfg, fmt.Errorf("FEDERATION_ADMIN_EMAIL and FEDERATION_ADMIN_PASSWORD are required")
	}

	if raw := strings.TrimSpace(os.Getenv("FEDERATION_POLL_INTERVAL")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return cfg, fmt.Errorf("invalid FEDERATION_POLL_INTERVAL: %w", err)
		}
		cfg.PollInterval = d
	}
	if raw := strings.TrimSpace(os.Getenv("FEDERATION_BATCH_SIZE")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return cfg, fmt.Errorf("invalid FEDERATION_BATCH_SIZE: %q", raw)
		}
		cfg.BatchSize = n
	}
	if raw := strings.TrimSpace(os.Getenv("FEDERATION_HTTP_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return cfg, fmt.Errorf("invalid FEDERATION_HTTP_TIMEOUT: %w", err)
		}
		cfg.HTTPTimeout = d
	}
	if raw := strings.TrimSpace(os.Getenv("FEDERATION_CURSOR_SCOPE")); raw != "" {
		cfg.CursorScope = raw
	}

	return cfg, nil
}
