package usermgmt

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/oklog/ulid/v2"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	log "github.com/sirupsen/logrus"
)

// RegisterManagementRoutes mounts handlers under the caller's existing admin
// authentication middleware. Register once, including when currently disabled,
// so hot enable needs no live Gin router mutation.
func (r *Runtime) RegisterManagementRoutes(group *gin.RouterGroup) {
	if group == nil {
		return
	}
	group = group.Group("", func(c *gin.Context) {
		if active, _ := r.Snapshot(); active == nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		requestCtx, cancel := r.operationContext(c.Request.Context())
		defer cancel()
		c.Request = c.Request.WithContext(requestCtx)
		c.Header("Cache-Control", "no-store")
		c.Next()
	})
	group.GET("/users", r.listUsers)
	group.POST("/users", r.createUser)
	group.GET("/users/:id", r.getUser)
	group.PATCH("/users/:id", r.updateUser)
	group.DELETE("/users/:id", r.deleteUser)
	group.GET("/users/:id/keys", r.listKeys)
	group.POST("/users/:id/keys", r.createKey)
	group.DELETE("/keys/:keyId", r.revokeKey)
	group.GET("/usage", r.listUsage)
	group.GET("/users/:id/permissions", r.getPermissions)
	group.PUT("/users/:id/permissions", r.replacePermissions)
	group.POST("/users/:id/password", r.setPassword)
	group.GET("/audit", r.listAudit)
	group.GET("/users/:id/requests", r.listRequestActivity)
	group.GET("/requests/:id", r.getRequestActivity)
	r.registerFinancialRoutes(group)
	r.registerActivityContentRoutes(group)
}

func newID() (string, error) {
	id, errGenerate := ulid.New(ulid.Timestamp(time.Now()), rand.Reader)
	if errGenerate != nil {
		return "", errors.New("user management: generate identifier failed")
	}
	return id.String(), nil
}

func decodeRequest(c *gin.Context, target any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	decoder := json.NewDecoder(c.Request.Body)
	var raw json.RawMessage
	if errDecode := decoder.Decode(&raw); errDecode != nil {
		badRequest(c, "Invalid JSON request")
		return false
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		badRequest(c, "Request must be a JSON object")
		return false
	}
	var extra any
	if errTrailing := decoder.Decode(&extra); errTrailing != io.EOF {
		badRequest(c, "Request must contain exactly one JSON object")
		return false
	}
	objectDecoder := json.NewDecoder(bytes.NewReader(raw))
	objectDecoder.DisallowUnknownFields()
	if errDecode := objectDecoder.Decode(target); errDecode != nil {
		badRequest(c, "Invalid JSON request")
		return false
	}
	return true
}

func badRequest(c *gin.Context, message string) {
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": message})
}

func respondError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrSystemAdminProtected):
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "The system Admin account cannot be disabled, demoted, or deleted"})
	case errors.Is(err, ErrDisabled), errors.Is(err, store.ErrNotFound):
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "Resource not found"})
	case errors.Is(err, store.ErrConflict):
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "Resource already exists"})
	default:
		log.WithError(err).Error("user management request failed")
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "User management is temporarily unavailable"})
	}
}

