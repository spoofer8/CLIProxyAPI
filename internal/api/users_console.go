package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Keep old bookmarks working without serving a second management application.
func (s *Server) serveUsersConsole(c *gin.Context) {
	if !s.managementAvailable(c) || !s.userManagementActive() {
		if !c.IsAborted() {
			c.AbortWithStatus(http.StatusNotFound)
		}
		return
	}
	ip := c.ClientIP()
	if allowed, status, message := s.mgmt.CheckManagementAccess(ip, ip == "127.0.0.1" || ip == "::1"); !allowed {
		c.AbortWithStatusJSON(status, gin.H{"error": message})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Redirect(http.StatusFound, "/management.html#/users")
}
