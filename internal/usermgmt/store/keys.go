package store

import (
	"context"
	"time"
)

// APIKey deliberately has no hash field; list/create responses share this DTO.
type APIKey struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	KeyPrefix  string     `json:"key_prefix"`
	Label      string     `json:"label"`
	Status     string     `json:"status"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

const keyColumns = `id, user_id, key_prefix, label, status, last_used_at, created_at, revoked_at`

func scanKey(row rowScanner) (APIKey, error) {
	var key APIKey
	errScan := row.Scan(&key.ID, &key.UserID, &key.KeyPrefix, &key.Label, &key.Status, &key.LastUsedAt, &key.CreatedAt, &key.RevokedAt)
	return key, domainError("read API key", errScan)
}

func (s *Store) CreateKey(ctx context.Context, key APIKey, hash string) (APIKey, error) {
	return scanKey(s.query().QueryRowContext(ctx, `INSERT INTO cpa_user_api_keys
		(id,user_id,key_hash,key_prefix,label) VALUES ($1,$2,$3,$4,$5) RETURNING `+keyColumns,
		key.ID, key.UserID, hash, key.KeyPrefix, key.Label))
}

func (s *Store) ListKeys(ctx context.Context, userID string, limit, offset int) ([]APIKey, int64, error) {
	if _, errUser := s.GetUser(ctx, userID); errUser != nil {
		return nil, 0, errUser
	}
	var total int64
	if errCount := s.query().QueryRowContext(ctx, `SELECT count(*) FROM cpa_user_api_keys WHERE user_id=$1`, userID).Scan(&total); errCount != nil {
		return nil, 0, domainError("count API keys", errCount)
	}
	rows, errQuery := s.query().QueryContext(ctx, `SELECT `+keyColumns+` FROM cpa_user_api_keys WHERE user_id=$1 ORDER BY id LIMIT $2 OFFSET $3`, userID, limit, offset)
	if errQuery != nil {
		return nil, 0, domainError("list API keys", errQuery)
	}
	defer func() { _ = rows.Close() }()
	keys := make([]APIKey, 0)
	for rows.Next() {
		key, errScan := scanKey(rows)
		if errScan != nil {
			return nil, 0, errScan
		}
		keys = append(keys, key)
	}
	return keys, total, domainError("list API keys", rows.Err())
}

func (s *Store) RevokeKey(ctx context.Context, id string) (APIKey, error) {
	return scanKey(s.query().QueryRowContext(ctx, `UPDATE cpa_user_api_keys
		SET status='revoked', revoked_at=COALESCE(revoked_at,now()) WHERE id=$1 RETURNING `+keyColumns, id))
}

// KeyIdentity is only populated for active keys belonging to active users.
type KeyIdentity struct {
	UserID      string
	Email       string
	Role        string
	KeyID       string
	SystemAdmin bool
}

func (s *Store) LookupActiveKey(ctx context.Context, hash string) (KeyIdentity, error) {
	var identity KeyIdentity
	errQuery := s.query().QueryRowContext(ctx, `SELECT u.id,u.email,u.role,k.id,u.system_admin
		FROM cpa_user_api_keys k JOIN cpa_users u ON u.id=k.user_id
		WHERE k.key_hash=$1 AND k.status='active' AND k.revoked_at IS NULL AND u.status='active'`, hash).
		Scan(&identity.UserID, &identity.Email, &identity.Role, &identity.KeyID, &identity.SystemAdmin)
	return identity, domainError("authenticate API key", errQuery)
}

// LookupActiveKeyIdentity revalidates an already-authenticated execution without
// retaining its plaintext key. Both key ownership and current account/key state
// must still match, including for subsequent frames on an existing WebSocket.
func (s *Store) LookupActiveKeyIdentity(ctx context.Context, userID, keyID string) (KeyIdentity, error) {
	var identity KeyIdentity
	errQuery := s.query().QueryRowContext(ctx, `SELECT u.id,u.email,u.role,k.id,u.system_admin
		FROM cpa_user_api_keys k JOIN cpa_users u ON u.id=k.user_id
		WHERE u.id=$1 AND k.id=$2 AND k.status='active' AND k.revoked_at IS NULL AND u.status='active'`, userID, keyID).
		Scan(&identity.UserID, &identity.Email, &identity.Role, &identity.KeyID, &identity.SystemAdmin)
	return identity, domainError("revalidate API key", errQuery)
}

// TouchKeys applies one batch statement, independent of individual request paths.
func (s *Store) TouchKeys(ctx context.Context, lastUsed map[string]time.Time) error {
	if len(lastUsed) == 0 {
		return nil
	}
	ids := make([]string, 0, len(lastUsed))
	timestamps := make([]time.Time, 0, len(lastUsed))
	for id, at := range lastUsed {
		ids = append(ids, id)
		timestamps = append(timestamps, at)
	}
	_, errUpdate := s.query().ExecContext(ctx, `UPDATE cpa_user_api_keys AS k
		SET last_used_at=GREATEST(k.last_used_at,b.at)
		FROM unnest($1::TEXT[],$2::TIMESTAMPTZ[]) AS b(id,at) WHERE k.id=b.id`, ids, timestamps)
	return domainError("update API key last used", errUpdate)
}
