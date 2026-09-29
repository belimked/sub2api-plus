// Package federation holds the pieces shared by the federation sidecars
// (backend/cmd/federation-pusher, backend/cmd/federation-usage-tailer):
// a small HTTP client for a remote sub2api-plus deployment's admin API,
// with login, Idempotency-Key support, and retryable-vs-terminal error
// classification. See sub2api-federation-design.md and
// openspec/changes/federation-outbox-sync/ for the architecture this
// implements.
package federation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// AdminAPIClient talks to one remote sub2api-plus deployment's admin HTTP
// API. It is not safe for concurrent callers to share (the token refresh
// path is not synchronized against concurrent requests), but each sidecar's
// main loop only ever has one delivery in flight at a time.
type AdminAPIClient struct {
	baseURL  string
	email    string
	password string
	http     *http.Client

	mu    sync.Mutex
	token string
}

func NewAdminAPIClient(baseURL, email, password string, timeout time.Duration) *AdminAPIClient {
	return &AdminAPIClient{
		baseURL:  strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		email:    email,
		password: password,
		http:     &http.Client{Timeout: timeout},
	}
}

type APIEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// DeliverableError distinguishes failures a poller should retry (network
// blips, 429, 5xx) from terminal ones (other 4xx) that need a human to look
// at the record instead of retrying forever.
type DeliverableError struct {
	Retryable bool
	Err       error
}

func (e *DeliverableError) Error() string { return e.Err.Error() }
func (e *DeliverableError) Unwrap() error { return e.Err }

func RetryableErr(err error) error { return &DeliverableError{Retryable: true, Err: err} }
func TerminalErr(err error) error  { return &DeliverableError{Retryable: false, Err: err} }

func IsRetryable(err error) bool {
	var de *DeliverableError
	if errors.As(err, &de) {
		return de.Retryable
	}
	return true // unclassified errors default to retry rather than silently dropping the event
}

func (c *AdminAPIClient) ensureToken(ctx context.Context) error {
	c.mu.Lock()
	hasToken := c.token != ""
	c.mu.Unlock()
	if hasToken {
		return nil
	}
	return c.Login(ctx)
}

func (c *AdminAPIClient) Login(ctx context.Context) error {
	body, _ := json.Marshal(map[string]string{
		"email":    c.email,
		"password": c.password,
	})
	env, status, err := c.rawDo(ctx, http.MethodPost, "/api/v1/auth/login", body, "", "")
	if err != nil {
		return err
	}
	if _, classifyErr := classifyStatus(env, status); classifyErr != nil {
		return classifyErr
	}
	var data struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return TerminalErr(fmt.Errorf("decode login response: %w", err))
	}
	if data.AccessToken == "" {
		return TerminalErr(fmt.Errorf("login response had no access_token"))
	}
	c.mu.Lock()
	c.token = data.AccessToken
	c.mu.Unlock()
	return nil
}

// AuthedDo makes an authenticated request, logging in first if needed and
// transparently re-logging in once on a 401 (expired/revoked token).
func (c *AdminAPIClient) AuthedDo(ctx context.Context, method, path string, body []byte, idempotencyKey string) (*APIEnvelope, error) {
	if err := c.ensureToken(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	token := c.token
	c.mu.Unlock()

	env, status, err := c.rawDo(ctx, method, path, body, token, idempotencyKey)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		if loginErr := c.Login(ctx); loginErr != nil {
			return nil, loginErr
		}
		c.mu.Lock()
		token = c.token
		c.mu.Unlock()
		env, status, err = c.rawDo(ctx, method, path, body, token, idempotencyKey)
		if err != nil {
			return nil, err
		}
	}
	return classifyStatus(env, status)
}

type RemoteUser struct {
	ID      int64   `json:"id"`
	Email   string  `json:"email"`
	Status  string  `json:"status"`
	Role    string  `json:"role"`
	Balance float64 `json:"balance"`
}

// FindUserByEmail looks up a user by exact email match. The admin users
// list endpoint does a fuzzy `search` match (email or username substring),
// so results are filtered client-side for an exact match to avoid acting
// on the wrong account.
func (c *AdminAPIClient) FindUserByEmail(ctx context.Context, email string) (*RemoteUser, error) {
	path := fmt.Sprintf("/api/v1/admin/users?search=%s&page_size=20", url.QueryEscape(email))
	env, err := c.AuthedDo(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return nil, err
	}
	var data struct {
		Items []RemoteUser `json:"items"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return nil, TerminalErr(fmt.Errorf("decode user list response: %w", err))
	}
	for i := range data.Items {
		if strings.EqualFold(data.Items[i].Email, email) {
			return &data.Items[i], nil
		}
	}
	return nil, nil
}

func (c *AdminAPIClient) rawDo(ctx context.Context, method, path string, body []byte, token, idempotencyKey string) (*APIEnvelope, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, TerminalErr(fmt.Errorf("build request: %w", err))
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, RetryableErr(fmt.Errorf("%s %s: %w", method, path, err))
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, RetryableErr(fmt.Errorf("read response body: %w", err))
	}

	var env APIEnvelope
	if len(raw) > 0 {
		if jsonErr := json.Unmarshal(raw, &env); jsonErr != nil {
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil, resp.StatusCode, RetryableErr(fmt.Errorf("decode response (status %d): %w", resp.StatusCode, jsonErr))
			}
			// A non-JSON error page (e.g. a router's plain-text "404 page not
			// found" from a deployment missing the endpoint) is classified by
			// status like any other error, so a 4xx stays terminal instead of
			// retrying forever.
			env = APIEnvelope{Message: Truncate(string(raw), 200)}
		}
	}
	return &env, resp.StatusCode, nil
}

func classifyStatus(env *APIEnvelope, status int) (*APIEnvelope, error) {
	switch {
	case status >= 200 && status < 300:
		return env, nil
	case status == http.StatusTooManyRequests || status >= 500:
		return nil, RetryableErr(fmt.Errorf("admin API returned %d: %s", status, env.Message))
	default:
		return nil, TerminalErr(fmt.Errorf("admin API returned %d: %s", status, env.Message))
	}
}
