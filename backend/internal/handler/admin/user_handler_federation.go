package admin

import (
	"strconv"

	"github.com/LuckyKuang/sub2api-plus/internal/pkg/response"

	"github.com/gin-gonic/gin"
)

type BumpFederationUsageWatermarkRequest struct {
	UsageSeq int64 `json:"usage_seq" binding:"required,min=1"`
}

// BumpFederationUsageWatermark advances federation_usage_watermark_seq to
// max(current, usage_seq). Pure data recording, naturally idempotent
// (monotonic max), so unlike UpdateBalance this does not need the admin
// idempotency-key middleware -- a duplicate or out-of-order call is already
// a safe no-op.
//
// POST /api/v1/admin/users/:id/federation-usage-watermark
func (h *UserHandler) BumpFederationUsageWatermark(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}

	var req BumpFederationUsageWatermarkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	if err := h.adminService.BumpFederationUsageWatermark(c.Request.Context(), userID, req.UsageSeq); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "watermark updated"})
}

type SetFederationPasswordHashRequest struct {
	PasswordHash string `json:"password_hash" binding:"required"`
}

// SetFederationPasswordHash stores a bcrypt hash pushed by the mainland
// federation-pusher so the user can log in here with their mainland
// password. 404 unless federation.accept_password_hash is enabled.
// Idempotent (writing the same hash again is a no-op), so it doesn't need
// the admin idempotency-key middleware.
//
// POST /api/v1/admin/users/:id/federation-password-hash
func (h *UserHandler) SetFederationPasswordHash(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}

	var req SetFederationPasswordHashRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: password_hash is required")
		return
	}

	if err := h.adminService.SetFederationPasswordHash(c.Request.Context(), userID, req.PasswordHash); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "password hash updated"})
}
