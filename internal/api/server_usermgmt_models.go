package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	claudemodels "github.com/router-for-me/CLIProxyAPI/v7/internal/client/claude/models"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

type modelResponseBuffer struct {
	gin.ResponseWriter
	body   bytes.Buffer
	status int
}

func (w *modelResponseBuffer) WriteHeader(status int)               { w.status = status }
func (w *modelResponseBuffer) WriteHeaderNow()                      {}
func (w *modelResponseBuffer) Write(data []byte) (int, error)       { return w.body.Write(data) }
func (w *modelResponseBuffer) WriteString(data string) (int, error) { return w.body.WriteString(data) }
func (w *modelResponseBuffer) Status() int                          { return w.status }
func (w *modelResponseBuffer) Size() int                            { return w.body.Len() }
func (w *modelResponseBuffer) Written() bool                        { return w.body.Len() > 0 }

// Model catalogs have several protocol-specific shapes. Filter the generated
// response for this caller without modifying shared registry entries or templates.
func (s *Server) userManagementModelsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, ok := sdkaccess.ResultFromContext(c.Request.Context())
		if s.userManagement == nil || !ok || identity.Provider != "user" {
			c.Next()
			return
		}
		original := c.Writer
		buffer := &modelResponseBuffer{ResponseWriter: original, status: http.StatusOK}
		c.Writer = buffer
		defer func() { c.Writer = original }()
		c.Next()
		c.Writer = original
		if buffer.status != http.StatusOK {
			c.Data(buffer.status, original.Header().Get("Content-Type"), buffer.body.Bytes())
			return
		}
		var payload map[string]any
		decoder := json.NewDecoder(bytes.NewReader(buffer.body.Bytes()))
		decoder.UseNumber()
		if err := decoder.Decode(&payload); err != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		allowed := func(entry map[string]any) (bool, error) {
			model := catalogModelName(entry)
			if isAnthropicModelsRequest(c) {
				model = claudemodels.ResolveClaudeModelIDPrefix(model)
			}
			if model == "" {
				return false, nil
			}
			providers := registry.GetGlobalRegistry().GetModelProviders(model)
			if len(providers) == 0 {
				providers = registry.GetGlobalRegistry().GetModelProviders(thinking.ParseSuffix(model).ModelName)
			}
			return s.userModelAllowed(c.Request.Context(), model, providers)
		}
		listed := false
		for _, key := range []string{"data", "models"} {
			models, exists := payload[key].([]any)
			if !exists {
				continue
			}
			listed = true
			filtered := make([]any, 0, len(models))
			for _, raw := range models {
				entry, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				permitted, err := allowed(entry)
				if err != nil {
					writeModelPolicyError(c, err)
					return
				}
				if permitted {
					filtered = append(filtered, entry)
				}
			}
			payload[key] = filtered
			if _, exists := payload["first_id"]; exists {
				payload["first_id"], payload["last_id"] = "", ""
				if len(filtered) > 0 {
					payload["first_id"] = catalogModelName(filtered[0].(map[string]any))
					payload["last_id"] = catalogModelName(filtered[len(filtered)-1].(map[string]any))
				}
			}
		}
		if !listed {
			permitted, err := allowed(payload)
			if err != nil {
				writeModelPolicyError(c, err)
				return
			}
			if !permitted {
				c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"message": "Not Found", "type": "not_found"}})
				return
			}
		}
		original.Header().Del("Content-Length")
		c.JSON(http.StatusOK, payload)
	}
}

func (s *Server) userModelAllowed(ctx context.Context, model string, providers []string) (bool, error) {
	allowed, err := s.userManagement.ModelAllowed(ctx, model, providers)
	if err != nil || !allowed || s.handlers == nil || s.handlers.AuthManager == nil {
		return allowed, err
	}
	knownTarget := false
	for _, auth := range s.handlers.AuthManager.List() {
		if auth == nil || !registry.GetGlobalRegistry().ClientSupportsModel(auth.ID, model) {
			continue
		}
		for _, executionModel := range s.handlers.AuthManager.PreviewExecutionModels(auth, model) {
			knownTarget = true
			err := s.userManagement.CheckPermissions(ctx, sdkaccess.PolicyTarget{
				RequestedModel: model, ResolvedModel: model, ExecutionModel: executionModel, Provider: auth.Provider,
			})
			if err == nil {
				return true, nil
			}
			var status interface{ StatusCode() int }
			if !errors.As(err, &status) || status.StatusCode() != http.StatusForbidden {
				return false, err
			}
		}
	}
	return !knownTarget, nil
}

func catalogModelName(entry map[string]any) string {
	for _, key := range []string{"id", "slug", "name"} {
		if model, ok := entry[key].(string); ok && model != "" {
			if key == "name" {
				model = strings.TrimPrefix(model, "models/")
			}
			return model
		}
	}
	return ""
}

func writeModelPolicyError(c *gin.Context, err error) {
	var response interface {
		StatusCode() int
		ResponseBody() []byte
	}
	if errors.As(err, &response) {
		c.Data(response.StatusCode(), "application/json", response.ResponseBody())
		return
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"message": "Model permissions are temporarily unavailable", "type": "server_error"}})
}
