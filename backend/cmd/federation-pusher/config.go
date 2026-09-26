package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type pusherConfig struct {
	OverseasBaseURL string
	AdminEmail      string
	AdminPassword   string
	PollInterval    time.Duration
	BatchSize       int
	MaxAttempts     int
	HTTPTimeout     time.Duration
	// DeliveredRetention is how long delivered outbox rows are kept before
	// the pusher deletes them; 0 disables the purge.
	DeliveredRetention time.Duration
}

func loadPusherConfig() (pusherConfig, error) {
	cfg := pusherConfig{
		OverseasBaseURL:    strings.TrimRight(strings.TrimSpace(os.Getenv("FEDERATION_OVERSEAS_BASE_URL")), "/"),
		AdminEmail:         strings.TrimSpace(os.Getenv("FEDERATION_ADMIN_EMAIL")),
		AdminPassword:      os.Getenv("FEDERATION_ADMIN_PASSWORD"),
		PollInterval:       5 * time.Second,
		BatchSize:          20,
		MaxAttempts:        8,
		HTTPTimeout:        20 * time.Second,
		DeliveredRetention: 7 * 24 * time.Hour,
	}

	if cfg.OverseasBaseURL == "" {
		return cfg, fmt.Errorf("FEDERATION_OVERSEAS_BASE_URL is required")
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
	if raw := strings.TrimSpace(os.Getenv("FEDERATION_MAX_ATTEMPTS")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return cfg, fmt.Errorf("invalid FEDERATION_MAX_ATTEMPTS: %q", raw)
		}
		cfg.MaxAttempts = n
	}
	if raw := strings.TrimSpace(os.Getenv("FEDERATION_HTTP_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return cfg, fmt.Errorf("invalid FEDERATION_HTTP_TIMEOUT: %w", err)
		}
		cfg.HTTPTimeout = d
	}
	if raw := strings.TrimSpace(os.Getenv("FEDERATION_DELIVERED_RETENTION")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d < 0 {
			return cfg, fmt.Errorf("invalid FEDERATION_DELIVERED_RETENTION: %q", raw)
		}
		cfg.DeliveredRetention = d
	}

	return cfg, nil
}
