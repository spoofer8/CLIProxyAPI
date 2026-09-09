package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	claudemodels "github.com/router-for-me/CLIProxyAPI/v7/internal/client/claude/models"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"golang.org/x/crypto/bcrypt"
)

type permissionHTTPExecutor struct {
	coreauth.ProviderExecutor
	provider string
	calls    atomic.Int64
	runtime  *usermgmt.Runtime
}

func (e *permissionHTTPExecutor) Identifier() string { return e.provider }
func (e *permissionHTTPExecutor) Execute(ctx context.Context, _ *coreauth.Auth, request coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls.Add(1)
	e.reportUsage(ctx, request.Model)
	return coreexecutor.Response{Payload: []byte(`{"id":"resp_policy","object":"response","output":[]}`)}, nil
}
func (e *permissionHTTPExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, request coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.calls.Add(1)
	e.reportUsage(ctx, request.Model)
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_policy\",\"output\":[]}}\n\n")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *permissionHTTPExecutor) reportUsage(ctx context.Context, model string) {
	if e.runtime == nil {
		return
	}
	db, _ := e.runtime.Snapshot()
	_ = db.SavePrice(context.Background(), store.ModelPrice{ID: "permission-fixture-" + e.provider + "-" + model, Provider: e.provider, Model: model, PriceRates: store.PriceRates{InputUSD: "1", OutputUSD: "1"}, Manual: true, UpdatedAt: time.Now().Add(-time.Minute)})
	e.runtime.HandleUsage(ctx, usage.Record{Provider: e.provider, Model: model, RequestedAt: time.Now(), Detail: usage.Detail{InputTokens: 1, OutputTokens: 1, TotalTokens: 2, TokenBreakdown: usage.NewSubsetTokenBreakdown(1, 0, 0, 1, 0, 2)}})
}

