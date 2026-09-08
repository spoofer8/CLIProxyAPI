package store

import (
	"context"
	"math"
	"time"
)

// RequestActivitySummary deliberately excludes all request content.
type RequestActivitySummary struct {
	Sequence     int64     `json:"-"`
	ID           string    `json:"id"`
	UserID       string    `json:"user_id"`
	KeyID        string    `json:"key_id"`
	At           time.Time `json:"at"`
	Method       string    `json:"method"`
	Path         string    `json:"path"`
	Model        string    `json:"model"`
	StatusCode   int       `json:"status_code"`
	DurationMS   int64     `json:"duration_ms"`
	Provider     *string   `json:"provider"`
	InputTokens  *int64    `json:"input_tokens"`
	OutputTokens *int64    `json:"output_tokens"`
	TotalTokens  *int64    `json:"total_tokens"`
}

type RequestActivity struct {
	RequestActivitySummary
	BodyPreview       string `json:"body_preview"`
	BodyTruncated     bool   `json:"body_truncated"`
	BodyOmittedReason string `json:"body_omitted_reason"`
}

func (s *Store) InsertRequestActivity(ctx context.Context, item RequestActivity) error {
	_, errInsert := s.query().ExecContext(ctx, `INSERT INTO cpa_request_activity
		(id,user_id,key_id,at,method,path,model,body_preview,body_truncated,body_omitted_reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, item.ID, item.UserID, item.KeyID, item.At, item.Method, item.Path, item.Model, item.BodyPreview, item.BodyTruncated, item.BodyOmittedReason)
	return domainError("insert request activity", errInsert)
}

func (s *Store) FinishRequestActivity(ctx context.Context, id string, status int, durationMS int64) error {
	_, errUpdate := s.query().ExecContext(ctx, `UPDATE cpa_request_activity SET status_code=$2,duration_ms=$3 WHERE id=$1`, id, status, durationMS)
	return domainError("finish request activity", errUpdate)
}

func (s *Store) FinishRequestActivityPreview(ctx context.Context, item RequestActivity, status int, durationMS int64) error {
	_, errUpdate := s.query().ExecContext(ctx, `UPDATE cpa_request_activity SET status_code=$2,duration_ms=$3,
		model=$4,body_preview=$5,body_truncated=$6,body_omitted_reason=$7 WHERE id=$1`,
		item.ID, status, durationMS, item.Model, item.BodyPreview, item.BodyTruncated, item.BodyOmittedReason)
	return domainError("finish request activity preview", errUpdate)
}

func (s *Store) AddRequestActivityUsage(ctx context.Context, id, provider string, delta UsageIncrement) error {
	if delta.InputTokens < 0 || delta.OutputTokens < 0 || delta.TotalTokens < 0 {
		return ErrInvalidUsage
	}
	_, errUpdate := s.query().ExecContext(ctx, `UPDATE cpa_request_activity SET
		provider=CASE WHEN $2='' THEN provider ELSE $2 END,
		input_tokens=COALESCE(input_tokens,0)+$3, output_tokens=COALESCE(output_tokens,0)+$4,
		total_tokens=COALESCE(total_tokens,0)+$5
		WHERE id=$1 AND user_id=$6 AND COALESCE(input_tokens,0)<=$7::BIGINT-$3::BIGINT
		AND COALESCE(output_tokens,0)<=$7::BIGINT-$4::BIGINT AND COALESCE(total_tokens,0)<=$7::BIGINT-$5::BIGINT`, id, provider, delta.InputTokens, delta.OutputTokens, delta.TotalTokens, delta.UserID, int64(math.MaxInt64))
	return domainError("enrich request activity", errUpdate)
}

const requestActivityColumns = `sequence,id,user_id,key_id,at,method,path,model,status_code,duration_ms,provider,input_tokens,output_tokens,total_tokens`

func requestActivityScanTargets(item *RequestActivitySummary) []any {
	return []any{&item.Sequence, &item.ID, &item.UserID, &item.KeyID, &item.At, &item.Method, &item.Path, &item.Model, &item.StatusCode, &item.DurationMS, &item.Provider, &item.InputTokens, &item.OutputTokens, &item.TotalTokens}
}

func (s *Store) ListRequestActivity(ctx context.Context, userID string, limit int, before int64, since time.Time) ([]RequestActivitySummary, error) {
	return s.ListRequestActivityFiltered(ctx, userID, limit, before, since, "", "")
}

func (s *Store) ListRequestActivityFiltered(ctx context.Context, userID string, limit int, before int64, since time.Time, model, status string) ([]RequestActivitySummary, error) {
	rows, errQuery := s.query().QueryContext(ctx, `SELECT `+requestActivityColumns+` FROM cpa_request_activity WHERE user_id=$1 AND ($2::BIGINT=0 OR sequence<$2) AND at >= $3
		AND ($5='' OR strpos(lower(model),lower($5))>0)
		AND ($6='' OR ($6='success' AND status_code BETWEEN 200 AND 399) OR ($6='error' AND status_code>=400))
		ORDER BY sequence DESC LIMIT $4`, userID, before, since, limit, model, status)
	if errQuery != nil {
		return nil, domainError("list request activity", errQuery)
	}
	defer func() { _ = rows.Close() }()
	items := make([]RequestActivitySummary, 0)
	for rows.Next() {
		var item RequestActivitySummary
		if errScan := rows.Scan(requestActivityScanTargets(&item)...); errScan != nil {
			return nil, domainError("read request activity", errScan)
		}
		items = append(items, item)
	}
	return items, domainError("list request activity", rows.Err())
}

func (s *Store) GetRequestActivity(ctx context.Context, id string, since time.Time) (RequestActivity, error) {
	var item RequestActivity
	targets := append(requestActivityScanTargets(&item.RequestActivitySummary), &item.BodyPreview, &item.BodyTruncated, &item.BodyOmittedReason)
	errScan := s.query().QueryRowContext(ctx, `SELECT `+requestActivityColumns+`,body_preview,body_truncated,body_omitted_reason FROM cpa_request_activity WHERE id=$1 AND at >= $2`, id, since).Scan(targets...)
	return item, domainError("read request activity", errScan)
}

// DeleteExpiredRequestActivity limits each transaction to avoid long table locks.
func (s *Store) DeleteExpiredRequestActivity(ctx context.Context, before time.Time) (int64, error) {
	result, errDelete := s.query().ExecContext(ctx, `DELETE FROM cpa_request_activity WHERE sequence IN (SELECT sequence FROM cpa_request_activity WHERE at<$1 ORDER BY at LIMIT 1000)`, before)
	if errDelete != nil {
		return 0, domainError("expire request activity", errDelete)
	}
	count, errCount := result.RowsAffected()
	return count, domainError("expire request activity", errCount)
}
