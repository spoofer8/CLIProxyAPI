// Package store persists user management data independently of proxy credentials
// and configuration storage. It uses the existing pgx database/sql driver.
package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type Config struct {
	DSN string
}

type Store struct {
	db *sql.DB
}

// Open connects to PostgreSQL and atomically creates the six domain tables.
// Connection/parser errors deliberately omit driver text because it may contain
// DSN credentials. Callers can still use errors.Is/As on the wrapped cause.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(cfg.DSN) == "" {
		return nil, errors.New("user management store: DSN is required")
	}
	db, errOpen := sql.Open("pgx", cfg.DSN)
	if errOpen != nil {
		return nil, operationError("open database", errOpen)
	}
	// This is a small, single-instance domain store; bound idle/open connections
	// independently of the optional configuration storage backend.
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(2)
	s := &Store{db: db}
	if errHealth := s.Health(ctx); errHealth != nil {
		_ = s.Close()
		return nil, errHealth
	}
	if errSchema := s.EnsureSchema(ctx); errSchema != nil {
		_ = s.Close()
		return nil, errSchema
	}
	return s, nil
}

// DB exposes the domain connection for store operations and integration checks.
// Its ownership stays with Store; callers must not close it.
func (s *Store) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

func (s *Store) Health(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("user management store: not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errPing := s.db.PingContext(ctx); errPing != nil {
		return operationError("ping database", errPing)
	}
	return nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if errClose := s.db.Close(); errClose != nil {
		return operationError("close database", errClose)
	}
	return nil
}

type databaseError struct {
	operation string
	cause     error
}

func (e *databaseError) Error() string { return "user management store: " + e.operation + " failed" }
func (e *databaseError) Unwrap() error { return e.cause }

func operationError(operation string, cause error) error {
	return &databaseError{operation: operation, cause: cause}
}
