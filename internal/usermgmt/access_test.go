package usermgmt

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func TestUserAccessCredentialSourcesAndRevocation(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	user, key, plaintext := createTestIdentity(t, engine)
	provider := runtime.AccessProvider()
	for _, testCase := range []struct {
		name, header, prefix, query, source string
	}{
		{name: "Bearer", header: "Authorization", prefix: "Bearer ", source: "authorization"},
		{name: "case-insensitive bearer", header: "Authorization", prefix: "bEaReR ", source: "authorization"},
		{name: "raw authorization", header: "Authorization", source: "authorization"},
		{name: "google", header: "X-Goog-Api-Key", source: "x-goog-api-key"},
		{name: "anthropic", header: "X-Api-Key", source: "x-api-key"},
		{name: "query key", query: "key", source: "query-key"},
		{name: "query auth token", query: "auth_token", source: "query-auth-token"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://proxy/v1/models", nil)
			if testCase.header != "" {
				request.Header.Set(testCase.header, testCase.prefix+plaintext)
			} else {
				query := request.URL.Query()
				query.Set(testCase.query, plaintext)
				request.URL.RawQuery = query.Encode()
			}
			result, errAuth := provider.Authenticate(request.Context(), request)
			if errAuth != nil || result.Principal != user.ID || result.Provider != "user" || result.Metadata["key_id"] != key.ID || result.Metadata["source"] != testCase.source {
				t.Fatalf("wrong identity or credential source (error %v)", errAuth)
			}
			// Provider result metadata must not alias an entry used by later requests.
			result.Metadata["key_id"] = "tampered"
		})
	}
	request := httptest.NewRequest(http.MethodGet, "http://proxy/v1/models", nil)
	request.Header.Set("Authorization", "Bearer unknown")
	request.Header.Set("X-Api-Key", plaintext)
	if result, errAuth := provider.Authenticate(request.Context(), request); errAuth != nil || result.Principal != user.ID {
		t.Fatal("invalid earlier credential blocked a valid later credential")
	}
	request.Header.Del("X-Api-Key")
	request.Header.Set("Authorization", "Bearer "+plaintext)
	requestJSON(t, engine, http.MethodPatch, "/users/"+user.ID, `{"status":"disabled"}`, http.StatusOK)
	assertUnauthorized(t, provider, request)
	requestJSON(t, engine, http.MethodPatch, "/users/"+user.ID, `{"status":"active"}`, http.StatusOK)
	if _, errAuth := provider.Authenticate(request.Context(), request); errAuth != nil {
		t.Fatal("reactivated user could not authenticate")
	}
	requestJSON(t, engine, http.MethodDelete, "/keys/"+key.ID, "", http.StatusOK)
	assertUnauthorized(t, provider, request)
	request.Header.Del("Authorization")
	if _, errAuth := provider.Authenticate(request.Context(), request); !sdkaccess.IsAuthErrorCode(errAuth, sdkaccess.AuthErrorCodeNoCredentials) {
		t.Fatal("missing credentials should return no_credentials")
	}
	if errApply := runtime.Apply(context.Background(), config.UserManagementConfig{}); errApply != nil {
		t.Fatal(errApply)
	}
	if runtime.AccessProvider() != nil {
		t.Fatal("disabled runtime retained provider registration")
	}
	if _, errAuth := provider.Authenticate(request.Context(), request); !sdkaccess.IsAuthErrorCode(errAuth, sdkaccess.AuthErrorCodeNotHandled) {
		t.Fatal("retained provider should stop handling requests after disable")
	}
}

func assertUnauthorized(t *testing.T, provider sdkaccess.Provider, request *http.Request) {
	t.Helper()
	if result, errAuth := provider.Authenticate(request.Context(), request); result != nil || errAuth == nil || errAuth.HTTPStatusCode() != http.StatusUnauthorized {
		t.Fatal("inactive or unavailable user credential did not fail closed")
	}
}

func TestUserAccessDatabaseFailurePreservesLegacyFallback(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	_, _, plaintext := createTestIdentity(t, engine)
	provider := runtime.AccessProvider()
	db, _ := runtime.Snapshot()
	if errClose := db.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	request := httptest.NewRequest(http.MethodGet, "http://proxy/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+plaintext)
	assertUnauthorized(t, provider, request)
	manager := sdkaccess.NewManager()
	manager.SetProviders([]sdkaccess.Provider{provider, testLegacyProvider{}})
	request.Header.Set("Authorization", "Bearer legacy-key")
	if result, errAuth := manager.Authenticate(request.Context(), request); errAuth != nil || result.Principal != "legacy-key" {
		t.Fatal("database outage blocked legacy provider fallback")
	}
	requestJSON(t, engine, http.MethodGet, "/users", "", http.StatusServiceUnavailable)
}

type testLegacyProvider struct{}

func (testLegacyProvider) Identifier() string { return "legacy-test" }
func (testLegacyProvider) Authenticate(_ context.Context, request *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if request.Header.Get("Authorization") == "Bearer legacy-key" {
		return &sdkaccess.Result{Provider: "legacy-test", Principal: "legacy-key"}, nil
	}
	return nil, sdkaccess.NewInvalidCredentialError()
}

func TestUserAccessLastUsedFlushOnShutdown(t *testing.T) {
	runtime, engine, dsn := testRuntime(t)
	_, key, plaintext := createTestIdentity(t, engine)
	request := httptest.NewRequest(http.MethodGet, "http://proxy/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+plaintext)
	if _, errAuth := runtime.AccessProvider().Authenticate(request.Context(), request); errAuth != nil {
		t.Fatal(errAuth)
	}
	checkDB, errOpen := sql.Open("pgx", dsn)
	if errOpen != nil {
		t.Fatal("open last-used verification connection failed")
	}
	t.Cleanup(func() {
		if errClose := checkDB.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	if errClose := runtime.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	var touched bool
	if errQuery := checkDB.QueryRow(`SELECT last_used_at IS NOT NULL FROM cpa_user_api_keys WHERE id=$1`, key.ID).Scan(&touched); errQuery != nil || !touched {
		t.Fatalf("shutdown lost pending last-used update (error %v)", errQuery)
	}
}
