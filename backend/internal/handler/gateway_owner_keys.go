package handler

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LuckyKuang/sub2api-plus/internal/pkg/pagination"
	middleware2 "github.com/LuckyKuang/sub2api-plus/internal/server/middleware"
	"github.com/LuckyKuang/sub2api-plus/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	ownerKeysTokenHeader   = "X-Auth-Token"
	ownerKeysTimestampSkew = 5 * time.Minute
	ownerKeysPageSize      = 1000
)

// MyKeys returns hashed metadata for every API key owned by the caller's user.
// GET /v1/me/keys?timestamp=<unix seconds>&email_hash=<sha256 hex>
func (h *GatewayHandler) MyKeys(c *gin.Context) {
	c.Header("Cache-Control", "no-store")

	if _, ok := middleware2.GetAPIKeyFromContext(c); !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	secret := ""
	if h.cfg != nil {
		secret = h.cfg.Gateway.OwnerKeysTokenSecret
	}
	if secret == "" {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Not found")
		return
	}

	rawTimestamp := strings.TrimSpace(c.Query("timestamp"))
	timestamp, err := strconv.ParseInt(rawTimestamp, 10, 64)
	if err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "timestamp must be unix seconds")
		return
	}
	if skew := time.Since(time.Unix(timestamp, 0)); skew > ownerKeysTimestampSkew || skew < -ownerKeysTimestampSkew {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "timestamp expired")
		return
	}
	if !ownerKeysTokenValid(secret, rawTimestamp, c.GetHeader(ownerKeysTokenHeader)) {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid auth token")
		return
	}

	emailHash := strings.ToLower(strings.TrimSpace(c.Query("email_hash")))
	if emailHash == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "email_hash is required")
		return
	}

	ctx := c.Request.Context()
	user, err := h.userService.GetByID(ctx, subject.UserID)
	if err != nil {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "Failed to get user info")
		return
	}
	if subtle.ConstantTimeCompare([]byte(emailHash), []byte(sha256Hex(strings.ToLower(strings.TrimSpace(user.Email))))) != 1 {
		h.errorResponse(c, http.StatusForbidden, "permission_error", "email_hash does not match")
		return
	}

	keys, err := h.listAllUserAPIKeys(c, user.ID)
	if err != nil {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "Failed to list API keys")
		return
	}

	out := make([]gin.H, 0, len(keys))
	for i := range keys {
		k := &keys[i]
		item := gin.H{
			"id":           k.ID,
			"name":         k.Name,
			"status":       k.Status,
			"group_id":     k.GroupID,
			"key_hash":     sha256Hex(k.Key),
			"created_at":   k.CreatedAt,
			"last_used_at": k.LastUsedAt,
			"expires_at":   k.ExpiresAt,
		}
		if k.Group != nil {
			item["group_name"] = k.Group.Name
		}
		out = append(out, item)
	}

	c.JSON(http.StatusOK, gin.H{
		"user_id": user.ID,
		"keys":    out,
	})
}

func (h *GatewayHandler) listAllUserAPIKeys(c *gin.Context, userID int64) ([]service.APIKey, error) {
	var all []service.APIKey
	for page := 1; ; page++ {
		params := pagination.PaginationParams{Page: page, PageSize: ownerKeysPageSize, SortBy: "id", SortOrder: pagination.SortOrderAsc}
		keys, result, err := h.apiKeyService.List(c.Request.Context(), userID, params, service.APIKeyListFilters{})
		if err != nil {
			return nil, err
		}
		all = append(all, keys...)
		if len(keys) < ownerKeysPageSize || result == nil || int64(len(all)) >= result.Total {
			return all, nil
		}
	}
}

func ownerKeysTokenValid(secret, rawTimestamp, token string) bool {
	sum := sha256.Sum256([]byte(secret + rawTimestamp))
	expected := base64.StdEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(expected)) == 1
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
