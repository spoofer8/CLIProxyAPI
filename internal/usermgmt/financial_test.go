package usermgmt

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func usd(value string) *string { return &value }
func TestBudgetLondonDSTAndRollingAvailability(t *testing.T) {
	for _, test := range []struct {
		now   string
		hours float64
	}{{"2026-03-29T12:00:00Z", 23}, {"2026-10-25T12:00:00Z", 25}} {
		now, _ := time.Parse(time.RFC3339, test.now)
		start, end := londonDay(now)
		if end.Sub(start).Hours() != test.hours {
			t.Fatalf("London DST day %s: %s", test.now, end.Sub(start))
		}
	}
	now := time.Date(2026, 9, 9, 14, 0, 0, 0, time.UTC)
	snapshot := store.FinancialSnapshot{LifetimeUSD: "20", Entries: []store.SpendEntry{{At: now.Add(-29 * 24 * time.Hour), USD: "8"}, {At: now.Add(-6 * 24 * time.Hour), USD: "7"}, {At: now.Add(-time.Hour), USD: "5"}}}
	status, e := evaluateBudget(store.BudgetLimits{DailyUSD: usd("5"), WeeklyUSD: usd("10"), MonthlyUSD: usd("15")}, snapshot, now)
	expected := now.Add(24 * time.Hour)
	if e != nil || !status.Blocked || status.AvailableAt == nil || !status.AvailableAt.Equal(expected) || len(status.BlockingLimits) != 3 {
		t.Fatalf("simultaneous limits %+v %v expected %s", status, e, expected)
	}
	status, e = evaluateBudget(store.BudgetLimits{LifetimeUSD: usd("20"), DailyUSD: usd("5")}, snapshot, now)
	if e != nil || !status.RequiresAdminAction || status.AvailableAt != nil {
		t.Fatal("lifetime cap advertised automatic reset")
	}
	status, e = evaluateBudget(store.BudgetLimits{DailyUSD: usd("0")}, snapshot, now)
	if e != nil || !status.RequiresAdminAction || status.AvailableAt != nil {
		t.Fatal("zero budget cannot recover at midnight")
	}
	// Exact equality expires out of a rolling window; no off-by-one extra day.
	boundary := now.Add(24 * time.Hour)
	status, e = evaluateBudget(store.BudgetLimits{WeeklyUSD: usd("10"), MonthlyUSD: usd("15")}, snapshot, boundary)
	if e != nil || status.Blocked {
		t.Fatalf("expiry boundary remained blocked %+v %v", status, e)
	}
}
func TestCostCanonicalBucketsExactAndUnresolved(t *testing.T) {
	p := store.ModelPrice{PriceRates: store.PriceRates{InputUSD: "1", OutputUSD: "4", CacheReadUSD: usd("0.1"), CacheWriteUSD: usd("2"), ReasoningUSD: usd("4")}}
	record := usage.Record{Provider: "openai", Detail: usage.Detail{TokenBreakdown: usage.NewSubsetTokenBreakdown(100, 20, 10, 50, 10, 150)}}
	value, reason := priceUsage(record, p)
	if reason != "" || value != "0.000292" {
		t.Fatalf("wrong mutuallyexclusive bucket costs %s %s", value, reason)
	}
	p.ContextTiers = []store.ContextPriceTier{{AboveTokens: 99, PriceRates: store.PriceRates{InputUSD: "2", OutputUSD: "8", CacheReadUSD: usd("0.2"), CacheWriteUSD: usd("4"), ReasoningUSD: usd("8")}}}
	value, reason = priceUsage(record, p)
	if reason != "" || value != "0.000584" {
		t.Fatalf("context tier not applied %s %s", value, reason)
	}
	record.Detail = usage.Detail{}
	if _, reason = priceUsage(record, p); reason != "missing_or_unreliable_usage" {
		t.Fatal("missing usage priced as free")
	}
	record.Detail = usage.Detail{TokenBreakdown: usage.NewUnclassifiedTokenBreakdown(10)}
	if _, reason = priceUsage(record, p); reason == "" {
		t.Fatal("unclassified tokens priced")
	}
	record.Detail = usage.Detail{TokenBreakdown: usage.NewSubsetTokenBreakdown(100, 1, 0, 1, 0, 101)}
	p.ContextTiers = nil
	p.CacheReadUSD = nil
	if _, reason = priceUsage(record, p); reason != "unpriced_token_bucket" {
		t.Fatal("unknown cache rate silently priced")
	}
	p.CacheReadUSD = usd("0")
	record.ServiceTier = "priority"
	if _, reason = priceUsage(record, p); reason != "unsupported_service_tier" {
		t.Fatal("premium tier charged base price")
	}
}
func TestPublishedCatalogPrecisionTiersAndUnsupportedPrices(t *testing.T) {
	fixture := `{"openai":{"doc":"https://openai.com/api/pricing/","models":{"test":{"cost":{"input":0.4,"output":1.6,"cache_read":0.1,"tiers":[{"input":0.8,"output":2.4,"cache_read":0.2,"tier":{"type":"context","size":200000}}]},"modalities":{"output":["text"]}},"unknown":{"cost":{}},"legacy":{"cost":{"input":1,"output":2,"context_over_200k":{"input":3,"output":4}}},"image":{"cost":{"input":1,"output":2},"modalities":{"output":["image"]}}}}}`
	prices, e := parsePublishedCatalog(strings.NewReader(fixture), time.Now())
	if e != nil || len(prices) != 1 {
		t.Fatalf("unprice unknown catalog entries %+v %v", prices, e)
	}
	p := prices[0]
	if p.InputUSD != "0.4" || p.ReasoningUSD == nil || *p.ReasoningUSD != "1.6" || len(p.ContextTiers) != 1 || p.ContextTiers[0].AboveTokens != 200000 {
		t.Fatalf("catalog data incorrect %+v", p)
	}
}
func TestFinancialUnresolvedBlocksUnlimitedAndAcceptedWorkFinishes(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	user, _, key := createTestIdentity(t, engine)
	ctx := usageContext(t, runtime, key)
	scope := runtime.current
	db, _ := runtime.Snapshot()
	now := time.Now()
	if e := runtime.beginFinancialRequest(ctx, scope, "accepted", user.ID, "model", now); e != nil {
		t.Fatal(e)
	}
	if e := runtime.CheckQuota(ctx); e != nil {
		t.Fatal("active requests are not reservations", e)
	}
	if e := runtime.finishFinancialRequest(context.Background(), scope, "accepted", 200); e != nil {
		t.Fatal(e)
	}
	if e := runtime.CheckQuota(ctx); e == nil || !strings.Contains(string(e.Body), "accounting_unresolved") {
		t.Fatal("missing usage must block even unlimited ordinary users")
	}
	if e := db.ResolveRequestCost(context.Background(), "accepted", "0", "Confirmed no billable usage", "resolved"); e != nil {
		t.Fatal(e)
	}
	if e := runtime.CheckQuota(ctx); e != nil {
		t.Fatal("resolution did not restore access", e)
	}
	// Crash recovery: the durable in_progress row exists but no live request does.
	if e := db.BeginFinancialRequest(context.Background(), "interrupted", user.ID, "model", now); e != nil {
		t.Fatal(e)
	}
	if e := runtime.CheckQuota(ctx); e == nil {
		t.Fatal("orphaned execution did not fail closed")
	}
}

