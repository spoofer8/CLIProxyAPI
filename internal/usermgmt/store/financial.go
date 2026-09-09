package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"time"
)

type BudgetLimits struct {
	LifetimeUSD *string `json:"lifetime_usd"`
	DailyUSD    *string `json:"daily_usd"`
	WeeklyUSD   *string `json:"weekly_usd"`
	MonthlyUSD  *string `json:"monthly_usd"`
}

var ErrResolutionBelowKnown = errors.New("resolved total must not be below already recorded cost")

func (b BudgetLimits) Limited() bool {
	return b.LifetimeUSD != nil || b.DailyUSD != nil || b.WeeklyUSD != nil || b.MonthlyUSD != nil
}
func (b *BudgetLimits) Validate() error {
	for _, v := range []**string{&b.LifetimeUSD, &b.DailyUSD, &b.WeeklyUSD, &b.MonthlyUSD} {
		if *v != nil {
			n, e := NormalizeMoney(**v)
			if e != nil {
				return e
			}
			*v = &n
		}
	}
	return nil
}
func (s *Store) GetBudget(ctx context.Context, id string) (BudgetLimits, error) {
	var b BudgetLimits
	err := s.query().QueryRowContext(ctx, `SELECT b.lifetime_usd::text,b.daily_usd::text,b.weekly_usd::text,b.monthly_usd::text FROM cpa_users u LEFT JOIN cpa_user_budgets b ON b.user_id=u.id WHERE u.id=$1`, id).Scan(&b.LifetimeUSD, &b.DailyUSD, &b.WeeklyUSD, &b.MonthlyUSD)
	if err != nil {
		return b, domainError("get budget", err)
	}
	return b, b.Validate()
}
func (s *Store) SetBudget(ctx context.Context, id string, b BudgetLimits) error {
	if e := b.Validate(); e != nil {
		return e
	}
	_, e := s.query().ExecContext(ctx, `INSERT INTO cpa_user_budgets(user_id,lifetime_usd,daily_usd,weekly_usd,monthly_usd) VALUES($1,$2,$3,$4,$5) ON CONFLICT(user_id) DO UPDATE SET lifetime_usd=EXCLUDED.lifetime_usd,daily_usd=EXCLUDED.daily_usd,weekly_usd=EXCLUDED.weekly_usd,monthly_usd=EXCLUDED.monthly_usd,updated_at=now()`, id, b.LifetimeUSD, b.DailyUSD, b.WeeklyUSD, b.MonthlyUSD)
	return domainError("set budget", e)
}
func (s *Store) FinancialActivation(ctx context.Context) (time.Time, error) {
	var t time.Time
	e := s.query().QueryRowContext(ctx, `SELECT activated_at FROM cpa_financial_settings WHERE singleton`).Scan(&t)
	return t, domainError("get accounting activation", e)
}
func (s *Store) BeginFinancialRequest(ctx context.Context, id, userID, model string, at time.Time) error {
	_, e := s.query().ExecContext(ctx, `INSERT INTO cpa_financial_requests(id,user_id,model,at) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO NOTHING`, id, userID, model, at)
	return domainError("begin cost accounting", e)
}
func (s *Store) FinishFinancialRequest(ctx context.Context, id string, status int) error {
	// A success without usage is unknown, not free. A pre-upstream rejection can
	// be completed without usage. Explicit upstream failures arrive as events.
	_, e := s.query().ExecContext(ctx, `UPDATE cpa_financial_requests r SET completed_at=now(),state=CASE WHEN state='unresolved' THEN 'unresolved' WHEN EXISTS(SELECT 1 FROM cpa_cost_events e WHERE e.request_id=r.id AND e.status='unresolved') OR ((r.attempted OR $2<400) AND NOT EXISTS(SELECT 1 FROM cpa_cost_events e WHERE e.request_id=r.id)) THEN 'unresolved' ELSE 'completed' END, reason=CASE WHEN (r.attempted OR $2<400) AND NOT EXISTS(SELECT 1 FROM cpa_cost_events e WHERE e.request_id=r.id) THEN 'missing_usage' ELSE reason END WHERE id=$1 AND state!='resolved'`, id, status)
	return domainError("finish cost accounting", e)
}
func (s *Store) MarkFinancialUnresolved(ctx context.Context, id, reason string) error {
	_, e := s.query().ExecContext(ctx, `UPDATE cpa_financial_requests SET state='unresolved',reason=$2 WHERE id=$1 AND state!='resolved'`, id, reason)
	return domainError("mark unresolved cost", e)
}

