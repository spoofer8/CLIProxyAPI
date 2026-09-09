package store

import (
	"context"
	"errors"
)

// SystemAdminUserID is independent of credentials, so rotation and restart never
// split this account's usage/activity history into different users.
const SystemAdminUserID = "00000000000000000000000001"

const SystemAdminEmail = "system-admin@cliproxy.invalid"

var ErrSystemAdminProtected = errors.New("the system Admin account cannot be disabled, demoted, or deleted")

// SystemAdminSchemaStatements upgrade deployed user tables without changing
// ordinary accounts or storing any configured credential material.
var SystemAdminSchemaStatements = []string{
	`ALTER TABLE cpa_users ADD COLUMN IF NOT EXISTS system_admin BOOLEAN NOT NULL DEFAULT false`,
	`CREATE UNIQUE INDEX IF NOT EXISTS cpa_users_single_system_admin ON cpa_users(system_admin) WHERE system_admin`,
}

// EnsureSystemAdmin creates only the reserved system account. It never promotes
// an ordinary user with a colliding ID/email, changes a password, or stores a key.
func (s *Store) EnsureSystemAdmin(ctx context.Context) (User, error) {
	user, errUser := scanUser(s.query().QueryRowContext(ctx, `INSERT INTO cpa_users
		(id,email,display_name,role,status,system_admin)
		VALUES ($1,$2,'Admin','admin','active',true)
		ON CONFLICT (id) DO UPDATE SET id=EXCLUDED.id WHERE cpa_users.system_admin
		RETURNING `+userColumns, SystemAdminUserID, SystemAdminEmail))
	if errUser != nil {
		return User{}, errUser
	}
	if !user.SystemAdmin || user.Role != "admin" || user.Status != "active" {
		return User{}, ErrSystemAdminProtected
	}
	return user, nil
}

func (s *Store) GetSystemAdmin(ctx context.Context) (User, error) {
	user, errUser := s.GetUser(ctx, SystemAdminUserID)
	if errUser != nil {
		return User{}, errUser
	}
	if !user.SystemAdmin || user.Role != "admin" || user.Status != "active" {
		return User{}, ErrSystemAdminProtected
	}
	return user, nil
}
