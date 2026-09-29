//go:build unit || !integration

package service

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	dbent "github.com/LuckyKuang/sub2api-plus/ent"
	"github.com/LuckyKuang/sub2api-plus/ent/enttest"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func newFederationAdmissionTestClient(t *testing.T) *dbent.Client {
	t.Helper()

	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", t.Name()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(10)

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	return client
}

func insertFederationAdmissionUsageFixtures(t *testing.T, ctx context.Context, client *dbent.Client, userID int64) (accountID, apiKeyID int64) {
	t.Helper()
	a, err := client.Account.Create().
		SetName(fmt.Sprintf("federation-admission-account-%d", userID)).
		SetPlatform("openai").
		SetType("apikey").
		SetStatus("active").
		SetCredentials(map[string]any{"api_key": "sk-test"}).
		Save(ctx)
	require.NoError(t, err)

	k, err := client.APIKey.Create().
		SetUserID(userID).
		SetKey(fmt.Sprintf("sk-federation-admission-%d-%d", userID, time.Now().UnixNano())).
		SetName("federation-admission-test-key").
		Save(ctx)
	require.NoError(t, err)

	return a.ID, k.ID
}

func insertFederationAdmissionUsageLog(t *testing.T, ctx context.Context, client *dbent.Client, userID, accountID, apiKeyID int64, cost float64) *dbent.UsageLog {
	t.Helper()
	row, err := client.UsageLog.Create().
		SetUserID(userID).
		SetAPIKeyID(apiKeyID).
		SetAccountID(accountID).
		SetRequestID(fmt.Sprintf("req-%d-%d", userID, time.Now().UnixNano())).
		SetModel("test-model").
		SetActualCost(cost).
		Save(ctx)
	require.NoError(t, err)
	return row
}

func TestFederationAvailableBalance_SubtractsUnackedUsageOnly(t *testing.T) {
	ctx := context.Background()
	client := newFederationAdmissionTestClient(t)
	u, err := client.User.Create().
		SetEmail("federation-admission@example.com").
		SetPasswordHash("test-password-hash").
		Save(ctx)
	require.NoError(t, err)
	accountID, apiKeyID := insertFederationAdmissionUsageFixtures(t, ctx, client, u.ID)

	acked := insertFederationAdmissionUsageLog(t, ctx, client, u.ID, accountID, apiKeyID, 1.0)
	unacked1 := insertFederationAdmissionUsageLog(t, ctx, client, u.ID, accountID, apiKeyID, 2.5)
	unacked2 := insertFederationAdmissionUsageLog(t, ctx, client, u.ID, accountID, apiKeyID, 3.5)
	_ = acked

	available, err := FederationAvailableBalance(ctx, client, u.ID, 10.0, acked.ID)
	require.NoError(t, err)
	require.InDelta(t, 4.0, available, 0.0001, "10.0 balance minus the two unacked rows (2.5+3.5), the acked row excluded")

	_ = unacked1
	_ = unacked2
}

func TestFederationAvailableBalance_NoUsageReturnsBalanceUnchanged(t *testing.T) {
	ctx := context.Background()
	client := newFederationAdmissionTestClient(t)
	u, err := client.User.Create().
		SetEmail("federation-admission-empty@example.com").
		SetPasswordHash("test-password-hash").
		Save(ctx)
	require.NoError(t, err)

	available, err := FederationAvailableBalance(ctx, client, u.ID, 5.0, 0)
	require.NoError(t, err)
	require.InDelta(t, 5.0, available, 0.0001)
}

func TestFederationAvailableBalance_CanGoNegative(t *testing.T) {
	ctx := context.Background()
	client := newFederationAdmissionTestClient(t)
	u, err := client.User.Create().
		SetEmail("federation-admission-negative@example.com").
		SetPasswordHash("test-password-hash").
		Save(ctx)
	require.NoError(t, err)
	accountID, apiKeyID := insertFederationAdmissionUsageFixtures(t, ctx, client, u.ID)
	insertFederationAdmissionUsageLog(t, ctx, client, u.ID, accountID, apiKeyID, 100.0)

	available, err := FederationAvailableBalance(ctx, client, u.ID, 5.0, 0)
	require.NoError(t, err)
	require.Less(t, available, 0.0, "unacked usage exceeding balance must be allowed to go negative -- the caller decides the admission threshold, this just computes the number")
}
