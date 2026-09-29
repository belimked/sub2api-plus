//go:build unit || !integration

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LuckyKuang/sub2api-plus/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	cachedClaudeCodeDeviceID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	storedClaudeCodeDeviceID = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

type stubClaudeCodeDeviceStore struct {
	stored     map[int64]string
	candidates []string
	err        error
}

func (s *stubClaudeCodeDeviceStore) GetOrCreateClaudeCodeDeviceID(_ context.Context, accountID int64, candidate string) (string, error) {
	s.candidates = append(s.candidates, candidate)
	if s.err != nil {
		return "", s.err
	}
	if s.stored == nil {
		s.stored = map[int64]string{}
	}
	if _, ok := s.stored[accountID]; !ok {
		s.stored[accountID] = candidate
	}
	return s.stored[accountID], nil
}

func anthropicOAuthDeviceAccount() *Account {
	return &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
}

func TestGetOrCreateFingerprintAdoptsCachedDeviceOnFirstPersistence(t *testing.T) {
	cache := &stubIdentityCache{fingerprint: &Fingerprint{ClientID: cachedClaudeCodeDeviceID}}
	store := &stubClaudeCodeDeviceStore{}

	fp, err := NewIdentityService(cache, store).GetOrCreateFingerprint(context.Background(), anthropicOAuthDeviceAccount(), nil)
	require.NoError(t, err)
	require.Equal(t, cachedClaudeCodeDeviceID, fp.ClientID)
	require.Equal(t, []string{cachedClaudeCodeDeviceID}, store.candidates)
	require.Equal(t, cachedClaudeCodeDeviceID, store.stored[7])
}

func TestGetOrCreateFingerprintKeepsStoredDeviceAcrossCacheLossAndRestart(t *testing.T) {
	store := &stubClaudeCodeDeviceStore{stored: map[int64]string{7: storedClaudeCodeDeviceID}}

	// Cache eviction: the stored device is served and written back to the cache.
	cache := &stubIdentityCache{}
	fp, err := NewIdentityService(cache, store).GetOrCreateFingerprint(context.Background(), anthropicOAuthDeviceAccount(), nil)
	require.NoError(t, err)
	require.Equal(t, storedClaudeCodeDeviceID, fp.ClientID)
	require.Equal(t, storedClaudeCodeDeviceID, cache.lastSet.ClientID)

	// A restarted replica whose cache holds another device still uses the stored one.
	cache = &stubIdentityCache{fingerprint: &Fingerprint{ClientID: cachedClaudeCodeDeviceID, UpdatedAt: time.Now().Unix()}}
	fp, err = NewIdentityService(cache, store).GetOrCreateFingerprint(context.Background(), anthropicOAuthDeviceAccount(), nil)
	require.NoError(t, err)
	require.Equal(t, storedClaudeCodeDeviceID, fp.ClientID)
	require.Equal(t, storedClaudeCodeDeviceID, cache.lastSet.ClientID)
}

func TestGetOrCreateFingerprintReadsDeviceStoreOncePerAccount(t *testing.T) {
	store := &stubClaudeCodeDeviceStore{}
	svc := NewIdentityService(&stubIdentityCache{}, store)

	for i := 0; i < 3; i++ {
		fp, err := svc.GetOrCreateFingerprint(context.Background(), anthropicOAuthDeviceAccount(), nil)
		require.NoError(t, err)
		require.Equal(t, store.stored[7], fp.ClientID)
	}
	require.Len(t, store.candidates, 1)
}

func TestGetOrCreateFingerprintServesCachedDeviceWhileStoreFails(t *testing.T) {
	cache := &stubIdentityCache{fingerprint: &Fingerprint{ClientID: cachedClaudeCodeDeviceID}}
	store := &stubClaudeCodeDeviceStore{err: errors.New("database unavailable")}
	svc := NewIdentityService(cache, store)

	for i := 0; i < 2; i++ {
		fp, err := svc.GetOrCreateFingerprint(context.Background(), anthropicOAuthDeviceAccount(), nil)
		require.NoError(t, err)
		require.Equal(t, cachedClaudeCodeDeviceID, fp.ClientID)
	}
	require.Len(t, store.candidates, 2, "store failures must not be memoized")

	store.err = nil
	fp, err := svc.GetOrCreateFingerprint(context.Background(), anthropicOAuthDeviceAccount(), nil)
	require.NoError(t, err)
	require.Equal(t, cachedClaudeCodeDeviceID, fp.ClientID)
	require.Equal(t, cachedClaudeCodeDeviceID, store.stored[7])
}

func TestGetOrCreateFingerprintReplacesMalformedCachedDevice(t *testing.T) {
	cache := &stubIdentityCache{fingerprint: &Fingerprint{ClientID: "legacy-device"}}
	store := &stubClaudeCodeDeviceStore{}

	fp, err := NewIdentityService(cache, store).GetOrCreateFingerprint(context.Background(), anthropicOAuthDeviceAccount(), nil)
	require.NoError(t, err)
	require.Len(t, store.candidates, 1)
	require.NotEqual(t, "legacy-device", store.candidates[0])
	require.True(t, isClaudeCodeDeviceID(fp.ClientID))
	require.Equal(t, store.stored[7], fp.ClientID)
}

