package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type handlerPolicyDenial struct{}

func (handlerPolicyDenial) Error() string   { return "model denied" }
func (handlerPolicyDenial) StatusCode() int { return http.StatusForbidden }
func (handlerPolicyDenial) ResponseBody() []byte {
	return []byte(`{"error":{"code":"model_not_permitted"}}`)
}

func TestPolicyFilterSeesAutoAndRouterVisibleNames(t *testing.T) {
	const authID, visible = "policy-auto-auth", "policy-visible-model"
	registry.GetGlobalRegistry().RegisterClient(authID, "policy-provider", []*registry.ModelInfo{{ID: visible}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	for _, route := range []string{"auto", "router"} {
		t.Run(route, func(t *testing.T) {
			handler := NewBaseAPIHandlers(&config.SDKConfig{}, nil)
			requested := "auto(high)"
			if route == "router" {
				requested = visible
				host := &handlerModelRouterTestHost{hasRouters: true}
				host.route = func(context.Context, pluginapi.ModelRouteRequest, string) (pluginapi.ModelRouteResponse, bool) {
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetProvider, Target: "policy-provider", TargetModel: "private-router-deployment"}, true
				}
				handler.SetModelRouterHost(host)
			}
			checked := false
			ctx := sdkaccess.WithRequestHooks(context.Background(), sdkaccess.RequestHooks{FilterProviders: func(_ context.Context, original, resolved string, providers []string) ([]string, error) {
				checked = true
				if original != requested || len(providers) == 0 {
					t.Errorf("incorrect filtering target: %q %q %v", original, resolved, providers)
				}
				if route == "router" && resolved != visible {
					t.Errorf("private router deployment became visible allow target: %q", resolved)
				}
				if route == "auto" && (resolved == requested || registry.GetGlobalRegistry().GetModelInfo(thinking.ParseSuffix(resolved).ModelName, "") == nil) {
					t.Errorf("auto did not resolve to a visible catalog model: %q", resolved)
				}
				return nil, handlerPolicyDenial{}
			}})
			_, _, err := handler.ExecuteWithAuthManager(ctx, "openai", requested, nil, "")
			if !checked || err == nil || err.StatusCode != http.StatusForbidden {
				t.Fatalf("routing bypassed provider filter: checked=%v error=%+v", checked, err)
			}
		})
	}
}

type policyPluginInterceptorHost struct {
	handlerDirectExecutorInterceptorHost
}

func (*policyPluginInterceptorHost) InterceptRequestAfterAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{Body: []byte(`{"model":"post-interceptor-denied"}`)}
}

func TestNestedPolicyKeepsVisibleAllowsAndIntermediateDenies(t *testing.T) {
	ctx := sdkaccess.WithRequestHooks(context.Background(), sdkaccess.RequestHooks{})
	ctx = requestPolicyContext(ctx, "outer-visible", "outer-visible", false)
	nested := requestPolicyContext(ctx, "inner-alias", "private-deployment", true)
	target, _ := sdkaccess.PolicyTargetFromContext(nested)
	if target.RequestedModel != "outer-visible" || target.ResolvedModel != "outer-visible" || len(target.DenyModels) != 2 || target.DenyModels[0] != "inner-alias" || target.DenyModels[1] != "private-deployment" {
		t.Fatalf("nested target lost visible/deny distinction: %+v", target)
	}
	target.DenyModels[0] = "mutated"
	again, _ := sdkaccess.PolicyTargetFromContext(nested)
	outer, _ := sdkaccess.PolicyTargetFromContext(ctx)
	next, _ := sdkaccess.PolicyTargetFromContext(requestPolicyContext(nested, "next-turn", "next-turn", false))
	if again.DenyModels[0] != "inner-alias" || len(outer.DenyModels) != 0 || len(next.DenyModels) != 0 || next.RequestedModel != "next-turn" {
		t.Fatal("nested policy mutated a parent or later WebSocket turn")
	}
}

func TestDirectPluginExecutionsCannotBypassPolicy(t *testing.T) {
	for _, path := range []string{"execute", "count", "stream"} {
		t.Run(path, func(t *testing.T) {
			host := &policyPluginInterceptorHost{}
			host.hasRouters = true
			host.route = func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
				return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: "denied-plugin"}, true
			}
			handler := NewBaseAPIHandlers(&config.SDKConfig{}, nil)
			handler.SetPluginHost(host)
			var target sdkaccess.PolicyTarget
			ctx := sdkaccess.WithRequestHooks(context.Background(), sdkaccess.RequestHooks{Authorize: func(_ context.Context, candidate sdkaccess.PolicyTarget) error {
				target = candidate
				return handlerPolicyDenial{}
			}})
			var status int
			switch path {
			case "execute":
				_, _, err := handler.ExecuteWithAuthManager(ctx, "openai", "public-alias", []byte(`{"model":"public-alias"}`), "")
				if err != nil {
					status = err.StatusCode
				}
			case "count":
				_, _, err := handler.ExecuteCountWithAuthManager(ctx, "openai", "public-alias", []byte(`{"model":"public-alias"}`), "")
				if err != nil {
					status = err.StatusCode
				}
			case "stream":
				_, _, errs := handler.ExecuteStreamWithAuthManager(ctx, "openai", "public-alias", []byte(`{"model":"public-alias"}`), "")
				if err := <-errs; err != nil {
					status = err.StatusCode
				}
			}
			if status != http.StatusForbidden || target.Provider != "plugin:denied-plugin" || target.RequestedModel != "public-alias" || target.PayloadModel != "post-interceptor-denied" || host.lastPluginID != "" {
				t.Fatalf("plugin policy bypass: status=%d target=%+v executed=%q", status, target, host.lastPluginID)
			}
		})
	}
}