func TestFinancialSynchronousAccountingReplayAndInFlightOverspend(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	user, _, key := createTestIdentity(t, engine)
	ctx := usageContext(t, runtime, key)
	scope := runtime.current
	db, _ := runtime.Snapshot()
	now := time.Now().UTC()
	price := store.ModelPrice{ID: "price", Provider: "openai", Model: "actual-model", PriceRates: store.PriceRates{InputUSD: "1", OutputUSD: "2", ReasoningUSD: usd("2")}, UpdatedAt: now.Add(-time.Minute)}
	if e := db.SavePrice(ctx, price); e != nil {
		t.Fatal(e)
	}
	if e := db.SetBudget(ctx, user.ID, store.BudgetLimits{LifetimeUSD: usd("1")}); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"first", "second"} {
		if e := runtime.beginFinancialRequest(ctx, scope, id, user.ID, "friendly-alias", now); e != nil {
			t.Fatal(e)
		}
	}
	first := context.WithValue(ctx, requestCaptureContextKey{}, requestCaptureIdentity{id: "first", scopeID: scope.id, userID: user.ID})
	second := context.WithValue(ctx, requestCaptureContextKey{}, requestCaptureIdentity{id: "second", scopeID: scope.id, userID: user.ID})
	record := usage.Record{EventID: "event-first", Provider: "openai", Model: "actual-model", Alias: "friendly-alias", RequestedAt: now, Detail: usage.Detail{TokenBreakdown: usage.NewSubsetTokenBreakdown(1_000_000, 0, 0, 0, 0, 1_000_000)}}
	runtime.HandleUsage(first, record)
	runtime.HandleUsage(first, record)
	if e := runtime.CheckQuota(ctx); e == nil {
		t.Fatal("synchronous committed cost did not block new requests")
	}
	record.EventID = "event-second"
	runtime.HandleUsage(second, record)
	for _, id := range []string{"first", "second"} {
		if e := runtime.finishFinancialRequest(ctx, scope, id, 200); e != nil {
			t.Fatal(e)
		}
	}
	snapshot, e := db.FinancialSnapshot(ctx, user.ID, now.Add(-time.Hour))
	if e != nil || snapshot.LifetimeUSD != "2" {
		t.Fatalf("accepted request aborted or duplicate replay charged twice %+v %v", snapshot, e)
	}
}

