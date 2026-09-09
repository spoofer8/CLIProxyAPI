package usermgmt

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

type RequestCaptureMetadata struct {
	Method, Path, Model                          string
	Body                                         []byte
	BodyOmittedReason                            string
	SessionID, SessionSource, PreviousResponseID string
}
type RequestCaptureResult struct {
	StatusCode               int
	Body                     []byte
	BodyOmittedReason, Model string
}
type requestCaptureContextKey struct{}
type requestCaptureIdentity struct {
	id, scopeID, userID string
	state               *requestCaptureState
}
type requestCaptureState struct {
	mu         sync.Mutex
	runtime    *Runtime
	scope      *usageScope
	ctx        context.Context
	journal    *activityJournal
	item       store.RequestActivity
	started    time.Time
	producers  int
	clientDone bool
	financial  bool
	finalOnce  sync.Once
}

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
func captureState(ctx context.Context) *requestCaptureState {
	if ctx == nil {
		return nil
	}
	identity, _ := ctx.Value(requestCaptureContextKey{}).(requestCaptureIdentity)
	return identity.state
}

// BeginDurableRequestCapture commits admission and creates an fsynced recovery
// journal before returning permission to forward any client content upstream.
func (r *Runtime) BeginDurableRequestCapture(ctx context.Context, metadata RequestCaptureMetadata) (context.Context, func(RequestCaptureResult), error) {
	noop := func(RequestCaptureResult) {}
	if ctx == nil {
		ctx = context.Background()
	}
	identity, exists := sdkaccess.ResultFromContext(ctx)
	if !exists || identity.Provider != AccessProviderName || identity.Principal == "" {
		return ctx, noop, nil
	}
	if r == nil {
		return ctx, noop, errors.New("activity storage unavailable")
	}
	r.mu.Lock()
	scope := r.scopes[identity.Metadata[UsageScopeMetadataKey]]
	if scope == nil || (scope.retired && scope.leases == 0) {
		r.mu.Unlock()
		return ctx, noop, errors.New("activity storage unavailable")
	}
	scope.leases++
	r.mu.Unlock()
	id, errID := newID()
	if errID != nil {
		r.releaseScope(scope)
		return ctx, noop, errID
	}
	started := time.Now()
	item := store.RequestActivity{RequestActivitySummary: store.RequestActivitySummary{ID: id, UserID: identity.Principal,
		KeyID: activityText(identity.Metadata["key_id"], 128), At: started.UTC(), Method: activityText(metadata.Method, 16),
		Path: activityText(strings.SplitN(metadata.Path, "?", 2)[0], 2048), Model: activityText(metadata.Model, 256),
		SessionID: activityText(metadata.SessionID, 512), SessionSource: activityText(metadata.SessionSource, 128),
		PreviousResponseID: activityText(metadata.PreviousResponseID, 256), CaptureState: "in_progress"}}
	admissionCtx := ctx
	if err := scope.store.InsertRequestActivity(admissionCtx, item); err != nil {
		r.releaseScope(scope)
		return ctx, noop, err
	}
	if err := scope.store.CompleteRequestActivityContent(admissionCtx, item, "in_progress"); err != nil {
		r.releaseScope(scope)
		return ctx, noop, err
	}
	journal, errJournal := scope.activity.begin(item)
	if errJournal != nil {
		_ = scope.store.SetRequestActivityCaptureState(admissionCtx, id, "admission_failed")
		r.releaseScope(scope)
		return ctx, noop, errJournal
	}
	state := &requestCaptureState{runtime: r, scope: scope, ctx: context.WithoutCancel(ctx), journal: journal, item: item, started: started, financial: billableActivity(metadata.Method, metadata.Path)}
	capturedCtx := context.WithValue(ctx, requestCaptureContextKey{}, requestCaptureIdentity{id: id, scopeID: scope.id, userID: identity.Principal, state: state})
	if state.financial {
		if err := r.beginFinancialRequest(admissionCtx, scope, id, identity.Principal, metadata.Model, started); err != nil {
			item.StatusCode = http.StatusServiceUnavailable
			_ = journal.append(activityJournalRecord{Kind: "finish", Item: &item})
			_ = journal.close()
			r.releaseScope(scope)
			return ctx, noop, err
		}
	}
	if metadata.Body != nil {
		if err := r.RecordCapturedContent(capturedCtx, "request", "json", metadata.Body); err != nil {
			state.finish(RequestCaptureResult{StatusCode: 503})
			return ctx, noop, err
		}
	}
	return capturedCtx, state.finish, nil
}

