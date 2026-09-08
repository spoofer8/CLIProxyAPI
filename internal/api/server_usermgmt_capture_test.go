package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"golang.org/x/crypto/bcrypt"
)

const captureTestUserID = "01KKKKKKKKKKKKKKKKKKKKKKKK"
const captureTestKey = "sk-cpa-capture-test-credential"
const captureTestAdmin = "capture-management-test"

func newCaptureTestServer(t *testing.T, authManager *coreauth.Manager, options ...ServerOption) (*Server, *usermgmt.Runtime) {
	t.Helper()
	dsn := userManagementHTTPTestDSN(t)
	preserveConfigAccessProvider(t)
	t.Setenv("MANAGEMENT_PASSWORD", "")
	runtime := &usermgmt.Runtime{}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	cfg := &config.Config{AuthDir: t.TempDir(), CommercialMode: true}
	cfg.APIKeys = []string{"capture-legacy-key"}
	secret, err := bcrypt.GenerateFromPassword([]byte(captureTestAdmin), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RemoteManagement.SecretKey = string(secret)
	cfg.RemoteManagement.AllowRemote = true
	cfg.UserManagement = config.UserManagementConfig{Enabled: true, DSN: dsn}
	if err := runtime.Apply(context.Background(), cfg.UserManagement); err != nil {
		t.Fatal(err)
	}
	db, _ := runtime.Snapshot()
	if _, err := db.CreateUser(context.Background(), store.User{ID: captureTestUserID, Email: "capture@example.test", Role: "user", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(captureTestKey))
	if _, err := db.CreateKey(context.Background(), store.APIKey{ID: "01MMMMMMMMMMMMMMMMMMMMMMMM", UserID: captureTestUserID, KeyPrefix: captureTestKey[:12]}, hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}
	options = append(options, WithUserManagement(runtime))
	return NewServer(cfg, authManager, sdkaccess.NewManager(), filepath.Join(t.TempDir(), "config.yaml"), options...), runtime
}

func TestUserRequestCaptureOriginalBodyUsageAndAdminAccess(t *testing.T) {
	server, runtime := newCaptureTestServer(t, nil)
	engine := gin.New()
	var observed string
	engine.POST("/v1/chat/completions", AuthMiddleware(server.accessManager), server.userManagementMiddleware(), func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			t.Error(err)
		}
		observed = string(body)
		ctx, cancel := server.handlers.GetContextWithCancel(nil, c, context.Background())
		defer cancel()
		runtime.HandleUsage(ctx, usage.Record{APIKey: captureTestUserID, Provider: "capture-provider", RequestedAt: time.Now(), Detail: usage.Detail{InputTokens: 9, OutputTokens: 5, TotalTokens: 14}})
		c.JSON(200, gin.H{"ok": true})
	})
	body := `{"model":"capture-model","messages":[{"role":"user","content":"Hello <script>example</script>"}],"api_key":"body-secret","tools":[{"arguments":"{\"password\":\"nested-secret\",\"query\":\"keep this\"}"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?auth_token=query-secret", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+captureTestKey)
	req.Header.Set("Cookie", "cpa_session=cookie-secret")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	if recorder.Code != 200 || observed != body {
		t.Fatal("capture changed request behavior or content")
	}
	if err := runtime.FlushRequestActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	db, _ := runtime.Snapshot()
	items, err := db.ListRequestActivity(context.Background(), captureTestUserID, 20, 0, time.Now().Add(-time.Hour))
	if err != nil || len(items) != 1 {
		t.Fatalf("activity count=%d error=%v", len(items), err)
	}
	item, err := db.GetRequestActivity(context.Background(), items[0].ID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if item.Path != "/v1/chat/completions" || item.Model != "capture-model" || item.StatusCode != 200 || item.TotalTokens == nil || *item.TotalTokens != 14 || item.Provider == nil || *item.Provider != "capture-provider" {
		t.Fatalf("metadata/correlation mismatch: %+v", item.RequestActivitySummary)
	}
	for _, secret := range []string{captureTestKey, "query-secret", "cookie-secret", "body-secret", "nested-secret"} {
		encoded, _ := json.Marshal(item)
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("credential material retained")
		}
	}
	if !strings.Contains(item.BodyPreview, "keep this") || !strings.Contains(item.BodyPreview, "Hello") {
		t.Fatal("readable submitted content lost")
	}
	adminGet := func(path, key string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+key)
		result := httptest.NewRecorder()
		server.engine.ServeHTTP(result, request)
		return result
	}
	if got := adminGet("/v0/management/requests/"+item.ID, captureTestKey); got.Code != 401 && got.Code != 403 {
		t.Fatal("user key accessed private content")
	}
	if got := adminGet("/v0/management/requests/"+item.ID, captureTestAdmin); got.Code != 200 || !strings.Contains(got.Body.String(), "keep this") {
		t.Fatal("admin could not inspect content")
	}
	if got := adminGet("/v0/management/users/"+captureTestUserID+"/requests", captureTestAdmin); got.Code != 200 || strings.Contains(got.Body.String(), "body_preview") || strings.Contains(got.Body.String(), "keep this") {
		t.Fatal("summary endpoint includes request body")
	}
	if _, err := db.DB().Exec(`UPDATE cpa_request_activity SET at=now()-interval '8 days' WHERE id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	if got := adminGet("/v0/management/requests/"+item.ID, captureTestAdmin); got.Code != 404 {
		t.Fatal("expired content remained visible")
	}
	if count, err := db.DeleteExpiredRequestActivity(context.Background(), time.Now().Add(-7*24*time.Hour)); err != nil || count != 1 {
		t.Fatalf("retention deletion count=%d error=%v", count, err)
	}
}

