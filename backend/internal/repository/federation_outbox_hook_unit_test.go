//go:build unit || !integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	dbent "github.com/LuckyKuang/sub2api-plus/ent"
	"github.com/LuckyKuang/sub2api-plus/ent/enttest"
	"github.com/LuckyKuang/sub2api-plus/ent/federationoutbox"
	"github.com/LuckyKuang/sub2api-plus/ent/schema/mixins"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

// enableFederationOutbox turns on the process-wide federation.outbox_enabled
// gate for one test and restores the default (off) afterwards.
func enableFederationOutbox(t *testing.T) {
	t.Helper()
	mixins.SetFederationOutboxEnabled(true)
	t.Cleanup(func() { mixins.SetFederationOutboxEnabled(false) })
}

func newFederationOutboxTestClient(t *testing.T) *dbent.Client {
	t.Helper()
	return newFederationOutboxTestClientWithGate(t, true)
}

func newFederationOutboxTestClientWithGate(t *testing.T, outboxEnabled bool) *dbent.Client {
	t.Helper()
	if outboxEnabled {
		enableFederationOutbox(t)
	}

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

func TestFederationOutbox_EmitsOnUserCreate(t *testing.T) {
	ctx := context.Background()
	client := newFederationOutboxTestClient(t)

	u, err := client.User.Create().
		SetEmail("federation-poc@example.com").
		SetPasswordHash("test-password-hash").
		Save(ctx)
	require.NoError(t, err, "create user")

	// Create also emits balance.snapshot (Balance defaults to 0, which ent
	// reports as "set" on the mutation) -- see
	// TestFederationOutbox_EmitsBalanceSnapshotOnBalanceChange for that half;
	// this test only cares about the user.upsert side.
	events, err := client.FederationOutbox.Query().Where(federationoutbox.EventTypeEQ("user.upsert")).All(ctx)
	require.NoError(t, err)
	require.Len(t, events, 1, "expected exactly one user.upsert event for the created user")

	ev := events[0]
	require.Equal(t, "user", ev.AggregateType)
	require.Equal(t, fmt.Sprintf("%d", u.ID), ev.AggregateID)
	require.Equal(t, "user.upsert", ev.EventType)
	require.Equal(t, "pending", ev.Status)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(ev.Payload), &payload))
	require.Equal(t, "federation-poc@example.com", payload["email"])
}

func TestFederationOutbox_EmitsOnEmailUpdate(t *testing.T) {
	ctx := context.Background()
	client := newFederationOutboxTestClient(t)

	u, err := client.User.Create().
		SetEmail("federation-update-before@example.com").
		SetPasswordHash("test-password-hash").
		Save(ctx)
	require.NoError(t, err, "create user")

	_, err = client.User.UpdateOneID(u.ID).
		SetEmail("federation-update-after@example.com").
		Save(ctx)
	require.NoError(t, err, "update user email")

	events, err := client.FederationOutbox.Query().
		Where(federationoutbox.EventTypeEQ("user.upsert")).
		Order(dbent.Asc(federationoutbox.FieldID)).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, events, 2, "expected one user.upsert from create and one from the email update")

	ev := events[1]
	require.Equal(t, "user.upsert", ev.EventType)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(ev.Payload), &payload))
	require.Equal(t, "federation-update-after@example.com", payload["email"])
}

func TestFederationOutbox_EmitsOnStatusUpdate(t *testing.T) {
	ctx := context.Background()
	client := newFederationOutboxTestClient(t)

	u, err := client.User.Create().
		SetEmail("federation-status@example.com").
		SetPasswordHash("test-password-hash").
		Save(ctx)
	require.NoError(t, err, "create user")

	_, err = client.User.UpdateOneID(u.ID).
		SetStatus("disabled").
		Save(ctx)
	require.NoError(t, err, "update user status")

	events, err := client.FederationOutbox.Query().
		Where(federationoutbox.EventTypeEQ("user.upsert")).
		Order(dbent.Asc(federationoutbox.FieldID)).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, events, 2, "expected one user.upsert from create and one from the status update")

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(events[1].Payload), &payload))
	require.Equal(t, "disabled", payload["status"])
}

func TestFederationOutbox_SkipsUnrelatedFieldUpdate(t *testing.T) {
	ctx := context.Background()
	client := newFederationOutboxTestClient(t)

	u, err := client.User.Create().
		SetEmail("federation-unrelated@example.com").
		SetPasswordHash("test-password-hash").
		Save(ctx)
	require.NoError(t, err, "create user")

	_, err = client.User.UpdateOneID(u.ID).
		SetConcurrency(10).
		Save(ctx)
	require.NoError(t, err, "update unrelated field")

	// 2, not 1: create itself emits both user.upsert and balance.snapshot
	// (Balance defaults to 0, which counts as "set"). The assertion here is
	// that the *unrelated* update adds nothing further.
	events, err := client.FederationOutbox.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, events, 2, "an update that doesn't touch email/status/balance must not emit a further event")
}

