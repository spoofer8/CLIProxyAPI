package store

import "context"

// Permission is a management-safe rule. The user is supplied by its API path.
type Permission struct {
	Scope  string `json:"scope"`
	Value  string `json:"value"`
	Effect string `json:"effect"`
}

func (s *Store) ListPermissions(ctx context.Context, userID string) ([]Permission, error) {
	// Missing users must not be confused with an existing default-open account.
	if _, errUser := s.GetUser(ctx, userID); errUser != nil {
		return nil, errUser
	}
	rows, errQuery := s.query().QueryContext(ctx, `SELECT scope,value,effect FROM cpa_user_permissions WHERE user_id=$1 ORDER BY scope,value`, userID)
	if errQuery != nil {
		return nil, domainError("list permissions", errQuery)
	}
	defer func() { _ = rows.Close() }()
	permissions := make([]Permission, 0)
	for rows.Next() {
		var permission Permission
		if errScan := rows.Scan(&permission.Scope, &permission.Value, &permission.Effect); errScan != nil {
			return nil, domainError("read permission", errScan)
		}
		permissions = append(permissions, permission)
	}
	return permissions, domainError("list permissions", rows.Err())
}

// ReplacePermissions serializes replacements on the user row. Readers see the
// complete old or new set, and insertion failures roll the deletion back.
func (s *Store) ReplacePermissions(ctx context.Context, userID string, permissions []Permission) error {
	return s.Transaction(ctx, func(txStore *Store) error {
		tx := txStore.query()
		var foundID string
		if errUser := tx.QueryRowContext(ctx, `SELECT id FROM cpa_users WHERE id=$1 FOR UPDATE`, userID).Scan(&foundID); errUser != nil {
			return domainError("find permission user", errUser)
		}
		if _, errDelete := tx.ExecContext(ctx, `DELETE FROM cpa_user_permissions WHERE user_id=$1`, userID); errDelete != nil {
			return domainError("replace permissions", errDelete)
		}
		for _, permission := range permissions {
			if _, errInsert := tx.ExecContext(ctx, `INSERT INTO cpa_user_permissions (user_id,scope,value,effect) VALUES ($1,$2,$3,$4)`, userID, permission.Scope, permission.Value, permission.Effect); errInsert != nil {
				return domainError("write permission", errInsert)
			}
		}
		return nil
	})
}
