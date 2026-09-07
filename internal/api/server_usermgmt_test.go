package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	configaccess "github.com/router-for-me/CLIProxyAPI/v7/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"golang.org/x/crypto/bcrypt"
)

func TestServerDisabledUserManagementPreservesNilManager(t *testing.T) {
	preserveConfigAccessProvider(t)
	var runtime usermgmt.Runtime
	cfg := &config.Config{AuthDir: t.TempDir(), CommercialMode: true}
	cfg.APIKeys = []string{"configured-but-no-manager"}
	server := NewServer(cfg, nil, nil, filepath.Join(t.TempDir(), "config.yaml"), WithUserManagement(&runtime))
	if server.accessManager == nil || len(server.accessManager.Providers()) != 0 {
		t.Fatal("disabled feature installed providers for a caller without an access manager")
	}
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("disabled feature changed nil-manager authentication behavior: status=%d", recorder.Code)
	}
}

func TestServerUserManagementNilManagerHotEnableAndDisable(t *testing.T) {
	dsn := userManagementHTTPTestDSN(t)
	preserveConfigAccessProvider(t)
	var runtime usermgmt.Runtime
	t.Cleanup(func() {
		if errClose := runtime.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	cfg := &config.Config{AuthDir: t.TempDir(), CommercialMode: true}
	cfg.APIKeys = []string{"legacy-test-key"}
	cfg.UserManagement.DSN = dsn
	server := NewServer(cfg, nil, nil, filepath.Join(t.TempDir(), "config.yaml"), WithUserManagement(&runtime))
	manager := server.accessManager
	request := func(key string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		recorder := httptest.NewRecorder()
		server.engine.ServeHTTP(recorder, req)
		if recorder.Code != want {
			t.Fatalf("models returned %d, want %d", recorder.Code, want)
		}
	}
	request("", http.StatusOK)
	enabled := *cfg
	enabled.UserManagement.Enabled = true
	if errApply := runtime.Apply(context.Background(), enabled.UserManagement); errApply != nil {
		t.Fatal(errApply)
	}
	server.UpdateClients(&enabled)
	request("", http.StatusUnauthorized)
	request("sk-cpa-unknown", http.StatusUnauthorized)
	request("legacy-test-key", http.StatusOK)
	if providers := manager.Providers(); len(providers) != 2 || providers[0].Identifier() != "user" {
		t.Fatal("active feature did not install user and legacy authentication")
	}
	if errApply := runtime.Apply(context.Background(), cfg.UserManagement); errApply != nil {
		t.Fatal(errApply)
	}
	server.UpdateClients(cfg)
	request("", http.StatusOK)
	if server.accessManager != manager || len(manager.Providers()) != 0 {
		t.Fatal("disable replaced the stable manager or failed to restore nil-caller bypass")
	}
}

func TestServerUserManagementHTTPAuthenticationAndReload(t *testing.T) {
	dsn := userManagementHTTPTestDSN(t)
	t.Setenv("MANAGEMENT_PASSWORD", "")
	const managementKey = "local-test-management-password"
	secretHash, errHash := bcrypt.GenerateFromPassword([]byte(managementKey), bcrypt.MinCost)
	if errHash != nil {
		t.Fatal(errHash)
	}
	var runtime usermgmt.Runtime
	t.Cleanup(func() {
		if errClose := runtime.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	manager := sdkaccess.NewManager()
	cfg := &config.Config{AuthDir: t.TempDir(), CommercialMode: true}
	cfg.APIKeys = []string{"legacy-test-key"}
	cfg.RemoteManagement.SecretKey = string(secretHash)
	cfg.UserManagement.DSN = dsn
	preserveConfigAccessProvider(t)
	server := NewServer(cfg, nil, manager, filepath.Join(t.TempDir(), "config.yaml"), WithUserManagement(&runtime),
		WithRouterConfigurator(func(engine *gin.Engine, base *handlers.BaseAPIHandler, _ *config.Config) {
			engine.GET("/test-user-identity", AuthMiddleware(manager), func(c *gin.Context) {
				ctx, cancel := base.GetContextWithCancel(nil, c, context.Background())
				defer cancel()
				// Execution identity must come from the authenticated snapshot even
				// if a later handler changes mutable Gin metadata.
				c.Set("userApiKey", "reused-gin-principal")
				identity, _ := sdkaccess.ResultFromContext(ctx)
				c.JSON(http.StatusOK, gin.H{"identity": identity, "usage_principal": helps.APIKeyFromContext(ctx)})
			})
		}))
	httpServer := httptest.NewServer(server.engine)
	t.Cleanup(httpServer.Close)
	request := func(method, path, key, body string, want int) map[string]json.RawMessage {
		t.Helper()
		req, errRequest := http.NewRequest(method, httpServer.URL+path, strings.NewReader(body))
		if errRequest != nil {
			t.Fatal(errRequest)
		}
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		req.Header.Set("Content-Type", "application/json")
		return userManagementHTTPResponse(t, httpServer.Client(), req, want)
	}
	request(http.MethodGet, "/v0/management/users", managementKey, "", http.StatusNotFound)
	request(http.MethodGet, "/v1/models", "legacy-test-key", "", http.StatusOK)

	enabled := *cfg
	enabled.UserManagement.Enabled = true
	if errApply := runtime.Apply(context.Background(), enabled.UserManagement); errApply != nil {
		t.Fatal(errApply)
	}
	server.UpdateClients(&enabled)
	user := request(http.MethodPost, "/v0/management/users", managementKey, `{"email":"http-test@example.test","display_name":"HTTP test","role":"user"}`, http.StatusCreated)
	var userID string
	if errDecode := json.Unmarshal(user["id"], &userID); errDecode != nil || userID == "" {
		t.Fatal("create user did not return an ID")
	}
	issueKey := func() (string, string) {
		t.Helper()
		issued := request(http.MethodPost, "/v0/management/users/"+userID+"/keys", managementKey, `{"label":"integration"}`, http.StatusCreated)
		var key string
		var apiKey struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(issued["key"], &key) != nil || json.Unmarshal(issued["api_key"], &apiKey) != nil || key == "" || apiKey.ID == "" {
			t.Fatal("key issuance did not return the one-time key and key ID")
		}
		return key, apiKey.ID
	}
	key, keyID := issueKey()
	for _, source := range []string{"Authorization", "X-Goog-Api-Key", "X-Api-Key", "key", "auth_token"} {
		t.Run(source, func(t *testing.T) {
			req, errRequest := http.NewRequest(http.MethodGet, httpServer.URL+"/v1/models", nil)
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			switch source {
			case "key", "auth_token":
				query := req.URL.Query()
				query.Set(source, key)
				req.URL.RawQuery = query.Encode()
			case "Authorization":
				req.Header.Set(source, "Bearer "+key)
			default:
				req.Header.Set(source, key)
			}
			userManagementHTTPResponse(t, httpServer.Client(), req, http.StatusOK)
		})
	}
	identityResponse := request(http.MethodGet, "/test-user-identity", key, "", http.StatusOK)
	var identity sdkaccess.Result
	var usagePrincipal string
	if json.Unmarshal(identityResponse["identity"], &identity) != nil || json.Unmarshal(identityResponse["usage_principal"], &usagePrincipal) != nil {
		t.Fatal("invalid identity response")
	}
	if identity.Provider != "user" || identity.Principal != userID || identity.Metadata["key_id"] != keyID || usagePrincipal != userID {
		t.Fatal("HTTP authentication did not propagate the user and key identity into execution usage")
	}
	request(http.MethodGet, "/v0/management/users", key, "", http.StatusUnauthorized)
	request(http.MethodGet, "/v1/models", "sk-cpa-unknown", "", http.StatusUnauthorized)

	// Replacing the plugin/config list must retain the manager-local user provider.
	manager.SetProviders(manager.ConfiguredProviders())
	reloaded := enabled
	reloaded.APIKeys = []string{"replacement-legacy-key"}
	server.UpdateClients(&reloaded)
	request(http.MethodGet, "/v1/models", key, "", http.StatusOK)
	request(http.MethodGet, "/v1/models", "replacement-legacy-key", "", http.StatusOK)
	request(http.MethodGet, "/v1/models", "legacy-test-key", "", http.StatusUnauthorized)
	for _, provider := range sdkaccess.RegisteredProviders() {
		if provider.Identifier() == "user" {
			t.Fatal("user provider leaked into the global registry")
		}
	}

	request(http.MethodDelete, "/v0/management/keys/"+keyID, managementKey, "", http.StatusOK)
	request(http.MethodGet, "/v1/models", key, "", http.StatusUnauthorized)
	key, _ = issueKey()
	request(http.MethodGet, "/v1/models", key, "", http.StatusOK)
	request(http.MethodPatch, "/v0/management/users/"+userID, managementKey, `{"status":"disabled"}`, http.StatusOK)
	request(http.MethodGet, "/v1/models", key, "", http.StatusUnauthorized)
	request(http.MethodPatch, "/v0/management/users/"+userID, managementKey, `{"status":"active"}`, http.StatusOK)
	request(http.MethodGet, "/v1/models", key, "", http.StatusOK)

	disabled := reloaded
	disabled.UserManagement.Enabled = false
	disabled.APIKeys = nil
	if errApply := runtime.Apply(context.Background(), disabled.UserManagement); errApply != nil {
		t.Fatal(errApply)
	}
	server.UpdateClients(&disabled)
	request(http.MethodGet, "/v0/management/users", managementKey, "", http.StatusNotFound)
	request(http.MethodGet, "/v1/models", "", "", http.StatusOK)
	if len(manager.Providers()) != 0 {
		t.Fatal("disabled runtime left an authentication provider installed")
	}
	if errApply := runtime.Apply(context.Background(), reloaded.UserManagement); errApply != nil {
		t.Fatal(errApply)
	}
	server.UpdateClients(&reloaded)
	request(http.MethodGet, "/v1/models", key, "", http.StatusOK)
	request(http.MethodDelete, "/v0/management/users/"+userID, managementKey, "", http.StatusOK)
	request(http.MethodGet, "/v1/models", key, "", http.StatusUnauthorized)
}

func userManagementHTTPResponse(t *testing.T, client *http.Client, request *http.Request, want int) map[string]json.RawMessage {
	t.Helper()
	response, errDo := client.Do(request)
	if errDo != nil {
		t.Fatal(errDo)
	}
	defer func() {
		if errClose := response.Body.Close(); errClose != nil {
			t.Error(errClose)
		}
	}()
	body, errRead := io.ReadAll(response.Body)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if response.StatusCode != want {
		t.Fatalf("%s %s returned %d, want %d", request.Method, request.URL.Path, response.StatusCode, want)
	}
	var decoded map[string]json.RawMessage
	if len(body) > 0 && json.Unmarshal(body, &decoded) != nil {
		t.Fatal("HTTP handler did not return a JSON object")
	}
	return decoded
}

func preserveConfigAccessProvider(t *testing.T) {
	t.Helper()
	var previous sdkaccess.Provider
	for _, provider := range sdkaccess.RegisteredProviders() {
		if provider.Identifier() == sdkaccess.DefaultAccessProviderName {
			previous = provider
		}
	}
	t.Cleanup(func() {
		configaccess.Register(nil)
		if previous != nil {
			sdkaccess.RegisterProvider(sdkaccess.AccessProviderTypeConfigAPIKey, previous)
		}
	})
}

func userManagementHTTPTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("CLIPROXY_USERMGMT_TEST_DSN")
	if dsn == "" {
		t.Skip("set CLIPROXY_USERMGMT_TEST_DSN to run PostgreSQL integration tests")
	}
	parsed, errParse := url.Parse(dsn)
	if errParse != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("test DSN must be a PostgreSQL URL")
	}
	db, errOpen := sql.Open("pgx", dsn)
	if errOpen != nil {
		t.Fatal("open test database failed")
	}
	schema := "cpa_http_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, errCreate := db.ExecContext(context.Background(), `CREATE SCHEMA "`+schema+`"`); errCreate != nil {
		_ = db.Close()
		t.Fatal("create isolated test schema failed")
	}
	t.Cleanup(func() {
		if _, errDrop := db.ExecContext(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`); errDrop != nil {
			t.Error("drop isolated test schema failed")
		}
		if errClose := db.Close(); errClose != nil {
			t.Error("close test database failed")
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
