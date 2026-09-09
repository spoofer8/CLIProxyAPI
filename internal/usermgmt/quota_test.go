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

func TestTokenQuotaSettingsDoNotEnforceDollarBudgets(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	user, _, key := createTestIdentity(t, engine)
	ctx := usageContext(t, runtime, key)
	db, cfg := runtime.Snapshot()
	cfg.Quota.Enforce = true
	cfg.Quota.DefaultMonthlyTokens = 1
	if e := runtime.Apply(context.Background(), cfg); e != nil {
		t.Fatal(e)
	}
	runtime.HandleUsage(ctx, usage.Record{Detail: usage.Detail{TotalTokens: 1000000}})
	if e := runtime.FlushUsage(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := runtime.CheckQuota(ctx); e != nil {
		t.Fatal("historical token settings must not enforce cost budgets", e)
	}
	now := time.Now()
	if e := db.SetBudget(context.Background(), user.ID, store.BudgetLimits{LifetimeUSD: usd("1")}); e != nil {
		t.Fatal(e)
	}
	if e := db.BeginFinancialRequest(context.Background(), "charged", user.ID, "model", now); e != nil {
		t.Fatal(e)
	}
	if e := db.RecordCostEvent(context.Background(), store.CostEvent{ID: "charge", RequestID: "charged", UserID: user.ID, At: now, CostUSD: usd("1"), Status: "priced"}); e != nil {
		t.Fatal(e)
	}
	if e := db.FinishFinancialRequest(context.Background(), "charged", 200); e != nil {
		t.Fatal(e)
	}
	cfg.Quota.Enforce = false
	if e := runtime.Apply(context.Background(), cfg); e != nil {
		t.Fatal(e)
	}
	if e := runtime.CheckQuota(ctx); e == nil || !stringsContainAll(string(e.Body), "budget_exceeded", "requires_admin_action") {
		t.Fatal("old token enforce:false must not bypass explicitly configured dollars", e)
	}
	legacy := sdkaccess.WithResult(context.Background(), &sdkaccess.Result{Provider: "config", Principal: user.ID})
	if runtime.CheckQuota(legacy) != nil {
		t.Fatal("unattributed external identities must not assume user rows")
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
