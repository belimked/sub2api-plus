package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	AuditActionAPIKeyPush = "system.api_key.push"

	apiKeyPushPath        = "/api/gw/tokens"
	apiKeyPushTimeout     = 15 * time.Second
	apiKeyPushMaxRespBody = 4 * 1024
)

// APIKeyPushService pushes newly created API keys to the gateway behind the
// configured quick access URL. Pushes run in the background and never affect
// key creation; every attempt is recorded in the admin audit log.
type APIKeyPushService struct {
	settings  *SettingService
	groupRepo GroupRepository
	userRepo  UserRepository
	audit     *AuditLogService
	client    *http.Client
}

func NewAPIKeyPushService(settings *SettingService, groupRepo GroupRepository, userRepo UserRepository, audit *AuditLogService) *APIKeyPushService {
	return &APIKeyPushService{
		settings:  settings,
		groupRepo: groupRepo,
		userRepo:  userRepo,
		audit:     audit,
		client: &http.Client{
			Timeout: apiKeyPushTimeout,
			// The shared secret header must not follow redirects to another host.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// PushAsync schedules a background push of key; it returns immediately.
func (s *APIKeyPushService) PushAsync(key *APIKey) {
	if s == nil || key == nil {
		return
	}
	snapshot := *key
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("api key push panicked", "api_key_id", snapshot.ID, "panic", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), apiKeyPushTimeout+5*time.Second)
		defer cancel()
		s.push(ctx, &snapshot)
	}()
}

type apiKeyPushPayload struct {
	UserID string `json:"user_id"`
	Group  string `json:"group"`
	APIKey string `json:"api_key"`
}

type apiKeyPushResult struct {
	endpoint   string
	group      string
	statusCode int
	written    *bool
	errMsg     string
	skipped    string
}

func (s *APIKeyPushService) push(ctx context.Context, key *APIKey) {
	baseURL, secret, err := s.settings.GetQuickAccessPushConfig(ctx)
	if err != nil {
		slog.Warn("api key push: read settings failed", "api_key_id", key.ID, "err", err)
		return
	}
	if baseURL == "" || secret == "" {
		return
	}

	started := time.Now()
	result := apiKeyPushResult{}
	defer func() { s.record(ctx, key, result, time.Since(started)) }()

	endpoint, err := apiKeyPushEndpoint(baseURL)
	if err != nil {
		result.skipped = "invalid_quick_access_url"
		return
	}
	result.endpoint = endpoint

	if key.GroupID == nil {
		result.skipped = "no_group"
		return
	}
	group, err := s.groupRepo.GetByID(ctx, *key.GroupID)
	if err != nil {
		result.errMsg = "load group: " + err.Error()
		return
	}
	result.group = group.Name

	body, err := json.Marshal(apiKeyPushPayload{
		UserID: strconv.FormatInt(key.UserID, 10),
		Group:  group.Name,
		APIKey: key.Key,
	})
	if err != nil {
		result.errMsg = "encode payload: " + err.Error()
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		result.errMsg = "build request: " + err.Error()
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Sub2api-Token", secret)

	resp, err := s.client.Do(req)
	if err != nil {
		result.errMsg = "request failed: " + err.Error()
		return
	}
	defer func() { _ = resp.Body.Close() }()
	result.statusCode = resp.StatusCode
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, apiKeyPushMaxRespBody))

	var parsed struct {
		OK      bool   `json:"ok"`
		Written *bool  `json:"written"`
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(respBody, &parsed)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		result.written = parsed.Written
		return
	}
	switch {
	case parsed.Error != "":
		result.errMsg = parsed.Error
	case parsed.Message != "":
		result.errMsg = parsed.Message
	default:
		result.errMsg = string(respBody)
	}
}

func apiKeyPushEndpoint(baseURL string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("invalid quick access url")
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: apiKeyPushPath}).String(), nil
}

func (s *APIKeyPushService) record(ctx context.Context, key *APIKey, result apiKeyPushResult, latency time.Duration) {
	status := "success"
	statusCode := result.statusCode
	switch {
	case result.skipped != "":
		status = "skipped"
		statusCode = http.StatusUnprocessableEntity
	case result.errMsg != "" || statusCode < 200 || statusCode >= 300:
		status = "failed"
		if statusCode == 0 {
			statusCode = http.StatusBadGateway
		}
	}
	if status != "success" {
		slog.Warn("api key push not delivered", "api_key_id", key.ID, "user_id", key.UserID, "status", status,
			"status_code", result.statusCode, "reason", result.skipped, "err", result.errMsg)
	}
	if s.audit == nil {
		return
	}

	extra := map[string]any{
		"result":      status,
		"user_id":     key.UserID,
		"api_key_id":  key.ID,
		"group_id":    key.GroupID,
		"group":       result.group,
		"endpoint":    result.endpoint,
		"status_code": result.statusCode,
	}
	if result.written != nil {
		extra["written"] = *result.written
	}
	if result.skipped != "" {
		extra["reason"] = result.skipped
	}
	if result.errMsg != "" {
		msg := result.errMsg
		if len(msg) > 512 {
			msg = msg[:512] + "...<truncated>"
		}
		extra["error"] = msg
	}

	userID := key.UserID
	entry := &AuditLog{
		ActorUserID:      &userID,
		ActorRole:        "system",
		AuthMethod:       "system",
		CredentialMasked: MaskAuditCredential(key.Key),
		Action:           AuditActionAPIKeyPush,
		Method:           http.MethodPost,
		Path:             apiKeyPushPath,
		StatusCode:       statusCode,
		LatencyMs:        latency.Milliseconds(),
		Extra:            extra,
	}
	entry.ActorEmail = "system"
	if s.userRepo != nil {
		if user, err := s.userRepo.GetByID(ctx, key.UserID); err == nil && user != nil {
			entry.ActorEmail = user.Email
		}
	}
	s.audit.Record(entry)
}
