//go:build unit

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LuckyKuang/sub2api-plus/internal/server/middleware"
	"github.com/LuckyKuang/sub2api-plus/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayMeReturnsAPIKeyOwnerProfile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &userHandlerRepoStub{user: &service.User{
		ID:           42,
		Email:        "owner@example.com",
		Username:     "owner",
		PasswordHash: "secret-hash",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
		Balance:      12.5,
		Concurrency:  3,
	}}
	h := &GatewayHandler{userService: service.NewUserService(repo, nil, nil, nil)}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	groupID := int64(7)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID:      9,
		Key:     "sk-should-not-leak",
		Name:    "laptop",
		Status:  service.StatusAPIKeyActive,
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Name: "Pro"},
	})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})

	h.Me(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "sk-should-not-leak")
	require.NotContains(t, recorder.Body.String(), "secret-hash")

	var resp struct {
		UserID      int64   `json:"user_id"`
		Username    string  `json:"username"`
		Email       string  `json:"email"`
		Role        string  `json:"role"`
		Status      string  `json:"status"`
		Balance     float64 `json:"balance"`
		Concurrency int     `json:"concurrency"`
		APIKey      struct {
			ID        int64  `json:"id"`
			Name      string `json:"name"`
			Status    string `json:"status"`
			GroupID   *int64 `json:"group_id"`
			GroupName string `json:"group_name"`
		} `json:"api_key"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, int64(42), resp.UserID)
	require.Equal(t, "owner", resp.Username)
	require.Equal(t, "owner@example.com", resp.Email)
	require.Equal(t, service.RoleUser, resp.Role)
	require.Equal(t, service.StatusActive, resp.Status)
	require.Equal(t, 12.5, resp.Balance)
	require.Equal(t, 3, resp.Concurrency)
	require.Equal(t, int64(9), resp.APIKey.ID)
	require.Equal(t, "laptop", resp.APIKey.Name)
	require.Equal(t, service.StatusAPIKeyActive, resp.APIKey.Status)
	require.NotNil(t, resp.APIKey.GroupID)
	require.Equal(t, groupID, *resp.APIKey.GroupID)
	require.Equal(t, "Pro", resp.APIKey.GroupName)
}

func TestGatewayMeRejectsMissingAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/me", nil)

	(&GatewayHandler{}).Me(c)

	require.Equal(t, http.StatusUnauthorized, recorder.Code)
}
