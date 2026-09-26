//go:build unit || !integration

package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	dbent "github.com/LuckyKuang/sub2api-plus/ent"
	"github.com/LuckyKuang/sub2api-plus/ent/enttest"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

// BumpFederationUsageWatermark talks to dbent.Client directly (see its doc
// comment for why), so unlike this file's neighbors it needs a real ent
// client rather than the package's usual stubbed UserRepository.
func newFederationWatermarkTestClient(t *testing.T) *dbent.Client {
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

func TestBumpFederationUsageWatermark_AdvancesForward(t *testing.T) {
	ctx := context.Background()
	client := newFederationWatermarkTestClient(t)
	u, err := client.User.Create().
		SetEmail("watermark@example.com").
		SetPasswordHash("test-password-hash").
		Save(ctx)
	require.NoError(t, err)

	s := &adminServiceImpl{entClient: client}
	require.NoError(t, s.BumpFederationUsageWatermark(ctx, u.ID, 100))

	got, err := client.User.Get(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, int64(100), got.FederationUsageWatermarkSeq)
}

func TestBumpFederationUsageWatermark_DoesNotRegressOnLowerSeq(t *testing.T) {
	ctx := context.Background()
	client := newFederationWatermarkTestClient(t)
	u, err := client.User.Create().
		SetEmail("watermark-lower@example.com").
		SetPasswordHash("test-password-hash").
		SetFederationUsageWatermarkSeq(100).
		Save(ctx)
	require.NoError(t, err)

	s := &adminServiceImpl{entClient: client}
	require.NoError(t, s.BumpFederationUsageWatermark(ctx, u.ID, 50))

	got, err := client.User.Get(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, int64(100), got.FederationUsageWatermarkSeq, "a lower/out-of-order seq must not move the watermark backwards")
}

func TestBumpFederationUsageWatermark_UnknownUserReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	client := newFederationWatermarkTestClient(t)

	s := &adminServiceImpl{entClient: client}
	err := s.BumpFederationUsageWatermark(ctx, 999999, 1)
	require.ErrorIs(t, err, ErrUserNotFound)
}

type federationWatermarkInvalidatorSpy struct {
	userIDs []int64
}

func (s *federationWatermarkInvalidatorSpy) InvalidateAuthCacheByKey(context.Context, string) {}
func (s *federationWatermarkInvalidatorSpy) InvalidateAuthCacheByUserID(_ context.Context, userID int64) {
	s.userIDs = append(s.userIDs, userID)
}
func (s *federationWatermarkInvalidatorSpy) InvalidateAuthCacheByGroupID(context.Context, int64) {}

// The API key auth cache snapshots the watermark, so an advance must drop the
// user's cached snapshots; a no-op (lower seq) has nothing to invalidate.
func TestBumpFederationUsageWatermark_InvalidatesAuthCacheOnAdvance(t *testing.T) {
	ctx := context.Background()
	client := newFederationWatermarkTestClient(t)
	u, err := client.User.Create().
		SetEmail("watermark-cache@example.com").
		SetPasswordHash("test-password-hash").
		Save(ctx)
	require.NoError(t, err)

	spy := &federationWatermarkInvalidatorSpy{}
	s := &adminServiceImpl{entClient: client, authCacheInvalidator: spy}
	require.NoError(t, s.BumpFederationUsageWatermark(ctx, u.ID, 10))
	require.Equal(t, []int64{u.ID}, spy.userIDs)

	require.NoError(t, s.BumpFederationUsageWatermark(ctx, u.ID, 5))
	require.Equal(t, []int64{u.ID}, spy.userIDs, "a non-advancing bump must not invalidate again")
}

// Snapshot build → L2 JSON round trip → restore must keep the watermark;
// losing it made every cache hit compute available balance from watermark 0.
func TestAPIKeyAuthSnapshotKeepsFederationWatermark(t *testing.T) {
	svc := &APIKeyService{}
	apiKey := &APIKey{
		ID:     7,
		UserID: 9,
		Key:    "sk-federation-watermark",
		Status: StatusActive,
		User: &User{
			ID:                          9,
			Status:                      StatusActive,
			Balance:                     7,
			FederationUsageWatermarkSeq: 42,
		},
	}

	snapshot := svc.snapshotFromAPIKey(context.Background(), apiKey)
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	var decoded APIKeyAuthSnapshot
	require.NoError(t, json.Unmarshal(raw, &decoded))

	restored := svc.snapshotToAPIKey(apiKey.Key, &decoded)
	require.Equal(t, int64(42), restored.User.FederationUsageWatermarkSeq)
}
