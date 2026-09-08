package usermgmt

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

type RequestCaptureMetadata struct {
	Method, Path, Model string
	Body                []byte
	BodyOmittedReason   string
}

type RequestCaptureResult struct {
	StatusCode        int
	Body              []byte
	BodyOmittedReason string
	Model             string
}
type requestCaptureContextKey struct{}
type requestCaptureIdentity struct{ id, scopeID, userID string }

// CopyRequestCaptureContext preserves correlation when SDK execution deliberately
// detaches from the incoming HTTP cancellation context.
func CopyRequestCaptureContext(dst, src context.Context) context.Context {
	if dst == nil {
		dst = context.Background()
	}
	if src == nil {
		return dst
	}
	if identity, ok := src.Value(requestCaptureContextKey{}).(requestCaptureIdentity); ok {
		return context.WithValue(dst, requestCaptureContextKey{}, identity)
	}
	return dst
}

// BeginRequestCapture never rejects a proxy request. Identity and correlation
// are copied from the authenticated request, never from mutable Gin state.
func (r *Runtime) BeginRequestCapture(ctx context.Context, metadata RequestCaptureMetadata) (context.Context, func(RequestCaptureResult)) {
	noop := func(RequestCaptureResult) {}
	if ctx == nil {
		ctx = context.Background()
	}
	identity, exists := sdkaccess.ResultFromContext(ctx)
	if r == nil || !exists || identity.Provider != AccessProviderName || identity.Principal == "" {
		return ctx, noop
	}
	r.mu.Lock()
	if r.store == nil || !r.cfg.RequestActivity.CaptureEnabled() {
		r.mu.Unlock()
		return ctx, noop
	}
	scope := r.scopes[identity.Metadata[UsageScopeMetadataKey]]
	if scope == nil || (scope.retired && scope.leases == 0) || !scope.cfg.RequestActivity.CaptureEnabled() {
		r.mu.Unlock()
		return ctx, noop
	}
	scope.leases++
	r.mu.Unlock()
	id, errID := newID()
	if errID != nil {
		r.releaseScope(scope)
		return ctx, noop
	}
	started := time.Now()
	body, truncated, omitted := requestBodyPreview(metadata.Body, metadata.BodyOmittedReason)
	item := store.RequestActivity{RequestActivitySummary: store.RequestActivitySummary{
		ID: id, UserID: identity.Principal, KeyID: activityText(identity.Metadata["key_id"], 128), At: started.UTC(),
		Method: activityText(metadata.Method, 16), Path: activityText(strings.SplitN(metadata.Path, "?", 2)[0], 2048), Model: activityText(metadata.Model, 256),
	}, BodyPreview: body, BodyTruncated: truncated, BodyOmittedReason: omitted}
	if !scope.activity.enqueue(requestActivityJob{initial: &item}) {
		r.releaseScope(scope)
		return ctx, noop
	}
	correlation := requestCaptureIdentity{id: id, scopeID: scope.id, userID: identity.Principal}
	var once sync.Once
	return context.WithValue(ctx, requestCaptureContextKey{}, correlation), func(result RequestCaptureResult) {
		once.Do(func() {
			defer r.releaseScope(scope)
			completed := item
			if result.Body != nil || result.BodyOmittedReason != "" {
				completed.BodyPreview, completed.BodyTruncated, completed.BodyOmittedReason = requestBodyPreview(result.Body, result.BodyOmittedReason)
			}
			if result.Model != "" {
				completed.Model = activityText(result.Model, 256)
			}
			scope.activity.enqueue(requestActivityJob{id: id, status: result.StatusCode, durationMS: time.Since(started).Milliseconds(), completion: &completed})
		})
	}
}

func activityText(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, value)
	if len(value) > limit {
		value = value[:limit]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

func (r *Runtime) recordRequestActivityUsage(ctx context.Context, scope *usageScope, provider string, delta store.UsageIncrement) {
	correlation, ok := ctx.Value(requestCaptureContextKey{}).(requestCaptureIdentity)
	if !ok || correlation.scopeID != scope.id || correlation.userID != delta.UserID {
		return
	}
	scope.activity.enqueue(requestActivityJob{id: correlation.id, provider: activityText(provider, 128), usage: &delta})
}

// FlushRequestActivity is intended for shutdown coordination and diagnostics;
// the request path only uses nonblocking enqueue operations.
func (r *Runtime) FlushRequestActivity(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	writers := make([]*requestActivityWriter, 0, len(r.scopes))
	for _, scope := range r.scopes {
		writers = append(writers, scope.activity)
	}
	r.mu.RUnlock()
	for _, writer := range writers {
		if errFlush := writer.flush(ctx); errFlush != nil {
			return errFlush
		}
	}
	return nil
}
