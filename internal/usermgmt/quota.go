package usermgmt

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

const quotaCacheTTL = 2 * time.Second

type QuotaError struct {
	Status  int
	Body    []byte
	Headers http.Header
	message string
}

func (e *QuotaError) ResponseHeaders() http.Header { return e.Headers.Clone() }
func (e *QuotaError) Error() string                { return e.message }
func (e *QuotaError) StatusCode() int              { return e.Status }
func (e *QuotaError) ResponseBody() []byte         { return append([]byte(nil), e.Body...) }

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

// CheckQuota evaluates exact committed USD spending for each logical request.
// The previous token settings remain readable historical configuration only.
func (r *Runtime) CheckQuota(ctx context.Context) *QuotaError {
	identity, exists := sdkaccess.ResultFromContext(ctx)
	if !exists || identity.Provider != AccessProviderName {
		return nil
	}
	if r == nil {
		return unavailableScopeError()
	}
	release, err := r.BeginRequest(ctx)
	if err != nil {
		return unavailableScopeError()
	}
	defer release()
	r.mu.RLock()
	scope := r.scopes[identity.Metadata[UsageScopeMetadataKey]]
	r.mu.RUnlock()
	if scope == nil {
		return unavailableScopeError()
	}
	// Middleware admits a logical request once. Later SDK producer/retry checks
	// must allow that accepted request to finish despite concurrent expenditure.
	if r.capturedBudgetAdmitted(ctx, scope) {
		return nil
	}
	status, e := r.budgetStatus(ctx, scope, identity.Principal, scope.accountant.quota.timeNow())
	if e != nil {
		return quotaError(http.StatusServiceUnavailable, "Cost accounting is unavailable. New requests are blocked until accounting recovers.", "server_error", "accounting_unavailable")
	}
	if r.capturedBudgetAdmitted(ctx, scope) {
		return nil
	}
	if !status.Blocked {
		r.admitCapturedBudget(ctx, scope)
		return nil
	}
	message := "Spending budget exhausted."
	code := "budget_exceeded"
	if status.UnresolvedRequests > 0 {
		message = "A previous request has unresolved cost accounting. An administrator must resolve its cost before new requests are accepted."
		code = "accounting_unresolved"
	} else if status.RequiresAdminAction {
		message += " An administrator must raise the limit."
	} else if status.AvailableAt != nil {
		message += " Available again at " + status.AvailableAt.Format(time.RFC3339) + "."
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "type": "insufficient_quota", "code": code, "currency": "USD", "available_at": status.AvailableAt, "requires_admin_action": status.RequiresAdminAction, "blocking_limits": status.BlockingLimits, "daily_resets_at": status.DailyResetsAt}})
	result := &QuotaError{Status: http.StatusTooManyRequests, Body: body, message: message}
	if status.AvailableAt != nil {
		seconds := int64((status.AvailableAt.Sub(scope.accountant.quota.timeNow()) + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		result.Headers = http.Header{"Retry-After": []string{strconv.FormatInt(seconds, 10)}}
	}
	return result
}
