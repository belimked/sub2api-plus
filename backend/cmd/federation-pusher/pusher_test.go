//go:build unit || !integration

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	dbent "github.com/LuckyKuang/sub2api-plus/ent"
	"github.com/LuckyKuang/sub2api-plus/ent/enttest"
	"github.com/LuckyKuang/sub2api-plus/ent/federationoutbox"
	"github.com/LuckyKuang/sub2api-plus/internal/federation"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func newPusherTestClient(t *testing.T) *dbent.Client {
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

func newTestPusher(t *testing.T, client *dbent.Client, overseasBaseURL string) *pusher {
	t.Helper()
	cfg := pusherConfig{
		OverseasBaseURL: overseasBaseURL,
		AdminEmail:      "admin@example.com",
		AdminPassword:   "test-password",
		BatchSize:       10,
		MaxAttempts:     3,
		HTTPTimeout:     5 * time.Second,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &pusher{
		db:       client,
		overseas: newOverseasClient(cfg, logger),
		cfg:      cfg,
		logger:   logger,
	}
}

func insertOutboxRow(t *testing.T, ctx context.Context, client *dbent.Client, email, status string) *dbent.FederationOutbox {
	t.Helper()
	payload, err := json.Marshal(outboxUserPayload{ID: 1, Email: email, Status: status})
	require.NoError(t, err)
	row, err := client.FederationOutbox.Create().
		SetAggregateType("user").
		SetAggregateID("1").
		SetEventType("user.upsert").
		SetPayload(string(payload)).
		Save(ctx)
	require.NoError(t, err)
	return row
}

func insertBalanceSnapshotRow(t *testing.T, ctx context.Context, client *dbent.Client, email string, balance float64, asOfUsageSeq int64) *dbent.FederationOutbox {
	t.Helper()
	payload, err := json.Marshal(outboxBalancePayload{ID: 1, Email: email, Balance: balance, AsOfUsageSeq: asOfUsageSeq})
	require.NoError(t, err)
	row, err := client.FederationOutbox.Create().
		SetAggregateType("user").
		SetAggregateID("1").
		SetEventType("balance.snapshot").
		SetPayload(string(payload)).
		Save(ctx)
	require.NoError(t, err)
	return row
}

func jsonOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "success", "data": data})
}

func TestPusher_DeliversNewUser(t *testing.T) {
	ctx := context.Background()
	client := newPusherTestClient(t)
	row := insertOutboxRow(t, ctx, client, "pusher-new@example.com", "active")

	var sawIdempotencyKey string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]string{"access_token": "test-token"})
	})
	mux.HandleFunc("/api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			jsonOK(w, map[string]any{"items": []federation.RemoteUser{}})
		case http.MethodPost:
			sawIdempotencyKey = r.Header.Get("Idempotency-Key")
			jsonOK(w, federation.RemoteUser{ID: 99, Email: "pusher-new@example.com", Status: "active"})
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	p := newTestPusher(t, client, server.URL)
	require.NoError(t, p.runOnce(ctx))

	got, err := client.FederationOutbox.Get(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, "delivered", got.Status)
	require.Equal(t, 1, got.Attempts)
	require.Nil(t, got.LastError)
	require.Equal(t, fmt.Sprintf("federation-outbox-%d", row.ID), sawIdempotencyKey)
}

func TestPusher_RetryableFailureThenSuccess(t *testing.T) {
	ctx := context.Background()
	client := newPusherTestClient(t)
	row := insertOutboxRow(t, ctx, client, "pusher-retry@example.com", "active")

	attempt := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]string{"access_token": "test-token"})
	})
	mux.HandleFunc("/api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			jsonOK(w, map[string]any{"items": []federation.RemoteUser{}})
		case http.MethodPost:
			attempt++
			if attempt == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": "boom"})
				return
			}
			jsonOK(w, federation.RemoteUser{ID: 99, Email: "pusher-retry@example.com", Status: "active"})
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	p := newTestPusher(t, client, server.URL)

	require.NoError(t, p.runOnce(ctx))
	afterFirst, err := client.FederationOutbox.Get(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", afterFirst.Status, "retryable failure must go back to pending, not failed")
	require.Equal(t, 1, afterFirst.Attempts)
	require.NotNil(t, afterFirst.LastError)
	require.NotNil(t, afterFirst.NextRetryAt)

	// Second run before next_retry_at elapses must not re-claim the row.
	require.NoError(t, p.runOnce(ctx))
	require.Equal(t, 1, attempt, "must not retry before next_retry_at")

	// Force the backoff to have elapsed and retry again.
	_, err = client.FederationOutbox.UpdateOneID(row.ID).
		SetNextRetryAt(time.Now().Add(-time.Second)).
		Save(ctx)
	require.NoError(t, err)

	require.NoError(t, p.runOnce(ctx))
	final, err := client.FederationOutbox.Get(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, "delivered", final.Status)
	require.Equal(t, 2, final.Attempts)
}

