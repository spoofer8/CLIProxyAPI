package usermgmt

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func rule(scope, value, effect string) store.Permission {
	return store.Permission{Scope: scope, Value: value, Effect: effect}
}

func TestPermissionEvaluation(t *testing.T) {
	azure := sdkaccess.PolicyTarget{RequestedModel: "azure-4.1-mini(high)", ResolvedModel: "azure-4.1-mini", ExecutionModel: "physical-deployment", PayloadModel: "physical-deployment", Provider: "openai-compatible-azure-openai"}
	for _, testCase := range []struct {
		name    string
		rules   []store.Permission
		target  sdkaccess.PolicyTarget
		allowed bool
	}{
		{name: "empty allows all", target: azure, allowed: true},
		{name: "alias allow ignores physical name", rules: []store.Permission{rule("model", "azure-*", "allow")}, target: azure, allowed: true},
		{name: "exact alias accepts reasoning suffix", rules: []store.Permission{rule("model", "azure-4.1-mini", "allow")}, target: azure, allowed: true},
		{name: "deployment is not an allow alias", rules: []store.Permission{rule("model", "physical-deployment", "allow")}, target: azure},
		{name: "execution deny wins", rules: []store.Permission{rule("model", "azure-*", "allow"), rule("model", "physical-deployment", "deny")}, target: azure},
		{name: "canonical provider", rules: []store.Permission{rule("provider", "openai-compatible-azure-openai", "allow")}, target: azure, allowed: true},
		{name: "friendly provider ignores case", rules: []store.Permission{rule("provider", " Azure-OpenAI ", "allow")}, target: azure, allowed: true},
		{name: "provider wildcard", rules: []store.Permission{rule("provider", "azure-*", "allow")}, target: azure, allowed: true},
		{name: "independent provider list", rules: []store.Permission{rule("model", "azure-*", "allow"), rule("provider", "claude", "allow")}, target: azure},
		{name: "independent model list", rules: []store.Permission{rule("model", "claude-*", "allow"), rule("provider", "azure-openai", "allow")}, target: azure},
		{name: "provider deny wins", rules: []store.Permission{rule("provider", "*", "allow"), rule("provider", "azure-openai", "deny")}, target: azure},
		{name: "deny-only permits others", rules: []store.Permission{rule("model", "claude-*", "deny")}, target: azure, allowed: true},
		{name: "model case sensitive", rules: []store.Permission{rule("model", "Azure-*", "allow")}, target: azure},
		{name: "star crosses slash", rules: []store.Permission{rule("model", "azure-*", "allow")}, target: sdkaccess.PolicyTarget{RequestedModel: "azure-org/nested/model"}, allowed: true},
		{name: "question matches rune", rules: []store.Permission{rule("model", "model-?", "allow")}, target: sdkaccess.PolicyTarget{RequestedModel: "model-α"}, allowed: true},
		{name: "question exactly one rune", rules: []store.Permission{rule("model", "model-?", "allow")}, target: sdkaccess.PolicyTarget{RequestedModel: "model-ab"}},
		{name: "glob fully anchored", rules: []store.Permission{rule("model", "azure", "allow")}, target: azure},
		{name: "brackets literal", rules: []store.Permission{rule("model", "model-[ab]", "allow")}, target: sdkaccess.PolicyTarget{RequestedModel: "model-a"}},
		{name: "payload rewrite denied", rules: []store.Permission{rule("model", "azure-*", "allow"), rule("model", "forbidden-*", "deny")}, target: sdkaccess.PolicyTarget{RequestedModel: "azure-mini", ResolvedModel: "azure-mini", ExecutionModel: "physical", PayloadModel: "forbidden-model(high)", Provider: azure.Provider}},
		{name: "resolved visible deny", rules: []store.Permission{rule("model", "*", "allow"), rule("model", "claude-*", "deny")}, target: sdkaccess.PolicyTarget{RequestedModel: "auto", ResolvedModel: "claude-opus-5", Provider: "claude"}},
		{name: "nested visible deny survives private mapping", rules: []store.Permission{rule("model", "azure-*", "allow"), rule("model", "claude-*", "deny")}, target: sdkaccess.PolicyTarget{RequestedModel: "azure-alias", ResolvedModel: "azure-alias", ExecutionModel: "private-inner", PayloadModel: "private-inner", DenyModels: []string{"claude-opus-5(high)"}, Provider: azure.Provider}},
		{name: "nested deny candidates cannot satisfy allows", rules: []store.Permission{rule("model", "azure-*", "allow")}, target: sdkaccess.PolicyTarget{RequestedModel: "unlisted-alias", ResolvedModel: "unlisted-alias", ExecutionModel: "private-inner", DenyModels: []string{"azure-inner"}, Provider: azure.Provider}},
		{name: "unknown provider fails closed", rules: []store.Permission{rule("provider", "claude", "deny")}, target: sdkaccess.PolicyTarget{RequestedModel: "azure-mini"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			compiled, errCompile := compilePermissions(testCase.rules)
			if errCompile != nil {
				t.Fatal(errCompile)
			}
			if allowed := compiled.allows(testCase.target, false); allowed != testCase.allowed {
				t.Fatalf("allowed=%v want %v", allowed, testCase.allowed)
			}
		})
	}
}

