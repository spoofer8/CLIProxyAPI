package store

import (
	"context"
	"encoding/json"
	"time"
)

type AuditEvent struct {
	ID       int64          `json:"id"`
	At       time.Time      `json:"at"`
	Actor    string         `json:"actor"`
	Action   string         `json:"action"`
	Target   string         `json:"target"`
	ClientIP string         `json:"client_ip"`
	Detail   map[string]any `json:"detail"`
}

func (s *Store) AppendAudit(ctx context.Context, event AuditEvent) error {
	if event.Detail == nil {
		event.Detail = map[string]any{}
	}
	detail, errJSON := json.Marshal(event.Detail)
	if errJSON != nil {
		return operationError("encode audit detail", errJSON)
	}
	_, errInsert := s.query().ExecContext(ctx, `INSERT INTO cpa_audit_events(actor,action,target,client_ip,detail) VALUES($1,$2,$3,$4,$5::jsonb)`, event.Actor, event.Action, event.Target, event.ClientIP, string(detail))
	return domainError("append audit event", errInsert)
}

func (s *Store) ListAudit(ctx context.Context, limit int, before int64, actor, action string) ([]AuditEvent, error) {
	rows, errQuery := s.query().QueryContext(ctx, `SELECT id,at,actor,action,target,client_ip,detail FROM cpa_audit_events WHERE ($1::BIGINT=0 OR id<$1) AND ($2='' OR actor=$2) AND ($3='' OR action=$3) ORDER BY id DESC LIMIT $4`, before, actor, action, limit)
	if errQuery != nil {
		return nil, domainError("list audit events", errQuery)
	}
	defer func() { _ = rows.Close() }()
	events := make([]AuditEvent, 0)
	for rows.Next() {
		var event AuditEvent
		var detail []byte
		if errScan := rows.Scan(&event.ID, &event.At, &event.Actor, &event.Action, &event.Target, &event.ClientIP, &detail); errScan != nil {
			return nil, domainError("read audit event", errScan)
		}
		if errJSON := json.Unmarshal(detail, &event.Detail); errJSON != nil {
			return nil, operationError("decode audit detail", errJSON)
		}
		events = append(events, event)
	}
	return events, domainError("list audit events", rows.Err())
}