func TestPusher_TerminalFailureDoesNotLoop(t *testing.T) {
	ctx := context.Background()
	client := newPusherTestClient(t)
	row := insertOutboxRow(t, ctx, client, "pusher-terminal@example.com", "active")

	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]string{"access_token": "test-token"})
	})
	mux.HandleFunc("/api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			jsonOK(w, map[string]any{"items": []federation.RemoteUser{}})
		case http.MethodPost:
			calls++
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": "invalid email"})
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	p := newTestPusher(t, client, server.URL)

	require.NoError(t, p.runOnce(ctx))
	got, err := client.FederationOutbox.Get(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", got.Status)
	require.Equal(t, 1, got.Attempts)
	require.NotNil(t, got.LastError)
	require.Nil(t, got.NextRetryAt)

	// A failed row must never be picked up again.
	require.NoError(t, p.runOnce(ctx))
	require.Equal(t, 1, calls, "a terminal failure must not be retried")
}

func TestPusher_SkipsUpToDateExistingUser(t *testing.T) {
	ctx := context.Background()
	client := newPusherTestClient(t)
	row := insertOutboxRow(t, ctx, client, "pusher-existing@example.com", "active")

	createCalls := 0
	updateCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]string{"access_token": "test-token"})
	})
	mux.HandleFunc("/api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			jsonOK(w, map[string]any{"items": []federation.RemoteUser{{ID: 7, Email: "pusher-existing@example.com", Status: "active"}}})
		case http.MethodPost:
			createCalls++
		}
	})
	mux.HandleFunc("/api/v1/admin/users/7", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			updateCalls++
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	p := newTestPusher(t, client, server.URL)
	require.NoError(t, p.runOnce(ctx))

	got, err := client.FederationOutbox.Get(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, "delivered", got.Status)
	require.Equal(t, 0, createCalls, "existing user with matching status must not be recreated")
	require.Equal(t, 0, updateCalls, "existing user with matching status must not be updated")
}

func TestPusher_ClaimBatchIsBoundedByBatchSize(t *testing.T) {
	ctx := context.Background()
	client := newPusherTestClient(t)
	for i := 0; i < 5; i++ {
		insertOutboxRow(t, ctx, client, fmt.Sprintf("pusher-batch-%d@example.com", i), "active")
	}

	p := newTestPusher(t, client, "http://unused.invalid")
	p.cfg.BatchSize = 2

	rows, err := p.claimBatch(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	inFlight, err := client.FederationOutbox.Query().Where(federationoutbox.StatusEQ("in_flight")).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, inFlight)
}

