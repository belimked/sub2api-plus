package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"

	dbent "github.com/LuckyKuang/sub2api-plus/ent"
	"github.com/LuckyKuang/sub2api-plus/ent/schema/mixins"
	"github.com/LuckyKuang/sub2api-plus/internal/service"
)

// federationBalancePayload mirrors the JSON shape
// backend/ent/schema/mixins/federation_outbox.go emits for balance.snapshot
// events. Duplicated deliberately (wire contract, not shared Go code) --
// same convention as the pusher/tailer commands.
type federationBalancePayload struct {
	ID           int64   `json:"id"`
	Email        string  `json:"email"`
	Balance      float64 `json:"balance"`
	AsOfUsageSeq int64   `json:"as_of_usage_seq"`
}

// emitFederationBalanceOutbox writes a balance.snapshot outbox row for a
// balance change made through AdjustBalance/SetBalance/DeductBalance/
// DeductAvailableBalance. Those four bypass FederationOutboxMixin's ent hook entirely: two use raw
// SQL for the atomic "don't go negative" guard (DeductAvailableBalance too),
// and DeductBalance
// uses ent's bulk Update (OpUpdate), which the hook's op filter deliberately
// doesn't cover (see the hook's doc comment). Real-world verification during
// federation-balance-sync's rollout found the hook silently never fired for
// any of these, only for User.Create()'s initial balance -- see
// openspec/changes/federation-balance-sync/tasks.md's addendum.
//
// Called within the same transaction as the balance change (clientFromContext
// already resolves to the tx-scoped client), but a failure here is logged
// and swallowed rather than propagated: a federation outbox hiccup must
// never break real billing.
//
// No-op unless federation.outbox_enabled is on (mixins.FederationOutboxEnabled),
// so non-federated deployments pay nothing extra on the billing hot path.
// Admin balances are federated too; only admin identity is not (see
// FederationOutboxMixin).
func emitFederationBalanceOutbox(ctx context.Context, client *dbent.Client, userID int64, email string, balance float64, watermarkSeq int64) {
	if !mixins.FederationOutboxEnabled() {
		return
	}
	payload, err := json.Marshal(federationBalancePayload{
		ID:           userID,
		Email:        email,
		Balance:      balance,
		AsOfUsageSeq: watermarkSeq,
	})
	if err != nil {
		slog.Error("federation balance outbox: marshal payload", "user_id", userID, "error", err)
		return
	}
	if err := client.FederationOutbox.Create().
		SetAggregateType("user").
		SetAggregateID(fmt.Sprintf("%d", userID)).
		SetEventType("balance.snapshot").
		SetPayload(string(payload)).
		Exec(ctx); err != nil {
		slog.Error("federation balance outbox: insert", "user_id", userID, "error", err)
	}
}

// emitFederationBalanceOutboxByID re-fetches the user (DeductBalance's ent
// bulk Update doesn't return field values the way the raw-SQL RETURNING
// clauses in AdjustBalance/SetBalance do) and emits its balance.snapshot.
// The extra read happens only when the outbox is enabled. Gateway billing
// does not come through here: it uses the usage billing repository, see
// emitFederationBalanceOutboxTx.
func emitFederationBalanceOutboxByID(ctx context.Context, client *dbent.Client, userID int64) {
	if !mixins.FederationOutboxEnabled() {
		return
	}
	u, err := client.User.Get(ctx, userID)
	if err != nil {
		slog.Error("federation balance outbox: refetch user", "user_id", userID, "error", err)
		return
	}
	emitFederationBalanceOutbox(ctx, client, u.ID, u.Email, u.Balance, u.FederationUsageWatermarkSeq)
}

// scanBalanceChangeWithFederation runs an AdjustBalance/SetBalance statement
// that RETURNs old balance, new balance, email and
// federation_usage_watermark_seq -- the last two feed the balance.snapshot
// payload without a second query on the already-atomic UPDATE. ok is false
// when the statement matched no row.
func scanBalanceChangeWithFederation(ctx context.Context, client *dbent.Client, query string, args ...any) (change service.BalanceChange, email string, watermarkSeq int64, ok bool, err error) {
	rows, err := client.QueryContext(ctx, query, args...)
	if err != nil {
		return service.BalanceChange{}, "", 0, false, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	if !rows.Next() {
		if rowsErr := rows.Err(); rowsErr != nil {
			return service.BalanceChange{}, "", 0, false, rowsErr
		}
		return service.BalanceChange{}, "", 0, false, nil
	}
	if scanErr := rows.Scan(&change.Old, &change.New, &email, &watermarkSeq); scanErr != nil {
		return service.BalanceChange{}, "", 0, false, scanErr
	}
	return change, email, watermarkSeq, true, rows.Err()
}

// emitFederationBalanceOutboxTx writes a balance.snapshot outbox row inside a
// raw database/sql transaction -- the usage billing repository's path
// (gateway billing and batch image holds), which has no ent client. The
// payload is built from the users row in the same statement, so it reflects
// the balance this transaction just wrote.
//
// Like emitFederationBalanceOutbox, a failure is logged and swallowed. A
// failed statement would abort the whole PostgreSQL transaction, so the
// insert runs under a savepoint and a failure rolls back only the savepoint.
func emitFederationBalanceOutboxTx(ctx context.Context, tx *sql.Tx, userID int64) {
	if !mixins.FederationOutboxEnabled() {
		return
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT federation_outbox"); err != nil {
		slog.Error("federation balance outbox: savepoint", "user_id", userID, "error", err)
		return
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO federation_outbox_events (aggregate_type, aggregate_id, event_type, payload)
		SELECT 'user', id::text, 'balance.snapshot',
		       json_build_object('id', id, 'email', email, 'balance', balance,
		                         'as_of_usage_seq', federation_usage_watermark_seq)::text
		FROM users
		WHERE id = $1
	`, userID)
	if err != nil {
		slog.Error("federation balance outbox: insert", "user_id", userID, "error", err)
		if _, rbErr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT federation_outbox"); rbErr != nil {
			slog.Error("federation balance outbox: rollback savepoint", "user_id", userID, "error", rbErr)
		}
		return
	}
	if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT federation_outbox"); err != nil {
		slog.Error("federation balance outbox: release savepoint", "user_id", userID, "error", err)
	}
}