func TestPermissionsAPIHeldIdentityAndFiltering(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	user, _, key := createTestIdentity(t, engine)
	ctx := usageContext(t, runtime, key)
	azure := sdkaccess.PolicyTarget{RequestedModel: "azure-4.1-mini", ResolvedModel: "azure-4.1-mini", ExecutionModel: "physical", Provider: "openai-compatible-azure-openai"}
	if errCheck := runtime.CheckPermissions(ctx, azure); errCheck != nil {
		t.Fatal(errCheck)
	}
	if allowed, errAllowed := runtime.ModelAllowed(ctx, "unknown-model", nil); errAllowed != nil || !allowed {
		t.Fatal("empty rules changed default model listing")
	}
	path := "/users/" + user.ID + "/permissions"
	requestJSON(t, engine, http.MethodPut, path, `{"permissions":[{"scope":"model","value":"azure-*"},{"scope":"provider","value":" Azure-OpenAI ","effect":"allow"},{"scope":"model","value":"azure-secret","effect":"deny"}]}`, http.StatusOK)
	if errCheck := runtime.CheckPermissions(ctx, azure); errCheck != nil {
		t.Fatal(errCheck)
	}
	claude := sdkaccess.PolicyTarget{RequestedModel: "claude-opus-5", ResolvedModel: "claude-opus-5", Provider: "claude"}
	assertPermissionDenied(t, runtime.CheckPermissions(ctx, claude))
	filtered, errFilter := runtime.FilterProviders(ctx, azure.RequestedModel, azure.ResolvedModel, []string{"claude", azure.Provider})
	if errFilter != nil || len(filtered) != 1 || filtered[0] != azure.Provider {
		t.Fatal("provider filtering did not retain the allowed fallback")
	}
	home, errHome := runtime.FilterProviders(ctx, azure.RequestedModel, azure.ResolvedModel, []string{"home"})
	if errHome != nil || len(home) != 1 {
		t.Fatal("Home dispatcher was treated as the final provider")
	}
	finalHome := azure
	finalHome.Provider = "claude"
	assertPermissionDenied(t, runtime.CheckPermissions(ctx, finalHome))
	for _, model := range []string{"claude-opus-5", "azure-secret"} {
		if allowed, errAllowed := runtime.ModelAllowed(ctx, model, []string{azure.Provider}); errAllowed != nil || allowed {
			t.Fatalf("forbidden model %s remained visible", model)
		}
	}
	if allowed, errAllowed := runtime.ModelAllowed(ctx, azure.RequestedModel, []string{azure.Provider}); errAllowed != nil || !allowed {
		t.Fatal("allowed Azure alias was hidden")
	}
	if allowed, errAllowed := runtime.ModelAllowed(ctx, azure.RequestedModel, nil); errAllowed != nil || allowed {
		t.Fatal("unknown provider satisfied a provider restriction")
	}
	requestJSON(t, engine, http.MethodPut, path, `{"permissions":[{"scope":"model","value":"*","effect":"allow"},{"scope":"model","value":"physical","effect":"deny"}]}`, http.StatusOK)
	assertPermissionDenied(t, runtime.CheckPermissions(ctx, azure))
	requestJSON(t, engine, http.MethodPut, path, `{"permissions":[]}`, http.StatusOK)
	if errCheck := runtime.CheckPermissions(ctx, claude); errCheck != nil {
		t.Fatal("explicit reset failed to restore default-open policy")
	}
	legacy := sdkaccess.WithResult(context.Background(), &sdkaccess.Result{Provider: "config", Principal: "legacy"})
	requestJSON(t, engine, http.MethodPut, path, `{"permissions":[{"scope":"model","value":"*","effect":"deny"}]}`, http.StatusOK)
	if errCheck := runtime.CheckPermissions(legacy, azure); errCheck != nil {
		t.Fatal("legacy admin was subjected to user permissions")
	}
	requestJSON(t, engine, http.MethodDelete, "/users/"+user.ID, "", http.StatusOK)
	if errCheck := runtime.CheckPermissions(ctx, azure); errCheck == nil {
		t.Fatal("deleted held identity became default-open")
	}
}

