package usermgmt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

type staleConfiguredProvider struct{ key string }

func (p staleConfiguredProvider) Identifier() string { return "stale-config" }
func (p staleConfiguredProvider) Authenticate(_ context.Context, request *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if request.Header.Get("Authorization") == "Bearer "+p.key {
		return &sdkaccess.Result{Provider: "stale-config", Principal: p.key}, nil
	}
	return nil, sdkaccess.NewInvalidCredentialError()
}

func TestConfiguredAdminStableIdentityRotationAndExemptions(t *testing.T) {
	runtime, engine, dsn := testRuntime(t)
	const first, second = "configured-main-first-test-secret", "configured-main-second-test-secret"
	if err := runtime.ConfigureMainAPIKey(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	db, cfg := runtime.Snapshot()
	user, err := db.GetSystemAdmin(context.Background())
	if err != nil || user.ID != store.SystemAdminUserID || !user.SystemAdmin || user.DisplayName != "Admin" {
		t.Fatalf("system identity unavailable: %v", err)
	}
	login, err := db.LoginUser(context.Background(), user.Email)
	if err != nil || login.PasswordHash != nil || !login.SystemAdmin {
		t.Fatal("system account acquired an automatic password")
	}
	var storedKeys int
	if err := db.DB().QueryRow(`SELECT count(*) FROM cpa_user_api_keys WHERE user_id=$1`, user.ID).Scan(&storedKeys); err != nil || storedKeys != 0 {
		t.Fatal("configured credential was copied into API-key storage")
	}
	for _, source := range []string{"Authorization", "X-Goog-Api-Key", "X-Api-Key", "key", "auth_token"} {
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		if source == "key" || source == "auth_token" {
			query := request.URL.Query()
			query.Set(source, first)
			request.URL.RawQuery = query.Encode()
		} else if source == "Authorization" {
			request.Header.Set(source, "Bearer "+first)
		} else {
			request.Header.Set(source, first)
		}
		identity, errAuth := runtime.AccessProvider().Authenticate(request.Context(), request)
		if errAuth != nil || identity.Principal != user.ID || identity.Provider != "user" || identity.Metadata["system_admin"] != "true" {
			t.Fatalf("source %s failed: %v", source, errAuth)
		}
		encoded, _ := json.Marshal(identity)
		if strings.Contains(string(encoded), first) {
			t.Fatal("configured key leaked into identity")
		}
	}
	ctx := usageContext(t, runtime, first)
	requestJSON(t, engine, http.MethodPut, "/users/"+user.ID+"/permissions", `{"permissions":[{"scope":"model","value":"*","effect":"deny"}]}`, http.StatusOK)
	if err := runtime.CheckPermissions(ctx, sdkaccess.PolicyTarget{RequestedModel: "anything", Provider: "unpriced-provider", ExecutionModel: "anything"}); err != nil {
		t.Fatalf("system account is not exempt: %v", err)
	}
	if err := runtime.CheckQuota(ctx); err != nil {
		t.Fatalf("system budget exemption failed: %v", err)
	}
	requestJSON(t, engine, http.MethodPatch, "/users/"+user.ID, `{"status":"disabled"}`, http.StatusConflict)
	requestJSON(t, engine, http.MethodPatch, "/users/"+user.ID, `{"role":"user"}`, http.StatusConflict)
	requestJSON(t, engine, http.MethodDelete, "/users/"+user.ID, "", http.StatusConflict)
	requestJSON(t, engine, http.MethodDelete, "/keys/"+ConfiguredAdminKeyID, "", http.StatusBadRequest)
	manager := sdkaccess.NewManager()
	manager.SetPriorityProviders([]sdkaccess.Provider{runtime.AccessProvider()})
	manager.SetProviders([]sdkaccess.Provider{staleConfiguredProvider{key: first}})
	if err := runtime.ConfigureMainAPIKey(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	old := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	old.Header.Set("Authorization", "Bearer "+first)
	if _, errAuth := manager.Authenticate(old.Context(), old); errAuth == nil || errAuth.HTTPStatusCode() != 401 {
		t.Fatal("rotated key fell through stale legacy authentication")
	}
	if err := runtime.CheckRequest(ctx); err == nil {
		t.Fatal("old socket identity survived configured-key rotation")
	}
	current := usageContext(t, runtime, second)
	if ok, err := runtime.IsSystemAdmin(current); err != nil || !ok {
		t.Fatal("new configured key failed confirmed exemption")
	}
	if err := runtime.ConfigureMainAPIKey(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CheckRequest(current); err != nil {
		t.Fatal("unchanged config unnecessarily invalidated current identity")
	}
	restarted := &Runtime{pricingRefresh: func(context.Context, *store.Store) error { return nil }}
	t.Cleanup(func() { _ = restarted.Close() })
	cfg.DSN = dsn
	if err := restarted.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := restarted.ConfigureMainAPIKey(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	after, _ := restarted.Snapshot()
	row, err := after.GetSystemAdmin(context.Background())
	if err != nil || row.ID != user.ID || !row.CreatedAt.Equal(user.CreatedAt) {
		t.Fatal("restart duplicated or replaced the system identity")
	}
	var count int
	if err := db.DB().QueryRow(`SELECT count(*) FROM cpa_users WHERE system_admin`).Scan(&count); err != nil || count != 1 {
		t.Fatal("expected exactly one system account")
	}
}

func TestConfiguredAdminOutageIsTerminalAndRoleAdminIsNotExempt(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	const key = "configured-main-outage-test-secret"
	if err := runtime.ConfigureMainAPIKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	user, _, ordinaryKey := createTestIdentity(t, engine)
	requestJSON(t, engine, http.MethodPatch, "/users/"+user.ID, `{"role":"admin"}`, http.StatusOK)
	requestJSON(t, engine, http.MethodPut, "/users/"+user.ID+"/permissions", `{"permissions":[{"scope":"model","value":"*","effect":"deny"}]}`, http.StatusOK)
	ordinary := usageContext(t, runtime, ordinaryKey)
	identity, _ := sdkaccess.ResultFromContext(ordinary)
	identity.Metadata["system_admin"] = "true"
	if err := runtime.CheckPermissions(sdkaccess.WithResult(ordinary, identity), sdkaccess.PolicyTarget{RequestedModel: "denied"}); err == nil {
		t.Fatal("role/metadata forged a system Admin exemption")
	}
	manager := sdkaccess.NewManager()
	manager.SetPriorityProviders([]sdkaccess.Provider{runtime.AccessProvider()})
	manager.SetProviders([]sdkaccess.Provider{staleConfiguredProvider{key: key}})
	db, _ := runtime.Snapshot()
	_ = db.Close()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("Authorization", "Bearer "+key)
	if result, errAuth := manager.Authenticate(request.Context(), request); result != nil || errAuth == nil || errAuth.HTTPStatusCode() != 503 {
		t.Fatal("unavailable system identity fell through to untracked legacy auth")
	}
}

func TestBillingProviderMappingRequiresActualEndpoint(t *testing.T) {
	runtime := &Runtime{}
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "azure-openai", BaseURL: "https://tenant.openai.azure.com/openai/v1"}, {Name: "not-azure", BaseURL: "https://api.openai.com.evil.invalid"}}}
	runtime.ConfigureBillingProviders(cfg)
	if got := runtime.billingProviderCandidates("openai-compatible-azure-openai", "visible-alias"); strings.Join(got, ",") != "openai-compatible-azure-openai,azure" {
		t.Fatalf("wrong provider candidates: %v", got)
	}
	if got := runtime.billingProviderCandidates("not-azure", "azure-gpt"); len(got) != 1 {
		t.Fatal("billing vendor was guessed from an alias")
	}
	snapshot := runtime.billingProviders
	cfg.OpenAICompatibility[0].BaseURL = "https://api.openai.com/v1"
	runtime.ConfigureBillingProviders(cfg)
	if snapshot["azure-openai"] != "azure" || runtime.billingProviderCandidates("azure-openai", "")[1] != "openai" {
		t.Fatal("reload mutated an accepted-request provider snapshot")
	}
	for endpoint, want := range map[string]string{"https://x.services.ai.azure.com": "azure", "https://api.anthropic.com": "anthropic", "https://us-central1-aiplatform.googleapis.com": "google", "https://generativelanguage.googleapis.com": "google", "https://api.openai.com.evil.invalid": "", "https://evil.invalid/?target=api.openai.com": ""} {
		if got := billingVendorFromEndpoint(endpoint); got != want {
			t.Fatalf("vendor for endpoint mismatch: got %q want %q", got, want)
		}
	}
}
