package management

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt"
	"golang.org/x/crypto/bcrypt"
)

func (h *Handler) SetUserManagement(runtime *usermgmt.Runtime) { h.userManagement = runtime }

func (h *Handler) managementAuthSettings() (bool, string, string, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	allowRemote, secret := h.allowRemoteOverride, ""
	if h.cfg != nil {
		allowRemote = allowRemote || h.cfg.RemoteManagement.AllowRemote
		secret = h.cfg.RemoteManagement.SecretKey
	}
	return allowRemote, secret, h.envSecret, h.localPassword
}

// CheckManagementAccess is shared by legacy-key authentication and named login.
// It intentionally does not require a shared secret to be configured.
func (h *Handler) CheckManagementAccess(ip string, local bool) (bool, int, string) {
	if h == nil {
		return false, http.StatusForbidden, "remote management disabled"
	}
	now := time.Now()
	h.attemptsMu.Lock()
	if attempt := h.failedAttempts[ip]; attempt != nil && !attempt.blockedUntil.IsZero() {
		if now.Before(attempt.blockedUntil) {
			remaining := attempt.blockedUntil.Sub(now).Round(time.Second)
			h.attemptsMu.Unlock()
			return false, http.StatusForbidden, fmt.Sprintf("IP banned due to too many failed attempts. Try again in %s", remaining)
		}
		attempt.count, attempt.blockedUntil = 0, time.Time{}
	}
	h.attemptsMu.Unlock()
	allowRemote, _, _, _ := h.managementAuthSettings()
	if !local && !allowRemote {
		return false, http.StatusForbidden, "remote management disabled"
	}
	return true, 0, ""
}

func (h *Handler) RecordAuthenticationFailure(ip string) {
	if h == nil {
		return
	}
	h.attemptsMu.Lock()
	defer h.attemptsMu.Unlock()
	if h.failedAttempts == nil {
		h.failedAttempts = make(map[string]*attemptInfo)
	}
	attempt := h.failedAttempts[ip]
	if attempt == nil {
		attempt = &attemptInfo{}
		h.failedAttempts[ip] = attempt
	}
	attempt.count++
	attempt.lastActivity = time.Now()
	if attempt.count >= 5 {
		attempt.blockedUntil = time.Now().Add(30 * time.Minute)
		attempt.count = 0
	}
}

func (h *Handler) ResetAuthenticationFailures(ip string) {
	if h == nil {
		return
	}
	h.attemptsMu.Lock()
	defer h.attemptsMu.Unlock()
	if attempt := h.failedAttempts[ip]; attempt != nil {
		attempt.count, attempt.blockedUntil = 0, time.Time{}
	}
}

func (h *Handler) hasLegacyManagementSecret() bool {
	_, secret, env, _ := h.managementAuthSettings()
	return secret != "" || env != ""
}

func (h *Handler) validManagementKey(provided string, local bool) bool {
	if h == nil || provided == "" || provided == usermgmt.SessionMarker {
		return false
	}
	_, secret, env, password := h.managementAuthSettings()
	if secret == "" && env == "" {
		return false
	}
	if local && password != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(password)) == 1 {
		return true
	}
	if env != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(env)) == 1 {
		return true
	}
	return secret != "" && bcrypt.CompareHashAndPassword([]byte(secret), []byte(provided)) == nil
}

func managementCredential(c *gin.Context) string {
	provided := c.GetHeader("Authorization")
	if parts := strings.SplitN(provided, " ", 2); len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
		provided = parts[1]
	}
	if provided == "" {
		provided = c.GetHeader("X-Management-Key")
	}
	return provided
}

func (h *Handler) managementMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-CPA-VERSION", buildinfo.Version)
		c.Header("X-CPA-COMMIT", buildinfo.Commit)
		c.Header("X-CPA-BUILD-DATE", buildinfo.BuildDate)
		c.Header("X-CPA-SUPPORT-PLUGIN", pluginhost.SupportPluginHeaderValue())
		ip := c.ClientIP()
		local := ip == "127.0.0.1" || ip == "::1"
		if allowed, status, message := h.CheckManagementAccess(ip, local); !allowed {
			c.AbortWithStatusJSON(status, gin.H{"error": message})
			return
		}
		provided := managementCredential(c)
		accept := func(actor string) {
			h.ResetAuthenticationFailures(ip)
			c.Request = c.Request.WithContext(usermgmt.WithAuditActor(c.Request.Context(), usermgmt.AuditActor{ID: actor, ClientIP: ip}))
			c.Next()
		}
		// An explicit verified legacy key is a DB-independent break-glass path.
		if h.validManagementKey(provided, local) {
			accept("legacy-admin-key")
			return
		}
		cookie, cookieError := c.Request.Cookie(usermgmt.SessionCookieName)
		if h.userManagement != nil && cookieError == nil && cookie.Value != "" {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
			identity, err := h.userManagement.AuthenticateSession(ctx, cookie.Value)
			cancel()
			if err == nil && identity != nil && identity.Role == "admin" {
				if isManagementWrite(c.Request.Method) && !SameOriginRequest(c.Request) {
					c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "same-origin request required"})
					return
				}
				accept(identity.UserID)
				return
			}
			if err != nil && !errors.Is(err, usermgmt.ErrInvalidSession) {
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "Panel session is temporarily unavailable"})
				return
			}
		}
		if provided == usermgmt.SessionMarker || (provided == "" && cookieError == nil) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired panel session"})
			return
		}
		if h.hasLegacyManagementSecret() || h.userManagementActive() {
			h.RecordAuthenticationFailure(ip)
			message := "invalid management key"
			if provided == "" {
				message = "missing management key"
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": message})
			return
		}
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "remote management key not set"})
	}
}

func (h *Handler) userManagementActive() bool {
	if h.userManagement == nil {
		return false
	}
	active, _ := h.userManagement.Snapshot()
	return active != nil
}

func isManagementWrite(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

// SameOriginRequest accepts CLI clients without browser origin metadata, while
// rejecting cross-origin browser cookie writes and login/logout submissions.
func SameOriginRequest(request *http.Request) bool {
	if strings.EqualFold(request.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	value := request.Header.Get("Origin")
	if value == "" {
		value = request.Header.Get("Referer")
	}
	if value == "" {
		return true
	}
	origin, err := url.Parse(value)
	if err != nil || origin.User != nil || origin.Host == "" {
		return false
	}
	scheme := "http"
	// The shared listener exposes ConnectionState even for cleartext sockets;
	// Go may therefore provide a nonnil but empty TLS state on plain HTTP.
	if request.TLS != nil && request.TLS.Version != 0 {
		scheme = "https"
	}
	return origin.Scheme == scheme && strings.EqualFold(origin.Host, request.Host)
}
