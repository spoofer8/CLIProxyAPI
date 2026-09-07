package usermgmt

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func setQuotaClock(scope *usageScope, now time.Time) {
	scope.accountant.quota.mu.Lock()
	scope.accountant.quota.now = func() time.Time { return now }
	scope.accountant.quota.mu.Unlock()
}

func TestQuotaModesUTCResetAndLegacyBypass(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	user, _, key := createTestIdentity(t, engine)
	ctx := usageContext(t, runtime, key)
	_, cfg := runtime.Snapshot()
	cfg.Quota.Enforce = true
	cfg.Quota.DefaultMonthlyTokens = 50
	if errApply := runtime.Apply(context.Background(), cfg); errApply != nil {
		t.Fatal(errApply)
	}
	scope := runtime.current
	now := time.Date(2026, 9, 30, 23, 59, 0, 0, time.UTC)
	setQuotaClock(scope, now)
	if quotaErr := runtime.CheckQuota(ctx); quotaErr != nil {
		t.Fatal(quotaErr)
	}
	runtime.HandleUsage(ctx, usage.Record{RequestedAt: now, Detail: usage.Detail{TotalTokens: 99}})
	if errFlush := runtime.FlushUsage(context.Background()); errFlush != nil {
		t.Fatal(errFlush)
	}
	if quotaErr := runtime.CheckQuota(ctx); quotaErr != nil {
		t.Fatal("under-limit request rejected")
	}
	runtime.HandleUsage(ctx, usage.Record{RequestedAt: now, Detail: usage.Detail{TotalTokens: 1}})
	if errFlush := runtime.FlushUsage(context.Background()); errFlush != nil {
		t.Fatal(errFlush)
	}
	quotaErr := runtime.CheckQuota(ctx)
	if quotaErr == nil || quotaErr.StatusCode() != http.StatusTooManyRequests || !stringsContainAll(string(quotaErr.ResponseBody()), "quota_exceeded", "insufficient_quota", "2026-10-01") {
		t.Fatalf("missing quota error shape/reset: %v", quotaErr)
	}
	legacy := sdkaccess.WithResult(context.Background(), &sdkaccess.Result{Provider: "config", Principal: user.ID})
	if runtime.CheckQuota(legacy) != nil {
		t.Fatal("legacy admin principal did not bypass quota")
	}
	for _, patch := range []string{`{"monthly_token_limit":0}`, `{"monthly_token_limit":-1}`} {
		requestJSON(t, engine, http.MethodPatch, "/users/"+user.ID, patch, http.StatusOK)
		if runtime.CheckQuota(ctx) != nil {
			t.Fatal("explicit unlimited limit was rejected")
		}
	}
	requestJSON(t, engine, http.MethodPatch, "/users/"+user.ID, `{"monthly_token_limit":null}`, http.StatusOK)
	if runtime.CheckQuota(ctx) == nil {
		t.Fatal("null quota did not inherit configured default")
	}
	cfg.Quota.DefaultMonthlyTokens = 0
	if errApply := runtime.Apply(context.Background(), cfg); errApply != nil {
		t.Fatal(errApply)
	}
	if runtime.CheckQuota(ctx) != nil {
		t.Fatal("zero default did not mean unlimited")
	}
	cfg.Quota.DefaultMonthlyTokens, cfg.Quota.Enforce = 1, false
	if errApply := runtime.Apply(context.Background(), cfg); errApply != nil {
		t.Fatal(errApply)
	}
	if runtime.CheckQuota(ctx) != nil {
		t.Fatal("enforce:false rejected existing usage")
	}
	cfg.Quota.Enforce = true
	if errApply := runtime.Apply(context.Background(), cfg); errApply != nil {
		t.Fatal(errApply)
	}
	// It is still September in this offset, but the UTC quota period is October.
	setQuotaClock(scope, time.Date(2026, 9, 30, 20, 0, 0, 0, time.FixedZone("UTC-7", -7*3600)))
	if runtime.CheckQuota(ctx) != nil {
		t.Fatal("quota did not reset at UTC calendar boundary")
	}
}

func TestQuotaRefreshDuringPersistenceDoesNotDoubleCount(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	user, _, key := createTestIdentity(t, engine)
	ctx := usageContext(t, runtime, key)
	db, cfg := runtime.Snapshot()
	cfg.Quota.Enforce = true
	if errApply := runtime.Apply(context.Background(), cfg); errApply != nil {
		t.Fatal(errApply)
	}
	scope := runtime.current
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	setQuotaClock(scope, now)
	if errWrite := db.IncrementUsage(context.Background(), store.UsageIncrement{UserID: user.ID, Period: "2026-09", TotalTokens: 50}); errWrite != nil {
		t.Fatal(errWrite)
	}
	if runtime.CheckQuota(ctx) != nil {
		t.Fatal("initial quota unexpectedly exceeded")
	}
	committed, finish := make(chan struct{}), make(chan struct{})
	// Hold persistence completion after commit so quota refresh can observe the
	// new committed total before the worker invalidates its previous cache.
	scope.accountant.persist = func(ctx context.Context, delta store.UsageIncrement) error {
		errWrite := db.IncrementUsage(ctx, delta)
		close(committed)
		select {
		case <-finish:
			return errWrite
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	runtime.HandleUsage(ctx, usage.Record{RequestedAt: now, Detail: usage.Detail{TotalTokens: 30}})
	<-committed
	setQuotaClock(scope, now.Add(quotaCacheTTL))
	if quotaErr := runtime.CheckQuota(ctx); quotaErr != nil {
		t.Fatalf("refresh double-counted already committed usage: %v", quotaErr)
	}
	close(finish)
	if errFlush := runtime.FlushUsage(context.Background()); errFlush != nil {
		t.Fatal(errFlush)
	}
	if quotaErr := runtime.CheckQuota(ctx); quotaErr != nil {
		t.Fatalf("post-persistence cache double-counted usage: %v", quotaErr)
	}
	monthly, errGet := db.GetMonthlyUsage(context.Background(), user.ID, "2026-09")
	if errGet != nil || monthly.TotalTokens != 80 {
		t.Fatal("unexpected persisted total")
	}
}

func TestQuotaDatabaseFailureAndUnknownScope(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	_, _, key := createTestIdentity(t, engine)
	ctx := usageContext(t, runtime, key)
	db, cfg := runtime.Snapshot()
	cfg.Quota.Enforce = true
	if errApply := runtime.Apply(context.Background(), cfg); errApply != nil {
		t.Fatal(errApply)
	}
	if errClose := db.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	if quotaErr := runtime.CheckQuota(ctx); quotaErr == nil || quotaErr.StatusCode() != http.StatusServiceUnavailable {
		t.Fatal("unknown quota availability did not fail closed")
	}
	identity, _ := sdkaccess.ResultFromContext(ctx)
	identity.Metadata[UsageScopeMetadataKey] = "foreign-scope"
	if quotaErr := runtime.CheckQuota(sdkaccess.WithResult(context.Background(), identity)); quotaErr == nil || quotaErr.StatusCode() != http.StatusUnauthorized {
		t.Fatal("foreign scope bypassed user quota enforcement")
	}
}
