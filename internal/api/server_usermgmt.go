package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

// New user keys cover the request paths whose execution and token accounting
// pass through the common handlers. Existing legacy credentials keep all routes.
func userManagementRouteSupported(method, path string) bool {
	if method == http.MethodGet {
		switch path {
		case "/v1/models", "/v1beta/models", "/v1/responses", "/backend-api/codex/responses":
			return true
		}
		if strings.HasPrefix(path, "/v1beta/models/") {
			model := strings.TrimPrefix(path, "/v1beta/models/")
			return model != "" && !strings.Contains(model, ":")
		}
	}
	if method != http.MethodPost {
		return false
	}
	switch path {
	case "/v1/chat/completions", "/v1/completions", "/v1/messages", "/v1/messages/count_tokens",
		"/v1/responses", "/v1/responses/compact", "/backend-api/codex/responses", "/backend-api/codex/responses/compact",
		"/v1beta/interactions":
		return true
	}
	if strings.HasPrefix(path, "/v1beta/models/") {
		model, action, ok := strings.Cut(strings.TrimPrefix(path, "/v1beta/models/"), ":")
		if !ok || model == "" {
			return false
		}
		return action == "generateContent" || action == "streamGenerateContent" || action == "countTokens"
	}
	return false
}

func (s *Server) userManagementMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, ok := sdkaccess.ResultFromContext(c.Request.Context())
		if s.userManagement == nil || !ok || identity.Provider != "user" {
			c.Next()
			return
		}
		hooks := sdkaccess.RequestHooks{
			Begin:           s.userManagement.BeginRequest,
			Check:           s.userManagement.CheckRequest,
			Authorize:       s.userManagement.CheckPermissions,
			FilterProviders: s.userManagement.FilterProviders,
		}
		s.attachUserRequestCapture(c, &hooks)
		c.Request = c.Request.WithContext(sdkaccess.WithRequestHooks(c.Request.Context(), hooks))
		finishCapture := s.beginUserHTTPRequestCapture(c)
		defer finishCapture()
		if errCheck := s.userManagement.CheckRequest(c.Request.Context()); errCheck != nil {
			var response interface {
				StatusCode() int
				ResponseBody() []byte
			}
			if errors.As(errCheck, &response) {
				c.Data(response.StatusCode(), "application/json", response.ResponseBody())
			} else {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"message": "User account is temporarily unavailable", "type": "server_error", "code": "account_unavailable"}})
			}
			c.Abort()
			return
		}
		c.Next()
	}
}
