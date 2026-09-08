package usermgmt

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	"golang.org/x/crypto/bcrypt"
)

type testLoginGuard struct{ failures, resets int }

func (*testLoginGuard) CheckManagementAccess(string, bool) (bool, int, string) { return true, 0, "" }
func (g *testLoginGuard) RecordAuthenticationFailure(string)                   { g.failures++ }
func (g *testLoginGuard) ResetAuthenticationFailures(string)                   { g.resets++ }

func sessionTestRuntime(t *testing.T) (*Runtime, http.Handler, *testLoginGuard) {
	runtime, engine, _ := testRuntime(t)
	guard := &testLoginGuard{}
	runtime.SetManagementLoginGuard(guard)
	engine.GET("/login", runtime.LoginPage)
	engine.POST("/v0/management/login", runtime.Login)
	engine.POST("/v0/management/logout", runtime.Logout)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "127.0.0.1:12345"
		r = r.WithContext(WithAuditActor(r.Context(), AuditActor{ID: "legacy-admin-key", ClientIP: "127.0.0.1"}))
		engine.ServeHTTP(w, r)
	})
	return runtime, handler, guard
}
func createTestAdmin(t *testing.T, handler http.Handler) store.User {
	response := requestJSON(t, handler, http.MethodPost, "/users", `{"email":"admin@example.com","role":"admin","display_name":"Administrator"}`, http.StatusCreated)
	var user store.User
	if err := json.Unmarshal(response.Body.Bytes(), &user); err != nil {
		t.Fatal(err)
	}
	requestJSON(t, handler, http.MethodPost, "/users/"+user.ID+"/password", `{"password":"test-admin-password"}`, http.StatusOK)
	return user
}
func loginCookie(t *testing.T, handler http.Handler, password string) *http.Cookie {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": "admin@example.com", "password": password})
	response := requestJSON(t, handler, http.MethodPost, "/login", string(body), http.StatusOK)
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == SessionCookieName {
			return cookie
		}
	}
	t.Fatal("successful login did not issue session cookie")
	return nil
}
func sessionRequest(t *testing.T, handler http.Handler, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v0/management"+path, nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("session request status=%d body=%s", response.Code, response.Body.String())
	}
	return response
}

func TestPanelSessionsPasswordRevocationAndAudit(t *testing.T) {
	runtime, handler, guard := sessionTestRuntime(t)
	user := createTestAdmin(t, handler)
	cookie := loginCookie(t, handler, "test-admin-password")
	if !cookie.HttpOnly || cookie.Secure || cookie.Path != "/" || cookie.SameSite != http.SameSiteStrictMode || time.Until(cookie.Expires) < 11*time.Hour {
		t.Fatal("session cookie flags/absolute expiry are incorrect")
	}
	identity, errSession := runtime.AuthenticateSession(context.Background(), cookie.Value)
	if errSession != nil || identity.UserID != user.ID || identity.Role != "admin" {
		t.Fatal("active admin session was rejected")
	}
	db, _ := runtime.Snapshot()
	var persistedID string
	if err := db.DB().QueryRow(`SELECT id FROM cpa_sessions WHERE user_id=$1`, user.ID).Scan(&persistedID); err != nil {
		t.Fatal(err)
	}
	if persistedID != sessionHash(cookie.Value) || persistedID == cookie.Value {
		t.Fatal("session credential was not hashed at rest")
	}
	requestJSON(t, handler, http.MethodPost, "/users/"+user.ID+"/password", `{"password":"replacement-password"}`, http.StatusOK)
	if _, err := runtime.AuthenticateSession(context.Background(), cookie.Value); !errors.Is(err, ErrInvalidSession) {
		t.Fatal("password change did not revoke session")
	}
	cookie = loginCookie(t, handler, "replacement-password")
	requestJSON(t, handler, http.MethodPatch, "/users/"+user.ID, `{"status":"disabled"}`, http.StatusOK)
	requestJSON(t, handler, http.MethodPatch, "/users/"+user.ID, `{"status":"active"}`, http.StatusOK)
	if _, err := runtime.AuthenticateSession(context.Background(), cookie.Value); !errors.Is(err, ErrInvalidSession) {
		t.Fatal("disabled session revived after re-enable")
	}
	cookie = loginCookie(t, handler, "replacement-password")
	requestJSON(t, handler, http.MethodPatch, "/users/"+user.ID, `{"role":"user"}`, http.StatusOK)
	requestJSON(t, handler, http.MethodPatch, "/users/"+user.ID, `{"role":"admin"}`, http.StatusOK)
	if _, err := runtime.AuthenticateSession(context.Background(), cookie.Value); !errors.Is(err, ErrInvalidSession) {
		t.Fatal("demoted session revived after promotion")
	}
	cookie = loginCookie(t, handler, "replacement-password")
	sessionRequest(t, handler, "/logout", cookie)
	if _, err := runtime.AuthenticateSession(context.Background(), cookie.Value); !errors.Is(err, ErrInvalidSession) {
		t.Fatal("logout failed to revoke session")
	}
	if guard.resets != 4 {
		t.Fatalf("shared guard reset count=%d", guard.resets)
	}
	events, errAudit := db.ListAudit(context.Background(), 100, 0, "", "")
	if errAudit != nil {
		t.Fatal(errAudit)
	}
	counts := make(map[string]int)
	for _, event := range events {
		counts[event.Action]++
		if event.ClientIP != "127.0.0.1" {
			t.Fatal("audit omitted caller IP")
		}
		if event.Action == "session.login" || event.Action == "session.logout" {
			if event.Actor != user.ID {
				t.Fatal("session action lacks named actor")
			}
		} else if event.Actor != "legacy-admin-key" {
			t.Fatal("legacy management actor was not preserved")
		}
	}
	if counts["user.create"] != 1 || counts["password.update"] != 2 || counts["session.login"] != 4 || counts["session.logout"] != 1 || counts["user.disable"] != 1 || counts["user.update"] != 3 {
		t.Fatalf("unexpected audit duplication/actions: %v", counts)
	}
	encoded, _ := json.Marshal(events)
	for _, secret := range []string{"test-admin-password", "replacement-password", cookie.Value, persistedID} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("credential/hash leaked into audit")
		}
	}
}

