package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/LuckyKuang/sub2api-plus/ent/user"
	infraerrors "github.com/LuckyKuang/sub2api-plus/internal/pkg/errors"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrFederationPasswordSyncDisabled = infraerrors.NotFound("FEDERATION_PASSWORD_SYNC_DISABLED", "federation password sync is not enabled")
	ErrFederationPasswordHashInvalid  = infraerrors.BadRequest("FEDERATION_PASSWORD_HASH_INVALID", "password_hash must be a bcrypt hash with cost 10-14")
	ErrFederationAdminNotSynced       = infraerrors.Forbidden("FEDERATION_ADMIN_NOT_SYNCED", "admin accounts are never federated")
)

// BumpFederationUsageWatermark advances federation_usage_watermark_seq to
// max(current, usageSeq) in a single conditional UPDATE, atomic and
// portable across Postgres/SQLite without a dialect-specific GREATEST/MAX
// call. Deliberately bypasses UserRepository (see
// openspec/changes/federation-balance-sync/design.md for why) and uses the
// service's own entClient directly, the same field UpdateUserBalance uses
// to open transactions.
func (s *adminServiceImpl) BumpFederationUsageWatermark(ctx context.Context, userID int64, usageSeq int64) error {
	if s.entClient == nil {
		return fmt.Errorf("federation usage watermark: no ent client configured")
	}

	n, err := s.entClient.User.Update().
		Where(user.IDEQ(userID), user.FederationUsageWatermarkSeqLT(usageSeq)).
		SetFederationUsageWatermarkSeq(usageSeq).
		Save(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		// The API key auth cache snapshots the watermark; drop it so the
		// admission check sees the new value on the next request.
		if s.authCacheInvalidator != nil {
			s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
		}
		return nil
	}

	// n == 0 means either the user doesn't exist, or the watermark is
	// already >= usageSeq (a benign no-op -- retried/out-of-order delivery).
	// Distinguish the two so callers get a real error for the former.
	exists, err := s.entClient.User.Query().Where(user.IDEQ(userID)).Exist(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return ErrUserNotFound
	}
	return nil
}

// SetFederationPasswordHash stores a bcrypt hash received from the mainland
// federation-pusher verbatim, so the federated user logs in here with their
// mainland password. Only enabled by federation.accept_password_hash (the
// overseas node). It goes through the same repository write as
// UserService.ChangePassword, so the token version (derived from email +
// password_hash) changes and existing sessions are invalidated. Admin
// accounts are refused: both deployments may share an admin email.
func (s *adminServiceImpl) SetFederationPasswordHash(ctx context.Context, userID int64, passwordHash string) error {
	if s.cfg == nil || !s.cfg.Federation.AcceptPasswordHash {
		return ErrFederationPasswordSyncDisabled
	}
	if !validFederationPasswordHash(passwordHash) {
		return ErrFederationPasswordHashInvalid
	}
	u, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	if u.Role == RoleAdmin {
		return ErrFederationAdminNotSynced
	}
	if u.PasswordHash == passwordHash {
		return nil
	}
	u.PasswordHash = passwordHash
	return s.userRepo.Update(ctx, u, UserUpdateFields{PasswordHash: true})
}

// validFederationPasswordHash accepts only well-formed bcrypt hashes of a
// sane cost: below 10 is too cheap to crack-resist, above 14 would make
// every login on this node pathologically slow.
func validFederationPasswordHash(h string) bool {
	if len(h) != 60 {
		return false
	}
	if !strings.HasPrefix(h, "$2a$") && !strings.HasPrefix(h, "$2b$") && !strings.HasPrefix(h, "$2y$") {
		return false
	}
	cost, err := bcrypt.Cost([]byte(h))
	return err == nil && cost >= 10 && cost <= 14
}