func assertPermissionDenied(t *testing.T, err error) {
	t.Helper()
	var policyErr *QuotaError
	if !errors.As(err, &policyErr) || policyErr.StatusCode() != http.StatusForbidden || !stringsContainAll(string(policyErr.ResponseBody()), "permission_error", "model_not_permitted") {
		t.Fatalf("expected terminal permission403, got %v", err)
	}
}

func TestPermissionsInvalidInputDoesNotClearExistingRules(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	user, _, key := createTestIdentity(t, engine)
	ctx := usageContext(t, runtime, key)
	path := "/users/" + user.ID + "/permissions"
	requestJSON(t, engine, http.MethodPut, path, `{"permissions":[{"scope":"model","value":"azure-*","effect":"allow"}]}`, http.StatusOK)
	invalid := []string{`{}`, `null`, `{"permissions":null}`, `{"permissions":[],"unknown":true}`, `{"permissions":{}}`, `{"permissions":[{"scope":"bad","value":"x"}]}`, `{"permissions":[{"scope":"model","value":""}]}`, `{"permissions":[{"scope":"model","value":"x","effect":"maybe"}]}`, `{"permissions":[{"scope":"model","value":"x\n"}]}`, `{"permissions":[{"scope":"model","value":"x","extra":1}]}`, `{"permissions":[{"scope":"provider","value":"Azure"},{"scope":"provider","value":"azure","effect":"deny"}]}`}
	// An embedded control character is invalid even though surrounding whitespace
	// is intentionally trimmed during normalization.
	invalid[8] = `{"permissions":[{"scope":"model","value":"x\ny"}]}`
	invalid = append(invalid, `{"permissions":[{"scope":"model","value":"`+strings.Repeat("x", 257)+`"}]}`)
	tooMany := make([]store.Permission, maxPermissions+1)
	for i := range tooMany {
		tooMany[i] = rule("model", strings.Repeat("x", i+1), "allow")
	}
	encoded, errJSON := json.Marshal(map[string]any{"permissions": tooMany})
	if errJSON != nil {
		t.Fatal(errJSON)
	}
	invalid = append(invalid, string(encoded))
	for _, body := range invalid {
		requestJSON(t, engine, http.MethodPut, path, body, http.StatusBadRequest)
		assertPermissionDenied(t, runtime.CheckPermissions(ctx, sdkaccess.PolicyTarget{RequestedModel: "claude-opus-5", Provider: "claude"}))
	}
	response := requestJSON(t, engine, http.MethodGet, path, "", http.StatusOK)
	if !strings.Contains(response.Body.String(), "azure-*") {
		t.Fatal("invalid input altered existing permissions")
	}
}

