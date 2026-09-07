package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"golang.org/x/crypto/bcrypt"
)

type quotaHTTPExecutor struct{ coreauth.ProviderExecutor }

func (quotaHTTPExecutor) Identifier() string { return "quota-test" }
func (quotaHTTPExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_quota_test\",\"output\":[]}}\n\n")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func TestUserQuotaHTTPAndEachResponsesWebsocketFrame(t *testing.T) {
	dsn := userManagementHTTPTestDSN(t)
	preserveConfigAccessProvider(t)
	var runtime usermgmt.Runtime
	t.Cleanup(func() {
		if errClose := runtime.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	cfg := &config.Config{AuthDir: t.TempDir(), CommercialMode: true}
	const managementKey = "local-quota-management-password"
	secretHash, errHash := bcrypt.GenerateFromPassword([]byte(managementKey), bcrypt.MinCost)
	if errHash != nil {
		t.Fatal(errHash)
	}
	cfg.RemoteManagement.SecretKey = string(secretHash)
	cfg.APIKeys = []string{"quota-legacy-key"}
	cfg.UserManagement = config.UserManagementConfig{Enabled: true, DSN: dsn}
	cfg.UserManagement.Quota.Enforce = true
	if errApply := runtime.Apply(context.Background(), cfg.UserManagement); errApply != nil {
		t.Fatal(errApply)
	}
	db, _ := runtime.Snapshot()
	limit := int64(10)
	user, errCreate := db.CreateUser(context.Background(), store.User{
		ID: "01KKKKKKKKKKKKKKKKKKKKKKKK", Email: "quota-http@example.test", Role: "user", Status: "active", MonthlyTokenLimit: &limit,
	})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	const key = "sk-cpa-local-quota-integration-secret"
	digest := sha256.Sum256([]byte(key))
	if _, errCreate := db.CreateKey(context.Background(), store.APIKey{ID: "01MMMMMMMMMMMMMMMMMMMMMMMM", UserID: user.ID, KeyPrefix: key[:12]}, hex.EncodeToString(digest[:])); errCreate != nil {
		t.Fatal(errCreate)
	}
	manager := sdkaccess.NewManager()
	authManager := coreauth.NewManager(nil, nil, nil)
	authManager.RegisterExecutor(quotaHTTPExecutor{})
	const authID = "quota-http-test-auth"
	if _, errRegister := authManager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: "quota-test", Status: coreauth.StatusActive}); errRegister != nil {
		t.Fatal(errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "quota-test", []*registry.ModelInfo{{ID: "quota-test-model", Object: "model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	websocketFinished := make(chan struct{}, 4)
	server := NewServer(cfg, authManager, manager, filepath.Join(t.TempDir(), "config.yaml"), WithUserManagement(&runtime), WithMiddleware(func(c *gin.Context) {
		c.Next()
		if c.Request.Method == http.MethodGet && c.Request.URL.Path == "/v1/responses" {
			websocketFinished <- struct{}{}
		}
	}))
	httpServer := httptest.NewServer(server.engine)
	t.Cleanup(httpServer.Close)
	request := func(method, path, token string, want int) {
		t.Helper()
		req, errRequest := http.NewRequest(method, httpServer.URL+path, strings.NewReader(`{"model":"unconfigured-model","input":[]}`))
		if errRequest != nil {
			t.Fatal(errRequest)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		response := userManagementHTTPResponse(t, httpServer.Client(), req, want)
		if want == http.StatusTooManyRequests && !strings.Contains(string(response["error"]), "quota_exceeded") {
			t.Fatal("quota rejection did not carry the OpenAI error code")
		}
	}
	request(http.MethodGet, "/v1/models", key, http.StatusOK)
	conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/v1/responses", http.Header{"Authorization": []string{"Bearer " + key}})
	if errDial != nil {
		t.Fatal(errDial)
	}
	t.Cleanup(func() { _ = conn.Close() })
	frame := []byte(`{"type":"response.create","model":"quota-test-model","input":[]}`)
	readFrame := func() []byte {
		t.Helper()
		if errDeadline := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); errDeadline != nil {
			t.Fatal(errDeadline)
		}
		if errWrite := conn.WriteMessage(websocket.TextMessage, frame); errWrite != nil {
			t.Fatal(errWrite)
		}
		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Fatal(errRead)
		}
		return payload
	}
	if payload := readFrame(); strings.Contains(string(payload), "quota_exceeded") {
		t.Fatal("under-limit WebSocket frame was rejected for quota")
	}
	authRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	authRequest.Header.Set("Authorization", "Bearer "+key)
	identity, errAuth := runtime.AccessProvider().Authenticate(context.Background(), authRequest)
	if errAuth != nil {
		t.Fatal(errAuth)
	}
	usageContext := sdkaccess.WithResult(context.Background(), identity)
	runtime.HandleUsage(usageContext, usage.Record{RequestedAt: time.Now(), Detail: usage.Detail{InputTokens: 4, OutputTokens: 6, TotalTokens: 10}})
	if errFlush := runtime.FlushUsage(context.Background()); errFlush != nil {
		t.Fatal(errFlush)
	}
	if payload := readFrame(); !strings.Contains(string(payload), "quota_exceeded") {
		t.Fatalf("later WebSocket frame bypassed quota admission: %s", payload)
	}
	if errClose := conn.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	<-websocketFinished
	request(http.MethodPost, "/v1/responses", key, http.StatusTooManyRequests)
	request(http.MethodPost, "/v1/messages/count_tokens", key, http.StatusTooManyRequests)
	request(http.MethodPost, "/v1beta/models/gemini-test:generateContent", key, http.StatusTooManyRequests)
	request(http.MethodPost, "/v1beta/interactions", key, http.StatusTooManyRequests)
	request(http.MethodPost, "/v1/images/generations", key, http.StatusForbidden)
	request(http.MethodPost, "/v1/realtime/client_secrets", key, http.StatusForbidden)
	request(http.MethodGet, "/v1/realtime", key, http.StatusForbidden)
	request(http.MethodGet, "/v1/models", "quota-legacy-key", http.StatusOK)
	unenforced := *cfg
	unenforced.UserManagement.Quota.Enforce = false
	if errApply := runtime.Apply(context.Background(), unenforced.UserManagement); errApply != nil {
		t.Fatal(errApply)
	}
	server.UpdateClients(&unenforced)
	request(http.MethodGet, "/v1/models", key, http.StatusOK)
	for _, test := range []struct{ name, keyID string }{
		{name: "revoked", keyID: "01NNNNNNNNNNNNNNNNNNNNNNNN"},
		{name: "disabled", keyID: "01PPPPPPPPPPPPPPPPPPPPPPPP"},
	} {
		t.Run(test.name+"-existing-socket-enforcement-disabled", func(t *testing.T) {
			userKey := "sk-cpa-socket-" + test.name + "-test-secret"
			digest := sha256.Sum256([]byte(userKey))
			if _, errCreate := db.CreateKey(context.Background(), store.APIKey{ID: test.keyID, UserID: user.ID, KeyPrefix: userKey[:12]}, hex.EncodeToString(digest[:])); errCreate != nil {
				t.Fatal(errCreate)
			}
			var errDial error
			conn, _, errDial = websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/v1/responses", http.Header{"Authorization": []string{"Bearer " + userKey}})
			if errDial != nil {
				t.Fatal(errDial)
			}
			if payload := readFrame(); !strings.Contains(string(payload), "response.completed") {
				t.Fatalf("first frame did not succeed: %s", payload)
			}
			method, path, body := http.MethodDelete, "/v0/management/keys/"+test.keyID, ""
			if test.name == "disabled" {
				method, path, body = http.MethodPatch, "/v0/management/users/"+user.ID, `{"status":"disabled"}`
			}
			req, errRequest := http.NewRequest(method, httpServer.URL+path, strings.NewReader(body))
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			req.Header.Set("Authorization", "Bearer "+managementKey)
			req.Header.Set("Content-Type", "application/json")
			userManagementHTTPResponse(t, httpServer.Client(), req, http.StatusOK)
			if payload := readFrame(); !strings.Contains(string(payload), "invalid_api_key") || !strings.Contains(string(payload), "401") {
				t.Fatalf("existing socket retained revoked/disabled access: %s", payload)
			}
			if errClose := conn.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			<-websocketFinished
		})
	}
}
