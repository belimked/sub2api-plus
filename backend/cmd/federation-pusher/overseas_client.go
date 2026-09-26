package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/LuckyKuang/sub2api-plus/internal/federation"
)

// overseasClient wraps the shared federation.AdminAPIClient with the
// user-upsert business logic this pusher needs.
type overseasClient struct {
	api    *federation.AdminAPIClient
	logger *slog.Logger
}

func newOverseasClient(cfg pusherConfig, logger *slog.Logger) *overseasClient {
	return &overseasClient{
		api:    federation.NewAdminAPIClient(cfg.OverseasBaseURL, cfg.AdminEmail, cfg.AdminPassword, cfg.HTTPTimeout),
		logger: logger,
	}
}

func isRetryable(err error) bool { return federation.IsRetryable(err) }

// adminRole mirrors domain.RoleAdmin as the overseas admin API reports it.
const adminRole = "admin"

func (c *overseasClient) createUser(ctx context.Context, idempotencyKey, email, status string) error {
	password, err := randomPassword()
	if err != nil {
		return federation.TerminalErr(fmt.Errorf("generate random password: %w", err))
	}
	body, _ := json.Marshal(map[string]any{
		"email":    email,
		"password": password,
	})
	if _, err := c.api.AuthedDo(ctx, http.MethodPost, "/api/v1/admin/users", body, idempotencyKey); err != nil {
		return err
	}
	// The create endpoint doesn't take a status; if the source status isn't
	// the default "active", follow up with an update so the mirror matches.
	if status != "" && status != "active" {
		u, findErr := c.api.FindUserByEmail(ctx, email)
		if findErr != nil {
			return findErr
		}
		if u == nil {
			return federation.RetryableErr(fmt.Errorf("created user %s but could not find it back to set status", email))
		}
		return c.updateStatus(ctx, idempotencyKey+"-status", u.ID, status)
	}
	return nil
}

func (c *overseasClient) updateStatus(ctx context.Context, idempotencyKey string, id int64, status string) error {
	body, _ := json.Marshal(map[string]any{"status": status})
	_, err := c.api.AuthedDo(ctx, http.MethodPut, fmt.Sprintf("/api/v1/admin/users/%d", id), body, idempotencyKey)
	return err
}

// upsertUser creates the overseas mirror account if it doesn't exist yet, or
// updates its status if it does and the status differs.
// upsertUser mirrors a mainland user. When the event carries a bcrypt
// password_hash it is stored verbatim overseas (needs
// federation.accept_password_hash there), so the mainland password works on
// both sides. An overseas admin account is never touched: both deployments
// may share an admin email.
func (c *overseasClient) upsertUser(ctx context.Context, idempotencyKey, email, status, passwordHash string) error {
	existing, err := c.api.FindUserByEmail(ctx, email)
	if err != nil {
		return err
	}
	if existing == nil {
		if err := c.createUser(ctx, idempotencyKey, email, status); err != nil {
			return err
		}
		if passwordHash == "" {
			return nil
		}
		if existing, err = c.api.FindUserByEmail(ctx, email); err != nil {
			return err
		}
		if existing == nil {
			return federation.RetryableErr(fmt.Errorf("created user %s but could not find it back to set its password", email))
		}
	} else {
		if existing.Role == adminRole {
			return federation.TerminalErr(fmt.Errorf("overseas user %s is an admin; admin accounts are never federated", email))
		}
		if existing.Status != status {
			if err := c.updateStatus(ctx, idempotencyKey, existing.ID, status); err != nil {
				return err
			}
		}
	}
	if passwordHash == "" {
		return nil
	}
	body, _ := json.Marshal(map[string]string{"password_hash": passwordHash})
	_, err = c.api.AuthedDo(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/admin/users/%d/federation-password-hash", existing.ID), body, idempotencyKey+"-password")
	return err
}

// applyBalanceSnapshot sets the overseas mirror's balance and, if this
// snapshot's watermark says anything meaningful (asOfUsageSeq > 0), bumps
// its federation_usage_watermark_seq too. The user must already exist
// overseas (created by a user.upsert event) -- if not, this is retryable so
// it waits for that event to land rather than failing terminally.
func (c *overseasClient) applyBalanceSnapshot(ctx context.Context, idempotencyKey, email string, balance float64, asOfUsageSeq int64) error {
	u, err := c.api.FindUserByEmail(ctx, email)
	if err != nil {
		return err
	}
	if u == nil {
		return federation.RetryableErr(fmt.Errorf("no overseas user for email %s yet; waiting for user.upsert to land first", email))
	}
	if u.Role == adminRole {
		return federation.TerminalErr(fmt.Errorf("overseas user %s is an admin; admin accounts are never federated", email))
	}

	if balance > 0 {
		body, _ := json.Marshal(map[string]any{
			"balance":   balance,
			"operation": "set",
		})
		if _, err := c.api.AuthedDo(ctx, http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/balance", u.ID), body, idempotencyKey); err != nil {
			return err
		}
	} else if err := c.drainBalance(ctx, idempotencyKey, u); err != nil {
		return err
	}

	if asOfUsageSeq <= 0 {
		return nil
	}
	watermarkBody, _ := json.Marshal(map[string]any{"usage_seq": asOfUsageSeq})
	_, err = c.api.AuthedDo(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/admin/users/%d/federation-usage-watermark", u.ID),
		watermarkBody, idempotencyKey+"-watermark")
	return err
}

// drainBalance mirrors a mainland balance <= 0. The overseas admin balance
// API only accepts amounts > 0 ("set 0" is a 400) and its subtract refuses
// to go negative, so the closest reachable state is exactly 0: subtract the
// user's current overseas balance. A negative mainland balance (overdraft)
// is clamped to 0, which the admission check treats the same (balance <= 0
// is rejected). If a concurrent local deduction lowered the balance between
// the read and the subtract, the subtract is refused; that is reported as
// retryable so the next attempt re-reads the balance. The idempotency key
// carries the amount because a retry with a different amount is a different
// request to the overseas idempotency layer.
func (c *overseasClient) drainBalance(ctx context.Context, idempotencyKey string, u *federation.RemoteUser) error {
	if u.Balance <= 0 {
		return nil
	}
	body, _ := json.Marshal(map[string]any{
		"balance":   u.Balance,
		"operation": "subtract",
	})
	key := fmt.Sprintf("%s-drain-%.8f", idempotencyKey, u.Balance)
	if _, err := c.api.AuthedDo(ctx, http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/balance", u.ID), body, key); err != nil {
		if isRetryable(err) {
			return err
		}
		return federation.RetryableErr(fmt.Errorf("drain overseas balance %.8f for %s: %w", u.Balance, u.Email, err))
	}
	return nil
}

func randomPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
