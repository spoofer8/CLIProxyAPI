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

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type budgetAliasExecutor struct {
	coreauth.ProviderExecutor
	runtime *usermgmt.Runtime
}

func (budgetAliasExecutor) Identifier() string { return "openai-compatible-ui-budget-alias" }
func (e budgetAliasExecutor) Execute(ctx context.Context, _ *coreauth.Auth, request coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.runtime.HandleUsage(ctx, usage.Record{EventID: "budget-alias-execution", Provider: e.Identifier(), Model: request.Model, RequestedAt: time.Now(), Detail: usage.Detail{TokenBreakdown: usage.NewSubsetTokenBreakdown(12, 0, 0, 2, 0, 14)}})
	return coreexecutor.Response{Payload: []byte(`{"id":"resp_budget_alias","object":"response","output":[]}`)}, nil
}
func TestBudgetedHTTPConfiguredAliasUsesCanonicalPrice(t *testing.T) {
	directory := t.TempDir()
	dsn := userManagementHTTPTestDSN(t)
	preserveConfigAccessProvider(t)
	var runtime usermgmt.Runtime
	t.Cleanup(func() {
		if e := runtime.Close(); e != nil {
			t.Error(e)
		}
	})
	cfg := &config.Config{AuthDir: filepath.Join(directory, "auths"), CommercialMode: true}
	cfg.UserManagement = config.UserManagementConfig{Enabled: true, DSN: dsn, RequestActivity: config.UserManagementRequestActivityConfig{SpoolDirectory: filepath.Join(directory, "spool")}}
	cfg.OpenAICompatibility = []config.OpenAICompatibility{{Name: "ui-budget-alias", Models: []config.OpenAICompatibilityModel{{Name: "fixture-upstream", Alias: "fixture-model"}}}}
	if e := runtime.Apply(context.Background(), cfg.UserManagement); e != nil {
		t.Fatal(e)
	}
	db, _ := runtime.Snapshot()
	const userID = "01KKKKKKKKKKKKKKKKKKKKKKK1"
	if _, e := db.CreateUser(context.Background(), store.User{ID: userID, Email: "budget-alias@example.test", Role: "user", Status: "active"}); e != nil {
		t.Fatal(e)
	}
	const rawKey = "sk-cpa-budget-alias-fixture-key"
	hash := sha256.Sum256([]byte(rawKey))
	if _, e := db.CreateKey(context.Background(), store.APIKey{ID: "01MMMMMMMMMMMMMMMMMMMMMMM1", UserID: userID, KeyPrefix: rawKey[:12]}, hex.EncodeToString(hash[:])); e != nil {
		t.Fatal(e)
	}
	limit := "1"
	if e := db.SetBudget(context.Background(), userID, store.BudgetLimits{DailyUSD: &limit}); e != nil {
		t.Fatal(e)
	}
	executor := budgetAliasExecutor{runtime: &runtime}
	if e := db.SavePrice(context.Background(), store.ModelPrice{ID: "canonical-budget-price", Provider: executor.Identifier(), Model: "fixture-upstream", PriceRates: store.PriceRates{InputUSD: "1", OutputUSD: "2"}, Manual: true, UpdatedAt: time.Now().Add(-time.Minute)}); e != nil {
		t.Fatal(e)
	}
	if e := db.ReplacePermissions(context.Background(), userID, []store.Permission{{Scope: "model", Value: "fixture-model", Effect: "allow"}}); e != nil {
		t.Fatal(e)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(executor)
	const authID = "budget-alias-auth"
	if _, e := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: executor.Identifier(), Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "fake-upstream", "compat_name": "ui-budget-alias", "provider_key": executor.Identifier()}}); e != nil {
		t.Fatal(e)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, executor.Identifier(), []*registry.ModelInfo{{ID: "fixture-model", Object: "model", OwnedBy: executor.Identifier()}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	server := NewServer(cfg, manager, sdkaccess.NewManager(), filepath.Join(directory, "config.yaml"), WithUserManagement(&runtime))
	httpServer := httptest.NewServer(server.engine)
	t.Cleanup(httpServer.Close)
	request, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/responses", strings.NewReader(`{"model":"fixture-model","input":"budgeted alias fixture"}`))
	request.Header.Set("Authorization", "Bearer "+rawKey)
	request.Header.Set("Content-Type", "application/json")
	userManagementHTTPResponse(t, httpServer.Client(), request, http.StatusOK)
	events, total, e := db.ListCostEvents(context.Background(), userID, 10, 0)
	if e != nil || total != 1 || events[0].Model != "fixture-upstream" || events[0].CostUSD == nil || *events[0].CostUSD != "0.000016" {
		t.Fatalf("price admission and accounting did not use same billable model: count=%d error=%v", total, e)
	}
}
