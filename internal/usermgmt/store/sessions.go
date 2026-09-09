package store

import (
	"context"
	"errors"
	"time"
)

var ErrCredentialsChanged = errors.New("user management: login credentials are no longer valid")

type LoginUser struct {
	User
	PasswordHash *string `json:"-"`
}

type SessionIdentity struct {
	UserID      string    `json:"user_id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func (s *Store) LoginUser(ctx context.Context, email string) (LoginUser, error) {
	var user LoginUser
	err := s.query().QueryRowContext(ctx, `SELECT `+userColumns+`,password_hash FROM cpa_users WHERE email=$1`, email).Scan(&user.ID, &user.Email, &user.DisplayName, &user.Role, &user.Status, &user.SystemAdmin, &user.MonthlyTokenLimit, &user.CreatedAt, &user.UpdatedAt, &user.PasswordHash)
	return user, domainError("find login account", err)
}

// CreateSession rechecks the verified hash under the same user-row lock used by
// password/status updates, preventing a late old-password login after revocation.
func (s *Store) CreateSession(ctx context.Context, id, userID, verifiedHash, clientIP string, expires time.Time) error {
	return s.Transaction(ctx, func(txStore *Store) error {
		var hash *string
		var role, status string
		if err := txStore.query().QueryRowContext(ctx, `SELECT password_hash,role,status FROM cpa_users WHERE id=$1 FOR UPDATE`, userID).Scan(&hash, &role, &status); err != nil {
			return domainError("lock login account", err)
		}
		if hash == nil || *hash != verifiedHash || role != "admin" || status != "active" {
			return ErrCredentialsChanged
		}
		_, err := txStore.query().ExecContext(ctx, `INSERT INTO cpa_sessions(id,user_id,expires_at,client_ip) VALUES($1,$2,$3,$4)`, id, userID, expires, clientIP)
		return domainError("create panel session", err)
	})
}

func (s *Store) GetSession(ctx context.Context, id string) (SessionIdentity, error) {
	var identity SessionIdentity
	err := s.query().QueryRowContext(ctx, `SELECT u.id,u.email,u.display_name,u.role,s.expires_at FROM cpa_sessions s JOIN cpa_users u ON u.id=s.user_id WHERE s.id=$1 AND s.expires_at>now() AND u.role='admin' AND u.status='active' AND u.password_hash IS NOT NULL`, id).Scan(&identity.UserID, &identity.Email, &identity.DisplayName, &identity.Role, &identity.ExpiresAt)
	return identity, domainError("authenticate panel session", err)
}

func (s *Store) DeleteSession(ctx context.Context, id string) (string, error) {
	var userID string
	err := s.query().QueryRowContext(ctx, `DELETE FROM cpa_sessions WHERE id=$1 RETURNING user_id`, id).Scan(&userID)
	return userID, domainError("delete panel session", err)
}

func (s *Store) RevokeUserSessions(ctx context.Context, userID string) error {
	_, err := s.query().ExecContext(ctx, `DELETE FROM cpa_sessions WHERE user_id=$1`, userID)
	return domainError("revoke account sessions", err)
}

func (s *Store) SetPassword(ctx context.Context, userID string, hash *string) error {
	return s.Transaction(ctx, func(txStore *Store) error {
		var role string
		if err := txStore.query().QueryRowContext(ctx, `SELECT role FROM cpa_users WHERE id=$1 FOR UPDATE`, userID).Scan(&role); err != nil {
			return domainError("find password account", err)
		}
		if hash != nil && role != "admin" {
			return ErrCredentialsChanged
		}
		if _, err := txStore.query().ExecContext(ctx, `UPDATE cpa_users SET password_hash=$2,updated_at=now() WHERE id=$1`, userID, hash); err != nil {
			return domainError("set account password", err)
		}
		return txStore.RevokeUserSessions(ctx, userID)
	})
}
