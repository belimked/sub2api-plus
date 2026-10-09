package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/LuckyKuang/sub2api-plus/internal/federation"
)

// mainlandClient wraps the shared federation.AdminAPIClient with the
// balance-subtract call this tailer needs, plus a small in-process cache
// for email -> mainland user id (usage_log only has the overseas-local
// user id; mainland ids are resolved by email, never assumed to match --
// see sub2api-federation-design.md §9's "id 不对齐" finding).
type mainlandClient struct {
	api *federation.AdminAPIClient

	mu    sync.Mutex
	cache map[string]federation.RemoteUser // email -> mainland user
}

func newMainlandClient(cfg tailerConfig) *mainlandClient {
	return &mainlandClient{
		api:   federation.NewAdminAPIClient(cfg.MainlandBaseURL, cfg.AdminEmail, cfg.AdminPassword, cfg.HTTPTimeout),
		cache: make(map[string]federation.RemoteUser),
	}
}

// resolveUser maps a local email to the mainland user. found is false
// only when mainland definitively has no such user; lookup failures are
// returned as errors. Misses are not cached, so a user created on mainland
// later is picked up without restarting the tailer.
func (c *mainlandClient) resolveUser(ctx context.Context, email string) (user federation.RemoteUser, found bool, err error) {
	c.mu.Lock()
	if u, ok := c.cache[email]; ok {
		c.mu.Unlock()
		return u, true, nil
	}
	c.mu.Unlock()

	u, err := c.api.FindUserByEmail(ctx, email)
	if err != nil {
		return federation.RemoteUser{}, false, err
	}
	if u == nil {
		return federation.RemoteUser{}, false, nil
	}

	c.mu.Lock()
	c.cache[email] = *u
	c.mu.Unlock()
	return *u, true, nil
}

func (c *mainlandClient) subtractBalance(ctx context.Context, idempotencyKey string, mainlandUserID int64, cost float64, notes string) error {
	body, _ := json.Marshal(map[string]any{
		"balance":   cost,
		"operation": "subtract",
		"notes":     notes,
	})
	_, err := c.api.AuthedDo(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/admin/users/%d/balance", mainlandUserID), body, idempotencyKey)
	return err
}

// bumpWatermark reports the usage_log id just delivered, so mainland can
// include it as as_of_usage_seq the next time it pushes a balance.snapshot
// back out. Deliberately not part of the same call/transaction as
// subtractBalance -- see openspec/changes/federation-balance-sync/design.md
// for why a lagging watermark is an acceptable failure mode.
func (c *mainlandClient) bumpWatermark(ctx context.Context, idempotencyKey string, mainlandUserID, usageSeq int64) error {
	body, _ := json.Marshal(map[string]any{"usage_seq": usageSeq})
	_, err := c.api.AuthedDo(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/admin/users/%d/federation-usage-watermark", mainlandUserID), body, idempotencyKey)
	return err
}
