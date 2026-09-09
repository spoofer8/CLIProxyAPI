package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"golang.org/x/crypto/bcrypt"
)

func TestNamedPanelSessionManagementAndAudit(t *testing.T) {
	spoolDirectory := t.TempDir()
	dsn := userManagementHTTPTestDSN(t)
	preserveConfigAccessProvider(t)
	t.Setenv("MANAGEMENT_PASSWORD", "")
	var runtime usermgmt.Runtime
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	const legacy, password, email = "session-test-legacy-secret", "SESSION-PASSWORD-DO-NOT-AUDIT", "session-admin@example.test"
	hash, err := bcrypt.GenerateFromPassword([]byte(legacy), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AuthDir: t.TempDir(), CommercialMode: true}
	cfg.RemoteManagement.SecretKey = string(hash)
	cfg.UserManagement = config.UserManagementConfig{Enabled: true, DSN: dsn, RequestActivity: config.UserManagementRequestActivityConfig{SpoolDirectory: spoolDirectory}}
	if err := runtime.Apply(context.Background(), cfg.UserManagement); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("debug: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{}, 1)
	server := NewServer(cfg, nil, sdkaccess.NewManager(), configPath, WithUserManagement(&runtime), WithMiddleware(func(c *gin.Context) {
		c.Next()
		if c.GetHeader("X-Test-Wait") == "yes" {
			finished <- struct{}{}
		}
	}))
	httpServer := httptest.NewServer(server.engine)
	t.Cleanup(httpServer.Close)
	type response struct {
		body    []byte
		cookies []*http.Cookie
		header  http.Header
	}
	request := func(method, path, key string, cookie *http.Cookie, origin, body string, want int) response {
		t.Helper()
		req, err := http.NewRequest(method, httpServer.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-Wait", "yes")
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := httpServer.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
		<-finished
		if resp.StatusCode != want {
			t.Fatalf("%s %s returned%d, want%d", method, path, resp.StatusCode, want)
		}
		return response{body: payload, cookies: resp.Cookies(), header: resp.Header.Clone()}
	}
	created := request(http.MethodPost, "/v0/management/users", legacy, nil, httpServer.URL, `{"email":"`+email+`","role":"admin","display_name":"Session admin"}`, http.StatusCreated)
	var admin struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(created.body, &admin) != nil || admin.ID == "" {
		t.Fatal("admin creation returned no ID")
	}
	passwordPath := "/v0/management/users/" + admin.ID + "/password"
	request(http.MethodPost, passwordPath, legacy, nil, httpServer.URL, `{"password":"`+password+`"}`, http.StatusOK)
	login := func() *http.Cookie {
		t.Helper()
		result := request(http.MethodPost, "/v0/management/login", "", nil, httpServer.URL, `{"email":"`+email+`","password":"`+password+`"}`, http.StatusOK)
		for _, cookie := range result.cookies {
			if cookie.Name == usermgmt.SessionCookieName {
				if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Secure {
					t.Fatal("session cookie flags changed")
				}
				return cookie
			}
		}
		t.Fatal("login returned no session cookie")
		return nil
	}
	cookie := login()
	request(http.MethodGet, "/v0/management/config", usermgmt.SessionMarker, cookie, "", "", http.StatusOK)
	request(http.MethodPut, "/v0/management/debug", usermgmt.SessionMarker, cookie, "http://evil.invalid", `{"value":true}`, http.StatusForbidden)
	request(http.MethodPut, "/v0/management/debug", "invalid-key", cookie, httpServer.URL, `{"value":true}`, http.StatusOK)
	request(http.MethodPut, "/v0/management/api-keys?secret=QUERY-DO-NOT-AUDIT", usermgmt.SessionMarker, cookie, httpServer.URL, `["KEY-DO-NOT-AUDIT"]`, http.StatusOK)
	db, _ := runtime.Snapshot()
	assertAudit := func(action, target, actor string, want int) {
		t.Helper()
		var count int
		if err := db.DB().QueryRowContext(context.Background(), `SELECT count(*) FROM cpa_audit_events WHERE action=$1 AND target=$2 AND actor=$3 AND client_ip='127.0.0.1'`, action, target, actor).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("audit %s target %s actor %s count=%d,want%d", action, target, actor, count, want)
		}
	}
	assertAudit("user.create", admin.ID, "legacy-admin-key", 1)
	assertAudit("password.update", admin.ID, "legacy-admin-key", 1)
	assertAudit("management.put", "/v0/management/debug", admin.ID, 1)
	assertAudit("management.put", "/v0/management/api-keys", admin.ID, 1)
	var details string
	if err := db.DB().QueryRowContext(context.Background(), `SELECT coalesce(string_agg(detail::text || action || target || actor,' '),'') FROM cpa_audit_events`).Scan(&details); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{password, legacy, cookie.Value, "KEY-DO-NOT-AUDIT", "QUERY-DO-NOT-AUDIT", string(hash)} {
		if strings.Contains(details, secret) {
			t.Fatal("audit serialized credential or request material")
		}
	}
	withoutSecret := *cfg
	withoutSecret.RemoteManagement.SecretKey = ""
	server.UpdateClients(&withoutSecret)
	request(http.MethodGet, "/v0/management/config", usermgmt.SessionMarker, cookie, "", "", http.StatusOK)
	server.UpdateClients(cfg)
	if _, err := db.DB().ExecContext(context.Background(), `UPDATE cpa_sessions SET expires_at=now()-interval '1 second' WHERE user_id=$1`, admin.ID); err != nil {
		t.Fatal(err)
	}
	for range 6 {
		request(http.MethodGet, "/v0/management/config", usermgmt.SessionMarker, cookie, "", "", http.StatusUnauthorized)
	}
	cookie = login() // Expired marker requests must not consume the shared ban budget.
	request(http.MethodPost, passwordPath, legacy, nil, httpServer.URL, `{"password":null}`, http.StatusOK)
	request(http.MethodGet, "/v0/management/config", usermgmt.SessionMarker, cookie, "", "", http.StatusUnauthorized)
	request(http.MethodPost, passwordPath, legacy, nil, httpServer.URL, `{"password":"`+password+`"}`, http.StatusOK)
	cookie = login()
	request(http.MethodPatch, "/v0/management/users/"+admin.ID, legacy, nil, httpServer.URL, `{"role":"user"}`, http.StatusOK)
	request(http.MethodGet, "/v0/management/config", usermgmt.SessionMarker, cookie, "", "", http.StatusUnauthorized)
	request(http.MethodPatch, "/v0/management/users/"+admin.ID, legacy, nil, httpServer.URL, `{"role":"admin"}`, http.StatusOK)
	request(http.MethodPost, passwordPath, legacy, nil, httpServer.URL, `{"password":"`+password+`"}`, http.StatusOK)
	cookie = login()
	request(http.MethodPatch, "/v0/management/users/"+admin.ID, legacy, nil, httpServer.URL, `{"status":"disabled"}`, http.StatusOK)
	request(http.MethodGet, "/v0/management/config", usermgmt.SessionMarker, cookie, "", "", http.StatusUnauthorized)
	request(http.MethodPatch, "/v0/management/users/"+admin.ID, legacy, nil, httpServer.URL, `{"status":"active"}`, http.StatusOK)
	cookie = login()
	request(http.MethodPost, "/v0/management/logout", "", cookie, "http://evil.invalid", `{}`, http.StatusForbidden)
	request(http.MethodGet, "/v0/management/config", usermgmt.SessionMarker, cookie, "", "", http.StatusOK)
	request(http.MethodPost, "/v0/management/logout", "", cookie, httpServer.URL, `{}`, http.StatusOK)
	request(http.MethodGet, "/v0/management/config", usermgmt.SessionMarker, cookie, "", "", http.StatusUnauthorized)
	request(http.MethodPost, "/v0/management/logout", "", cookie, httpServer.URL, `{}`, http.StatusOK)
	request(http.MethodGet, "/v0/management/config", legacy, cookie, "", "", http.StatusOK)
	assertAudit("session.logout", admin.ID, admin.ID, 1)
	request(http.MethodPost, "/v0/management/login", "", nil, "http://evil.invalid", `{"email":"`+email+`","password":"`+password+`"}`, http.StatusForbidden)
	for range 5 {
		request(http.MethodPost, "/v0/management/login", "", nil, httpServer.URL, `{"email":"`+email+`","password":"wrong-password"}`, http.StatusUnauthorized)
	}
	request(http.MethodGet, "/v0/management/config", legacy, nil, "", "", http.StatusForbidden)
	request(http.MethodPost, "/v0/management/login", "", nil, httpServer.URL, `{"email":"`+email+`","password":"`+password+`"}`, http.StatusForbidden)
	assertAudit("session.login_failed", admin.ID, "", 5)
}