func TestPusher_DeliversBalanceSnapshot(t *testing.T) {
	ctx := context.Background()
	client := newPusherTestClient(t)
	row := insertBalanceSnapshotRow(t, ctx, client, "pusher-balance@example.com", 42.5, 137)

	var sawBalanceBody, sawWatermarkBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]string{"access_token": "test-token"})
	})
	mux.HandleFunc("/api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]any{"items": []federation.RemoteUser{{ID: 7, Email: "pusher-balance@example.com", Status: "active"}}})
	})
	mux.HandleFunc("/api/v1/admin/users/7/balance", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sawBalanceBody)
		jsonOK(w, map[string]any{"message": "ok"})
	})
	mux.HandleFunc("/api/v1/admin/users/7/federation-usage-watermark", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sawWatermarkBody)
		jsonOK(w, map[string]any{"message": "ok"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	p := newTestPusher(t, client, server.URL)
	require.NoError(t, p.runOnce(ctx))

	got, err := client.FederationOutbox.Get(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, "delivered", got.Status)

	require.Equal(t, "set", sawBalanceBody["operation"])
	require.InDelta(t, 42.5, sawBalanceBody["balance"], 0.0001)
	require.InDelta(t, float64(137), sawWatermarkBody["usage_seq"], 0.0001)
}

func TestPusher_BalanceSnapshotSkipsWatermarkCallWhenSeqIsZero(t *testing.T) {
	ctx := context.Background()
	client := newPusherTestClient(t)
	row := insertBalanceSnapshotRow(t, ctx, client, "pusher-balance-zero@example.com", 10, 0)

	watermarkCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]string{"access_token": "test-token"})
	})
	mux.HandleFunc("/api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]any{"items": []federation.RemoteUser{{ID: 8, Email: "pusher-balance-zero@example.com", Status: "active"}}})
	})
	mux.HandleFunc("/api/v1/admin/users/8/balance", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]any{"message": "ok"})
	})
	mux.HandleFunc("/api/v1/admin/users/8/federation-usage-watermark", func(w http.ResponseWriter, r *http.Request) {
		watermarkCalls++
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	p := newTestPusher(t, client, server.URL)
	require.NoError(t, p.runOnce(ctx))

	got, err := client.FederationOutbox.Get(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, "delivered", got.Status)
	require.Equal(t, 0, watermarkCalls, "as_of_usage_seq=0 means nothing meaningful to record yet")
}

func TestPusher_BalanceSnapshotRetriesWhenOverseasUserMissing(t *testing.T) {
	ctx := context.Background()
	client := newPusherTestClient(t)
	row := insertBalanceSnapshotRow(t, ctx, client, "pusher-balance-missing@example.com", 10, 5)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]string{"access_token": "test-token"})
	})
	mux.HandleFunc("/api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]any{"items": []federation.RemoteUser{}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	p := newTestPusher(t, client, server.URL)
	require.NoError(t, p.runOnce(ctx))

	got, err := client.FederationOutbox.Get(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", got.Status, "must retry, waiting for the user.upsert event to land first")
	require.NotNil(t, got.NextRetryAt)
}

func TestPusher_PurgesOnlyExpiredDeliveredRows(t *testing.T) {
	ctx := context.Background()
	client := newPusherTestClient(t)
	p := newTestPusher(t, client, "http://unused.invalid")
	p.cfg.DeliveredRetention = 24 * time.Hour

	now := time.Now()
	old := now.Add(-48 * time.Hour)
	mk := func(email, status string, updatedAt time.Time) int64 {
		row := insertOutboxRow(t, ctx, client, email, "active")
		_, err := client.FederationOutbox.UpdateOneID(row.ID).
			SetStatus(status).
			SetUpdatedAt(updatedAt).
			Save(ctx)
		require.NoError(t, err)
		return row.ID
	}
	expired := mk("expired@example.com", "delivered", old)
	recent := mk("recent@example.com", "delivered", now)
	failed := mk("failed@example.com", "failed", old)
	pending := mk("pending@example.com", "pending", old)

	p.purgeDeliveredIfDue(ctx, now)

	_, err := client.FederationOutbox.Get(ctx, expired)
	require.True(t, dbent.IsNotFound(err), "delivered row past retention must be purged")
	for _, id := range []int64{recent, failed, pending} {
		_, err := client.FederationOutbox.Get(ctx, id)
		require.NoError(t, err, "row %d must survive the purge", id)
	}
}

func TestPusher_PurgeRespectsIntervalAndDisable(t *testing.T) {
	ctx := context.Background()
	client := newPusherTestClient(t)
	p := newTestPusher(t, client, "http://unused.invalid")
	p.cfg.DeliveredRetention = time.Hour

	now := time.Now()
	insertDelivered := func() int64 {
		row := insertOutboxRow(t, ctx, client, "interval@example.com", "active")
		_, err := client.FederationOutbox.UpdateOneID(row.ID).
			SetStatus("delivered").
			SetUpdatedAt(now.Add(-2 * time.Hour)).
			Save(ctx)
		require.NoError(t, err)
		return row.ID
	}

	p.purgeDeliveredIfDue(ctx, now)
	second := insertDelivered()
	p.purgeDeliveredIfDue(ctx, now.Add(time.Minute))
	_, err := client.FederationOutbox.Get(ctx, second)
	require.NoError(t, err, "a second purge within purgeInterval must not run")

	p.purgeDeliveredIfDue(ctx, now.Add(purgeInterval+time.Minute))
	_, err = client.FederationOutbox.Get(ctx, second)
	require.True(t, dbent.IsNotFound(err), "the purge must run again once purgeInterval has elapsed")

	p.cfg.DeliveredRetention = 0
	third := insertDelivered()
	p.purgeDeliveredIfDue(ctx, now.Add(3*purgeInterval))
	_, err = client.FederationOutbox.Get(ctx, third)
	require.NoError(t, err, "DeliveredRetention=0 disables the purge")
}

// The overseas balance API rejects "set" with an amount <= 0, so a zero or
// negative mainland snapshot must drain the overseas balance to exactly 0 via
// subtract (and do nothing when it is already 0).
func TestPusher_BalanceSnapshotAtOrBelowZeroDrainsOverseasBalance(t *testing.T) {
	for _, tc := range []struct {
		name            string
		snapshot        float64
		overseasBalance float64
		wantSubtract    float64 // 0 = no balance call expected
	}{
		{name: "zero drains positive", snapshot: 0, overseasBalance: 12.5, wantSubtract: 12.5},
		{name: "negative clamps to zero", snapshot: -994, overseasBalance: 3, wantSubtract: 3},
		{name: "already zero is a no-op", snapshot: 0, overseasBalance: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := newPusherTestClient(t)
			row := insertBalanceSnapshotRow(t, ctx, client, "pusher-drain@example.com", tc.snapshot, 0)

			var balanceCalls []map[string]any
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
				jsonOK(w, map[string]string{"access_token": "test-token"})
			})
			mux.HandleFunc("/api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
				jsonOK(w, map[string]any{"items": []federation.RemoteUser{{ID: 9, Email: "pusher-drain@example.com", Status: "active", Balance: tc.overseasBalance}}})
			})
			mux.HandleFunc("/api/v1/admin/users/9/balance", func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				balanceCalls = append(balanceCalls, body)
				jsonOK(w, map[string]any{"message": "ok"})
			})
			server := httptest.NewServer(mux)
			defer server.Close()

			p := newTestPusher(t, client, server.URL)
			require.NoError(t, p.runOnce(ctx))

			got, err := client.FederationOutbox.Get(ctx, row.ID)
			require.NoError(t, err)
			require.Equal(t, "delivered", got.Status)

			if tc.wantSubtract == 0 {
				require.Empty(t, balanceCalls)
				return
			}
			require.Len(t, balanceCalls, 1)
			require.Equal(t, "subtract", balanceCalls[0]["operation"])
			require.InDelta(t, tc.wantSubtract, balanceCalls[0]["balance"], 0.0001)
		})
	}
}

