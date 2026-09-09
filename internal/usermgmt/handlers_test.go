package usermgmt

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/oklog/ulid/v2"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
)

func testRuntime(t *testing.T) (*Runtime, *gin.Engine, string) {
	t.Helper()
	spoolDirectory := t.TempDir()
	dsn := os.Getenv("CLIPROXY_USERMGMT_TEST_DSN")
	if dsn == "" {
		t.Skip("set CLIPROXY_USERMGMT_TEST_DSN for PostgreSQL integration tests")
	}
	parsed, errParse := url.Parse(dsn)
	if errParse != nil {
		t.Fatal("invalid integration DSN")
	}
	db, errOpen := sql.Open("pgx", dsn)
	if errOpen != nil {
		t.Fatal("open integration database failed")
	}
	id, errID := newID()
	if errID != nil {
		t.Fatal(errID)
	}
	schema := "cpa_test_" + strings.ToLower(id)
	if _, errCreate := db.Exec(`CREATE SCHEMA "` + schema + `"`); errCreate != nil {
		_ = db.Close()
		t.Fatal("create integration schema failed")
	}
	runtime := &Runtime{pricingRefresh: func(context.Context, *store.Store) error { return nil }}
	t.Cleanup(func() {
		if errClose := runtime.Close(); errClose != nil {
			t.Error(errClose)
		}
		if _, errDrop := db.Exec(`DROP SCHEMA "` + schema + `" CASCADE`); errDrop != nil {
			t.Error("drop integration schema failed")
		}
		if errClose := db.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	dsn = parsed.String()
	if errApply := runtime.Apply(context.Background(), config.UserManagementConfig{Enabled: true, DSN: dsn, RequestActivity: config.UserManagementRequestActivityConfig{SpoolDirectory: spoolDirectory}}); errApply != nil {
		t.Fatal(errApply)
	}
	engine := gin.New()
	runtime.RegisterManagementRoutes(engine.Group("/v0/management"))
	return runtime, engine, dsn
}

func requestJSON(t *testing.T, engine http.Handler, method, path, body string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "/v0/management"+path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s status %d, want %d; response %s", method, path, response.Code, wantStatus, response.Body.String())
	}
	return response
}

func createTestIdentity(t *testing.T, engine http.Handler) (store.User, store.APIKey, string) {
	t.Helper()
	created := requestJSON(t, engine, http.MethodPost, "/users", `{"email":" ALICE@example.com ","display_name":"Alice","monthly_token_limit":100}`, http.StatusCreated)
	var user store.User
	if errJSON := json.Unmarshal(created.Body.Bytes(), &user); errJSON != nil {
		t.Fatal(errJSON)
	}
	issued := requestJSON(t, engine, http.MethodPost, "/users/"+user.ID+"/keys", `{"label":"test laptop"}`, http.StatusCreated)
	var result struct {
		Key    string       `json:"key"`
		APIKey store.APIKey `json:"api_key"`
	}
	if errJSON := json.Unmarshal(issued.Body.Bytes(), &result); errJSON != nil {
		t.Fatal(errJSON)
	}
	return user, result.APIKey, result.Key
}

func TestManagementUsersAndKeys(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	user, key, plaintext := createTestIdentity(t, engine)
	if _, errID := ulid.ParseStrict(user.ID); errID != nil || user.Email != "alice@example.com" {
		t.Fatal("expected ULID and normalized email")
	}
	if _, errID := ulid.ParseStrict(key.ID); errID != nil || len(plaintext) != 50 || !strings.HasPrefix(plaintext, "sk-cpa-") {
		t.Fatal("expected ULID key ID and 256-bit API key")
	}
	db, _ := runtime.Snapshot()
	var persistedHash string
	if errQuery := db.DB().QueryRow(`SELECT key_hash FROM cpa_user_api_keys WHERE id=$1`, key.ID).Scan(&persistedHash); errQuery != nil {
		t.Fatal(errQuery)
	}
	digest := sha256.Sum256([]byte(plaintext))
	if persistedHash != hex.EncodeToString(digest[:]) {
		t.Fatal("API key was not persisted as its SHA256 digest")
	}
	if _, errUpdate := db.DB().Exec(`UPDATE cpa_users SET password_hash='PASSWORD-HASH-MUST-NOT-LEAK' WHERE id=$1`, user.ID); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	for _, path := range []string{"/users", "/users/" + user.ID, "/users/" + user.ID + "/keys"} {
		response := requestJSON(t, engine, http.MethodGet, path, "", http.StatusOK)
		for _, secret := range []string{plaintext, persistedHash, "PASSWORD-HASH-MUST-NOT-LEAK", "password_hash", "key_hash"} {
			if strings.Contains(response.Body.String(), secret) {
				t.Fatalf("secret material exposed by %s", path)
			}
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("account response lacks no-store")
		}
	}
	requestJSON(t, engine, http.MethodPost, "/users", `{"email":"alice@EXAMPLE.com"}`, http.StatusConflict)
	requestJSON(t, engine, http.MethodPatch, "/users/"+user.ID, `{"role":"admin","display_name":"Updated"}`, http.StatusOK)
	page := requestJSON(t, engine, http.MethodGet, "/users?limit=1&offset=1", "", http.StatusOK)
	var listed struct {
		Users []store.User `json:"users"`
		Total int64        `json:"total"`
	}
	if errJSON := json.Unmarshal(page.Body.Bytes(), &listed); errJSON != nil || len(listed.Users) != 0 || listed.Total != 1 {
		t.Fatal("pagination omitted total or failed to apply offset")
	}
	requestJSON(t, engine, http.MethodDelete, "/keys/"+key.ID, "", http.StatusOK)
	requestJSON(t, engine, http.MethodDelete, "/keys/"+key.ID, "", http.StatusOK)
	keys, _, errKeys := db.ListKeys(context.Background(), user.ID, 50, 0)
	if errKeys != nil || len(keys) != 1 || keys[0].RevokedAt == nil || keys[0].Status != "revoked" {
		t.Fatal("key revocation was not retained in key list")
	}
	requestJSON(t, engine, http.MethodDelete, "/users/"+user.ID, "", http.StatusOK)
	requestJSON(t, engine, http.MethodGet, "/users/"+user.ID, "", http.StatusNotFound)
	var count int
	if errCount := db.DB().QueryRow(`SELECT count(*) FROM cpa_user_api_keys WHERE user_id=$1`, user.ID).Scan(&count); errCount != nil || count != 0 {
		t.Fatal("user delete did not cascade API keys")
	}
}

func TestManagementNullableQuotaPatch(t *testing.T) {
	_, engine, _ := testRuntime(t)
	user, _, _ := createTestIdentity(t, engine)
	for _, testCase := range []struct {
		body string
		want *int64
	}{
		{`{"display_name":"Retain"}`, int64Pointer(100)},
		{`{"monthly_token_limit":null}`, nil},
		{`{"monthly_token_limit":0}`, int64Pointer(0)},
		{`{"monthly_token_limit":-1}`, int64Pointer(-1)},
		{`{"monthly_token_limit":1234567890123}`, int64Pointer(1234567890123)},
	} {
		response := requestJSON(t, engine, http.MethodPatch, "/users/"+user.ID, testCase.body, http.StatusOK)
		var updated store.User
		if errJSON := json.Unmarshal(response.Body.Bytes(), &updated); errJSON != nil {
			t.Fatal(errJSON)
		}
		if (updated.MonthlyTokenLimit == nil) != (testCase.want == nil) || testCase.want != nil && *updated.MonthlyTokenLimit != *testCase.want {
			t.Fatalf("quota patch %s lost omitted/null/explicit semantics", testCase.body)
		}
	}
}

func int64Pointer(value int64) *int64 { return &value }

func TestManagementInputValidationAndDisabledRuntime(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	user, _, _ := createTestIdentity(t, engine)
	for _, body := range []string{`null`, `[]`, `{}`, `{"email":"invalid"}`, `{"email":"other@example.com","role":"owner"}`, `{"email":"other@example.com","password":"secret"}`, `{"email":"other@example.com"} {}`} {
		requestJSON(t, engine, http.MethodPost, "/users", body, http.StatusBadRequest)
	}
	for _, body := range []string{`{}`, `{"status":"deleted"}`, `{"monthly_token_limit":1.5}`, `{"monthly_token_limit":9223372036854775808}`, `{"display_name":"\u0000"}`} {
		requestJSON(t, engine, http.MethodPatch, "/users/"+user.ID, body, http.StatusBadRequest)
	}
	for _, query := range []string{"limit=0", "limit=201", "limit=1&limit=2", "offset=-1", "offset=hello"} {
		requestJSON(t, engine, http.MethodGet, "/users?"+query, "", http.StatusBadRequest)
	}
	requestJSON(t, engine, http.MethodGet, "/users/invalid", "", http.StatusBadRequest)
	requestJSON(t, engine, http.MethodPost, "/users/"+user.ID+"/keys", "null", http.StatusBadRequest)
	requestJSON(t, engine, http.MethodPost, "/users/"+user.ID+"/keys", `{"label":"`+strings.Repeat("x", 65<<10)+`"}`, http.StatusBadRequest)
	if errApply := runtime.Apply(context.Background(), config.UserManagementConfig{}); errApply != nil {
		t.Fatal(errApply)
	}
	requestJSON(t, engine, http.MethodGet, "/users", "", http.StatusNotFound)
	requestJSON(t, engine, http.MethodPost, "/users", `{"email":"new@example.com"}`, http.StatusNotFound)
	var absent *Runtime
	if absent.AccessProvider() != nil {
		t.Fatal("nil runtime registered a provider")
	}
}
