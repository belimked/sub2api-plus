//go:build unit || !integration

package service

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildOAuthMetadataUserID_FallbackWithoutAccountUUID(t *testing.T) {
	svc := &GatewayService{}

	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-5",
		Stream:         true,
		MetadataUserID: "",
	}

	account := &Account{
		ID:    123,
		Type:  AccountTypeOAuth,
		Extra: map[string]any{}, // intentionally missing account_uuid / claude_user_id
	}

	fp := &Fingerprint{ClientID: "deadbeef"} // should be used as user id in legacy format

	got := svc.buildOAuthMetadataUserID(parsed, account, fp)
	require.NotEmpty(t, got)

	// Legacy format: user_{client}_account__session_{uuid}
	re := regexp.MustCompile(`^user_[a-zA-Z0-9]+_account__session_[a-f0-9-]{36}$`)
	require.True(t, re.MatchString(got), "unexpected user_id format: %s", got)
}

func TestBuildOAuthMetadataUserID_UsesAccountUUIDWhenPresent(t *testing.T) {
	svc := &GatewayService{}

	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-5",
		Stream:         true,
		MetadataUserID: "",
	}

	const configuredDevice = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	account := &Account{
		ID:          123,
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"claude_user_id": strings.ToUpper(configuredDevice)},
		Extra:       map[string]any{"account_uuid": "acc-uuid"},
	}

	got := svc.buildOAuthMetadataUserID(parsed, account, nil)
	require.NotEmpty(t, got)

	// New format: user_{client}_account_{account_uuid}_session_{uuid}
	re := regexp.MustCompile(`^user_` + configuredDevice + `_account_acc-uuid_session_[a-f0-9-]{36}$`)
	require.True(t, re.MatchString(got), "unexpected user_id format: %s", got)
}

func TestBuildOAuthMetadataUserID_IgnoresUnusableDeviceOverride(t *testing.T) {
	svc := &GatewayService{}
	parsed := &ParsedRequest{Model: "claude-sonnet-4-5"}
	fp := &Fingerprint{ClientID: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}

	for name, account := range map[string]*Account{
		"malformed_credential": {ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
			Credentials: map[string]any{"claude_user_id": "clientid123"}},
		"legacy_extra_location": {ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
			Extra: map[string]any{"claude_user_id": "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"}},
		"non_anthropic_account": {ID: 3, Platform: PlatformGemini, Type: AccountTypeOAuth,
			Credentials: map[string]any{"claude_user_id": "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := svc.buildOAuthMetadataUserID(parsed, account, fp)
			require.True(t, strings.HasPrefix(got, "user_"+fp.ClientID+"_account_"), "unexpected user_id: %s", got)
		})
	}
}

// TestBuildOAuthMetadataUserID_SessionIDStableAcrossTurns 验证伪装路径合成的
// metadata.user_id 在同一会话多轮请求间保持不变（session_id 稳定），贴近真实 Claude Code
// 进程级稳定的 session。账号 / 指纹 / UA 版本均相同，唯一可能变化的就是 session_id，
// 因此直接比较完整 user_id 字符串即可判定 session_id 是否稳定。
func TestBuildOAuthMetadataUserID_SessionIDStableAcrossTurns(t *testing.T) {
	svc := &GatewayService{}
	account := &Account{ID: 777, Type: AccountTypeOAuth, Extra: map[string]any{"account_uuid": "acc-uuid"}}
	fp := &Fingerprint{ClientID: "clientid777", UserAgent: "claude-cli/2.1.161 (external, cli)"}

	mustParse := func(body string) *ParsedRequest {
		parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), PlatformAnthropic)
		require.NoError(t, err)
		return parsed
	}

	round1 := mustParse(`{"model":"claude-sonnet-4-5","system":"sys","messages":[` +
		`{"role":"user","content":"first question"}]}`)
	round2 := mustParse(`{"model":"claude-sonnet-4-5","system":"sys","messages":[` +
		`{"role":"user","content":"first question"},` +
		`{"role":"assistant","content":"answer 1"},` +
		`{"role":"user","content":"second question"}]}`)
	round3 := mustParse(`{"model":"claude-sonnet-4-5","system":"sys","messages":[` +
		`{"role":"user","content":"first question"},` +
		`{"role":"assistant","content":"answer 1"},` +
		`{"role":"user","content":"second question"},` +
		`{"role":"assistant","content":"answer 2"},` +
		`{"role":"user","content":"third question"}]}`)

	id1 := svc.buildOAuthMetadataUserID(round1, account, fp)
	id2 := svc.buildOAuthMetadataUserID(round2, account, fp)
	id3 := svc.buildOAuthMetadataUserID(round3, account, fp)

	require.NotEmpty(t, id1)
	require.Equal(t, id1, id2, "session_id 应随对话增长保持不变")
	require.Equal(t, id2, id3, "session_id 应跨所有轮次保持不变")

	// 不同的首条 user 消息应派生出不同的 session_id（不同会话）。
	other := mustParse(`{"model":"claude-sonnet-4-5","system":"sys","messages":[` +
		`{"role":"user","content":"a completely different opener"}]}`)
	idOther := svc.buildOAuthMetadataUserID(other, account, fp)
	require.NotEqual(t, id1, idOther, "不同首条消息应派生不同 session_id")
}