func validText(value string, maxLength int) bool {
	return len(value) <= maxLength && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func parsePagination(c *gin.Context) (limit, offset int, ok bool) {
	limit, offset = 50, 0
	for _, parameter := range []struct {
		name string
		into *int
	}{
		{"limit", &limit}, {"offset", &offset},
	} {
		if values, present := c.Request.URL.Query()[parameter.name]; present {
			if len(values) != 1 {
				badRequest(c, "Invalid pagination")
				return 0, 0, false
			}
			parsed, errParse := strconv.Atoi(values[0])
			if errParse != nil {
				badRequest(c, "Invalid pagination")
				return 0, 0, false
			}
			*parameter.into = parsed
		}
	}
	if limit < 1 || limit > 200 || offset < 0 || offset > 1_000_000_000 {
		badRequest(c, "Pagination requires limit 1..200 and offset 0..1000000000")
		return 0, 0, false
	}
	return limit, offset, true
}

func pathID(c *gin.Context, name string) (string, bool) {
	id, errParse := ulid.ParseStrict(c.Param(name))
	if errParse != nil {
		badRequest(c, "Invalid resource identifier")
		return "", false
	}
	return id.String(), true
}

func (r *Runtime) createUser(c *gin.Context) {
	var input struct {
		Email             string `json:"email"`
		DisplayName       string `json:"display_name"`
		Role              string `json:"role"`
		Status            string `json:"status"`
		MonthlyTokenLimit *int64 `json:"monthly_token_limit"`
	}
	if !decodeRequest(c, &input) {
		return
	}
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	address, errEmail := mail.ParseAddress(input.Email)
	if errEmail != nil || address.Address != input.Email || !validText(input.Email, 320) {
		badRequest(c, "A valid email address is required")
		return
	}
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if !validText(input.DisplayName, 200) {
		badRequest(c, "display_name must be at most 200 bytes")
		return
	}
	if input.Role == "" {
		input.Role = "user"
	}
	if input.Status == "" {
		input.Status = "active"
	}
	if (input.Role != "admin" && input.Role != "user") || (input.Status != "active" && input.Status != "disabled") {
		badRequest(c, "Invalid account role or status")
		return
	}
	id, errID := newID()
	if errID != nil {
		respondError(c, errID)
		return
	}
	var user store.User
	errCreate := r.mutate(c.Request.Context(), AuditEvent{Action: "user.create", Target: id}, func(db *store.Store) error {
		var errStore error
		user, errStore = db.CreateUser(c.Request.Context(), store.User{
			ID: id, Email: input.Email, DisplayName: input.DisplayName, Role: input.Role,
			Status: input.Status, MonthlyTokenLimit: input.MonthlyTokenLimit,
		})
		return errStore
	})
	if errCreate != nil {
		respondError(c, errCreate)
		return
	}
	c.JSON(http.StatusCreated, user)
}

func (r *Runtime) listUsers(c *gin.Context) {
	limit, offset, ok := parsePagination(c)
	if !ok {
		return
	}
	var users []store.User
	var total int64
	errList := r.withStore(false, func(db *store.Store) error {
		var errStore error
		users, total, errStore = db.ListUsers(c.Request.Context(), limit, offset)
		return errStore
	})
	if errList != nil {
		respondError(c, errList)
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": users, "total": total, "limit": limit, "offset": offset})
}

func (r *Runtime) getUser(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var user store.User
	var monthly store.MonthlyUsage
	errGet := r.withStore(false, func(db *store.Store) error {
		var errStore error
		user, errStore = db.GetUser(c.Request.Context(), id)
		if errStore != nil {
			return errStore
		}
		monthly, errStore = db.GetMonthlyUsage(c.Request.Context(), id, time.Now().UTC().Format("2006-01"))
		return errStore
	})
	if errGet != nil {
		respondError(c, errGet)
		return
	}
	c.JSON(http.StatusOK, struct {
		store.User
		Usage store.MonthlyUsage `json:"usage"`
	}{User: user, Usage: monthly})
}

func (r *Runtime) updateUser(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var input struct {
		DisplayName       *string         `json:"display_name"`
		Role              *string         `json:"role"`
		Status            *string         `json:"status"`
		MonthlyTokenLimit json.RawMessage `json:"monthly_token_limit"`
	}
	if !decodeRequest(c, &input) {
		return
	}
	patch := store.UserPatch{DisplayName: input.DisplayName, Role: input.Role, Status: input.Status}
	if input.DisplayName != nil {
		*input.DisplayName = strings.TrimSpace(*input.DisplayName)
		if !validText(*input.DisplayName, 200) {
			badRequest(c, "display_name must be at most 200 bytes")
			return
		}
	}
	if input.Role != nil && *input.Role != "admin" && *input.Role != "user" {
		badRequest(c, "Invalid account role")
		return
	}
	if input.Status != nil && *input.Status != "active" && *input.Status != "disabled" {
		badRequest(c, "Invalid account status")
		return
	}
	if input.MonthlyTokenLimit != nil {
		patch.MonthlyTokenLimitSet = true
		if errLimit := json.Unmarshal(input.MonthlyTokenLimit, &patch.MonthlyTokenLimit); errLimit != nil {
			badRequest(c, "monthly_token_limit must be an integer or null")
			return
		}
	}
	if patch.DisplayName == nil && patch.Role == nil && patch.Status == nil && !patch.MonthlyTokenLimitSet {
		badRequest(c, "At least one account field is required")
		return
	}
	var user store.User
	action := "user.update"
	fields := make([]string, 0, 4)
	if patch.DisplayName != nil {
		fields = append(fields, "display_name")
	}
	if patch.Role != nil {
		fields = append(fields, "role")
	}
	if patch.Status != nil {
		fields = append(fields, "status")
		if *patch.Status == "disabled" {
			action = "user.disable"
		}
	}
	if patch.MonthlyTokenLimitSet {
		fields = append(fields, "monthly_token_limit")
		if len(fields) == 1 {
			action = "quota.update"
		}
	}
	errUpdate := r.mutate(c.Request.Context(), AuditEvent{Action: action, Target: id, Detail: map[string]any{"fields": fields}}, func(db *store.Store) error {
		var errStore error
		user, errStore = db.UpdateUser(c.Request.Context(), id, patch)
		return errStore
	})
	if errUpdate != nil {
		respondError(c, errUpdate)
		return
	}
	c.JSON(http.StatusOK, user)
}

func (r *Runtime) deleteUser(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	if errDelete := r.mutate(c.Request.Context(), AuditEvent{Action: "user.delete", Target: id}, func(db *store.Store) error {
		return db.DeleteUser(c.Request.Context(), id)
	}); errDelete != nil {
		respondError(c, errDelete)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func (r *Runtime) createKey(c *gin.Context) {
	userID, ok := pathID(c, "id")
	if !ok {
		return
	}
	var input struct {
		Label string `json:"label"`
	}
	if !decodeRequest(c, &input) {
		return
	}
	input.Label = strings.TrimSpace(input.Label)
	if !validText(input.Label, 200) {
		badRequest(c, "label must be at most 200 bytes")
		return
	}
	id, errID := newID()
	if errID != nil {
		respondError(c, errID)
		return
	}
	random := make([]byte, 32)
	if _, errRandom := rand.Read(random); errRandom != nil {
		respondError(c, errors.New("user management: generate API key failed"))
		return
	}
	plaintext := "sk-cpa-" + base64.RawURLEncoding.EncodeToString(random)
	digest := sha256.Sum256([]byte(plaintext))
	var key store.APIKey
	errCreate := r.mutate(c.Request.Context(), AuditEvent{Action: "key.create", Target: id, Detail: map[string]any{"key_prefix": plaintext[:15]}}, func(db *store.Store) error {
		var errStore error
		key, errStore = db.CreateKey(c.Request.Context(), store.APIKey{
			ID: id, UserID: userID, KeyPrefix: plaintext[:15], Label: input.Label,
		}, hex.EncodeToString(digest[:]))
		return errStore
	})
	if errCreate != nil {
		respondError(c, errCreate)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"key": plaintext, "api_key": key})
}

func (r *Runtime) listKeys(c *gin.Context) {
	userID, ok := pathID(c, "id")
	if !ok {
		return
	}
	limit, offset, ok := parsePagination(c)
	if !ok {
		return
	}
	var keys []store.APIKey
	var total int64
	errList := r.withStore(false, func(db *store.Store) error {
		var errStore error
		keys, total, errStore = db.ListKeys(c.Request.Context(), userID, limit, offset)
		return errStore
	})
	if errList != nil {
		respondError(c, errList)
		return
	}
	c.JSON(http.StatusOK, gin.H{"keys": keys, "total": total, "limit": limit, "offset": offset})
}

func (r *Runtime) revokeKey(c *gin.Context) {
	id, ok := pathID(c, "keyId")
	if !ok {
		return
	}
	if errRevoke := r.mutate(c.Request.Context(), AuditEvent{Action: "key.revoke", Target: id}, func(db *store.Store) error {
		_, errStore := db.RevokeKey(c.Request.Context(), id)
		return errStore
	}); errRevoke != nil {
		respondError(c, errRevoke)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "revoked"})
}
