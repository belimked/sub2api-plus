package repository

import (
	"context"
	"database/sql"

	"github.com/LuckyKuang/sub2api-plus/internal/service"
)

type claudeCodeDeviceStore struct {
	db *sql.DB
}

func NewClaudeCodeDeviceStore(db *sql.DB) service.ClaudeCodeDeviceStore {
	return &claudeCodeDeviceStore{db: db}
}

// GetOrCreateClaudeCodeDeviceID inserts candidate only when the account has no
// device ID, then reads the stored value. The read is a separate statement so
// it observes a row committed by a concurrent replica during the insert.
func (r *claudeCodeDeviceStore) GetOrCreateClaudeCodeDeviceID(ctx context.Context, accountID int64, candidate string) (string, error) {
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO claude_code_account_identities (account_id, device_id)
		VALUES ($1, $2)
		ON CONFLICT (account_id) DO NOTHING
	`, accountID, candidate); err != nil {
		return "", err
	}
	var deviceID string
	if err := scanSingleRow(ctx, r.db, `
		SELECT device_id FROM claude_code_account_identities WHERE account_id = $1
	`, []any{accountID}, &deviceID); err != nil {
		return "", err
	}
	return deviceID, nil
}
