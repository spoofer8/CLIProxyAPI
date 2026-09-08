package store

import (
	"context"
	"errors"
	"math"
	"time"
)

var ErrInvalidUsage = errors.New("user management: invalid or overflowing usage counter")

// MonthlyUsage reports upstream usage records/attempts, not unique client HTTP
// requests. Retried upstream attempts can each contribute a request_count.
type MonthlyUsage struct {
	UserID       string     `json:"user_id"`
	Email        string     `json:"email"`
	Period       string     `json:"period"`
	InputTokens  int64      `json:"input_tokens"`
	OutputTokens int64      `json:"output_tokens"`
	TotalTokens  int64      `json:"total_tokens"`
	RequestCount int64      `json:"request_count"`
	UpdatedAt    *time.Time `json:"updated_at"`
}

type UsageIncrement struct {
	UserID       string
	Period       string
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

// IncrementUsage atomically accounts for one upstream usage record. Concurrent
// writers cannot lose updates. The predicates prevent BIGINT overflow without
// ever publishing partial increments to any of the counters.
func (s *Store) IncrementUsage(ctx context.Context, delta UsageIncrement) error {
	if delta.InputTokens < 0 || delta.OutputTokens < 0 || delta.TotalTokens < 0 || delta.InputTokens > math.MaxInt64-delta.OutputTokens {
		return ErrInvalidUsage
	}
	if _, errPeriod := time.Parse("2006-01", delta.Period); errPeriod != nil {
		return ErrInvalidUsage
	}
	result, errWrite := s.query().ExecContext(ctx, `INSERT INTO cpa_usage_monthly
		(user_id,period,input_tokens,output_tokens,total_tokens,request_count)
		VALUES ($1,$2,$3,$4,$5,1)
		ON CONFLICT (user_id,period) DO UPDATE SET
		input_tokens=cpa_usage_monthly.input_tokens+EXCLUDED.input_tokens,
		output_tokens=cpa_usage_monthly.output_tokens+EXCLUDED.output_tokens,
		total_tokens=cpa_usage_monthly.total_tokens+EXCLUDED.total_tokens,
		request_count=cpa_usage_monthly.request_count+1, updated_at=now()
		WHERE cpa_usage_monthly.input_tokens <= $6-EXCLUDED.input_tokens
		AND cpa_usage_monthly.output_tokens <= $6-EXCLUDED.output_tokens
		AND cpa_usage_monthly.total_tokens <= $6-EXCLUDED.total_tokens
		AND cpa_usage_monthly.request_count < $6`, delta.UserID, delta.Period, delta.InputTokens, delta.OutputTokens, delta.TotalTokens, int64(math.MaxInt64))
	if errWrite != nil {
		return domainError("increment monthly usage", errWrite)
	}
	count, errCount := result.RowsAffected()
	if errCount != nil {
		return domainError("increment monthly usage", errCount)
	}
	if count != 1 {
		return ErrInvalidUsage
	}
	return nil
}

const monthlyUsageColumns = `u.id,u.email,$1::TEXT,COALESCE(m.input_tokens,0),COALESCE(m.output_tokens,0),COALESCE(m.total_tokens,0),COALESCE(m.request_count,0),m.updated_at`

func scanMonthlyUsage(row rowScanner) (MonthlyUsage, error) {
	var usage MonthlyUsage
	errScan := row.Scan(&usage.UserID, &usage.Email, &usage.Period, &usage.InputTokens, &usage.OutputTokens, &usage.TotalTokens, &usage.RequestCount, &usage.UpdatedAt)
	return usage, domainError("read monthly usage", errScan)
}

func (s *Store) GetMonthlyUsage(ctx context.Context, userID, period string) (MonthlyUsage, error) {
	return scanMonthlyUsage(s.query().QueryRowContext(ctx, `SELECT `+monthlyUsageColumns+`
		FROM cpa_users u LEFT JOIN cpa_usage_monthly m ON m.user_id=u.id AND m.period=$1 WHERE u.id=$2`, period, userID))
}

func (s *Store) ListMonthlyUsage(ctx context.Context, period string, limit, offset int) ([]MonthlyUsage, int64, error) {
	var total int64
	if errCount := s.query().QueryRowContext(ctx, `SELECT count(*) FROM cpa_users`).Scan(&total); errCount != nil {
		return nil, 0, domainError("count monthly usage users", errCount)
	}
	rows, errQuery := s.query().QueryContext(ctx, `SELECT `+monthlyUsageColumns+`
		FROM cpa_users u LEFT JOIN cpa_usage_monthly m ON m.user_id=u.id AND m.period=$1
		ORDER BY u.id LIMIT $2 OFFSET $3`, period, limit, offset)
	if errQuery != nil {
		return nil, 0, domainError("list monthly usage", errQuery)
	}
	defer func() { _ = rows.Close() }()
	items := make([]MonthlyUsage, 0)
	for rows.Next() {
		item, errScan := scanMonthlyUsage(rows)
		if errScan != nil {
			return nil, 0, errScan
		}
		items = append(items, item)
	}
	return items, total, domainError("list monthly usage", rows.Err())
}
