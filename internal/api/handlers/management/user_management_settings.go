package management

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Settings deliberately exposes no DSN, credentials, or session configuration.
func (h *Handler) GetUserManagementSettings(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil || h.userManagement == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if active, _ := h.userManagement.Snapshot(); active == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	cfg := h.cfg.UserManagement.WithDefaults()
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"quota":            cfg.Quota,
		"request_activity": gin.H{"enabled": cfg.RequestActivity.CaptureEnabled(), "retention_days": cfg.RequestActivity.RetentionDays},
	})
}

// The shared management middleware authenticates and audits this mutation.
// Updating only quota fields preserves the DSN reference and all other settings.
func (h *Handler) PutUserManagementSettings(c *gin.Context) {
	var input struct {
		DefaultMonthlyTokens *int64 `json:"default_monthly_tokens"`
		Enforce              *bool  `json:"enforce"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(&input); errDecode != nil || input.DefaultMonthlyTokens == nil || input.Enforce == nil || *input.DefaultMonthlyTokens < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "default_monthly_tokens must be a non-negative integer and enforce must be a boolean"})
		return
	}
	var extra any
	if errTrailing := decoder.Decode(&extra); errTrailing != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Request must contain exactly one JSON object"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil || h.userManagement == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if active, _ := h.userManagement.Snapshot(); active == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	previous := h.cfg.UserManagement.Quota
	h.cfg.UserManagement.Quota.DefaultMonthlyTokens = *input.DefaultMonthlyTokens
	h.cfg.UserManagement.Quota.Enforce = *input.Enforce
	c.Header("Cache-Control", "no-store")
	if !h.persistLocked(c) {
		h.cfg.UserManagement.Quota = previous
	}
}
