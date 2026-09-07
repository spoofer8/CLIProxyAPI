package usermgmt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

const quotaCacheTTL = 2 * time.Second

type QuotaError struct {
	Status  int
	Body    []byte
	message string
}

func (e *QuotaError) Error() string        { return e.message }
func (e *QuotaError) StatusCode() int      { return e.Status }
func (e *QuotaError) ResponseBody() []byte { return append([]byte(nil), e.Body...) }

func quotaError(status int, message, kind, code string) *QuotaError {
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"message": message, "type": kind, "code": code}})
	return &QuotaError{Status: status, Body: body, message: message}
}

func unavailableScopeError() *QuotaError {
	return quotaError(http.StatusUnauthorized, "User account is no longer available for this request", "authentication_error", "invalid_api_key")
}

type quotaCacheKey struct{ userID, period string }
type quotaSnapshot struct {
	user    store.User
	total   int64
	expires time.Time
}

type quotaCache struct {
	mu         sync.Mutex
	generation uint64
	entries    map[quotaCacheKey]quotaSnapshot
	now        func() time.Time
}

func newQuotaCache() *quotaCache {
	return &quotaCache{entries: make(map[quotaCacheKey]quotaSnapshot), now: time.Now}
}

func (q *quotaCache) invalidate() {
	q.mu.Lock()
	q.generation++
	clear(q.entries)
	q.mu.Unlock()
}

func (q *quotaCache) timeNow() time.Time {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.now()
}

func (q *quotaCache) get(ctx context.Context, db *store.Store, userID, period string) (quotaSnapshot, error) {
	key := quotaCacheKey{userID: userID, period: period}
	q.mu.Lock()
	now := q.now()
	if cached, exists := q.entries[key]; exists && now.Before(cached.expires) {
		q.mu.Unlock()
		return cached, nil
	}
	generation := q.generation
	q.mu.Unlock()
	user, errUser := db.GetUser(ctx, userID)
	if errUser != nil {
		return quotaSnapshot{}, errUser
	}
	monthly, errUsage := db.GetMonthlyUsage(ctx, userID, period)
	if errUsage != nil {
		return quotaSnapshot{}, errUsage
	}
	entry := quotaSnapshot{user: user, total: monthly.TotalTokens, expires: now.Add(quotaCacheTTL)}
	q.mu.Lock()
	// Avoid caching a query that raced with a committed counter or mutation.
	// Concurrent/async usage can still overshoot a pre-request check, by design.
	if generation == q.generation {
		if len(q.entries) >= maxCachedKeys {
			clear(q.entries)
		}
		q.entries[key] = entry
	}
	q.mu.Unlock()
	return entry, nil
}

// CheckQuota is called before each logical upstream execution, including each
// Responses WebSocket frame. Legacy principals bypass it. Calendar months and
// reset dates are UTC; <=0 effective limits and enforce:false never reject.
func (r *Runtime) CheckQuota(ctx context.Context) *QuotaError {
	identity, exists := sdkaccess.ResultFromContext(ctx)
	if !exists || identity.Provider != AccessProviderName {
		return nil
	}
	if r == nil {
		return unavailableScopeError()
	}
	release, errLease := r.BeginRequest(ctx)
	if errLease != nil {
		return unavailableScopeError()
	}
	defer release()
	r.mu.RLock()
	scope := r.scopes[identity.Metadata[UsageScopeMetadataKey]]
	if scope == nil {
		r.mu.RUnlock()
		return unavailableScopeError()
	}
	cfg := scope.cfg
	r.mu.RUnlock()
	if !cfg.Quota.Enforce {
		return nil
	}
	// The lease keeps the selected store alive without holding the runtime
	// mutex across a database read. Forced cleanup cancels that read as well.
	queryCtx, cancel := context.WithCancel(ctx)
	stopCleanup := context.AfterFunc(scope.cleanupCtx, cancel)
	defer func() { stopCleanup(); cancel() }()
	now := scope.accountant.quota.timeNow().UTC()
	period := now.Format("2006-01")
	snapshot, errSnapshot := scope.accountant.quota.get(queryCtx, scope.store, identity.Principal, period)
	if errSnapshot != nil {
		return quotaError(http.StatusServiceUnavailable, "Monthly usage is temporarily unavailable", "server_error", "usage_unavailable")
	}
	if snapshot.user.Status != "active" {
		return unavailableScopeError()
	}
	limit := cfg.Quota.DefaultMonthlyTokens
	if snapshot.user.MonthlyTokenLimit != nil {
		limit = *snapshot.user.MonthlyTokenLimit
	}
	if limit <= 0 || snapshot.total < limit {
		return nil
	}
	reset := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	return quotaError(http.StatusTooManyRequests,
		fmt.Sprintf("Monthly token quota exceeded (%d tokens). Resets %s.", limit, reset),
		"insufficient_quota", "quota_exceeded")
}
