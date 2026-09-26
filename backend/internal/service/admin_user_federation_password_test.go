//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/LuckyKuang/sub2api-plus/internal/config"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func federationPasswordTestService(enabled bool, u *User) (*adminServiceImpl, *userRepoStub) {
	repo := &userRepoStub{user: u}
	cfg := &config.Config{}
	cfg.Federation.AcceptPasswordHash = enabled
	return &adminServiceImpl{cfg: cfg, userRepo: repo}, repo
}

func mustBcrypt(t *testing.T, password string, cost int) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	require.NoError(t, err)
	return string(h)
}

func TestSetFederationPasswordHash_DisabledByDefault(t *testing.T) {
	s, repo := federationPasswordTestService(false, &User{ID: 7, Email: "u@example.com", Role: RoleUser})
	err := s.SetFederationPasswordHash(context.Background(), 7, mustBcrypt(t, "pw-123456", 10))
	require.ErrorIs(t, err, ErrFederationPasswordSyncDisabled)
	require.Empty(t, repo.updated)
}

func TestSetFederationPasswordHash_RejectsInvalidHashes(t *testing.T) {
	for name, h := range map[string]string{
		"plaintext":    "pw-123456",
		"empty":        "",
		"cost too low": mustBcrypt(t, "pw-123456", 4),
		"not bcrypt":   "$argon2id$v=19$m=65536,t=3,p=4$" + strings.Repeat("a", 40),
	} {
		t.Run(name, func(t *testing.T) {
			s, repo := federationPasswordTestService(true, &User{ID: 7, Email: "u@example.com", Role: RoleUser})
			err := s.SetFederationPasswordHash(context.Background(), 7, h)
			require.ErrorIs(t, err, ErrFederationPasswordHashInvalid)
			require.Empty(t, repo.updated)
		})
	}
}

func TestSetFederationPasswordHash_RefusesAdmin(t *testing.T) {
	s, repo := federationPasswordTestService(true, &User{ID: 1, Email: "admin@example.com", Role: RoleAdmin, PasswordHash: "old"})
	err := s.SetFederationPasswordHash(context.Background(), 1, mustBcrypt(t, "pw-123456", 10))
	require.ErrorIs(t, err, ErrFederationAdminNotSynced)
	require.Empty(t, repo.updated)
}

// The stored hash must let the user log in with the original (mainland)
// password, and the token version must change so old sessions die.
func TestSetFederationPasswordHash_StoresVerbatimAndRotatesTokenVersion(t *testing.T) {
	u := &User{ID: 7, Email: "u@example.com", Role: RoleUser, PasswordHash: mustBcrypt(t, "old-password", 10)}
	before := resolvedTokenVersion(&User{Email: u.Email, PasswordHash: u.PasswordHash})
	s, repo := federationPasswordTestService(true, u)

	h := mustBcrypt(t, "mainland-password", 10)
	require.NoError(t, s.SetFederationPasswordHash(context.Background(), 7, h))

	require.Len(t, repo.updated, 1)
	stored := repo.updated[0]
	require.Equal(t, h, stored.PasswordHash, "hash is stored verbatim, not re-hashed")
	require.True(t, stored.CheckPassword("mainland-password"))
	require.False(t, stored.CheckPassword("old-password"))
	require.NotEqual(t, before, resolvedTokenVersion(&User{Email: stored.Email, PasswordHash: stored.PasswordHash}))

	require.NoError(t, s.SetFederationPasswordHash(context.Background(), 7, h))
	require.Len(t, repo.updated, 1, "re-sending the same hash is a no-op")
}

func TestAuditBodyRedactsFederationPasswordHash(t *testing.T) {
	h := mustBcrypt(t, "pw-123456", 10)
	redacted := RedactAuditBody([]byte(`{"password_hash":"`+h+`"}`), "application/json")
	require.NotContains(t, redacted, h)
}
