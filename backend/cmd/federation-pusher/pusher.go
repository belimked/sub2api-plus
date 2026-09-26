package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	dbent "github.com/LuckyKuang/sub2api-plus/ent"
	"github.com/LuckyKuang/sub2api-plus/ent/federationoutbox"
	"github.com/LuckyKuang/sub2api-plus/internal/federation"

	"entgo.io/ent/dialect"
)

// purgeInterval bounds how often the delivered-row purge runs; the poll loop
// ticks every few seconds, the purge doesn't need to.
const purgeInterval = time.Hour

type pusher struct {
	db       *dbent.Client
	overseas *overseasClient
	cfg      pusherConfig
	logger   *slog.Logger

	lastPurge time.Time
}

// outboxUserPayload and outboxBalancePayload mirror the JSON shapes the
// FederationOutboxMixin hook writes
// (backend/ent/schema/mixins/federation_outbox.go). They're a wire
// contract, not shared Go code, so they're duplicated here deliberately
// rather than importing the schema package into a cmd/ binary.
type outboxUserPayload struct {
	ID           int64  `json:"id"`
	Email        string `json:"email"`
	Status       string `json:"status"`
	PasswordHash string `json:"password_hash,omitempty"`
}

type outboxBalancePayload struct {
	ID           int64   `json:"id"`
	Email        string  `json:"email"`
	Balance      float64 `json:"balance"`
	AsOfUsageSeq int64   `json:"as_of_usage_seq"`
}

func (p *pusher) runOnce(ctx context.Context) error {
	rows, err := p.claimBatch(ctx)
	if err != nil {
		return fmt.Errorf("claim batch: %w", err)
	}
	for _, row := range rows {
		p.deliver(ctx, row)
	}
	p.purgeDeliveredIfDue(ctx, time.Now())
	return nil
}

// purgeDeliveredIfDue deletes delivered rows whose last update is older than
// cfg.DeliveredRetention, at most once per purgeInterval. Only "delivered"
// rows are removed: "failed" rows stay for manual review, and pending /
// in_flight rows are still owed to the overseas side. A purge error is
// logged and retried on the next interval; it never blocks delivery.
func (p *pusher) purgeDeliveredIfDue(ctx context.Context, now time.Time) {
	if p.cfg.DeliveredRetention <= 0 || now.Sub(p.lastPurge) < purgeInterval {
		return
	}
	p.lastPurge = now
	n, err := p.db.FederationOutbox.Delete().
		Where(
			federationoutbox.StatusEQ("delivered"),
			federationoutbox.UpdatedAtLT(now.Add(-p.cfg.DeliveredRetention)),
		).
		Exec(ctx)
	if err != nil {
		p.logger.Error("federation-pusher: purge delivered", "error", err)
		return
	}
	if n > 0 {
		p.logger.Info("federation-pusher: purged delivered rows", "count", n, "retention", p.cfg.DeliveredRetention)
	}
}