// Kept for source compatibility; actual transports use the error-returning API.
func (r *Runtime) BeginRequestCapture(ctx context.Context, metadata RequestCaptureMetadata) (context.Context, func(RequestCaptureResult)) {
	captured, finish, _ := r.BeginDurableRequestCapture(ctx, metadata)
	return captured, finish
}

func billableActivity(method, path string) bool {
	if method != "POST" && method != "WS" {
		return false
	}
	path = strings.SplitN(path, "?", 2)[0]
	return !strings.HasSuffix(path, "/count_tokens") && !strings.HasSuffix(path, ":countTokens")
}

// beginCapturedProducer is paired with each scope producer lease. Client
// completion alone cannot finalize financial usage while detached work remains.
func beginCapturedProducer(ctx context.Context) func() {
	state := captureState(ctx)
	if state == nil {
		return func() {}
	}
	state.mu.Lock()
	state.producers++
	state.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			state.mu.Lock()
			state.producers--
			ready := state.clientDone && state.producers == 0
			state.mu.Unlock()
			if ready {
				state.finalize()
			}
		})
	}
}

func (r *Runtime) RecordCapturedContent(ctx context.Context, direction, format string, body []byte) error {
	state := captureState(ctx)
	if state == nil {
		return nil
	}
	switch direction {
	case "request", "request_raw", "response":
	default:
		return errors.New("invalid activity direction")
	}
	switch format {
	case "json", "sse", "ws", "text", "zstd", "gzip", "raw":
	default:
		format = "raw"
	}
	if err := state.journal.append(activityJournalRecord{Kind: "content", Direction: direction, Format: format, Body: body}); err != nil {
		return err
	}
	return nil
}

func (state *requestCaptureState) finish(result RequestCaptureResult) {
	state.mu.Lock()
	if state.clientDone {
		state.mu.Unlock()
		return
	}
	state.item.StatusCode = result.StatusCode
	state.item.DurationMS = time.Since(state.started).Milliseconds()
	if result.Model != "" {
		state.item.Model = activityText(result.Model, 256)
	}
	if result.BodyOmittedReason != "" {
		state.item.BodyOmittedReason = safeBodyOmissionReason(result.BodyOmittedReason)
	}
	if result.Body != nil {
		_ = state.journal.append(activityJournalRecord{Kind: "content", Direction: "request", Format: "json", Body: result.Body})
	}
	state.clientDone = true
	ready := state.producers == 0
	state.mu.Unlock()
	if ready {
		state.finalize()
	}
}
func (state *requestCaptureState) finalize() {
	state.finalOnce.Do(func() {
		defer state.runtime.releaseScope(state.scope)
		if state.financial {
			_ = state.runtime.finishFinancialRequest(state.ctx, state.scope, state.item.ID, state.item.StatusCode)
		}
		if err := state.journal.append(activityJournalRecord{Kind: "finish", Item: &state.item}); err != nil {
			_ = state.scope.store.SetRequestActivityCaptureState(state.ctx, state.item.ID, "interrupted")
		}
		_ = state.journal.close()
		select {
		case state.scope.activity.wake <- struct{}{}:
		default:
		}
	})
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
	_ = scope.store.AddRequestActivityUsage(context.WithoutCancel(ctx), correlation.id, activityText(provider, 128), delta)
}
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
		if err := writer.flush(ctx); err != nil {
			return err
		}
	}
	return nil
}