func TestUserRequestCaptureEachOriginalWebsocketFrame(t *testing.T) {
	authManager := coreauth.NewManager(nil, nil, nil)
	authManager.RegisterExecutor(quotaHTTPExecutor{})
	const authID = "capture-ws-auth"
	if _, err := authManager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: "quota-test", Status: coreauth.StatusActive}); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "quota-test", []*registry.ModelInfo{{ID: "capture-ws-model", Object: "model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	done := make(chan struct{}, 1)
	server, runtime := newCaptureTestServer(t, authManager, WithMiddleware(func(c *gin.Context) {
		c.Next()
		if c.Request.URL.Path == "/v1/responses" {
			done <- struct{}{}
		}
	}))
	httpServer := httptest.NewServer(server.engine)
	t.Cleanup(httpServer.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/v1/responses", http.Header{"Authorization": []string{"Bearer " + captureTestKey}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, text := range []string{"first-original", "second-original"} {
		frame := []byte(`{"type":"response.create","model":"capture-ws-model","input":[{"role":"user","content":"` + text + `"}]}`)
		if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
			t.Fatal(err)
		}
		if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Fatal(err)
		}
	}
	_ = conn.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("WebSocket handler did not finish")
	}
	if err := runtime.FlushRequestActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	db, _ := runtime.Snapshot()
	items, err := db.ListRequestActivity(context.Background(), captureTestUserID, 20, 0, time.Now().Add(-time.Hour))
	if err != nil || len(items) != 2 {
		t.Fatalf("frame activity count=%d error=%v", len(items), err)
	}
	latest, err := db.GetRequestActivity(context.Background(), items[0].ID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if latest.Method != "WS" || latest.StatusCode != 200 || !strings.Contains(latest.BodyPreview, "second-original") || strings.Contains(latest.BodyPreview, "first-original") {
		t.Fatal("WebSocket content was normalized/combined or completion missing")
	}
}

func TestRequestCaptureReaderBoundAndPassThrough(t *testing.T) {
	input := strings.Repeat("x", usermgmt.MaxRequestCaptureInspectBytes+17)
	reader := &requestCaptureReader{ReadCloser: io.NopCloser(strings.NewReader(input))}
	output, err := io.ReadAll(reader)
	if err != nil || string(output) != input || len(reader.preview) != usermgmt.MaxRequestCaptureInspectBytes || !reader.overflow {
		t.Fatal("bounded capture altered stream or exceeded cap")
	}
}
