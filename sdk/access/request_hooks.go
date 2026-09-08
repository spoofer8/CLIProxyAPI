package access

import "context"

// RequestHooks carries service-local admission and lifetime callbacks. It is
// attached only to authenticated user requests; legacy callers have no hooks.
type RequestHooks struct {
	Check           func(context.Context) error
	Begin           func(context.Context) (func(), error)
	Authorize       func(context.Context, PolicyTarget) error
	FilterProviders func(context.Context, string, string, []string) ([]string, error)
	// CopyContext carries service-owned, immutable request correlation into SDK contexts.
	CopyContext func(context.Context, context.Context) context.Context
	// Capture records an original WebSocket turn and returns its completion callback.
	Capture func(context.Context, string, []byte) (context.Context, func(int))
}

// PolicyTarget distinguishes client-visible model names from names rewritten
// for the selected upstream. Provider identifies the actual selected provider.
type PolicyTarget struct {
	RequestedModel string
	ResolvedModel  string
	ExecutionModel string
	PayloadModel   string
	Provider       string
	// DenyModels preserves intermediate nested names without granting allows.
	DenyModels []string
}

type policyTargetContextKey struct{}

func WithPolicyTarget(ctx context.Context, target PolicyTarget) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	target.DenyModels = append([]string(nil), target.DenyModels...)
	return context.WithValue(ctx, policyTargetContextKey{}, target)
}

func PolicyTargetFromContext(ctx context.Context) (PolicyTarget, bool) {
	if ctx == nil {
		return PolicyTarget{}, false
	}
	target, ok := ctx.Value(policyTargetContextKey{}).(PolicyTarget)
	target.DenyModels = append([]string(nil), target.DenyModels...)
	return target, ok
}

type requestHooksContextKey struct{}

func WithRequestHooks(ctx context.Context, hooks RequestHooks) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestHooksContextKey{}, hooks)
}

func RequestHooksFromContext(ctx context.Context) (RequestHooks, bool) {
	if ctx == nil {
		return RequestHooks{}, false
	}
	hooks, ok := ctx.Value(requestHooksContextKey{}).(RequestHooks)
	return hooks, ok
}

// BeginRequestLease acquires a producer lifetime without repeating admission.
func BeginRequestLease(ctx context.Context) (func(), error) {
	if hooks, ok := RequestHooksFromContext(ctx); ok && hooks.Begin != nil {
		release, err := hooks.Begin(ctx)
		if release != nil || err != nil {
			return release, err
		}
	}
	return func() {}, nil
}
