package auth

import (
	"context"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type testPolicyDenial struct{}

func (testPolicyDenial) Error() string   { return "model denied" }
func (testPolicyDenial) StatusCode() int { return http.StatusForbidden }
func (testPolicyDenial) ResponseBody() []byte {
	return []byte(`{"error":{"code":"model_not_permitted"}}`)
}

func TestRequestPolicyChecksActualAliasAndPostInterceptorPayload(t *testing.T) {
	for _, path := range []string{"execute", "count", "stream"} {
		t.Run(path, func(t *testing.T) {
			executor := &openAICompatPoolExecutor{id: openAICompatPoolProviderKey}
			manager := newOpenAICompatPoolTestManager(t, "public-alias", []internalconfig.OpenAICompatibilityModel{{Name: "private-deployment", Alias: "public-alias"}}, executor)
			var target sdkaccess.PolicyTarget
			ctx := sdkaccess.WithRequestHooks(context.Background(), sdkaccess.RequestHooks{Authorize: func(_ context.Context, candidate sdkaccess.PolicyTarget) error {
				target = candidate
				return testPolicyDenial{}
			}})
			ctx = sdkaccess.WithPolicyTarget(ctx, sdkaccess.PolicyTarget{RequestedModel: "public-alias(high)", ResolvedModel: "public-alias(high)"})
			opts := cliproxyexecutor.Options{RequestAfterAuthInterceptor: func(context.Context, cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
				return cliproxyexecutor.RequestAfterAuthInterceptResponse{Body: []byte(`{"model":"rewritten-denied"}`)}
			}}
			req := cliproxyexecutor.Request{Model: "public-alias(high)", Payload: []byte(`{"model":"public-alias(high)"}`)}
			var err error
			switch path {
			case "execute":
				_, err = manager.Execute(ctx, []string{openAICompatPoolProviderKey}, req, opts)
			case "count":
				_, err = manager.ExecuteCount(ctx, []string{openAICompatPoolProviderKey}, req, opts)
			case "stream":
				_, err = manager.ExecuteStream(ctx, []string{openAICompatPoolProviderKey}, req, opts)
			}
			if !sdkaccess.IsPolicyError(err) {
				t.Fatalf("expected terminal policy error, got %v", err)
			}
			if target.RequestedModel != "public-alias(high)" || target.ResolvedModel != "public-alias(high)" || target.ExecutionModel != "private-deployment(high)" || target.PayloadModel != "rewritten-denied" || target.Provider != openAICompatPoolProviderKey {
				t.Fatalf("incorrect final target: %+v", target)
			}
			if len(executor.ExecuteModels())+len(executor.CountModels())+len(executor.StreamModels()) != 0 {
				t.Fatal("denied request reached the executor")
			}
		})
	}
}

func TestPolicyRejectionAfterFailedAttemptIsTerminal(t *testing.T) {
	executor := &openAICompatPoolExecutor{id: openAICompatPoolProviderKey, executeErrors: map[string]error{"first": &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream unavailable"}}}
	manager := newOpenAICompatPoolTestManager(t, "public-pool", []internalconfig.OpenAICompatibilityModel{{Name: "first", Alias: "public-pool"}, {Name: "second", Alias: "public-pool"}}, executor)
	manager.SetRetryConfig(3, 0, 0)
	ctx := sdkaccess.WithRequestHooks(context.Background(), sdkaccess.RequestHooks{Authorize: func(_ context.Context, target sdkaccess.PolicyTarget) error {
		if target.ExecutionModel == "second" {
			return testPolicyDenial{}
		}
		return nil
	}})
	_, err := manager.Execute(ctx, []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: "public-pool"}, cliproxyexecutor.Options{})
	if !sdkaccess.IsPolicyError(err) {
		t.Fatalf("previous upstream error replaced policy denial: %v", err)
	}
	if calls := executor.ExecuteModels(); len(calls) != 1 || calls[0] != "first" {
		t.Fatalf("denied fallback executed or retried: %v", calls)
	}
}

func TestPolicyPreviewDoesNotRotateAliasPools(t *testing.T) {
	manager := newOpenAICompatPoolTestManager(t, "public-pool", []internalconfig.OpenAICompatibilityModel{{Name: "first", Alias: "public-pool"}, {Name: "second", Alias: "public-pool"}}, nil)
	auth := manager.List()[0]
	first := manager.PreviewExecutionModels(auth, "public-pool")
	first[0] = "mutated"
	second := manager.PreviewExecutionModels(auth, "public-pool")
	actual := manager.executionModelCandidates(auth, "public-pool")
	if len(second) != 2 || second[0] != "first" || actual[0] != "first" {
		t.Fatalf("preview mutated or rotated routing: preview=%v actual=%v", second, actual)
	}
}

func TestHomeExecutionPoliciesUseActualProvider(t *testing.T) {
	for _, path := range []string{"execute", "count", "stream"} {
		t.Run(path, func(t *testing.T) {
			executor := &homeRequestMetadataExecutor{}
			manager := newHomeRequestMetadataManager(t, executor, nil)
			var provider string
			ctx := sdkaccess.WithRequestHooks(context.Background(), sdkaccess.RequestHooks{Authorize: func(_ context.Context, target sdkaccess.PolicyTarget) error {
				provider = target.Provider
				return testPolicyDenial{}
			}})
			req, opts := cliproxyexecutor.Request{Model: "route-model"}, homeRequestMetadataOptions("default")
			var err error
			switch path {
			case "execute":
				_, err = manager.Execute(ctx, []string{"home"}, req, opts)
			case "count":
				_, err = manager.ExecuteCount(ctx, []string{"home"}, req, opts)
			case "stream":
				_, err = manager.ExecuteStream(ctx, []string{"home"}, req, opts)
			}
			if !sdkaccess.IsPolicyError(err) || provider != "home-execution" {
				t.Fatalf("Home policy did not check actual provider: provider=%q error=%v", provider, err)
			}
			_, executed := executor.snapshots()
			if executed.requestedModel != "" {
				t.Fatal("denied Home executor was invoked")
			}
		})
	}
}

func TestCreditsFallbackPolicyIsTerminal(t *testing.T) {
	const model, authID = "claude-credits-policy-model", "credits-policy-auth"
	executor := &antigravityCreditsFallbackExecutor{}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true}})
	manager.RegisterExecutor(executor)
	registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	if _, err := manager.Register(context.Background(), &Auth{ID: authID, Provider: "antigravity"}); err != nil {
		t.Fatal(err)
	}
	ctx := sdkaccess.WithRequestHooks(context.Background(), sdkaccess.RequestHooks{Authorize: func(ctx context.Context, target sdkaccess.PolicyTarget) error {
		if AntigravityCreditsRequested(ctx) {
			return testPolicyDenial{}
		}
		return nil
	}})
	_, err := manager.ExecuteStream(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if !sdkaccess.IsPolicyError(err) {
		t.Fatalf("credits policy denial was hidden or bypassed: %v", err)
	}
	for _, credits := range executor.streamCreditsRequested {
		if credits {
			t.Fatal("denied credits fallback reached executor")
		}
	}
}