func TestFederationOutbox_EmitsBalanceSnapshotOnBalanceChange(t *testing.T) {
	ctx := context.Background()
	client := newFederationOutboxTestClient(t)

	u, err := client.User.Create().
		SetEmail("federation-balance@example.com").
		SetPasswordHash("test-password-hash").
		Save(ctx)
	require.NoError(t, err, "create user")

	before, err := client.FederationOutbox.Query().Count(ctx)
	require.NoError(t, err)

	_, err = client.User.UpdateOneID(u.ID).
		SetBalance(42.5).
		Save(ctx)
	require.NoError(t, err, "update user balance")

	// Create already emitted one balance.snapshot (balance=0, see
	// TestFederationOutbox_SkipsUnrelatedFieldUpdate's comment); this test
	// cares about the one from the explicit update, i.e. the latest one.
	events, err := client.FederationOutbox.Query().
		Where(federationoutbox.EventTypeEQ("balance.snapshot")).
		Order(dbent.Asc(federationoutbox.FieldID)).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, events, 2, "expected one balance.snapshot from create (balance=0) and one from the update")

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(events[1].Payload), &payload))
	require.InDelta(t, 42.5, payload["balance"], 0.0001)
	require.Equal(t, "federation-balance@example.com", payload["email"])
	require.Contains(t, payload, "as_of_usage_seq")

	after, err := client.FederationOutbox.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, before+1, after, "the balance update must not also re-emit user.upsert (email/status unchanged)")
}

// UpdateOneID(...).AddBalance -- the OAuth first-bind grant -- sets the
// "added" side of the mutation, not the "set" side.
func TestFederationOutbox_EmitsBalanceSnapshotOnAddBalance(t *testing.T) {
	ctx := context.Background()
	client := newFederationOutboxTestClient(t)

	u, err := client.User.Create().
		SetEmail("federation-add-balance@example.com").
		SetPasswordHash("test-password-hash").
		SetBalance(1.5).
		Save(ctx)
	require.NoError(t, err, "create user")

	require.NoError(t, client.User.UpdateOneID(u.ID).AddBalance(2).Exec(ctx))

	events, err := client.FederationOutbox.Query().
		Where(federationoutbox.EventTypeEQ("balance.snapshot")).
		Order(dbent.Asc(federationoutbox.FieldID)).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, events, 2, "one from create, one from AddBalance")

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(events[1].Payload), &payload))
	require.InDelta(t, 3.5, payload["balance"], 0.0001)
}

func TestFederationOutbox_RollbackDiscardsEvent(t *testing.T) {
	ctx := context.Background()
	client := newFederationOutboxTestClient(t)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)

	_, err = tx.Client().User.Create().
		SetEmail("federation-rollback@example.com").
		SetPasswordHash("test-password-hash").
		Save(ctx)
	require.NoError(t, err, "create user inside transaction")

	require.NoError(t, tx.Rollback())

	events, err := client.FederationOutbox.Query().All(ctx)
	require.NoError(t, err)
	require.Empty(t, events, "rollback must discard the outbox event along with the user row")
}

func TestFederationOutbox_DisabledGateWritesNothing(t *testing.T) {
	ctx := context.Background()
	client := newFederationOutboxTestClientWithGate(t, false)

	u, err := client.User.Create().
		SetEmail("federation-disabled@example.com").
		SetPasswordHash("test-password-hash").
		Save(ctx)
	require.NoError(t, err, "create user")

	_, err = client.User.UpdateOneID(u.ID).
		SetEmail("federation-disabled-renamed@example.com").
		SetStatus("disabled").
		SetBalance(12.5).
		Save(ctx)
	require.NoError(t, err, "update identity and balance")

	events, err := client.FederationOutbox.Query().All(ctx)
	require.NoError(t, err)
	require.Empty(t, events, "federation.outbox_enabled=false must not write any outbox event")
}

func TestFederationOutbox_UserUpsertCarriesPasswordHash(t *testing.T) {
	ctx := context.Background()
	client := newFederationOutboxTestClient(t)

	u, err := client.User.Create().
		SetEmail("federation-pw@example.com").
		SetPasswordHash("$2a$10$first").
		Save(ctx)
	require.NoError(t, err)

	_, err = client.User.UpdateOneID(u.ID).SetPasswordHash("$2a$10$second").Save(ctx)
	require.NoError(t, err, "password change")

	events, err := client.FederationOutbox.Query().
		Where(federationoutbox.EventTypeEQ("user.upsert")).
		Order(dbent.Asc(federationoutbox.FieldID)).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, events, 2, "create and password change each emit user.upsert")

	var hashes []string
	for _, e := range events {
		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(e.Payload), &payload))
		hash, _ := payload["password_hash"].(string)
		hashes = append(hashes, hash)
	}
	require.Equal(t, []string{"$2a$10$first", "$2a$10$second"}, hashes)
}

// Admin identity is never federated (both deployments may share an admin
// email), but admin balances are.
func TestFederationOutbox_AdminUsersEmitBalanceOnly(t *testing.T) {
	ctx := context.Background()
	client := newFederationOutboxTestClient(t)

	u, err := client.User.Create().
		SetEmail("federation-admin@example.com").
		SetPasswordHash("$2a$10$admin").
		SetRole("admin").
		Save(ctx)
	require.NoError(t, err)
	_, err = client.User.UpdateOneID(u.ID).SetPasswordHash("$2a$10$admin2").SetBalance(5).Save(ctx)
	require.NoError(t, err)

	events, err := client.FederationOutbox.Query().All(ctx)
	require.NoError(t, err)
	types := make([]string, 0, len(events))
	for _, e := range events {
		types = append(types, e.EventType)
	}
	require.NotContains(t, types, "user.upsert")
	require.Contains(t, types, "balance.snapshot")
}