func TestFinancialPersistenceFailureCannotReopenAccessAfterDatabaseRecovery(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	user, _, key := createTestIdentity(t, engine)
	ctx := usageContext(t, runtime, key)
	scope := runtime.current
	db, _ := runtime.Snapshot()
	now := time.Now().UTC()
	if e := db.SavePrice(ctx, store.ModelPrice{ID: "known-price", Provider: "openai", Model: "model", PriceRates: store.PriceRates{InputUSD: "1", OutputUSD: "1"}, UpdatedAt: now.Add(-time.Minute)}); e != nil {
		t.Fatal(e)
	}
	if e := runtime.beginFinancialRequest(ctx, scope, "failed-write", user.ID, "model", now); e != nil {
		t.Fatal(e)
	}
	if _, e := db.DB().ExecContext(ctx, `CREATE FUNCTION reject_financial_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'simulated outage'; END $$`); e != nil {
		t.Fatal(e)
	}
	if _, e := db.DB().ExecContext(ctx, `CREATE TRIGGER reject_financial_write BEFORE INSERT ON cpa_cost_events FOR EACH ROW EXECUTE FUNCTION reject_financial_write()`); e != nil {
		t.Fatal(e)
	}
	captured := context.WithValue(ctx, requestCaptureContextKey{}, requestCaptureIdentity{id: "failed-write", scopeID: scope.id, userID: user.ID})
	runtime.HandleUsage(captured, usage.Record{EventID: "lost-record", Provider: "openai", Model: "model", RequestedAt: now, Detail: usage.Detail{TokenBreakdown: usage.NewSubsetTokenBreakdown(1, 0, 0, 1, 0, 2)}})
	if _, e := db.DB().ExecContext(ctx, `DROP TRIGGER reject_financial_write ON cpa_cost_events`); e != nil {
		t.Fatal(e)
	}
	if e := runtime.CheckQuota(ctx); e == nil {
		t.Fatal("recovered database reopened access while financial write remained unresolved")
	}
	if e := runtime.finishFinancialRequest(ctx, scope, "failed-write", 200); e != nil {
		t.Fatal(e)
	}
	if e := runtime.CheckQuota(ctx); e == nil {
		t.Fatal("failed accounting wasn't durably marked after producer finished")
	}
	if e := db.ResolveRequestCost(ctx, "failed-write", "0.000002", "Verified exact provider usage", "recovery-resolution"); e != nil {
		t.Fatal(e)
	}
	if e := runtime.CheckQuota(ctx); e != nil {
		t.Fatal("administrator recovery did not restore access", e)
	}
}

func TestFinancialManagementExactBudgetAndActiveResolutionContract(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	user, _, key := createTestIdentity(t, engine)
	response := requestJSON(t, engine, "PUT", "/users/"+user.ID+"/budget", `{"daily_usd":"0.000000000000000001","weekly_usd":"5","monthly_usd":null,"lifetime_usd":"100"}`, 200)
	if !strings.Contains(response.Body.String(), `"daily_usd":"0.000000000000000001"`) {
		t.Fatal("decimal precision lost in management API")
	}
	requestJSON(t, engine, "PUT", "/users/"+user.ID+"/budget", `{"daily_usd":0.1}`, 400)
	requestJSON(t, engine, "PUT", "/pricing/override", `{"provider":"custom","model":"custom-model","input_usd_per_million":"0.5","output_usd_per_million":"1","cache_read_usd_per_million":null,"cache_write_usd_per_million":null,"reasoning_usd_per_million":"1"}`, 200)
	prices := requestJSON(t, engine, "GET", "/pricing", "", 200)
	if !strings.Contains(prices.Body.String(), `"manual":true`) {
		t.Fatal("manual override not returned")
	}
	ctx := usageContext(t, runtime, key)
	id := "01J00000000000000000000000"
	if e := runtime.beginFinancialRequest(ctx, runtime.current, id, user.ID, "custom-model", time.Now()); e != nil {
		t.Fatal(e)
	}
	requestJSON(t, engine, "POST", "/requests/"+id+"/cost-resolution", `{"cost_usd":"1","note":"attempt to seal a running request"}`, 409)
	if e := runtime.finishFinancialRequest(ctx, runtime.current, id, 200); e != nil {
		t.Fatal(e)
	}
	requestJSON(t, engine, "POST", "/requests/"+id+"/cost-resolution", `{"cost_usd":"1","note":"verified final usage"}`, 200)
}