func TestGetOrCreateFingerprintPersistsOnlyAnthropicOAuthAndSetupToken(t *testing.T) {
	for _, tc := range []struct {
		name    string
		account *Account
		persist bool
	}{
		{name: "oauth", account: &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}, persist: true},
		{name: "setup_token", account: &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeSetupToken}, persist: true},
		{name: "api_key", account: &Account{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}},
		{name: "other_platform_oauth", account: &Account{ID: 4, Platform: PlatformGemini, Type: AccountTypeOAuth}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := &stubIdentityCache{fingerprint: &Fingerprint{ClientID: cachedClaudeCodeDeviceID}}
			store := &stubClaudeCodeDeviceStore{stored: map[int64]string{tc.account.ID: storedClaudeCodeDeviceID}}

			fp, err := NewIdentityService(cache, store).GetOrCreateFingerprint(context.Background(), tc.account, nil)
			require.NoError(t, err)
			if tc.persist {
				require.Equal(t, storedClaudeCodeDeviceID, fp.ClientID)
				require.Len(t, store.candidates, 1)
				return
			}
			require.Equal(t, cachedClaudeCodeDeviceID, fp.ClientID)
			require.Empty(t, store.candidates)
		})
	}
}

func TestGetOrCreateFingerprintAppliesDeviceOverrideWithoutPersistingIt(t *testing.T) {
	cache := &stubIdentityCache{}
	store := &stubClaudeCodeDeviceStore{stored: map[int64]string{7: storedClaudeCodeDeviceID}}
	svc := NewIdentityService(cache, store)
	account := anthropicOAuthDeviceAccount()
	account.Credentials = map[string]any{"claude_user_id": overrideClaudeCodeDeviceID}

	fp, err := svc.GetOrCreateFingerprint(context.Background(), account, nil)
	require.NoError(t, err)
	require.Equal(t, overrideClaudeCodeDeviceID, fp.ClientID)
	require.Equal(t, storedClaudeCodeDeviceID, cache.lastSet.ClientID, "the cache keeps the persisted device")
	require.Equal(t, storedClaudeCodeDeviceID, store.stored[7])

	account.Credentials = map[string]any{}
	fp, err = svc.GetOrCreateFingerprint(context.Background(), account, nil)
	require.NoError(t, err)
	require.Equal(t, storedClaudeCodeDeviceID, fp.ClientID, "clearing the override restores the persisted device")
}

func TestClaudeCodeOutboundPathsUseResolvedDevice(t *testing.T) {
	for _, tc := range []struct {
		name        string
		credentials map[string]any
		want        string
	}{
		{name: "persisted", want: storedClaudeCodeDeviceID},
		{name: "account_override", credentials: map[string]any{"claude_user_id": overrideClaudeCodeDeviceID}, want: overrideClaudeCodeDeviceID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertClaudeCodeOutboundPathsUseDevice(t, tc.credentials, tc.want)
		})
	}
}

func assertClaudeCodeOutboundPathsUseDevice(t *testing.T, credentials map[string]any, want string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	account := &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: credentials, Extra: map[string]any{
		"account_uuid": "7d0c7a52-9a8b-4c1e-8f4b-1c2d3e4f5a6b",
	}}
	clientUserID := FormatMetadataUserID(cachedClaudeCodeDeviceID, "", "7578cf37-aaca-46e4-a45c-71285d9dbb83", "2.1.78")
	withMetadata := []byte(`{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hello"}],"metadata":{"user_id":` + strconvQuote(clientUserID) + `}}`)
	withoutMetadata := []byte(`{"model":"claude-haiku-4-5","system":"instructions","messages":[{"role":"user","content":"hello"}]}`)

	newService := func() *GatewayService {
		cache := &stubIdentityCache{fingerprint: &Fingerprint{ClientID: cachedClaudeCodeDeviceID}}
		store := &stubClaudeCodeDeviceStore{stored: map[int64]string{account.ID: storedClaudeCodeDeviceID}}
		return &GatewayService{cfg: &config.Config{}, identityService: NewIdentityService(cache, store)}
	}
	newContext := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		return c
	}
	deviceOf := func(t *testing.T, body []byte) string {
		t.Helper()
		parsed := ParseMetadataUserID(gjson.GetBytes(body, "metadata.user_id").String())
		require.NotNil(t, parsed)
		return parsed.DeviceID
	}

	_, wireBody, err := newService().buildUpstreamRequest(context.Background(), newContext(), account,
		withMetadata, "test-token", "oauth", "claude-haiku-4-5", false, false)
	require.NoError(t, err)
	require.Equal(t, want, deviceOf(t, wireBody), "messages")

	_, wireBody, err = newService().buildCountTokensRequest(context.Background(), newContext(), account,
		withMetadata, "test-token", "oauth", "claude-haiku-4-5", false)
	require.NoError(t, err)
	require.Equal(t, want, deviceOf(t, wireBody), "count_tokens")

	out := newService().applyClaudeCodeOAuthMimicryToBody(context.Background(), newContext(), account,
		withoutMetadata, "instructions", "claude-haiku-4-5")
	require.Equal(t, want, deviceOf(t, out), "chat-completions and responses mimicry")
}