// claimBatch locks up to cfg.BatchSize eligible rows (pending, or an
// in_flight row left behind by a crashed run whose retry time has passed)
// and flips them to in_flight in the same transaction, so two pusher
// instances running concurrently never deliver the same row twice.
func (p *pusher) claimBatch(ctx context.Context) ([]*dbent.FederationOutbox, error) {
	tx, err := p.db.Tx(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	query := tx.FederationOutbox.Query().
		Where(
			federationoutbox.StatusIn("pending", "in_flight"),
			federationoutbox.Or(
				federationoutbox.NextRetryAtIsNil(),
				federationoutbox.NextRetryAtLTE(now),
			),
		).
		Order(dbent.Asc(federationoutbox.FieldID)).
		Limit(p.cfg.BatchSize)
	// SQLite (used by unit tests) doesn't support SELECT .. FOR UPDATE; only
	// lock rows on Postgres, matching the pattern in
	// internal/repository/setting_repo.go. This means the unit tests below
	// don't cover concurrent-claim safety between two pusher instances --
	// that property only actually applies (and would need an `integration`
	// test with real Postgres to verify) in production.
	if p.db.Driver().Dialect() == dialect.Postgres {
		query = query.ForUpdate()
	}
	rows, err := query.All(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if len(rows) == 0 {
		return nil, tx.Commit()
	}

	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	if _, err := tx.FederationOutbox.Update().
		Where(federationoutbox.IDIn(ids...)).
		SetStatus("in_flight").
		Save(ctx); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return rows, nil
}

func (p *pusher) deliver(ctx context.Context, row *dbent.FederationOutbox) {
	idempotencyKey := fmt.Sprintf("federation-outbox-%d", row.ID)

	var err error
	switch row.EventType {
	case "user.upsert":
		var payload outboxUserPayload
		if unmarshalErr := json.Unmarshal([]byte(row.Payload), &payload); unmarshalErr != nil {
			p.markFailed(ctx, row, fmt.Sprintf("invalid payload: %v", unmarshalErr))
			return
		}
		err = p.overseas.upsertUser(ctx, idempotencyKey, payload.Email, payload.Status, payload.PasswordHash)
	case "balance.snapshot":
		var payload outboxBalancePayload
		if unmarshalErr := json.Unmarshal([]byte(row.Payload), &payload); unmarshalErr != nil {
			p.markFailed(ctx, row, fmt.Sprintf("invalid payload: %v", unmarshalErr))
			return
		}
		err = p.overseas.applyBalanceSnapshot(ctx, idempotencyKey, payload.Email, payload.Balance, payload.AsOfUsageSeq)
	default:
		p.markFailed(ctx, row, fmt.Sprintf("unknown event_type %q", row.EventType))
		return
	}

	switch {
	case err == nil:
		p.markDelivered(ctx, row)
	case isRetryable(err) && row.Attempts+1 < p.cfg.MaxAttempts:
		p.markRetry(ctx, row, err)
	default:
		p.markFailed(ctx, row, err.Error())
	}
}

func (p *pusher) markDelivered(ctx context.Context, row *dbent.FederationOutbox) {
	if _, err := p.db.FederationOutbox.UpdateOneID(row.ID).
		SetStatus("delivered").
		AddAttempts(1).
		ClearLastError().
		ClearNextRetryAt().
		Save(ctx); err != nil {
		p.logger.Error("federation-pusher: mark delivered", "id", row.ID, "error", err)
		return
	}
	p.logger.Info("federation-pusher: delivered", "id", row.ID, "aggregate_id", row.AggregateID)
}

// markFailed is terminal: the pusher will not pick this row up again. It
// needs a human to look at last_error and either fix the data and reset
// status back to "pending", or accept the row as permanently undeliverable.
func (p *pusher) markFailed(ctx context.Context, row *dbent.FederationOutbox, reason string) {
	if _, err := p.db.FederationOutbox.UpdateOneID(row.ID).
		SetStatus("failed").
		AddAttempts(1).
		SetLastError(federation.Truncate(reason, 2000)).
		ClearNextRetryAt().
		Save(ctx); err != nil {
		p.logger.Error("federation-pusher: mark failed", "id", row.ID, "error", err)
	}
	p.logger.Warn("federation-pusher: event marked failed, needs manual review", "id", row.ID, "reason", reason)
}

func (p *pusher) markRetry(ctx context.Context, row *dbent.FederationOutbox, cause error) {
	attempts := row.Attempts + 1
	next := time.Now().Add(federation.RetryBackoff(attempts))
	if _, err := p.db.FederationOutbox.UpdateOneID(row.ID).
		SetStatus("pending").
		SetAttempts(attempts).
		SetLastError(federation.Truncate(cause.Error(), 2000)).
		SetNextRetryAt(next).
		Save(ctx); err != nil {
		p.logger.Error("federation-pusher: mark retry", "id", row.ID, "error", err)
		return
	}
	p.logger.Warn("federation-pusher: retrying", "id", row.ID, "attempts", attempts, "next_retry_at", next, "cause", cause)
}
