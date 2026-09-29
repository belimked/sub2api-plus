//go:build integration

package repository

// 投影漏列回归：GetByKeyForAuth 的用户显式投影必须带出
// federation_usage_watermark_seq。联邦准入检查按
// balance - Σ(usage_logs.id > 水位) 计算可用余额，漏选该列会让水位恒为 0，
// 把全部历史用量当作未同步用量，真实流量上的用户会被持续误拒。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/LuckyKuang/sub2api-plus/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGetByKeyForAuthCarriesFederationWatermarkProjection(t *testing.T) {
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	user := mustCreateUser(t, integrationEntClient, &service.User{
		Email: fmt.Sprintf("federation-proj-%d@example.com", suffix), Concurrency: 5,
	})
	_, err := integrationDB.ExecContext(ctx, "UPDATE users SET federation_usage_watermark_seq = 42 WHERE id = $1", user.ID)
	require.NoError(t, err)

	keyValue := fmt.Sprintf("sk-federation-proj-%d", suffix)
	apiKeyRepo := NewAPIKeyRepository(integrationEntClient, integrationDB)
	key := &service.APIKey{UserID: user.ID, Key: keyValue, Name: "federation-proj", Status: service.StatusActive}
	require.NoError(t, apiKeyRepo.Create(ctx, key))
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, "DELETE FROM auth_cache_invalidation_outbox WHERE cache_key = encode(sha256(convert_to($1, 'UTF8')), 'hex')", keyValue)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, "DELETE FROM api_keys WHERE id = $1", key.ID)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, "DELETE FROM users WHERE id = $1", user.ID)
		require.NoError(t, err)
	})

	got, err := apiKeyRepo.GetByKeyForAuth(ctx, keyValue)
	require.NoError(t, err)
	require.NotNil(t, got.User)
	require.Equal(t, int64(42), got.User.FederationUsageWatermarkSeq, "federation_usage_watermark_seq 必须进入认证投影")
}
