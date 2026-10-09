package main

import (
	"context"
	"fmt"

	dbent "github.com/LuckyKuang/sub2api-plus/ent"
	"github.com/LuckyKuang/sub2api-plus/ent/schema/mixins"
	"github.com/LuckyKuang/sub2api-plus/ent/user"
	"github.com/LuckyKuang/sub2api-plus/internal/domain"
)

// backfillBatchSize bounds each backfill transaction.
const backfillBatchSize = 500

// backfillUsers queues a user.upsert (with password_hash) and a
// balance.snapshot for every existing non-deleted user, so users that
// predate federation (or password sync) get an overseas mirror they can log
// in to. The regular pusher loop then delivers them. Admins only get the
// balance.snapshot: admin identity is never federated. Returns the number of users queued (or that would be,
// with dryRun).
func backfillUsers(ctx context.Context, client *dbent.Client, dryRun bool) (int, error) {
	users, err := client.User.Query().
		Order(dbent.Asc(user.FieldID)).
		All(ctx)
	if err != nil {
		return 0, fmt.Errorf("list users: %w", err)
	}
	if dryRun {
		return len(users), nil
	}
	for start := 0; start < len(users); start += backfillBatchSize {
		end := min(start+backfillBatchSize, len(users))
		tx, err := client.Tx(ctx)
		if err != nil {
			return start, err
		}
		for _, u := range users[start:end] {
			if u.Role != domain.RoleAdmin {
				if err := mixins.EmitUserUpsertOutbox(ctx, tx.Client(), u); err != nil {
					_ = tx.Rollback()
					return start, fmt.Errorf("queue user.upsert for user %d: %w", u.ID, err)
				}
			}
			if err := mixins.EmitBalanceSnapshotOutbox(ctx, tx.Client(), u); err != nil {
				_ = tx.Rollback()
				return start, fmt.Errorf("queue balance.snapshot for user %d: %w", u.ID, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return start, err
		}
	}
	return len(users), nil
}
