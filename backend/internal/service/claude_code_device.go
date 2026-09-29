package service

import (
	"strings"

	infraerrors "github.com/LuckyKuang/sub2api-plus/internal/pkg/errors"
)

// claudeCodeDeviceCredential stores an administrator-selected Claude Code
// device ID for an Anthropic OAuth or setup-token account.
const claudeCodeDeviceCredential = "claude_user_id"

// ClaudeCodeDeviceOverride returns the account's configured device ID. It
// outranks the persisted device on every Claude path; an empty or malformed
// value falls through to the persisted device.
func (a *Account) ClaudeCodeDeviceOverride() string {
	if a == nil || !a.IsAnthropicOAuthOrSetupToken() {
		return ""
	}
	id := strings.ToLower(strings.TrimSpace(a.GetCredential(claudeCodeDeviceCredential)))
	if !isClaudeCodeDeviceID(id) {
		return ""
	}
	return id
}

// NormalizeClaudeCodeDeviceCredential validates the device override before an
// account is saved. A blank value clears the override; any other value must be
// 64 hexadecimal characters and is stored in lowercase.
func NormalizeClaudeCodeDeviceCredential(credentials map[string]any) error {
	raw, ok := credentials[claudeCodeDeviceCredential]
	if !ok {
		return nil
	}
	if raw == nil {
		delete(credentials, claudeCodeDeviceCredential)
		return nil
	}
	value, isString := raw.(string)
	if !isString {
		return infraerrors.BadRequest("CLAUDE_DEVICE_ID_INVALID", "claude_user_id must be a string")
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		delete(credentials, claudeCodeDeviceCredential)
		return nil
	}
	if !isClaudeCodeDeviceID(value) {
		return infraerrors.BadRequest("CLAUDE_DEVICE_ID_INVALID", "claude_user_id must be 64 hexadecimal characters")
	}
	credentials[claudeCodeDeviceCredential] = value
	return nil
}
