package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type adapterConfig struct {
	Listen          string
	APIKey          string
	AccessKeyID     string
	AccessKeySecret string
	Region          string
	Endpoint        string
	Service         string
	// ContentMinLevel is the lowest Aliyun RiskLevel that is reported as a
	// flagged OpenAI category.
	ContentMinLevel riskLevel
	// AttackMinLevel is the same for AttackLevel; levelOff ignores prompt
	// attack results entirely.
	AttackMinLevel riskLevel
	Timeout        time.Duration
	ChunkChars     int
	MaxChunks      int
}

func loadAdapterConfig() (adapterConfig, error) {
	cfg := adapterConfig{
		Listen:          envOr("MODERATION_LISTEN", ":8090"),
		APIKey:          strings.TrimSpace(os.Getenv("MODERATION_API_KEY")),
		AccessKeyID:     strings.TrimSpace(os.Getenv("ALIYUN_ACCESS_KEY_ID")),
		AccessKeySecret: strings.TrimSpace(os.Getenv("ALIYUN_ACCESS_KEY_SECRET")),
		Region:          envOr("ALIYUN_GREEN_REGION", "cn-shanghai"),
		Service:         envOr("ALIYUN_GREEN_SERVICE", "query_security_check_pro"),
		ContentMinLevel: levelHigh,
		AttackMinLevel:  levelHigh,
		Timeout:         3 * time.Second,
		ChunkChars:      2000,
		MaxChunks:       3,
	}
	cfg.Endpoint = strings.TrimRight(envOr("ALIYUN_GREEN_ENDPOINT", "https://green-cip."+cfg.Region+".aliyuncs.com"), "/")

	if cfg.APIKey == "" {
		return cfg, fmt.Errorf("MODERATION_API_KEY is required")
	}
	if cfg.AccessKeyID == "" || cfg.AccessKeySecret == "" {
		return cfg, fmt.Errorf("ALIYUN_ACCESS_KEY_ID and ALIYUN_ACCESS_KEY_SECRET are required")
	}

	var err error
	if cfg.ContentMinLevel, err = parseMinLevel("ALIYUN_GREEN_MIN_RISK_LEVEL", cfg.ContentMinLevel, false); err != nil {
		return cfg, err
	}
	if cfg.AttackMinLevel, err = parseMinLevel("ALIYUN_GREEN_ATTACK_MIN_LEVEL", cfg.AttackMinLevel, true); err != nil {
		return cfg, err
	}
	if raw := strings.TrimSpace(os.Getenv("ALIYUN_GREEN_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return cfg, fmt.Errorf("invalid ALIYUN_GREEN_TIMEOUT: %q", raw)
		}
		cfg.Timeout = d
	}
	if raw := strings.TrimSpace(os.Getenv("ALIYUN_GREEN_MAX_CHUNKS")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return cfg, fmt.Errorf("invalid ALIYUN_GREEN_MAX_CHUNKS: %q", raw)
		}
		cfg.MaxChunks = n
	}
	return cfg, nil
}

func parseMinLevel(name string, def riskLevel, allowOff bool) (riskLevel, error) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	if raw == "" {
		return def, nil
	}
	if raw == "off" && allowOff {
		return levelOff, nil
	}
	level := parseRiskLevel(raw)
	if level == levelNone || level == levelOff {
		return def, fmt.Errorf("invalid %s: %q (want low, medium or high)", name, raw)
	}
	return level, nil
}

func envOr(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}