// A refused drain (e.g. a concurrent local deduction made the subtract go
// negative, a 400) must stay retryable so the next attempt re-reads the balance.
func TestPusher_BalanceDrainRefusalIsRetryable(t *testing.T) {
	ctx := context.Background()
	client := newPusherTestClient(t)
	row := insertBalanceSnapshotRow(t, ctx, client, "pusher-drain-race@example.com", 0, 0)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]string{"access_token": "test-token"})
	})
	mux.HandleFunc("/api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]any{"items": []federation.RemoteUser{{ID: 10, Email: "pusher-drain-race@example.com", Status: "active", Balance: 5}}})
	})
	mux.HandleFunc("/api/v1/admin/users/10/balance", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":400,"message":"balance cannot be negative"}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	p := newTestPusher(t, client, server.URL)
	require.NoError(t, p.runOnce(ctx))

	got, err := client.FederationOutbox.Get(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", got.Status, "a refused drain must be retried, not marked failed")
	require.Equal(t, 1, got.Attempts)
}

func insertUserUpsertRowWithHash(t *testing.T, ctx context.Context, client *dbent.Client, email, passwordHash string) *dbent.FederationOutbox {
	t.Helper()
	payload, err := json.Marshal(outboxUserPayload{ID: 1, Email: email, Status: "active", PasswordHash: passwordHash})
	require.NoError(t, err)
	row, err := client.FederationOutbox.Create().
		SetAggregateType("user").
		SetAggregateID("1").
		SetEventType("user.upsert").
		SetPayload(string(payload)).
		Save(ctx)
	require.NoError(t, err)
	return row
}

const testPasswordHash = "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234"

// passwordSyncServer fakes the overseas admin API. existing is the user the
// list endpoint returns (nil = not found until created). passwordStatus is
// what the federation-password-hash endpoint answers.
type passwordSyncServer struct {
	existing       *federation.RemoteUser
	passwordStatus int
	created        bool
	passwordBodies []map[string]string
}

func (s *passwordSyncServer) handler(email string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]string{"access_token": "test-token"})
	})
	mux.HandleFunc("/api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			s.created = true
			s.existing = &federation.RemoteUser{ID: 21, Email: email, Status: "active", Role: "user"}
			jsonOK(w, map[string]any{"id": 21})
			return
		}
		items := []federation.RemoteUser{}
		if s.existing != nil {
			items = append(items, *s.existing)
		}
		jsonOK(w, map[string]any{"items": items})
	})
	mux.HandleFunc("/api/v1/admin/users/21/federation-password-hash", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.passwordBodies = append(s.passwordBodies, body)
		if s.passwordStatus != 0 && s.passwordStatus != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(s.passwordStatus)
			_, _ = w.Write([]byte(`{"code":404,"message":"federation password sync is not enabled"}`))
			return
		}
		jsonOK(w, map[string]any{"message": "ok"})
	})
	return mux
}

