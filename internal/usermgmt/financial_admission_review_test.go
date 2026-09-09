package usermgmt

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func reviewedSpend(t *testing.T, db *store.Store, userID, amount string) string {
	t.Helper()
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.BeginFinancialRequest(context.Background(), id, userID, "fixture", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordCostEvent(context.Background(), store.CostEvent{ID: id + "-event", RequestID: id, UserID: userID, Provider: "fixture", Model: "fixture", At: time.Now(), CostUSD: &amount, Status: "priced"}); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishFinancialRequest(context.Background(), id, 200); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAcceptedFinancialRetrySurvivesIdentityOutageWithoutApprovingNewTargets(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	user, _, key := createTestIdentity(t, engine)
	originalIdentity := usageContext(t, runtime, key)
	ctx, finish, err := runtime.BeginDurableRequestCapture(originalIdentity, RequestCaptureMetadata{Method: "WS", Path: "/v1/responses"})
	if err != nil {
		t.Fatal(err)
	}
	defer finish(RequestCaptureResult{StatusCode: 400})
	target := sdkaccess.PolicyTarget{RequestedModel: "known", ExecutionModel: "known", Provider: "fixture"}
	if err := runtime.CheckRequest(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CheckPermissions(ctx, target); err != nil {
		t.Fatal(err)
	}
	db, _ := runtime.Snapshot()
	if _, err := db.DB().Exec(`ALTER TABLE cpa_users RENAME TO cpa_users_offline`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = db.DB().Exec(`ALTER TABLE IF EXISTS cpa_users_offline RENAME TO cpa_users`) }()
	runtime.current.auth.invalidate()
	runtime.current.permissions.invalidate()
	if err := runtime.CheckRequest(ctx); err != nil {
		t.Fatal("accepted retry lost its identity during DB outage", err)
	}
	if err := runtime.CheckPermissions(ctx, target); err != nil {
		t.Fatal("approved provider/model retry stopped during DB outage", err)
	}
	unapproved := target
	unapproved.Provider = "different-provider"
	if err := runtime.CheckPermissions(ctx, unapproved); err == nil {
		t.Fatal("retry snapshot widened approval to another provider")
	}
	newCtx, newFinish, err := runtime.BeginDurableRequestCapture(originalIdentity, RequestCaptureMetadata{Method: "WS", Path: "/v1/responses"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.CheckRequest(newCtx); err == nil {
		t.Fatal("new WebSocket turn inherited an accepted identity")
	}
	newFinish(RequestCaptureResult{StatusCode: 403})
	if _, err := db.DB().Exec(`ALTER TABLE cpa_users_offline RENAME TO cpa_users`); err != nil {
		t.Fatal(err)
	}
	status := "disabled"
	if _, err := db.UpdateUser(context.Background(), user.ID, store.UserPatch{Status: &status}); err != nil {
		t.Fatal(err)
	}
	runtime.current.auth.invalidate()
	if err := runtime.CheckRequest(ctx); err != nil {
		t.Fatal("revocation interrupted an accepted request", err)
	}
	if err := runtime.CheckRequest(originalIdentity); err == nil {
		t.Fatal("revoked identity admitted a new request")
	}
}

func TestAcceptedFinancialRequestFinishesAfterConcurrentExhaustion(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	user, _, key := createTestIdentity(t, engine)
	db, _ := runtime.Snapshot()
	limit := "1"
	if err := db.SetBudget(context.Background(), user.ID, store.BudgetLimits{DailyUSD: &limit}); err != nil {
		t.Fatal(err)
	}
	ctx, finish, err := runtime.BeginDurableRequestCapture(usageContext(t, runtime, key), RequestCaptureMetadata{Method: "POST", Path: "/v1/responses"})
	if err != nil {
		t.Fatal(err)
	}
	defer finish(RequestCaptureResult{StatusCode: 400})
	if err := runtime.CheckRequest(ctx); err != nil {
		t.Fatal("first admission rejected", err)
	}
	reviewedSpend(t, db, user.ID, "2")
	if err := runtime.CheckRequest(ctx); err != nil {
		t.Fatalf("accepted logical request rejected at a later producer check: %v", err)
	}
	if err := runtime.CheckRequest(usageContext(t, runtime, key)); err == nil {
		t.Fatal("new request bypassed exhausted cap")
	}
}

func TestResolutionBelowKnownCostIsAnActionableBadRequest(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	user, _, _ := createTestIdentity(t, engine)
	db, _ := runtime.Snapshot()
	id := reviewedSpend(t, db, user.ID, "2")
	response := requestJSON(t, engine, http.MethodPost, "/requests/"+id+"/cost-resolution", `{"cost_usd":"1","note":"Reconcile the request"}`, http.StatusBadRequest)
	if response.Code != 400 {
		t.Fatal("known-cost validation should not report service unavailable")
	}
}