type CostEvent struct {
	ID             string          `json:"id"`
	RequestID      string          `json:"request_id"`
	UserID         string          `json:"user_id"`
	Provider       string          `json:"provider"`
	Model          string          `json:"model"`
	Alias          string          `json:"alias"`
	At             time.Time       `json:"at"`
	CostUSD        *string         `json:"cost_usd"`
	Status         string          `json:"status"`
	Reason         string          `json:"reason"`
	PriceID        *string         `json:"price_id"`
	TokenBreakdown json.RawMessage `json:"token_breakdown"`
	PriceSnapshot  json.RawMessage `json:"price_snapshot"`
	ResolutionNote string          `json:"resolution_note,omitempty"`
}

func (s *Store) RecordCostEvent(ctx context.Context, e CostEvent) error {
	if e.CostUSD != nil {
		n, err := NormalizeMoney(*e.CostUSD)
		if err != nil {
			return err
		}
		e.CostUSD = &n
	}
	if len(e.TokenBreakdown) == 0 {
		e.TokenBreakdown = json.RawMessage(`{}`)
	}
	if len(e.PriceSnapshot) == 0 {
		e.PriceSnapshot = json.RawMessage(`{}`)
	}
	return s.Transaction(ctx, func(tx *Store) error {
		var locked string
		if er := tx.query().QueryRowContext(ctx, `SELECT state FROM cpa_financial_requests WHERE id=$1 AND user_id=$2 FOR UPDATE`, e.RequestID, e.UserID).Scan(&locked); er != nil {
			return domainError("lock financial request", er)
		}
		// Resolution is a final administrator accounting decision, including late records.
		if locked == "resolved" {
			return nil
		}
		inserted, er := tx.query().ExecContext(ctx, `INSERT INTO cpa_cost_events(id,request_id,user_id,provider,model,alias,at,cost_usd,status,reason,price_id,token_breakdown,price_snapshot,resolution_note) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT(id) DO NOTHING`, e.ID, e.RequestID, e.UserID, e.Provider, e.Model, e.Alias, e.At, e.CostUSD, e.Status, e.Reason, e.PriceID, e.TokenBreakdown, e.PriceSnapshot, e.ResolutionNote)
		if er != nil {
			return domainError("record cost event", er)
		}
		if count, err := inserted.RowsAffected(); err != nil {
			return domainError("check cost event insert", err)
		} else if count == 0 {
			return nil
		}
		if e.Status == "unresolved" {
			return tx.MarkFinancialUnresolved(ctx, e.RequestID, e.Reason)
		}
		return nil
	})
}
func (s *Store) ListCostEvents(ctx context.Context, userID string, limit, offset int) ([]CostEvent, int, error) {
	var total int
	e := s.query().QueryRowContext(ctx, `SELECT count(*) FROM cpa_cost_events WHERE user_id=$1`, userID).Scan(&total)
	if e != nil {
		return nil, 0, domainError("count costs", e)
	}
	rows, e := s.query().QueryContext(ctx, `SELECT id,request_id,user_id,provider,model,alias,at,cost_usd::text,status,reason,price_id,token_breakdown,price_snapshot,resolution_note FROM cpa_cost_events WHERE user_id=$1 ORDER BY at DESC,id DESC LIMIT $2 OFFSET $3`, userID, limit, offset)
	if e != nil {
		return nil, 0, domainError("list costs", e)
	}
	defer rows.Close()
	result := []CostEvent{}
	for rows.Next() {
		var v CostEvent
		if e = rows.Scan(&v.ID, &v.RequestID, &v.UserID, &v.Provider, &v.Model, &v.Alias, &v.At, &v.CostUSD, &v.Status, &v.Reason, &v.PriceID, &v.TokenBreakdown, &v.PriceSnapshot, &v.ResolutionNote); e != nil {
			return nil, 0, domainError("read cost", e)
		}
		if v.CostUSD != nil {
			n, _ := NormalizeMoney(*v.CostUSD)
			v.CostUSD = &n
		}
		result = append(result, v)
	}
	return result, total, domainError("read costs", rows.Err())
}

type SpendEntry struct {
	At  time.Time
	USD string
}
type FinancialSnapshot struct {
	LifetimeUSD   string
	Entries       []SpendEntry
	UnresolvedIDs []string
	UnpricedIDs   []string
}