func TestPusher_UserUpsertSyncsPasswordHash(t *testing.T) {
	for _, tc := range []struct {
		name        string
		existing    *federation.RemoteUser
		wantCreated bool
	}{
		{name: "new user is created then gets the hash", wantCreated: true},
		{name: "existing user gets the hash", existing: &federation.RemoteUser{ID: 21, Email: "pw-sync@example.com", Status: "active", Role: "user"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := newPusherTestClient(t)
			row := insertUserUpsertRowWithHash(t, ctx, client, "pw-sync@example.com", testPasswordHash)
			fake := &passwordSyncServer{existing: tc.existing}
			server := httptest.NewServer(fake.handler("pw-sync@example.com"))
			defer server.Close()

			p := newTestPusher(t, client, server.URL)
			require.NoError(t, p.runOnce(ctx))

			got, err := client.FederationOutbox.Get(ctx, row.ID)
			require.NoError(t, err)
			require.Equal(t, "delivered", got.Status)
			require.Equal(t, tc.wantCreated, fake.created)
			require.Equal(t, []map[string]string{{"password_hash": testPasswordHash}}, fake.passwordBodies)
		})
	}
}

func TestPusher_UserUpsertWithoutHashSkipsPasswordCall(t *testing.T) {
	ctx := context.Background()
	client := newPusherTestClient(t)
	row := insertUserUpsertRowWithHash(t, ctx, client, "pw-legacy@example.com", "")
	fake := &passwordSyncServer{existing: &federation.RemoteUser{ID: 21, Email: "pw-legacy@example.com", Status: "active", Role: "user"}}
	server := httptest.NewServer(fake.handler("pw-legacy@example.com"))
	defer server.Close()

	p := newTestPusher(t, client, server.URL)
	require.NoError(t, p.runOnce(ctx))

	got, err := client.FederationOutbox.Get(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, "delivered", got.Status)
	require.Empty(t, fake.passwordBodies)
}

