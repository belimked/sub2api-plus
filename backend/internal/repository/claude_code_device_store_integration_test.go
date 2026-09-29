//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LuckyKuang/sub2api-plus/internal/service"
	"github.com/stretchr/testify/require"
)

func createClaudeCodeDeviceTestAccount(t *testing.T) *service.Account {
	t.Helper()
	account := mustCreateAccount(t, testEntClient(t), &service.Account{
		Name: fmt.Sprintf("claude-code-device-%d", time.Now().UnixNano()),
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM claude_code_account_identities WHERE account_id = $1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = $1", account.ID)
	})
	return account
}

func countClaudeCodeDeviceRows(t *testing.T, accountID int64) int {
	t.Helper()
	var rows int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM claude_code_account_identities WHERE account_id = $1", accountID).Scan(&rows))
	return rows
}

func TestClaudeCodeDeviceStoreKeepsFirstDeviceUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	account := createClaudeCodeDeviceTestAccount(t)
	store := NewClaudeCodeDeviceStore(integrationDB)

	const replicas = 8
	results := make([]string, replicas)
	errs := make([]error, replicas)
	var wg sync.WaitGroup
	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			candidate := strings.Repeat(fmt.Sprintf("%x", i), 64)
			results[i], errs[i] = store.GetOrCreateClaudeCodeDeviceID(ctx, account.ID, candidate)
		}(i)
	}
	wg.Wait()
	for i := range results {
		require.NoError(t, errs[i])
		require.Equal(t, results[0], results[i])
	}

	again, err := store.GetOrCreateClaudeCodeDeviceID(ctx, account.ID, strings.Repeat("f", 64))
	require.NoError(t, err)
	require.Equal(t, results[0], again, "a stored device ID must never be replaced")
	require.Equal(t, 1, countClaudeCodeDeviceRows(t, account.ID))
}

func TestClaudeCodeDeviceStoreRejectsMalformedDeviceAndCascadesOnAccountDelete(t *testing.T) {
	ctx := context.Background()
	account := createClaudeCodeDeviceTestAccount(t)
	store := NewClaudeCodeDeviceStore(integrationDB)

	_, err := store.GetOrCreateClaudeCodeDeviceID(ctx, account.ID, "not-a-device")
	require.Error(t, err)
	require.Zero(t, countClaudeCodeDeviceRows(t, account.ID))

	deviceID := strings.Repeat("a", 64)
	stored, err := store.GetOrCreateClaudeCodeDeviceID(ctx, account.ID, deviceID)
	require.NoError(t, err)
	require.Equal(t, deviceID, stored)

	_, err = integrationDB.ExecContext(ctx, "DELETE FROM accounts WHERE id = $1", account.ID)
	require.NoError(t, err)
	require.Zero(t, countClaudeCodeDeviceRows(t, account.ID))
}