func TestLoginFailuresExpiryAndClear(t *testing.T) {
	runtime, handler, guard := sessionTestRuntime(t)
	user := createTestAdmin(t, handler)
	wrong := requestJSON(t, handler, http.MethodPost, "/login", `{"email":"admin@example.com","password":"incorrect-password"}`, http.StatusUnauthorized)
	missing := requestJSON(t, handler, http.MethodPost, "/login", `{"email":"missing@example.com","password":"incorrect-password"}`, http.StatusUnauthorized)
	if wrong.Body.String() != missing.Body.String() {
		t.Fatal("login response distinguishes nonexistent account")
	}
	requestJSON(t, handler, http.MethodPatch, "/users/"+user.ID, `{"role":"user"}`, http.StatusOK)
	requestJSON(t, handler, http.MethodPost, "/login", `{"email":"admin@example.com","password":"test-admin-password"}`, http.StatusUnauthorized)
	requestJSON(t, handler, http.MethodPatch, "/users/"+user.ID, `{"role":"admin"}`, http.StatusOK)
	if guard.failures != 3 {
		t.Fatalf("failed credentials did not reuse shared guard: %d", guard.failures)
	}
	cookie := loginCookie(t, handler, "test-admin-password")
	db, _ := runtime.Snapshot()
	if _, err := db.DB().Exec(`UPDATE cpa_sessions SET expires_at=now()-interval '1 second' WHERE id=$1`, sessionHash(cookie.Value)); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AuthenticateSession(context.Background(), cookie.Value); !errors.Is(err, ErrInvalidSession) {
		t.Fatal("expired session accepted")
	}
	cookie = loginCookie(t, handler, "test-admin-password")
	requestJSON(t, handler, http.MethodPost, "/users/"+user.ID+"/password", `{}`, http.StatusBadRequest)
	requestJSON(t, handler, http.MethodPost, "/users/"+user.ID+"/password", `{"password":null}`, http.StatusOK)
	if _, err := runtime.AuthenticateSession(context.Background(), cookie.Value); !errors.Is(err, ErrInvalidSession) {
		t.Fatal("password clear did not revoke sessions")
	}
	failed, errAudit := db.ListAudit(context.Background(), 100, 0, "", "session.login_failed")
	if errAudit != nil || len(failed) != 3 {
		t.Fatal("failed credential logins were not audited exactly once")
	}
	page := requestJSON(t, handler, http.MethodGet, "/audit?limit=2&action=session.login_failed", "", http.StatusOK)
	if !strings.Contains(page.Body.String(), "next_before") {
		t.Fatal("audit pagination cursor missing")
	}
}

