package usermgmt

import (
	"context"
	"math/big"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
)

type SpendTotals struct {
	LifetimeUSD string `json:"lifetime_usd"`
	DailyUSD    string `json:"daily_usd"`
	WeeklyUSD   string `json:"weekly_usd"`
	MonthlyUSD  string `json:"monthly_usd"`
}
type BudgetStatus struct {
	Budget               store.BudgetLimits `json:"budget"`
	Spend                SpendTotals        `json:"spend"`
	Currency             string             `json:"currency"`
	Timezone             string             `json:"timezone"`
	DailyResetsAt        time.Time          `json:"daily_resets_at"`
	AvailableAt          *time.Time         `json:"available_at"`
	Blocked              bool               `json:"blocked"`
	AdminExempt          bool               `json:"admin_exempt"`
	RequiresAdminAction  bool               `json:"requires_admin_action"`
	UnresolvedRequestIDs []string           `json:"unresolved_request_ids"`
	UnpricedRequests     int                `json:"unpriced_requests"`
	UnresolvedRequests   int                `json:"unresolved_requests"`
	BlockingLimits       []string           `json:"blocking_limits"`
	ActivatedAt          time.Time          `json:"activated_at"`
}

func londonDay(now time.Time) (time.Time, time.Time) {
	location, _ := time.LoadLocation("Europe/London")
	local := now.In(location)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	return start.UTC(), start.AddDate(0, 0, 1).UTC()
}
func evaluateBudget(limits store.BudgetLimits, snapshot store.FinancialSnapshot, now time.Time) (BudgetStatus, error) {
	dayStart, dayEnd := londonDay(now)
	result := BudgetStatus{Budget: limits, Currency: "USD", Timezone: "Europe/London", DailyResetsAt: dayEnd, BlockingLimits: []string{}}
	life, e := store.ParseMoney(snapshot.LifetimeUSD)
	if e != nil {
		return result, e
	}
	daily, weekly, monthly := new(big.Int), new(big.Int), new(big.Int)
	for _, v := range snapshot.Entries {
		n, e := store.ParseMoney(v.USD)
		if e != nil {
			return result, e
		}
		if !v.At.Before(dayStart) {
			daily.Add(daily, n)
		}
		if v.At.After(now.Add(-7 * 24 * time.Hour)) {
			weekly.Add(weekly, n)
		}
		if v.At.After(now.Add(-30 * 24 * time.Hour)) {
			monthly.Add(monthly, n)
		}
	}
	result.Spend = SpendTotals{store.FormatMoney(life), store.FormatMoney(daily), store.FormatMoney(weekly), store.FormatMoney(monthly)}
	for _, window := range []struct {
		name     string
		limit    *string
		spend    *big.Int
		duration time.Duration
	}{{"lifetime", limits.LifetimeUSD, life, 0}, {"daily", limits.DailyUSD, daily, 0}, {"weekly", limits.WeeklyUSD, weekly, 7 * 24 * time.Hour}, {"monthly", limits.MonthlyUSD, monthly, 30 * 24 * time.Hour}} {
		if window.limit == nil {
			continue
		}
		limit, e := store.ParseMoney(*window.limit)
		if e != nil {
			return result, e
		}
		if window.spend.Cmp(limit) < 0 {
			continue
		}
		result.Blocked = true
		result.BlockingLimits = append(result.BlockingLimits, window.name)
		if window.name == "lifetime" || limit.Sign() == 0 {
			result.RequiresAdminAction = true
			continue
		}
		available := dayEnd
		if window.duration > 0 {
			remaining := new(big.Int).Set(window.spend)
			for _, entry := range snapshot.Entries {
				if !entry.At.After(now.Add(-window.duration)) {
					continue
				}
				n, e := store.ParseMoney(entry.USD)
				if e != nil {
					return result, e
				}
				remaining.Sub(remaining, n)
				if remaining.Cmp(limit) < 0 {
					available = entry.At.Add(window.duration)
					break
				}
			}
		}
		if result.AvailableAt == nil || available.After(*result.AvailableAt) {
			t := available
			result.AvailableAt = &t
		}
	}
	if result.RequiresAdminAction {
		result.AvailableAt = nil
	}
	return result, nil
}
func (r *Runtime) budgetStatus(ctx context.Context, scope *usageScope, userID string, now time.Time) (BudgetStatus, error) {
	user, e := scope.store.GetUser(ctx, userID)
	if e != nil {
		return BudgetStatus{}, e
	}
	limits, e := scope.store.GetBudget(ctx, userID)
	if e != nil {
		return BudgetStatus{}, e
	}
	snapshot, e := scope.store.FinancialSnapshot(ctx, userID, now.Add(-30*24*time.Hour))
	if e != nil {
		return BudgetStatus{}, e
	}
	result, e := evaluateBudget(limits, snapshot, now)
	if e != nil {
		return result, e
	}
	result.ActivatedAt, e = scope.store.FinancialActivation(ctx)
	if e != nil {
		return result, e
	}
	result.UnpricedRequests = len(snapshot.UnpricedIDs)
	unresolved := snapshot.UnresolvedIDs
	if limits.Limited() {
		unresolved = append(unresolved, snapshot.UnpricedIDs...)
	}
	r.financialMu.Lock()
	for _, id := range unresolved {
		active := false
		failed := false
		for key := range r.financialActive {
			if strings.HasSuffix(key, ":"+id) {
				active = true
				_, failed = r.financialFailed[key]
				break
			}
		}
		if !active || failed {
			result.UnresolvedRequests++
			result.UnresolvedRequestIDs = append(result.UnresolvedRequestIDs, id)
		}
	}
	r.financialMu.Unlock()
	if result.UnresolvedRequests > 0 {
		result.Blocked = true
		result.RequiresAdminAction = true
		result.AvailableAt = nil
	}
	result.AdminExempt = user.SystemAdmin
	if user.SystemAdmin {
		result.Blocked = false
		result.RequiresAdminAction = false
		result.AvailableAt = nil
		result.BlockingLimits = []string{}
	}
	return result, nil
}
