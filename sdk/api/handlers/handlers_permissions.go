package handlers

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func requestPolicyContext(ctx context.Context, requested, resolved string, internal bool) context.Context {
	if _, ok := sdkaccess.RequestHooksFromContext(ctx); !ok {
		return ctx
	}
	if inherited, ok := sdkaccess.PolicyTargetFromContext(ctx); ok && internal {
		for _, model := range []string{requested, resolved} {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			found := false
			for _, existing := range inherited.DenyModels {
				if existing == model {
					found = true
					break
				}
			}
			if !found {
				inherited.DenyModels = append(inherited.DenyModels, model)
			}
		}
		return sdkaccess.WithPolicyTarget(ctx, inherited)
	}
	visible := strings.TrimSpace(resolved)
	// A router target may be a private deployment. Only catalog model names can
	// supplement the caller's visible alias in an allowlist.
	if visible != requested && registry.GetGlobalRegistry().GetModelInfo(thinking.ParseSuffix(visible).ModelName, "") == nil {
		visible = requested
	}
	return sdkaccess.WithPolicyTarget(ctx, sdkaccess.PolicyTarget{RequestedModel: requested, ResolvedModel: visible})
}

func filterRequestProviders(ctx context.Context, providers []string) ([]string, *interfaces.ErrorMessage) {
	hooks, ok := sdkaccess.RequestHooksFromContext(ctx)
	if !ok || hooks.FilterProviders == nil {
		return providers, nil
	}
	target, _ := sdkaccess.PolicyTargetFromContext(ctx)
	filtered, err := hooks.FilterProviders(ctx, target.RequestedModel, target.ResolvedModel, append([]string(nil), providers...))
	if err != nil {
		return nil, requestHookError(err)
	}
	return filtered, nil
}

func pluginRequestPolicyContext(ctx context.Context, provider string, req coreexecutor.Request) context.Context {
	if hooks, ok := sdkaccess.RequestHooksFromContext(ctx); !ok || hooks.Authorize == nil {
		return ctx
	}
	target, _ := sdkaccess.PolicyTargetFromContext(ctx)
	target.Provider = "plugin:" + provider
	target.ExecutionModel = req.Model
	target.PayloadModel = gjson.GetBytes(req.Payload, "model").String()
	return sdkaccess.WithPolicyTarget(ctx, target)
}

func authorizePluginRequest(ctx context.Context, provider string, req coreexecutor.Request) *interfaces.ErrorMessage {
	target, _ := sdkaccess.PolicyTargetFromContext(pluginRequestPolicyContext(ctx, provider, req))
	if err := sdkaccess.AuthorizeRequest(ctx, target); err != nil {
		return requestHookError(err)
	}
	return nil
}
