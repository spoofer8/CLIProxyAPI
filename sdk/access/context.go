package access

import "context"

type resultContextKey struct{}

// WithResult stores an immutable authentication snapshot. A nil result clears
// any inherited result. The provider's mutable metadata map is never retained.
func WithResult(ctx context.Context, result *Result) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, resultContextKey{}, cloneResult(result))
}

// ResultFromContext returns a copy of the authenticated identity and metadata,
// safe to retain after the HTTP handler completes or its Gin context is reused.
func ResultFromContext(ctx context.Context) (*Result, bool) {
	if ctx == nil {
		return nil, false
	}
	result, ok := ctx.Value(resultContextKey{}).(*Result)
	if !ok || result == nil {
		return nil, false
	}
	return cloneResult(result), true
}

func cloneResult(result *Result) *Result {
	if result == nil {
		return nil
	}
	cloned := *result
	if result.Metadata != nil {
		cloned.Metadata = make(map[string]string, len(result.Metadata))
		for key, value := range result.Metadata {
			cloned.Metadata[key] = value
		}
	}
	return &cloned
}