func TestUserPermissionsHTTPAliasesListingsAndLiveWebsocketUpdate(t *testing.T) {
	spoolDirectory := t.TempDir()
	dsn := userManagementHTTPTestDSN(t)
	preserveConfigAccessProvider(t)
	var runtime usermgmt.Runtime
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	const userID, keyID = "01QQQQQQQQQQQQQQQQQQQQQQQQ", "01RRRRRRRRRRRRRRRRRRRRRRRR"
	const userKey, adminKey = "sk-cpa-permission-http-test-secret", "permission-http-management-secret"
	const azure = "openai-compatible-azure-openai"
	hash, err := bcrypt.GenerateFromPassword([]byte(adminKey), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AuthDir: t.TempDir(), CommercialMode: true}
	cfg.APIKeys = []string{"permission-legacy-key"}
	cfg.RemoteManagement.SecretKey = string(hash)
	cfg.UserManagement = config.UserManagementConfig{Enabled: true, DSN: dsn, RequestActivity: config.UserManagementRequestActivityConfig{SpoolDirectory: spoolDirectory}}
	cfg.OpenAICompatibility = []config.OpenAICompatibility{{Name: "azure-openai", Models: []config.OpenAICompatibilityModel{{Name: "deployment-internal", Alias: "azure-visible"}}}}
	if err := runtime.Apply(context.Background(), cfg.UserManagement); err != nil {
		t.Fatal(err)
	}
	db, _ := runtime.Snapshot()
	if _, err := db.CreateUser(context.Background(), store.User{ID: userID, Email: "permissions@example.test", Role: "user", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(userKey))
	if _, err := db.CreateKey(context.Background(), store.APIKey{ID: keyID, UserID: userID, KeyPrefix: userKey[:12]}, hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}
	authManager := coreauth.NewManager(nil, nil, nil)
	authManager.SetConfig(cfg)
	allowed, denied := &permissionHTTPExecutor{provider: azure}, &permissionHTTPExecutor{provider: "other-provider"}
	for _, entry := range []struct {
		id, provider string
		models       []string
		executor     *permissionHTTPExecutor
	}{
		{"permission-azure-auth", azure, []string{"azure-visible"}, allowed},
		{"permission-other-auth", "other-provider", []string{"azure-visible", "claude-visible"}, denied},
		{"permission-gemini-auth", "gemini", []string{"team/gemini-visible"}, &permissionHTTPExecutor{provider: "gemini"}},
	} {
		entry.executor.runtime = &runtime
		authManager.RegisterExecutor(entry.executor)
		attributes := map[string]string{"api_key": "fake-upstream-key"}
		if entry.provider == azure {
			attributes["compat_name"], attributes["provider_key"] = "azure-openai", azure
		}
		if _, err := authManager.Register(context.Background(), &coreauth.Auth{ID: entry.id, Provider: entry.provider, Status: coreauth.StatusActive, Attributes: attributes}); err != nil {
			t.Fatal(err)
		}
		models := make([]*registry.ModelInfo, 0, len(entry.models))
		for _, model := range entry.models {
			models = append(models, &registry.ModelInfo{ID: model, Object: "model", OwnedBy: entry.provider})
		}
		registry.GetGlobalRegistry().RegisterClient(entry.id, entry.provider, models)
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(entry.id) })
	}
	wsFinished := make(chan struct{}, 1)
	server := NewServer(cfg, authManager, sdkaccess.NewManager(), filepath.Join(t.TempDir(), "config.yaml"), WithUserManagement(&runtime), WithMiddleware(func(c *gin.Context) {
		c.Next()
		if c.Request.Method == http.MethodGet && c.Request.URL.Path == "/v1/responses" {
			wsFinished <- struct{}{}
		}
	}))
	httpServer := httptest.NewServer(server.engine)
	t.Cleanup(httpServer.Close)
	request := func(method, path, key, body string, want int) map[string]json.RawMessage {
		t.Helper()
		req, err := http.NewRequest(method, httpServer.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		return userManagementHTTPResponse(t, httpServer.Client(), req, want)
	}
	put := func(rules string) {
		t.Helper()
		request(http.MethodPut, "/v0/management/users/"+userID+"/permissions", adminKey, `{"permissions":`+rules+`}`, http.StatusOK)
	}
	contains := func(payload map[string]json.RawMessage, name string) bool {
		raw, _ := json.Marshal(payload)
		return strings.Contains(string(raw), name)
	}
	initial := request(http.MethodGet, "/v1/models", userKey, "", http.StatusOK)
	if !contains(initial, "azure-visible") || !contains(initial, "claude-visible") {
		t.Fatal("empty policy changed the catalog")
	}
	allowAzure := `[{"scope":"model","value":"azure-*","effect":"allow"},{"scope":"provider","value":"azure-openai","effect":"allow"}]`
	put(allowAzure)
	for range 4 {
		request(http.MethodPost, "/v1/responses", userKey, `{"model":"azure-visible","input":[]}`, http.StatusOK)
	}
	if allowed.calls.Load() != 4 || denied.calls.Load() != 0 {
		t.Fatalf("mixed-provider alias used forbidden alternative: allowed=%d denied=%d", allowed.calls.Load(), denied.calls.Load())
	}
	filtered := request(http.MethodGet, "/v1/models", userKey, "", http.StatusOK)
	if !contains(filtered, "azure-visible") || contains(filtered, "claude-visible") {
		t.Fatal("model catalog did not match allow policy")
	}
	anthropicRequest, err := http.NewRequest(http.MethodGet, httpServer.URL+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	anthropicRequest.Header.Set("Authorization", "Bearer "+userKey)
	anthropicRequest.Header.Set("Anthropic-Version", "2023-06-01")
	anthropicCatalog := userManagementHTTPResponse(t, httpServer.Client(), anthropicRequest, http.StatusOK)
	if !contains(anthropicCatalog, claudemodels.EnsureClaudeModelIDPrefix("azure-visible")) || contains(anthropicCatalog, "claude-visible") {
		t.Fatal("Anthropic model cloaking changed permission meaning")
	}
	legacy := request(http.MethodGet, "/v1/models", "permission-legacy-key", "", http.StatusOK)
	if !contains(legacy, "claude-visible") {
		t.Fatal("user policy mutated legacy/global catalog")
	}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/v1/responses", http.Header{"Authorization": []string{"Bearer " + userKey}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	frame := func() []byte {
		t.Helper()
		if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"azure-visible","input":[]}`)); err != nil {
			t.Fatal(err)
		}
		_, body, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	if !strings.Contains(string(frame()), "response.completed") {
		t.Fatal("allowed websocket request failed")
	}
	put(`[{"scope":"model","value":"azure-*","effect":"allow"},{"scope":"model","value":"azure-visible","effect":"deny"}]`)
	if body := frame(); !strings.Contains(string(body), "model_not_permitted") || !strings.Contains(string(body), "403") {
		t.Fatalf("live policy update did not reject later frame: %s", body)
	}
	_ = conn.Close()
	<-wsFinished
	put(`[{"scope":"model","value":"azure-*","effect":"allow"},{"scope":"provider","value":"openai-compatible-azure-openai","effect":"allow"},{"scope":"model","value":"deployment-internal","effect":"deny"}]`)
	request(http.MethodPost, "/v1/responses", userKey, `{"model":"azure-visible(high)","input":[]}`, http.StatusForbidden)
	if catalog := request(http.MethodGet, "/v1/models", userKey, "", http.StatusOK); contains(catalog, "azure-visible") {
		t.Fatal("catalog advertised alias whose private deployment is denied")
	}
	put(`[{"scope":"model","value":"team/*","effect":"allow"}]`)
	if catalog := request(http.MethodGet, "/v1beta/models", userKey, "", http.StatusOK); !contains(catalog, "team/gemini-visible") || contains(catalog, "azure-visible") {
		t.Fatal("Gemini catalog policy mismatch")
	}
	request(http.MethodGet, "/v1beta/models/team/gemini-visible", userKey, "", http.StatusOK)
	request(http.MethodGet, "/v1beta/models/azure-visible", userKey, "", http.StatusNotFound)
	put(`[]`)
	reset := request(http.MethodGet, "/v1/models", userKey, "", http.StatusOK)
	if !contains(reset, "azure-visible") || !contains(reset, "claude-visible") {
		t.Fatal("empty policy reset did not restore catalog")
	}
}