// Both deployments may share an admin email: user.upsert must never touch an
// overseas admin, but balance.snapshot updates it (admin balances are shared).
func TestPusher_OverseasAdminGetsBalanceButNotIdentity(t *testing.T) {
	ctx := context.Background()
	client := newPusherTestClient(t)
	upsert := insertUserUpsertRowWithHash(t, ctx, client, "admin@sub2api.local", testPasswordHash)
	snapshot := insertBalanceSnapshotRow(t, ctx, client, "admin@sub2api.local", 5, 7)
	fake := &passwordSyncServer{existing: &federation.RemoteUser{ID: 21, Email: "admin@sub2api.local", Status: "active", Role: "admin"}}
	mux := fake.handler("admin@sub2api.local")
	var balanceBodies, watermarkBodies []map[string]any
	mux.HandleFunc("/api/v1/admin/users/21/balance", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		balanceBodies = append(balanceBodies, body)
		jsonOK(w, map[string]any{"message": "ok"})
	})
	mux.HandleFunc("/api/v1/admin/users/21/federation-usage-watermark", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		watermarkBodies = append(watermarkBodies, body)
		jsonOK(w, map[string]any{"message": "ok"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	p := newTestPusher(t, client, server.URL)
	require.NoError(t, p.runOnce(ctx))

	gotUpsert, err := client.FederationOutbox.Get(ctx, upsert.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", gotUpsert.Status)
	require.Empty(t, fake.passwordBodies)

	gotSnapshot, err := client.FederationOutbox.Get(ctx, snapshot.ID)
	require.NoError(t, err)
	require.Equal(t, "delivered", gotSnapshot.Status)
	require.Equal(t, []map[string]any{{"balance": 5.0, "operation": "set"}}, balanceBodies)
	require.Equal(t, []map[string]any{{"usage_seq": 7.0}}, watermarkBodies)
}

// No user.upsert ever creates an admin mirror, so a mainland admin without an
// overseas account is skipped instead of retried; a regular user still waits.
func TestPusher_SkipsAdminSnapshotWithoutOverseasAccount(t *testing.T) {
	for _, tc := range []struct {
		role       string
		wantStatus string
	}{
		{role: "admin", wantStatus: "delivered"},
		{role: "user", wantStatus: "pending"},
	} {
		t.Run(tc.role, func(t *testing.T) {
			ctx := context.Background()
			client := newPusherTestClient(t)
			local, err := client.User.Create().SetEmail("mainland-only@example.com").SetPasswordHash("x").SetRole(tc.role).Save(ctx)
			require.NoError(t, err)
			payload, err := json.Marshal(outboxBalancePayload{ID: local.ID, Email: local.Email, Balance: 5})
			require.NoError(t, err)
			row, err := client.FederationOutbox.Create().
				SetAggregateType("user").
				SetAggregateID(fmt.Sprintf("%d", local.ID)).
				SetEventType("balance.snapshot").
				SetPayload(string(payload)).
				Save(ctx)
			require.NoError(t, err)
			fake := &passwordSyncServer{}
			server := httptest.NewServer(fake.handler(local.Email))
			defer server.Close()

			p := newTestPusher(t, client, server.URL)
			require.NoError(t, p.runOnce(ctx))

			got, err := client.FederationOutbox.Get(ctx, row.ID)
			require.NoError(t, err)
			require.Equal(t, tc.wantStatus, got.Status)
		})
	}
}

// Overseas without federation.accept_password_hash answers 404: the row fails
// terminally, and the hash must not leak into last_error or the logs.
func TestPusher_PasswordSyncDisabledOverseasFailsWithoutLeakingHash(t *testing.T) {
	ctx := context.Background()
	client := newPusherTestClient(t)
	row := insertUserUpsertRowWithHash(t, ctx, client, "pw-off@example.com", testPasswordHash)
	fake := &passwordSyncServer{
		existing:       &federation.RemoteUser{ID: 21, Email: "pw-off@example.com", Status: "active", Role: "user"},
		passwordStatus: http.StatusNotFound,
	}
	server := httptest.NewServer(fake.handler("pw-off@example.com"))
	defer server.Close()

	p := newTestPusher(t, client, server.URL)
	var logs bytes.Buffer
	p.logger = slog.New(slog.NewTextHandler(&logs, nil))
	require.NoError(t, p.runOnce(ctx))

	got, err := client.FederationOutbox.Get(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", got.Status)
	require.NotNil(t, got.LastError)
	require.NotContains(t, *got.LastError, testPasswordHash)
	require.NotContains(t, logs.String(), testPasswordHash)
}

func TestBackfillUsers_QueuesAdminBalanceButNotIdentity(t *testing.T) {
	ctx := context.Background()
	client := newPusherTestClient(t)
	mk := func(email, role string) *dbent.User {
		u, err := client.User.Create().SetEmail(email).SetPasswordHash("$2a$10$" + email).SetRole(role).SetBalance(3).Save(ctx)
		require.NoError(t, err)
		return u
	}
	regular := mk("backfill-user@example.com", "user")
	admin := mk("backfill-admin@example.com", "admin")
	gone := mk("backfill-deleted@example.com", "user")
	require.NoError(t, client.User.DeleteOneID(gone.ID).Exec(ctx))

	n, err := backfillUsers(ctx, client, true)
	require.NoError(t, err)
	require.Equal(t, 2, n, "dry run counts every live user")
	count, err := client.FederationOutbox.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count, "dry run writes nothing")

	n, err = backfillUsers(ctx, client, false)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	rows, err := client.FederationOutbox.Query().Order(dbent.Asc(federationoutbox.FieldID)).All(ctx)
	require.NoError(t, err)
	var got []string
	for _, r := range rows {
		got = append(got, r.EventType+":"+r.AggregateID)
	}
	require.Equal(t, []string{
		fmt.Sprintf("user.upsert:%d", regular.ID),
		fmt.Sprintf("balance.snapshot:%d", regular.ID),
		fmt.Sprintf("balance.snapshot:%d", admin.ID),
	}, got, "admins get a balance.snapshot but never a user.upsert")
	var payload outboxUserPayload
	require.NoError(t, json.Unmarshal([]byte(rows[0].Payload), &payload))
	require.Equal(t, "$2a$10$backfill-user@example.com", payload.PasswordHash)
}
