package api

import (
	"embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed assets/users_console.html assets/users_console.css assets/users_console.js
var usersConsoleAssets embed.FS

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
	name, contentType := "users_console.html", "text/html; charset=utf-8"
	switch c.Param("asset") {
	case "":
	case "users_console.css":
		name, contentType = "users_console.css", "text/css; charset=utf-8"
	case "users_console.js":
		name, contentType = "users_console.js", "text/javascript; charset=utf-8"
	default:
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	body, err := usersConsoleAssets.ReadFile("assets/" + name)
	if err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Referrer-Policy", "same-origin")
	c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'none'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'self'")
	c.Data(http.StatusOK, contentType, body)
}
