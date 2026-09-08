package store

import (
	"context"
	"errors"
)

// API key hashes and session IDs are SHA-256 digests; plaintext credentials never
// belong in this schema. Usage.Record.APIKey contains cpa_users.id for user keys,
// while the existing usage pipeline retains its legacy semantics for admin keys.
var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS cpa_users (
		id TEXT PRIMARY KEY,
		email TEXT NOT NULL UNIQUE,
		display_name TEXT NOT NULL DEFAULT '',
		role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin', 'user')),
		status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
		password_hash TEXT,
		monthly_token_limit BIGINT,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE TABLE IF NOT EXISTS cpa_user_api_keys (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES cpa_users(id) ON DELETE CASCADE,
		key_hash TEXT NOT NULL UNIQUE,
		key_prefix TEXT NOT NULL,
		label TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'revoked')),
		last_used_at TIMESTAMPTZ,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		revoked_at TIMESTAMPTZ
	)`,
	`CREATE INDEX IF NOT EXISTS cpa_user_api_keys_user ON cpa_user_api_keys(user_id)`,
	`CREATE TABLE IF NOT EXISTS cpa_user_permissions (
		user_id TEXT NOT NULL REFERENCES cpa_users(id) ON DELETE CASCADE,
		scope TEXT NOT NULL CHECK (scope IN ('model', 'provider')),
		value TEXT NOT NULL,
		effect TEXT NOT NULL DEFAULT 'allow' CHECK (effect IN ('allow', 'deny')),
		PRIMARY KEY (user_id, scope, value)
	)`,
	`CREATE TABLE IF NOT EXISTS cpa_usage_monthly (
		user_id TEXT NOT NULL REFERENCES cpa_users(id) ON DELETE CASCADE,
		period TEXT NOT NULL,
		input_tokens BIGINT NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
		output_tokens BIGINT NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
		total_tokens BIGINT NOT NULL DEFAULT 0 CHECK (total_tokens >= 0),
		request_count BIGINT NOT NULL DEFAULT 0 CHECK (request_count >= 0),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (user_id, period)
	)`,
	`CREATE TABLE IF NOT EXISTS cpa_sessions (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES cpa_users(id) ON DELETE CASCADE,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		expires_at TIMESTAMPTZ NOT NULL,
		client_ip TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS cpa_sessions_expiry ON cpa_sessions(expires_at)`,
	`CREATE TABLE IF NOT EXISTS cpa_audit_events (
		id BIGSERIAL PRIMARY KEY,
		at TIMESTAMPTZ NOT NULL DEFAULT now(),
		actor TEXT NOT NULL DEFAULT '',
		action TEXT NOT NULL,
		target TEXT NOT NULL DEFAULT '',
		client_ip TEXT NOT NULL DEFAULT '',
		detail JSONB NOT NULL DEFAULT '{}'::jsonb
	)`,
	`CREATE INDEX IF NOT EXISTS cpa_audit_at ON cpa_audit_events(at DESC)`,
	`CREATE TABLE IF NOT EXISTS cpa_request_activity (
		sequence BIGSERIAL PRIMARY KEY,
		id TEXT NOT NULL UNIQUE,
		user_id TEXT NOT NULL REFERENCES cpa_users(id) ON DELETE CASCADE,
		key_id TEXT NOT NULL DEFAULT '',
		at TIMESTAMPTZ NOT NULL,
		method TEXT NOT NULL, path TEXT NOT NULL, model TEXT NOT NULL DEFAULT '',
		status_code INTEGER NOT NULL DEFAULT 0,
		duration_ms BIGINT NOT NULL DEFAULT 0,
		provider TEXT, input_tokens BIGINT, output_tokens BIGINT, total_tokens BIGINT,
		body_preview TEXT NOT NULL DEFAULT '' CHECK (octet_length(body_preview) <= 262144),
		body_truncated BOOLEAN NOT NULL DEFAULT FALSE,
		body_omitted_reason TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS cpa_request_activity_user_sequence ON cpa_request_activity(user_id,sequence DESC)`,
	`CREATE INDEX IF NOT EXISTS cpa_request_activity_at ON cpa_request_activity(at)`,
}

// EnsureSchema is idempotent and transactional. An advisory lock also makes
// concurrent bootstrap safe when two startup attempts share a database.
func (s *Store) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("user management store: not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tx, errBegin := s.db.BeginTx(ctx, nil)
	if errBegin != nil {
		return operationError("begin schema transaction", errBegin)
	}
	defer func() { _ = tx.Rollback() }()
	if _, errLock := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(1129333077)`); errLock != nil {
		return operationError("lock schema bootstrap", errLock)
	}
	for _, statement := range schemaStatements {
		if _, errSchema := tx.ExecContext(ctx, statement); errSchema != nil {
			return operationError("create schema", errSchema)
		}
	}
	if errCommit := tx.Commit(); errCommit != nil {
		return operationError("commit schema transaction", errCommit)
	}
	return nil
}
