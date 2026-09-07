package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

type routeScopeProvider struct{ provider string }

func (p routeScopeProvider) Identifier() string { return p.provider }
func (p routeScopeProvider) Authenticate(context.Context, *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	return &sdkaccess.Result{Provider: p.provider, Principal: "test-principal"}, nil
}

func TestUserRouteScopePreservesLegacyAndCoversSupportedProtocols(t *testing.T) {
	for _, test := range []struct {
		method, path string
		allowed      bool
	}{
		{http.MethodPost, "/v1/responses", true},
		{http.MethodGet, "/v1/responses", true},
		{http.MethodPost, "/backend-api/codex/responses/compact", true},
		{http.MethodPost, "/v1/chat/completions", true},
		{http.MethodPost, "/v1/messages/count_tokens", true},
		{http.MethodGet, "/v1/models", true},
		{http.MethodGet, "/v1beta/models/gemini-test", true},
		{http.MethodPost, "/v1beta/models/gemini-test:generateContent", true},
		{http.MethodPost, "/v1beta/models/gemini-test:streamGenerateContent", true},
		{http.MethodPost, "/v1beta/models/gemini-test:countTokens", true},
		{http.MethodPost, "/v1beta/interactions", true},
		{http.MethodGet, "/v1/realtime", false},
		{http.MethodPost, "/v1/realtime/client_secrets", false},
		{http.MethodPost, "/v1/live", false},
		{http.MethodPost, "/v1/images/generations", false},
		{http.MethodPost, "/openai/v1/videos", false},
		{http.MethodPost, "/backend-api/codex/alpha/search", false},
		{http.MethodPost, "/v1beta/models/gemini-test:unknownAction", false},
		{http.MethodPost, "/plugin/custom", false},
	} {
		for _, provider := range []string{"user", "legacy"} {
			t.Run(provider+" "+test.method+" "+test.path, func(t *testing.T) {
				manager := sdkaccess.NewManager()
				manager.SetProviders([]sdkaccess.Provider{routeScopeProvider{provider: provider}})
				router := gin.New()
				router.Handle(test.method, test.path, AuthMiddleware(manager), func(c *gin.Context) { c.Status(http.StatusNoContent) })
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))
				want := http.StatusNoContent
				if provider == "user" && !test.allowed {
					want = http.StatusForbidden
				}
				if recorder.Code != want {
					t.Fatalf("route returned %d, want %d", recorder.Code, want)
				}
			})
		}
	}
}