func TestPermissionStoreFailureDoesNotBecomeAllowAll(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	_, _, key := createTestIdentity(t, engine)
	ctx := usageContext(t, runtime, key)
	target := sdkaccess.PolicyTarget{RequestedModel: "azure-mini", Provider: "openai-compatible-azure-openai"}
	if errCheck := runtime.CheckPermissions(ctx, target); errCheck != nil {
		t.Fatal(errCheck)
	}
	runtime.current.permissions.invalidate()
	db, _ := runtime.Snapshot()
	if errClose := db.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	var policyErr *QuotaError
	if errCheck := runtime.CheckPermissions(ctx, target); !errors.As(errCheck, &policyErr) || policyErr.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("store failure was hidden as empty permissions: %v", errCheck)
	}
	if allowed, errAllowed := runtime.ModelAllowed(ctx, target.RequestedModel, []string{target.Provider}); errAllowed == nil || allowed {
		t.Fatal("listing swallowed permissions store failure")
	}
}

func TestPermissionCacheTTLAndInvalidationRace(t *testing.T) {
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	loads := 0
	cache := &permissionCache{entries: make(map[string]cachedPermissions), ttl: time.Minute, now: func() time.Time { return now }, load: func(context.Context, string) ([]store.Permission, error) { loads++; return nil, nil }}
	for range 2 {
		if _, errGet := cache.get(context.Background(), "user"); errGet != nil {
			t.Fatal(errGet)
		}
	}
	if loads != 1 {
		t.Fatal("permission cache did not avoid repeated reads")
	}
	now = now.Add(time.Minute)
	if _, errGet := cache.get(context.Background(), "user"); errGet != nil || loads != 2 {
		t.Fatal("expired permission cache did not reload")
	}
	started, release := make(chan struct{}), make(chan struct{})
	cache.invalidate()
	cache.load = func(context.Context, string) ([]store.Permission, error) { close(started); <-release; return nil, nil }
	done := make(chan error, 1)
	go func() { _, errGet := cache.get(context.Background(), "user"); done <- errGet }()
	<-started
	cache.invalidate()
	close(release)
	if errGet := <-done; errGet == nil {
		t.Fatal("stale allow-all lookup survived policy mutation")
	}
	if len(cache.entries) != 0 {
		t.Fatal("stale permissions repopulated cache")
	}
}

func TestPermissionMutationInvalidatesRetiredScopesOnSameDatabase(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	user, _, key := createTestIdentity(t, engine)
	ctx := usageContext(t, runtime, key)
	release, errLease := runtime.BeginRequest(ctx)
	if errLease != nil {
		t.Fatal(errLease)
	}
	defer release()
	target := sdkaccess.PolicyTarget{RequestedModel: "azure-mini", ResolvedModel: "azure-mini", Provider: "openai-compatible-azure-openai"}
	if errCheck := runtime.CheckPermissions(ctx, target); errCheck != nil {
		t.Fatal(errCheck)
	}
	_, cfg := runtime.Snapshot()
	disabled := cfg
	disabled.Enabled = false
	if errApply := runtime.Apply(context.Background(), disabled); errApply != nil {
		t.Fatal(errApply)
	}
	if errApply := runtime.Apply(context.Background(), cfg); errApply != nil {
		t.Fatal(errApply)
	}
	requestJSON(t, engine, http.MethodPut, "/users/"+user.ID+"/permissions", `{"permissions":[{"scope":"model","value":"azure-*","effect":"deny"}]}`, http.StatusOK)
	assertPermissionDenied(t, runtime.CheckPermissions(ctx, target))
	requestJSON(t, engine, http.MethodPatch, "/users/"+user.ID, `{"status":"disabled"}`, http.StatusOK)
	requireRequestUnauthorized(t, runtime, ctx)
}
