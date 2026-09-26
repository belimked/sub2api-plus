//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/LuckyKuang/sub2api-plus/ent/schema/mixins"
	"github.com/LuckyKuang/sub2api-plus/internal/domain"
	"github.com/LuckyKuang/sub2api-plus/internal/service"
)

func usageBillingFederationSnapshots(t *testing.T, ctx context.Context, userID int64) []federationBalancePayload {
	t.Helper()
	rows, err := integrationDB.QueryContext(ctx, `
		SELECT payload FROM federation_outbox_events
		WHERE aggregate_id = $1 AND event_type = 'balance.snapshot'
		ORDER BY id`, fmt.Sprintf("%d", userID))
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var out []federationBalancePayload
	for rows.Next() {
		var raw string
		require.NoError(t, rows.Scan(&raw))
		var p federationBalancePayload
		require.NoError(t, json.Unmarshal([]byte(raw), &p))
		out = append(out, p)
	}
	require.NoError(t, rows.Err())
	return out
}

func newUsageBillingFederationFixture(t *testing.T, role string) (*usageBillingRepository, *service.User, *service.APIKey) {
	t.Helper()
	client := testEntClient(t)
	repo, ok := NewUsageBillingRepository(client, integrationDB).(*usageBillingRepository)
	require.True(t, ok)
	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-federation-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         role,
		Balance:      100,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, Key: "sk-usage-billing-fed-" + uuid.NewString(), Name: "billing-fed",
	})
	return repo, user, apiKey
}

func TestUsageBillingRepositoryApply_EmitsFederationBalanceSnapshot(t *testing.T) {
	ctx := context.Background()
	// Fixture first: with the gate on, User.Create emits its own snapshot.
	repo, user, apiKey := newUsageBillingFederationFixture(t, domain.RoleUser)
	mixins.SetFederationOutboxEnabled(true)
	t.Cleanup(func() { mixins.SetFederationOutboxEnabled(false) })

	cmd := &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID, BalanceCost: 1.25,
	}
	result, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	// A duplicate request is not applied and must not emit again.
	result, err = repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, result.Applied)

	events := usageBillingFederationSnapshots(t, ctx, user.ID)
	require.Len(t, events, 1)
	require.Equal(t, user.ID, events[0].ID)
	require.Equal(t, user.Email, events[0].Email)
	require.InDelta(t, 98.75, events[0].Balance, 0.000001)
	require.Equal(t, int64(0), events[0].AsOfUsageSeq)
}

func TestUsageBillingRepositoryBatchImage_EmitsFederationBalanceSnapshot(t *testing.T) {
	ctx := context.Background()
	// Fixture first: with the gate on, User.Create emits its own snapshot.
	repo, user, apiKey := newUsageBillingFederationFixture(t, domain.RoleUser)
	mixins.SetFederationOutboxEnabled(true)
	t.Cleanup(func() { mixins.SetFederationOutboxEnabled(false) })

	batchID := "imgbatch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := repo.ReserveBatchImageBalance(ctx, &service.BatchImageBalanceHoldCommand{
		RequestID: service.BatchImageHoldRequestID(batchID), APIKeyID: apiKey.ID,
		UserID: user.ID, BatchID: batchID, HoldAmount: 2,
	})
	require.NoError(t, err)
	_, err = repo.CaptureBatchImageBalance(ctx, &service.BatchImageBalanceHoldCommand{
		RequestID: service.BatchImageCaptureRequestID(batchID), APIKeyID: apiKey.ID,
		UserID: user.ID, BatchID: batchID, HoldAmount: 2, ActualAmount: 1.25,
	})
	require.NoError(t, err)

	events := usageBillingFederationSnapshots(t, ctx, user.ID)
	require.Len(t, events, 2)
	require.InDelta(t, 98, events[0].Balance, 0.000001)
	require.InDelta(t, 98.75, events[1].Balance, 0.000001)
}

func TestUsageBillingRepositoryApply_FederationSnapshotSkippedWhenDisabledOrAdmin(t *testing.T) {
	ctx := context.Background()

	repo, user, apiKey := newUsageBillingFederationFixture(t, domain.RoleUser)
	_, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID, BalanceCost: 1,
	})
	require.NoError(t, err)
	require.Empty(t, usageBillingFederationSnapshots(t, ctx, user.ID))

	mixins.SetFederationOutboxEnabled(true)
	t.Cleanup(func() { mixins.SetFederationOutboxEnabled(false) })
	repo, admin, adminKey := newUsageBillingFederationFixture(t, domain.RoleAdmin)
	result, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: adminKey.ID, UserID: admin.ID, BalanceCost: 1,
	})
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.Empty(t, usageBillingFederationSnapshots(t, ctx, admin.ID))
}
