package access

import "context"

// RequestHooks carries service-local admission and lifetime callbacks. It is
// attached only to authenticated user requests; legacy callers have no hooks.
type RequestHooks struct {
	Check func(context.Context) error
	Begin func(context.Context) (func(), error)
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
