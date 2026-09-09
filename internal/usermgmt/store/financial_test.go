package store

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func moneyPointer(s string) *string { return &s }
func TestMoneyExactnessAndRejection(t *testing.T) {
	for input, want := range map[string]string{"000.100000000000000000": "0.1", "1.000000000000000001": "1.000000000000000001", "0": "0", "99999999999999999999.999999999999999999": "99999999999999999999.999999999999999999"} {
		got, e := NormalizeMoney(input)
		if e != nil || got != want {
			t.Fatalf("%s => %s, %v", input, got, e)
		}
	}
	for _, input := range []string{"", "-1", "NaN", "1e3", ".1", "1.", " 1", "1.0000000000000000001", "100000000000000000000"} {
		if _, e := ParseMoney(input); e == nil {
			t.Errorf("accepted invalid money %q", input)
		}
	}
	if _, e := NormalizeRate("0.0000000000001"); e == nil {
		t.Fatal("rate exceeds exact pertoken precision")
	}
}
func financialStore(t *testing.T) *Store {
	t.Helper()
	db, e := Open(context.Background(), Config{DSN: integrationDSN(t)})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, e = db.CreateUser(context.Background(), User{ID: "money-user", Email: "money@example.com", Role: "user", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	return db
}
func TestPostgresFinancialIdempotencyResolutionAndActivation(t *testing.T) {
	db := financialStore(t)
	ctx := context.Background()
	at := time.Now().UTC()
	activated, e := db.FinancialActivation(ctx)
	if e != nil || activated.IsZero() {
		t.Fatal(e)
	}
	if e = db.IncrementUsage(ctx, UsageIncrement{UserID: "money-user", Period: "2020-01", TotalTokens: 9999}); e != nil {
		t.Fatal(e)
	}
	initial, e := db.FinancialSnapshot(ctx, "money-user", at.Add(-time.Hour))
	if e != nil || initial.LifetimeUSD != "0" {
		t.Fatal("historical tokens were backfilled")
	}
	if e = db.BeginFinancialRequest(ctx, "request", "money-user", "model", at); e != nil {
		t.Fatal(e)
	}
	event := CostEvent{ID: "event", RequestID: "request", UserID: "money-user", At: at, CostUSD: moneyPointer("0.000000000000000001"), Status: "priced", TokenBreakdown: json.RawMessage(`{"total_tokens":1}`)}
	var workers sync.WaitGroup
	for range 12 {
		workers.Go(func() {
			if e := db.RecordCostEvent(ctx, event); e != nil {
				t.Error(e)
			}
		})
	}
	workers.Wait()
	snapshot, e := db.FinancialSnapshot(ctx, "money-user", at.Add(-time.Hour))
	if e != nil || snapshot.LifetimeUSD != "0.000000000000000001" {
		t.Fatalf("duplicate financial charge: %+v %v", snapshot, e)
	}
	if e = db.RecordCostEvent(ctx, CostEvent{ID: "unknown", RequestID: "request", UserID: "money-user", At: at, Status: "unresolved", Reason: "missing_usage"}); e != nil {
		t.Fatal(e)
	}
	if e = db.FinishFinancialRequest(ctx, "request", 200); e != nil {
		t.Fatal(e)
	}
	snapshot, e = db.FinancialSnapshot(ctx, "money-user", at.Add(-time.Hour))
	if e != nil || len(snapshot.UnresolvedIDs) != 1 {
		t.Fatal("missing usage not durable")
	}
	if e = db.ResolveRequestCost(ctx, "request", "0.1", "Verified provider statement", "resolution"); e != nil {
		t.Fatal(e)
	}
	if e = db.RecordCostEvent(ctx, event); e != nil {
		t.Fatal(e)
	}
	snapshot, e = db.FinancialSnapshot(ctx, "money-user", at.Add(-time.Hour))
	if e != nil || snapshot.LifetimeUSD != "0.1" || len(snapshot.UnresolvedIDs) != 0 {
		t.Fatalf("resolution incorrect: %+v %v", snapshot, e)
	}
	if e = db.EnsureSchema(ctx); e != nil {
		t.Fatal(e)
	}
	again, e := db.FinancialActivation(ctx)
	if e != nil || !again.Equal(activated) {
		t.Fatal("activation moved on restart")
	}
}
func TestPostgresPriceHistoryOverridesAndContextTiers(t *testing.T) {
	db := financialStore(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour).UTC()
	rates := PriceRates{InputUSD: "1", OutputUSD: "2", ReasoningUSD: moneyPointer("2")}
	p := ModelPrice{ID: "default-v1", Provider: "openai", Model: "test", PriceRates: rates, Source: "test", UpdatedAt: at, ContextTiers: []ContextPriceTier{{AboveTokens: 200000, PriceRates: PriceRates{InputUSD: "2", OutputUSD: "3"}}}}
	if e := db.SavePrice(ctx, p); e != nil {
		t.Fatal(e)
	}
	p.ID = "override"
	p.Manual = true
	p.UpdatedAt = at.Add(time.Minute)
	p.InputUSD = "5"
	if e := db.SavePrice(ctx, p); e != nil {
		t.Fatal(e)
	}
	p.ID = "default-v2"
	p.Manual = false
	p.UpdatedAt = at.Add(2 * time.Minute)
	p.InputUSD = "3"
	if e := db.SavePrice(ctx, p); e != nil {
		t.Fatal(e)
	}
	for _, test := range []struct {
		at time.Time
		id string
	}{{at.Add(time.Second), "default-v1"}, {at.Add(90 * time.Second), "override"}, {time.Now(), "override"}} {
		got, e := db.GetPriceAt(ctx, "openai", "test", test.at)
		if e != nil || got.ID != test.id || len(got.ContextTiers) != 1 {
			t.Fatalf("bad price snapshot %+v %v", got, e)
		}
	}
	if e := db.DeletePriceOverride(ctx, "openai", "test"); e != nil {
		t.Fatal(e)
	}
	got, e := db.GetPriceAt(ctx, "openai", "test", time.Now().Add(time.Second))
	if e != nil || got.ID != "default-v2" {
		t.Fatalf("restoredefault %+v %v", got, e)
	}
	old, e := db.GetPriceAt(ctx, "openai", "test", at.Add(90*time.Second))
	if e != nil || old.ID != "override" {
		t.Fatal("deleting override changed historical rates")
	}
}
func TestPostgresFinancialFailedAttemptAndPreflight(t *testing.T) {
	db := financialStore(t)
	ctx := context.Background()
	for _, id := range []string{"preflight", "attempted", "success"} {
		if e := db.BeginFinancialRequest(ctx, id, "money-user", "model", time.Now()); e != nil {
			t.Fatal(e)
		}
	}
	if e := db.MarkFinancialAttempted(ctx, "attempted"); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"preflight", "attempted"} {
		if e := db.FinishFinancialRequest(ctx, id, 403); e != nil {
			t.Fatal(e)
		}
	}
	if e := db.FinishFinancialRequest(ctx, "success", 200); e != nil {
		t.Fatal(e)
	}
	snapshot, e := db.FinancialSnapshot(ctx, "money-user", time.Now().Add(-time.Hour))
	if e != nil || len(snapshot.UnresolvedIDs) != 2 {
		t.Fatalf("unknown failedattempt usage should block, rejectedpreflight should not: %+v %v", snapshot, e)
	}
}
