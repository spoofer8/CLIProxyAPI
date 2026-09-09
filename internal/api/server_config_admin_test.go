package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func TestConfiguredAdminProxyKeyNeverGrantsManagementAccess(t *testing.T) {
	dsn := userManagementHTTPTestDSN(t)
	const mainKey = "configured-admin-proxy-test-key"
	const managementKey = "separate-management-test-password"
	t.Setenv("MANAGEMENT_PASSWORD", managementKey)
	preserveConfigAccessProvider(t)
	var runtime usermgmt.Runtime
	t.Cleanup(func() { _ = runtime.Close() })
	settings := config.UserManagementConfig{Enabled: true, DSN: dsn, RequestActivity: config.UserManagementRequestActivityConfig{SpoolDirectory: t.TempDir()}}
	if err := runtime.Apply(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AuthDir: t.TempDir(), CommercialMode: true, UserManagement: settings}
	cfg.APIKeys = []string{mainKey}
	server := NewServer(cfg, nil, sdkaccess.NewManager(), "", WithUserManagement(&runtime))
	server.engine.GET("/configured-admin-extra-route", AuthMiddleware(server.accessManager), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	request := func(method, path, key, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		response := httptest.NewRecorder()
		server.engine.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("%s returned %d, want %d", path, response.Code, want)
		}
		return response
	}
	request(http.MethodGet, "/v1/models", mainKey, "", http.StatusOK)
	request(http.MethodGet, "/configured-admin-extra-route", mainKey, "", http.StatusNoContent)
	request(http.MethodGet, "/v0/management/users", mainKey, "", http.StatusUnauthorized)
	request(http.MethodPost, "/v0/management/login", "", `{"email":"`+store.SystemAdminEmail+`","password":"`+mainKey+`"}`, http.StatusUnauthorized)
	response := request(http.MethodGet, "/v0/management/users/"+store.SystemAdminUserID, managementKey, "", http.StatusOK)
	if strings.Contains(response.Body.String(), mainKey) || !strings.Contains(response.Body.String(), `"system_admin":true`) {
		t.Fatal("system Admin DTO leaked credential or lost its flag")
	}
}
