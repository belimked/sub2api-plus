//go:build unit

package handler

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/LuckyKuang/sub2api-plus/internal/config"
	"github.com/LuckyKuang/sub2api-plus/internal/pkg/pagination"
	"github.com/LuckyKuang/sub2api-plus/internal/server/middleware"
	"github.com/LuckyKuang/sub2api-plus/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const ownerKeysTestSecret = "belimed"

type ownerKeysAPIKeyRepoStub struct {
	service.APIKeyRepository
	keys       []service.APIKey
	listedUser int64
}

func (s *ownerKeysAPIKeyRepoStub) ListByUserID(_ context.Context, userID int64, params pagination.PaginationParams, _ service.APIKeyListFilters) ([]service.APIKey, *pagination.PaginationResult, error) {
	s.listedUser = userID
	start := min(params.Offset(), len(s.keys))
	end := min(start+params.Limit(), len(s.keys))
	return s.keys[start:end], &pagination.PaginationResult{Total: int64(len(s.keys))}, nil
}

func ownerKeysToken(secret, ts string) string {
	sum := sha256.Sum256([]byte(secret + ts))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func newOwnerKeysTestHandler(secret string, keys []service.APIKey) (*GatewayHandler, *ownerKeysAPIKeyRepoStub) {
	userRepo := &userHandlerRepoStub{user: &service.User{ID: 42, Email: "Owner@Example.com", Status: service.StatusActive}}
	keyRepo := &ownerKeysAPIKeyRepoStub{keys: keys}
	cfg := &config.Config{}
	cfg.Gateway.OwnerKeysTokenSecret = secret
	return &GatewayHandler{
		userService:   service.NewUserService(userRepo, nil, nil, nil),
		apiKeyService: service.NewAPIKeyService(keyRepo, nil, nil, nil, nil, nil, nil),
		cfg:           cfg,
	}, keyRepo
}

func serveOwnerKeys(h *GatewayHandler, ts, token, emailHash string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodGet, "/v1/me/keys?timestamp="+ts+"&email_hash="+emailHash, nil)
	if token != "" {
		req.Header.Set("X-Auth-Token", token)
	}
	c.Request = req
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 9, Key: "sk-current"})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})
	h.MyKeys(c)
	return recorder
}

func TestGatewayMyKeysReturnsHashedKeysWithStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(7)
	h, repo := newOwnerKeysTestHandler(ownerKeysTestSecret, []service.APIKey{
		{ID: 9, Key: "sk-current", Name: "laptop", Status: service.StatusAPIKeyActive, GroupID: &groupID, Group: &service.Group{ID: groupID, Name: "Pro"}},
		{ID: 10, Key: "sk-disabled", Name: "old", Status: service.StatusAPIKeyDisabled},
		{ID: 11, Key: "sk-expired", Name: "temp", Status: service.StatusAPIKeyExpired},
	})
	ts := strconv.FormatInt(time.Now().Unix(), 10)

	recorder := serveOwnerKeys(h, ts, ownerKeysToken(ownerKeysTestSecret, ts), sha256Hex("owner@example.com"))

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	for _, raw := range []string{"sk-current", "sk-disabled", "sk-expired"} {
		require.NotContains(t, recorder.Body.String(), raw)
	}
	require.Equal(t, int64(42), repo.listedUser)

	var resp struct {
		UserID int64 `json:"user_id"`
		Keys   []struct {
			ID        int64  `json:"id"`
			Name      string `json:"name"`
			Status    string `json:"status"`
			GroupName string `json:"group_name"`
			KeyHash   string `json:"key_hash"`
		} `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, int64(42), resp.UserID)
	require.Len(t, resp.Keys, 3)
	require.Equal(t, sha256Hex("sk-current"), resp.Keys[0].KeyHash)
	require.Equal(t, "Pro", resp.Keys[0].GroupName)
	require.Equal(t, service.StatusAPIKeyDisabled, resp.Keys[1].Status)
	require.Equal(t, service.StatusAPIKeyExpired, resp.Keys[2].Status)
}

func TestGatewayMyKeysRejectsInvalidRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := strconv.FormatInt(time.Now().Unix(), 10)
	stale := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	goodEmail := sha256Hex("owner@example.com")

	cases := []struct {
		name      string
		secret    string
		ts        string
		token     string
		emailHash string
		status    int
	}{
		{"disabled", "", now, ownerKeysToken("", now), goodEmail, http.StatusNotFound},
		{"bad timestamp", ownerKeysTestSecret, "abc", ownerKeysToken(ownerKeysTestSecret, "abc"), goodEmail, http.StatusBadRequest},
		{"stale timestamp", ownerKeysTestSecret, stale, ownerKeysToken(ownerKeysTestSecret, stale), goodEmail, http.StatusUnauthorized},
		{"missing token", ownerKeysTestSecret, now, "", goodEmail, http.StatusUnauthorized},
		{"wrong token", ownerKeysTestSecret, now, ownerKeysToken("other", now), goodEmail, http.StatusUnauthorized},
		{"missing email hash", ownerKeysTestSecret, now, ownerKeysToken(ownerKeysTestSecret, now), "", http.StatusBadRequest},
		{"wrong email hash", ownerKeysTestSecret, now, ownerKeysToken(ownerKeysTestSecret, now), sha256Hex("other@example.com"), http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, repo := newOwnerKeysTestHandler(tc.secret, []service.APIKey{{ID: 9, Key: "sk-current"}})
			recorder := serveOwnerKeys(h, tc.ts, tc.token, tc.emailHash)
			require.Equal(t, tc.status, recorder.Code, recorder.Body.String())
			require.Zero(t, repo.listedUser)
		})
	}
}
