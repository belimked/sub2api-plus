//go:build unit || !integration

package service

import (
	"strings"
	"testing"

	infraerrors "github.com/LuckyKuang/sub2api-plus/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

const overrideClaudeCodeDeviceID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

func TestNormalizeClaudeCodeDeviceCredentialStoresLowercaseDevice(t *testing.T) {
	credentials := map[string]any{"claude_user_id": "  " + strings.ToUpper(overrideClaudeCodeDeviceID) + " "}
	require.NoError(t, NormalizeClaudeCodeDeviceCredential(credentials))
	require.Equal(t, overrideClaudeCodeDeviceID, credentials["claude_user_id"])
}

func TestNormalizeClaudeCodeDeviceCredentialClearsBlankValues(t *testing.T) {
	for _, blank := range []any{nil, "", "   "} {
		credentials := map[string]any{"claude_user_id": blank, "access_token": "kept"}
		require.NoError(t, NormalizeClaudeCodeDeviceCredential(credentials))
		require.NotContains(t, credentials, "claude_user_id")
		require.Equal(t, "kept", credentials["access_token"])
	}
	require.NoError(t, NormalizeClaudeCodeDeviceCredential(nil))
	require.NoError(t, NormalizeClaudeCodeDeviceCredential(map[string]any{}))
}

func TestNormalizeClaudeCodeDeviceCredentialRejectsMalformedValues(t *testing.T) {
	for _, invalid := range []any{"clientid123", strings.Repeat("g", 64), strings.Repeat("a", 63), strings.Repeat("a", 65), 42} {
		err := NormalizeClaudeCodeDeviceCredential(map[string]any{"claude_user_id": invalid})
		require.Error(t, err, "%v", invalid)
		require.Equal(t, "CLAUDE_DEVICE_ID_INVALID", infraerrors.Reason(err))
	}
}

func TestClaudeCodeDeviceOverrideScope(t *testing.T) {
	credentials := map[string]any{"claude_user_id": strings.ToUpper(overrideClaudeCodeDeviceID)}
	for _, tc := range []struct {
		name    string
		account *Account
		want    string
	}{
		{name: "oauth", account: &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: credentials}, want: overrideClaudeCodeDeviceID},
		{name: "setup_token", account: &Account{Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Credentials: credentials}, want: overrideClaudeCodeDeviceID},
		{name: "api_key", account: &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: credentials}},
		{name: "malformed", account: &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: map[string]any{"claude_user_id": "clientid123"}}},
		{name: "extra_is_not_a_source", account: &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: credentials}},
		{name: "nil_account"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.account.ClaudeCodeDeviceOverride())
		})
	}
}
