package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound = errors.New("user management: resource not found")
	ErrConflict = errors.New("user management: resource already exists")
)

// User contains only management-safe account fields. Password hashes are never
// selected into this DTO, including when a future session login is configured.
type User struct {
	ID                string    `json:"id"`
	Email             string    `json:"email"`
	DisplayName       string    `json:"display_name"`
	Role              string    `json:"role"`
	Status            string    `json:"status"`
	MonthlyTokenLimit *int64    `json:"monthly_token_limit"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type UserPatch struct {
	DisplayName          *string
	Role                 *string
	Status               *string
	MonthlyTokenLimitSet bool
	MonthlyTokenLimit    *int64
}

const userColumns = `id, email, display_name, role, status, monthly_token_limit, created_at, updated_at`

type rowScanner interface{ Scan(...any) error }

func scanUser(row rowScanner) (User, error) {
	var user User
	errScan := row.Scan(&user.ID, &user.Email, &user.DisplayName, &user.Role, &user.Status, &user.MonthlyTokenLimit, &user.CreatedAt, &user.UpdatedAt)
	return user, domainError("read user", errScan)
}

func domainError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrConflict
		case "23503":
			return ErrNotFound
		}
	}
	return operationError(operation, err)
}

func (s *Store) CreateUser(ctx context.Context, user User) (User, error) {
	return scanUser(s.query().QueryRowContext(ctx, `INSERT INTO cpa_users
		(id, email, display_name, role, status, monthly_token_limit) VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING `+userColumns, user.ID, user.Email, user.DisplayName, user.Role, user.Status, user.MonthlyTokenLimit))
}

func (s *Store) GetUser(ctx context.Context, id string) (User, error) {
	return scanUser(s.query().QueryRowContext(ctx, `SELECT `+userColumns+` FROM cpa_users WHERE id=$1`, id))
}

func (s *Store) ListUsers(ctx context.Context, limit, offset int) ([]User, int64, error) {
	var total int64
	if errCount := s.query().QueryRowContext(ctx, `SELECT count(*) FROM cpa_users`).Scan(&total); errCount != nil {
		return nil, 0, domainError("count users", errCount)
	}
	rows, errQuery := s.query().QueryContext(ctx, `SELECT `+userColumns+` FROM cpa_users ORDER BY id LIMIT $1 OFFSET $2`, limit, offset)
	if errQuery != nil {
		return nil, 0, domainError("list users", errQuery)
	}
	defer func() { _ = rows.Close() }()
	users := make([]User, 0)
	for rows.Next() {
		user, errScan := scanUser(rows)
		if errScan != nil {
			return nil, 0, errScan
		}
		users = append(users, user)
	}
	return users, total, domainError("list users", rows.Err())
}

func (s *Store) UpdateUser(ctx context.Context, id string, patch UserPatch) (User, error) {
	var updated User
	errUpdate := s.Transaction(ctx, func(txStore *Store) error {
		var errScan error
		updated, errScan = scanUser(txStore.query().QueryRowContext(ctx, `UPDATE cpa_users SET
		display_name=COALESCE($2,display_name), role=COALESCE($3,role), status=COALESCE($4,status),
		monthly_token_limit=CASE WHEN $5 THEN $6::BIGINT ELSE monthly_token_limit END,
		updated_at=now() WHERE id=$1 RETURNING `+userColumns,
			id, patch.DisplayName, patch.Role, patch.Status, patch.MonthlyTokenLimitSet, patch.MonthlyTokenLimit))
		if errScan != nil {
			return errScan
		}
		if (patch.Role != nil || patch.Status != nil) && (updated.Role != "admin" || updated.Status != "active") {
			return txStore.RevokeUserSessions(ctx, id)
		}
		return nil
	})
	return updated, errUpdate
}

func (s *Store) DeleteUser(ctx context.Context, id string) error {
	result, errDelete := s.query().ExecContext(ctx, `DELETE FROM cpa_users WHERE id=$1`, id)
	if errDelete != nil {
		return domainError("delete user", errDelete)
	}
	count, errCount := result.RowsAffected()
	if errCount != nil {
		return domainError("delete user", errCount)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}
