package auth

import (
	"context"
	"strings"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

func executionPolicyTarget(ctx context.Context, executor ProviderExecutor, auth *Auth, req cliproxyexecutor.Request) sdkaccess.PolicyTarget {
	target, _ := sdkaccess.PolicyTargetFromContext(ctx)
	if target.RequestedModel == "" {
		target.RequestedModel = coreusage.RequestedModelAliasFromContext(ctx)
	}
	if target.RequestedModel == "" {
		target.RequestedModel = req.Model
	}
	target.ExecutionModel = req.Model
	target.PayloadModel = gjson.GetBytes(req.Payload, "model").String()
	target.Provider = ""
	if executor != nil {
		target.Provider = strings.ToLower(strings.TrimSpace(executor.Identifier()))
	}
	if target.Provider == "" && auth != nil && strings.TrimSpace(auth.Provider) != "" {
		target.Provider = strings.ToLower(strings.TrimSpace(auth.Provider))
	}
	return target
}

func withExecutionPolicy(ctx context.Context, executor ProviderExecutor, auth *Auth, req cliproxyexecutor.Request) context.Context {
	if hooks, ok := sdkaccess.RequestHooksFromContext(ctx); !ok || hooks.Authorize == nil {
		return ctx
	}
	return sdkaccess.WithPolicyTarget(ctx, executionPolicyTarget(ctx, executor, auth, req))
}

func authorizeExecutionRequest(ctx context.Context, executor ProviderExecutor, auth *Auth, req cliproxyexecutor.Request) error {
	if hooks, ok := sdkaccess.RequestHooksFromContext(ctx); !ok || hooks.Authorize == nil {
		return nil
	}
	return sdkaccess.AuthorizeRequest(ctx, executionPolicyTarget(ctx, executor, auth, req))
}

func executeWithRequestPolicy(ctx context.Context, executor ProviderExecutor, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, count bool) (cliproxyexecutor.Response, error) {
	ctx = withExecutionPolicy(ctx, executor, auth, req)
	if err := authorizeExecutionRequest(ctx, executor, auth, req); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	if count {
		response, err := executor.CountTokens(ctx, auth, req, opts)
		return response, sdkaccess.NormalizePolicyError(err)
	}
	response, err := executor.Execute(ctx, auth, req, opts)
	return response, sdkaccess.NormalizePolicyError(err)
}