func (s *Store) FinancialSnapshot(ctx context.Context, userID string, since time.Time) (FinancialSnapshot, error) {
	var result FinancialSnapshot
	e := s.query().QueryRowContext(ctx, `SELECT COALESCE(SUM(cost_usd),0)::text FROM cpa_cost_events WHERE user_id=$1`, userID).Scan(&result.LifetimeUSD)
	if e != nil {
		return result, domainError("sum cost", e)
	}
	result.LifetimeUSD, e = NormalizeMoney(result.LifetimeUSD)
	if e != nil {
		return result, e
	}
	rows, e := s.query().QueryContext(ctx, `SELECT at,cost_usd::text FROM cpa_cost_events WHERE user_id=$1 AND at>$2 AND cost_usd IS NOT NULL ORDER BY at ASC`, userID, since)
	if e != nil {
		return result, domainError("get window costs", e)
	}
	for rows.Next() {
		var v SpendEntry
		if e = rows.Scan(&v.At, &v.USD); e != nil {
			rows.Close()
			return result, domainError("read window cost", e)
		}
		result.Entries = append(result.Entries, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return result, domainError("read costs", e)
	}
	rows, e = s.query().QueryContext(ctx, `SELECT id,reason FROM cpa_financial_requests WHERE user_id=$1 AND state IN ('unresolved','in_progress')`, userID)
	if e != nil {
		return result, domainError("get unresolved costs", e)
	}
	defer rows.Close()
	for rows.Next() {
		var id, reason string
		if e = rows.Scan(&id, &reason); e != nil {
			return result, domainError("read unresolved cost", e)
		}
		if reason == "unpriced_model" {
			result.UnpricedIDs = append(result.UnpricedIDs, id)
		} else {
			result.UnresolvedIDs = append(result.UnresolvedIDs, id)
		}
	}
	return result, domainError("read unresolved costs", rows.Err())
}
func (s *Store) ResolveRequestCost(ctx context.Context, id, total, note, eventID string) error {
	amount, e := ParseMoney(total)
	if e != nil {
		return e
	}
	return s.Transaction(ctx, func(tx *Store) error {
		var userID, state string
		var at time.Time
		e := tx.query().QueryRowContext(ctx, `SELECT user_id,state,COALESCE(completed_at,at) FROM cpa_financial_requests WHERE id=$1 FOR UPDATE`, id).Scan(&userID, &state, &at)
		if e != nil {
			return domainError("get request to resolve", e)
		}
		if state == "resolved" {
			return ErrConflict
		}
		var known string
		e = tx.query().QueryRowContext(ctx, `SELECT COALESCE(sum(cost_usd),0)::text FROM cpa_cost_events WHERE request_id=$1`, id).Scan(&known)
		if e != nil {
			return domainError("sum known request costs", e)
		}
		prior, e := ParseMoney(known)
		if e != nil {
			return e
		}
		if amount.Cmp(prior) < 0 {
			return ErrResolutionBelowKnown
		}
		delta := FormatMoney(new(big.Int).Sub(amount, prior))
		_, e = tx.query().ExecContext(ctx, `INSERT INTO cpa_cost_events(id,request_id,user_id,at,cost_usd,status,reason,resolution_note) VALUES($1,$2,$3,$4,$5,'resolved','administrator_resolution',$6)`, eventID, id, userID, at, delta, note)
		if e != nil {
			return domainError("record resolution", e)
		}
		_, e = tx.query().ExecContext(ctx, `UPDATE cpa_cost_events SET status='resolved',reason='administrator_resolution' WHERE request_id=$1 AND status='unresolved'`, id)
		if e != nil {
			return domainError("resolve unknown events", e)
		}
		_, e = tx.query().ExecContext(ctx, `UPDATE cpa_financial_requests SET state='resolved',reason='administrator_resolution',completed_at=COALESCE(completed_at,now()) WHERE id=$1`, id)
		return domainError("resolve financial request", e)
	})
}

var _ = sql.ErrNoRows

func (s *Store) MarkFinancialAttempted(ctx context.Context, id string) error {
	_, e := s.query().ExecContext(ctx, `UPDATE cpa_financial_requests SET attempted=true WHERE id=$1`, id)
	return domainError("mark execution attempt", e)
}

func (s *Store) CompleteNonGeneratingRequest(ctx context.Context, id string) error {
	result, err := s.query().ExecContext(ctx, `UPDATE cpa_financial_requests SET state='completed',reason='no_generation',completed_at=now()
		WHERE id=$1 AND state='in_progress' AND NOT attempted AND NOT EXISTS(SELECT 1 FROM cpa_cost_events WHERE request_id=$1)`, id)
	if err != nil {
		return domainError("complete non-generating request", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return domainError("complete non-generating request", err)
	}
	if count != 1 {
		return errors.New("request cannot be completed as non-generating")
	}
	return nil
}
