//go:build unit

package middleware

import (
	"context"
	"errors"
	"testing"

	"github.com/LuckyKuang/sub2api-plus/internal/config"
	"github.com/LuckyKuang/sub2api-plus/internal/service"

	"github.com/stretchr/testify/require"
)

func withFederationHook(t *testing.T, fn func(ctx context.Context, userID int64, balance float64, watermarkSeq int64) (float64, error)) {
	t.Helper()
	prev := FederationAvailableBalanceFunc
	FederationAvailableBalanceFunc = fn
	t.Cleanup(func() { FederationAvailableBalanceFunc = prev })
}

func TestApiKeyBalanceBelowAuthThreshold_DisabledIgnoresHook(t *testing.T) {
	withFederationHook(t, func(ctx context.Context, userID int64, balance float64, watermarkSeq int64) (float64, error) {
		t.Fatal("the hook must never be called when federation.admission_check_enabled is false")
		return 0, nil
	})

	cfg := &config.Config{}
	u := &service.User{ID: 1, Balance: 5}
	require.False(t, apiKeyBalanceBelowAuthThreshold(context.Background(), u, cfg), "positive balance, feature off: plain balance check applies")
}

func TestApiKeyBalanceBelowAuthThreshold_EnabledUsesHookResult(t *testing.T) {
	withFederationHook(t, func(ctx context.Context, userID int64, balance float64, watermarkSeq int64) (float64, error) {
		require.Equal(t, int64(1), userID)
		require.Equal(t, 5.0, balance)
		require.Equal(t, int64(42), watermarkSeq)
		return -1, nil // hook says unacked local usage has eaten into the balance
	})

	cfg := &config.Config{}
	cfg.Federation.AdmissionCheckEnabled = true
	u := &service.User{ID: 1, Balance: 5, FederationUsageWatermarkSeq: 42}
	require.True(t, apiKeyBalanceBelowAuthThreshold(context.Background(), u, cfg), "hook-computed available balance is negative, so this must reject")
}

func TestApiKeyBalanceBelowAuthThreshold_HookErrorFailsOpenToPlainBalance(t *testing.T) {
	withFederationHook(t, func(ctx context.Context, userID int64, balance float64, watermarkSeq int64) (float64, error) {
		return 0, errors.New("db hiccup")
	})

	cfg := &config.Config{}
	cfg.Federation.AdmissionCheckEnabled = true
	u := &service.User{ID: 1, Balance: 5, FederationUsageWatermarkSeq: 42}
	require.False(t, apiKeyBalanceBelowAuthThreshold(context.Background(), u, cfg),
		"a federation query error must not block a user who has a positive plain balance")
}

func TestApiKeyBalanceBelowAuthThreshold_EnabledButNoHookFallsBackToPlainBalance(t *testing.T) {
	withFederationHook(t, nil)

	cfg := &config.Config{}
	cfg.Federation.AdmissionCheckEnabled = true
	u := &service.User{ID: 1, Balance: 0}
	require.True(t, apiKeyBalanceBelowAuthThreshold(context.Background(), u, cfg), "no hook wired: falls back to the plain balance <= 0 check")
}

func TestApiKeyBalanceBelowAuthThreshold_SkipsUsersWithoutFederation(t *testing.T) {
	withFederationHook(t, func(ctx context.Context, userID int64, balance float64, watermarkSeq int64) (float64, error) {
		t.Fatal("admins and users that never received a mainland watermark must use their local balance")
		return 0, nil
	})

	cfg := &config.Config{}
	cfg.Federation.AdmissionCheckEnabled = true
	for name, u := range map[string]*service.User{
		"local-only user":            {ID: 1, Balance: 5, Role: service.RoleUser},
		"admin without watermark":    {ID: 2, Balance: 5, Role: service.RoleAdmin},
		"admin with stale watermark": {ID: 3, Balance: 5, Role: service.RoleAdmin, FederationUsageWatermarkSeq: 42},
	} {
		require.False(t, apiKeyBalanceBelowAuthThreshold(context.Background(), u, cfg), name)
	}
	require.True(t, apiKeyBalanceBelowAuthThreshold(context.Background(), &service.User{ID: 4, Balance: 0, Role: service.RoleAdmin}, cfg),
		"skipping the formula still rejects an exhausted local balance")
}