func TestAuditFailureRollsBackMutationAndSession(t *testing.T) {
	runtime, handler, _ := sessionTestRuntime(t)
	user := createTestAdmin(t, handler)
	db, _ := runtime.Snapshot()
	if _, err := db.DB().Exec(`CREATE FUNCTION reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit unavailable'; END; $$; CREATE TRIGGER reject_audit BEFORE INSERT ON cpa_audit_events FOR EACH ROW EXECUTE FUNCTION reject_audit()`); err != nil {
		t.Fatal(err)
	}
	requestJSON(t, handler, http.MethodPatch, "/users/"+user.ID, `{"display_name":"must roll back"}`, http.StatusServiceUnavailable)
	current, errUser := db.GetUser(context.Background(), user.ID)
	if errUser != nil || current.DisplayName != "Administrator" {
		t.Fatal("failed audit did not roll back account mutation")
	}
	response := requestJSON(t, handler, http.MethodPost, "/login", `{"email":"admin@example.com","password":"test-admin-password"}`, http.StatusServiceUnavailable)
	if len(response.Result().Cookies()) != 0 {
		t.Fatal("failed audit created a browser session")
	}
	var count int
	if err := db.DB().QueryRow(`SELECT count(*) FROM cpa_sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed audit left a session row")
	}
}

func TestPasswordChangePreventsLateVerifiedLogin(t *testing.T) {
	runtime, handler, _ := sessionTestRuntime(t)
	user := createTestAdmin(t, handler)
	db, _ := runtime.Snapshot()
	ctx := context.Background()
	verified, errUser := db.LoginUser(ctx, user.Email)
	if errUser != nil {
		t.Fatal(errUser)
	}
	newHash, errHash := bcrypt.GenerateFromPassword([]byte("new-password"), bcrypt.MinCost)
	if errHash != nil {
		t.Fatal(errHash)
	}
	hash := string(newHash)
	if err := db.SetPassword(ctx, user.ID, &hash); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, "late-session-hash", user.ID, *verified.PasswordHash, "127.0.0.1", time.Now().Add(time.Hour)); !errors.Is(err, store.ErrCredentialsChanged) {
		t.Fatal("old-password verification minted a session after password change")
	}
	if err := db.CreateSession(ctx, "current-session-hash", user.ID, hash, "127.0.0.1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.SetPassword(ctx, user.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetSession(ctx, "current-session-hash"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("password clear left earlier successful session")
	}
}

func TestAuditMutationActionsAndScopedDrain(t *testing.T) {
	runtime, handler, _ := sessionTestRuntime(t)
	user, key, plaintext := createTestIdentity(t, handler)
	requestJSON(t, handler, http.MethodPatch, "/users/"+user.ID, `{"monthly_token_limit":20}`, http.StatusOK)
	requestJSON(t, handler, http.MethodPut, "/users/"+user.ID+"/permissions", `{"permissions":[{"scope":"model","value":"azure-*"}]}`, http.StatusOK)
	requestJSON(t, handler, http.MethodPut, "/users/"+user.ID+"/permissions", `{"permissions":[]}`, http.StatusOK)
	requestJSON(t, handler, http.MethodDelete, "/keys/"+key.ID, "", http.StatusOK)
	db, cfg := runtime.Snapshot()
	events, errAudit := db.ListAudit(context.Background(), 100, 0, "", "")
	if errAudit != nil {
		t.Fatal(errAudit)
	}
	counts := map[string]int{}
	for _, event := range events {
		counts[event.Action]++
	}
	for _, action := range []string{"user.create", "key.create", "quota.update", "permission.grant", "permission.revoke", "key.revoke"} {
		if counts[action] != 1 {
			t.Fatalf("action %s count=%d", action, counts[action])
		}
	}
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), plaintext) {
		t.Fatal("plaintext key appeared in audit")
	}
	ctx, release, errLease := runtime.BeginAudit(WithAuditActor(context.Background(), AuditActor{ID: "legacy-admin-key", ClientIP: "127.0.0.1"}))
	if errLease != nil {
		t.Fatal(errLease)
	}
	cfg.Enabled = false
	if err := runtime.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := runtime.RecordAudit(ctx, AuditEvent{Action: "management.update", Detail: map[string]any{"method": "PATCH", "route": "/v0/management/config", "password": "must-never-appear"}}); err != nil {
		t.Fatal("admitted audit lost original store on disable")
	}
	events, errAudit = db.ListAudit(ctx, 1, 0, "", "management.update")
	if errAudit != nil || len(events) != 1 {
		t.Fatal("scoped management audit missing")
	}
	encoded, _ = json.Marshal(events)
	if strings.Contains(string(encoded), "must-never-appear") {
		t.Fatal("arbitrary audit detail was not stripped")
	}
	release()
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidKeyAuditLimiter(t *testing.T) {
	var limiter invalidKeyLimiter
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	if !limiter.allow("one", now) || limiter.allow("one", now.Add(time.Second)) || limiter.allow("two", now.Add(time.Millisecond)) {
		t.Fatal("invalid-key audit rate bounds failed")
	}
	if !limiter.allow("two", now.Add(time.Second)) || !limiter.allow("one", now.Add(time.Minute)) {
		t.Fatal("invalid-key limiter did not reopen after interval")
	}
}

func TestInvalidKeyAuditDrainsOnShutdown(t *testing.T) {
	runtime, _, dsn := testRuntime(t)
	observer, errOpen := sql.Open("pgx", dsn)
	if errOpen != nil {
		t.Fatal("open audit verification connection failed")
	}
	t.Cleanup(func() {
		if errClose := observer.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	runtime.RecordInvalidKey("127.0.0.1")
	runtime.RecordInvalidKey("127.0.0.1")
	if errClose := runtime.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	var count int
	if errQuery := observer.QueryRow(`SELECT count(*) FROM cpa_audit_events WHERE action='auth.invalid_key' AND client_ip='127.0.0.1'`).Scan(&count); errQuery != nil || count != 1 {
		t.Fatal("bounded invalid-key audit did not drain exactly once before database close")
	}
}
