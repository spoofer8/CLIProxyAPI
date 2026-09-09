package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	management "github.com/router-for-me/CLIProxyAPI/v7/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt"
	log "github.com/sirupsen/logrus"
)

const invalidUserKeyAuditContext = "cpa_invalid_user_key_audit"

func (s *Server) userManagementActive() bool {
	if s.userManagement == nil {
		return false
	}
	active, _ := s.userManagement.Snapshot()
	return active != nil
}

func (s *Server) registerUserManagementAuthRoutes() {
	if s.userManagement == nil {
		return
	}
	available := func(c *gin.Context) {
		if !s.userManagementActive() || s.cfg == nil || s.cfg.Home.Enabled {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		ip := c.ClientIP()
		local := ip == "127.0.0.1" || ip == "::1"
		if allowed, status, message := s.mgmt.CheckManagementAccess(ip, local); !allowed {
			c.AbortWithStatusJSON(status, gin.H{"error": message})
			return
		}
		c.Next()
	}
	origin := func(c *gin.Context) {
		if !management.SameOriginRequest(c.Request) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "same-origin request required"})
			return
		}
		c.Next()
	}
	s.engine.GET("/login", available, s.userManagement.LoginPage)
	s.engine.GET("/users", s.serveUsersConsole)
	s.engine.POST("/v0/management/login", available, origin, s.userManagement.Login)
	s.engine.POST("/v0/management/logout", available, origin, s.userManagement.Logout)
}

func sourceAuditsManagementPath(path string) bool {
	for _, prefix := range []string{"/v0/management/users", "/v0/management/keys", "/v0/management/audit"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return path == "/v0/management/login" || path == "/v0/management/logout"
}

func (s *Server) userManagementAuditMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.userManagement == nil {
			c.Next()
			return
		}
		path := c.Request.URL.Path
		mutation := strings.HasPrefix(path, "/v0/management/") && c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead && c.Request.Method != http.MethodOptions && !sourceAuditsManagementPath(path)
		auditReady := false
		if mutation {
			if ctx, release, err := s.userManagement.BeginAudit(c.Request.Context()); err == nil {
				defer release()
				c.Request = c.Request.WithContext(ctx)
				auditReady = true
			}
		}
		c.Next()
		if failed, exists := c.Get(invalidUserKeyAuditContext); exists && failed == true {
			s.userManagement.RecordInvalidKey(c.ClientIP())
		}
		actor := usermgmt.AuditActorFromContext(c.Request.Context())
		if !auditReady || actor.ID == "" || c.Writer.Status() < 200 || c.Writer.Status() >= 300 {
			return
		}
		route := c.FullPath()
		if route == "" {
			route = "plugin-management"
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 2*time.Second)
		defer cancel()
		if err := s.userManagement.RecordAudit(ctx, usermgmt.AuditEvent{Action: "management." + strings.ToLower(c.Request.Method), Target: route, Detail: map[string]any{"method": c.Request.Method, "route": route, "status_code": c.Writer.Status()}}); err != nil {
			log.WithError(err).Warn("management mutation audit write failed")
		}
	}
}

func (s *Server) panelSessionAuthenticated(c *gin.Context) bool {
	if !s.userManagementActive() || s.mgmt == nil {
		return false
	}
	ip := c.ClientIP()
	local := ip == "127.0.0.1" || ip == "::1"
	if allowed, _, _ := s.mgmt.CheckManagementAccess(ip, local); !allowed {
		return false
	}
	cookie, err := c.Request.Cookie(usermgmt.SessionCookieName)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	identity, err := s.userManagement.AuthenticateSession(ctx, cookie.Value)
	return err == nil && identity != nil && identity.Role == "admin"
}
