package store

import (
	"context"
	"time"
)

// Explicit ALTER statements upgrade installations which already have previews.
var requestActivitySchemaStatements = []string{
	`ALTER TABLE cpa_request_activity ADD COLUMN IF NOT EXISTS prompt_preview TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE cpa_request_activity ADD COLUMN IF NOT EXISTS response_preview TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE cpa_request_activity ADD COLUMN IF NOT EXISTS session_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE cpa_request_activity ADD COLUMN IF NOT EXISTS session_source TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE cpa_request_activity ADD COLUMN IF NOT EXISTS previous_response_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE cpa_request_activity ADD COLUMN IF NOT EXISTS response_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE cpa_request_activity ADD COLUMN IF NOT EXISTS capture_state TEXT NOT NULL DEFAULT 'legacy_preview'`,
	`ALTER TABLE cpa_request_activity ADD COLUMN IF NOT EXISTS request_content_bytes BIGINT NOT NULL DEFAULT 0`,
	`ALTER TABLE cpa_request_activity ADD COLUMN IF NOT EXISTS response_content_bytes BIGINT NOT NULL DEFAULT 0`,
	`CREATE INDEX IF NOT EXISTS cpa_request_activity_session ON cpa_request_activity(user_id,session_id,sequence DESC)`,
	`CREATE INDEX IF NOT EXISTS cpa_request_activity_response ON cpa_request_activity(user_id,response_id) WHERE response_id<>''`,
	`CREATE TABLE IF NOT EXISTS cpa_request_content (
		sequence BIGSERIAL PRIMARY KEY,
		request_id TEXT NOT NULL REFERENCES cpa_request_activity(id) ON DELETE CASCADE,
		direction TEXT NOT NULL CHECK(direction IN ('request','response')),
		part BIGINT NOT NULL, format TEXT NOT NULL, text TEXT NOT NULL,
		UNIQUE(request_id,direction,part)
	)`,
}

type RequestContentChunk struct {
	Sequence  int64  `json:"sequence"`
	Direction string `json:"direction"`
	Format    string `json:"format"`
	Text      string `json:"text"`
}

func (s *Store) SaveRequestContentChunk(ctx context.Context, id, direction, format string, part int64, text string) error {
	_, errWrite := s.query().ExecContext(ctx, `INSERT INTO cpa_request_content(request_id,direction,part,format,text) VALUES($1,$2,$3,$4,$5)
		ON CONFLICT(request_id,direction,part) DO UPDATE SET format=EXCLUDED.format,text=EXCLUDED.text`, id, direction, part, format, text)
	return domainError("save complete activity content", errWrite)
}

func (s *Store) ListRequestContent(ctx context.Context, id, direction string, after int64, limit int) ([]RequestContentChunk, error) {
	rows, errQuery := s.query().QueryContext(ctx, `SELECT sequence,direction,format,text FROM cpa_request_content WHERE request_id=$1 AND direction=$2 AND sequence>$3 ORDER BY sequence LIMIT $4`, id, direction, after, limit)
	if errQuery != nil {
		return nil, domainError("list activity content", errQuery)
	}
	defer func() { _ = rows.Close() }()
	items := make([]RequestContentChunk, 0)
	for rows.Next() {
		var item RequestContentChunk
		if errScan := rows.Scan(&item.Sequence, &item.Direction, &item.Format, &item.Text); errScan != nil {
			return nil, domainError("read activity content", errScan)
		}
		items = append(items, item)
	}
	return items, domainError("list activity content", rows.Err())
}

func (s *Store) SetRequestActivityCaptureState(ctx context.Context, id, state string) error {
	_, errUpdate := s.query().ExecContext(ctx, `UPDATE cpa_request_activity SET capture_state=$2 WHERE id=$1`, id, state)
	return domainError("set activity capture state", errUpdate)
}

func (s *Store) CompleteRequestActivityContent(ctx context.Context, item RequestActivity, state string) error {
	_, errUpdate := s.query().ExecContext(ctx, `UPDATE cpa_request_activity SET model=$2,status_code=$3,duration_ms=$4,
		body_preview=$5,body_truncated=$6,body_omitted_reason=$7,response_preview=$8,prompt_preview=$9,
		session_id=$10,session_source=$11,previous_response_id=$12,response_id=$13,capture_state=$14,
		request_content_bytes=$15,response_content_bytes=$16 WHERE id=$1`, item.ID, item.Model, item.StatusCode, item.DurationMS,
		item.BodyPreview, item.BodyTruncated, item.BodyOmittedReason, item.ResponsePreview, item.PromptPreview,
		item.SessionID, item.SessionSource, item.PreviousResponseID, item.ResponseID, state, item.RequestContentBytes, item.ResponseContentBytes)
	return domainError("complete activity content", errUpdate)
}

func (s *Store) FindResponseSession(ctx context.Context, userID, responseID string) (string, string, error) {
	var sessionID, source string
	errQuery := s.query().QueryRowContext(ctx, `SELECT session_id,session_source FROM cpa_request_activity WHERE user_id=$1 AND response_id=$2 AND capture_state IN ('complete','interrupted') ORDER BY sequence DESC LIMIT 1`, userID, responseID).Scan(&sessionID, &source)
	return sessionID, source, domainError("find previous response session", errQuery)
}

// A child can finish before the parent's usage producer has released its lease.
// Reconcile its known response edge when the parent's durable replay completes.
func (s *Store) ReconcileRequestSessions(ctx context.Context, id string) error {
	_, errUpdate := s.query().ExecContext(ctx, `WITH RECURSIVE linked AS (
		SELECT id,user_id,response_id,sequence,session_id FROM cpa_request_activity WHERE id=$1 AND session_id<>''
		UNION ALL
		SELECT child.id,child.user_id,child.response_id,child.sequence,parent.session_id
		FROM cpa_request_activity child JOIN linked parent
		ON child.user_id=parent.user_id AND child.previous_response_id=parent.response_id AND parent.response_id<>'' AND child.sequence>parent.sequence
		WHERE child.session_source IN ('','response_id','previous_response_id')
	) UPDATE cpa_request_activity target SET session_id=linked.session_id,session_source='previous_response_id'
	FROM linked WHERE target.id=linked.id AND target.id<>$1 AND target.session_id IS DISTINCT FROM linked.session_id`, id)
	return domainError("reconcile captured response sessions", errUpdate)
}

func (s *Store) ListRequestActivitySearch(ctx context.Context, userID string, limit int, before int64, since time.Time, model, status, prompt, sessionID string) ([]RequestActivitySummary, error) {
	rows, errQuery := s.query().QueryContext(ctx, `SELECT `+requestActivityColumns+` FROM cpa_request_activity WHERE user_id=$1 AND ($2::BIGINT=0 OR sequence<$2) AND at>=$3
		AND ($5='' OR strpos(lower(model),lower($5))>0) AND ($6='' OR ($6='success' AND status_code BETWEEN 200 AND 399) OR ($6='error' AND status_code>=400))
		AND ($7='' OR strpos(lower(prompt_preview),lower($7))>0) AND ($8='' OR session_id=$8) ORDER BY sequence DESC LIMIT $4`, userID, before, since, limit, model, status, prompt, sessionID)
	if errQuery != nil {
		return nil, domainError("search request activity", errQuery)
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
	return items, domainError("search request activity", rows.Err())
}
