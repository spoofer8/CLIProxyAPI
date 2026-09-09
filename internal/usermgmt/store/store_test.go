package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestStoreValidationAndSafeErrors(t *testing.T) {
	for _, dsn := range []string{"", "postgres://test:DO-NOT-LOG-THIS@localhost:%/test"} {
		if _, errOpen := Open(context.Background(), Config{DSN: dsn}); errOpen == nil || strings.Contains(errOpen.Error(), "DO-NOT-LOG-THIS") {
			t.Fatal("expected a safe DSN error")
		}
	}
	var absent *Store
	if absent.DB() != nil || absent.Health(context.Background()) == nil || absent.EnsureSchema(context.Background()) == nil {
		t.Fatal("uninitialized store should report unavailable")
	}
	if errClose := absent.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	if !errors.Is(operationError("test", context.Canceled), context.Canceled) {
		t.Fatal("safe database errors should preserve cancellation identity")
	}
}

// Each integration test owns a temporary schema. The supplied DSN must identify
// a test database with CREATE SCHEMA permission; no existing tables are touched.
func integrationDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("CLIPROXY_USERMGMT_TEST_DSN")
	if dsn == "" {
		t.Skip("set CLIPROXY_USERMGMT_TEST_DSN to run PostgreSQL integration tests")
	}
	parsed, errParse := url.Parse(dsn)
	if errParse != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("test DSN must be a PostgreSQL URL")
	}
	db, errOpen := sql.Open("pgx", dsn)
	if errOpen != nil {
		t.Fatal("open test database failed")
	}
	schema := "cpa_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, errCreate := db.ExecContext(context.Background(), `CREATE SCHEMA "`+schema+`"`); errCreate != nil {
		_ = db.Close()
		t.Fatal("create isolated test schema failed")
	}
	t.Cleanup(func() {
		if _, errDrop := db.ExecContext(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`); errDrop != nil {
			t.Error("drop isolated test schema failed")
		}
		if errClose := db.Close(); errClose != nil {
			t.Error("close test database failed")
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func TestPostgresSchemaAndConstraints(t *testing.T) {
	dsn := integrationDSN(t)
	ctx := context.Background()
	s, errOpen := Open(ctx, Config{DSN: dsn})
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	t.Cleanup(func() {
		if errClose := s.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	if errHealth := s.Health(ctx); errHealth != nil {
		t.Fatal(errHealth)
	}
	if errSchema := s.EnsureSchema(ctx); errSchema != nil {
		t.Fatal(errSchema)
	}
	var tableCount int
	if errQuery := s.db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name LIKE 'cpa_%'`).Scan(&tableCount); errQuery != nil || tableCount < 13 {
		t.Fatalf("expected original and financial domain tables, got %d (error %v)", tableCount, errQuery)
	}
	for _, statement := range []string{
		`INSERT INTO cpa_users (id, email) VALUES ('user1', 'alice@example.com')`,
		`INSERT INTO cpa_user_api_keys (id, user_id, key_hash, key_prefix) VALUES ('key1', 'user1', 'hash', 'sk-cpa-test')`,
		`INSERT INTO cpa_user_permissions (user_id, scope, value) VALUES ('user1', 'provider', 'azure')`,
		`INSERT INTO cpa_usage_monthly (user_id, period, total_tokens) VALUES ('user1', '2026-09', 10)`,
		`INSERT INTO cpa_sessions (id, user_id, expires_at) VALUES ('sessionhash', 'user1', now() + interval '12 hours')`,
		`INSERT INTO cpa_audit_events (actor, action, target) VALUES ('user1', 'user.create', 'user1')`,
	} {
		if _, errInsert := s.db.ExecContext(ctx, statement); errInsert != nil {
			t.Fatal(errInsert)
		}
	}
	for _, statement := range []string{
		`INSERT INTO cpa_users (id, email) VALUES ('user2', 'alice@example.com')`,
		`INSERT INTO cpa_user_api_keys (id, user_id, key_hash, key_prefix) VALUES ('key2', 'user1', 'hash', 'prefix')`,
		`INSERT INTO cpa_user_permissions (user_id, scope, value) VALUES ('missing-user', 'model', 'test')`,
		`INSERT INTO cpa_usage_monthly (user_id, period, total_tokens) VALUES ('user1', '2026-10', -1)`,
	} {
		if _, errInsert := s.db.ExecContext(ctx, statement); errInsert == nil {
			t.Fatal("schema accepted a duplicate key, orphan, or invalid counter")
		}
	}
	if _, errDelete := s.db.ExecContext(ctx, `DELETE FROM cpa_users WHERE id = 'user1'`); errDelete != nil {
		t.Fatal(errDelete)
	}
	for _, table := range []string{"cpa_user_api_keys", "cpa_user_permissions", "cpa_usage_monthly", "cpa_sessions"} {
		var count int
		if errCount := s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); errCount != nil || count != 0 {
			t.Fatalf("cascade left rows in %s", table)
		}
	}
	var auditCount int
	if errCount := s.db.QueryRowContext(ctx, `SELECT count(*) FROM cpa_audit_events`).Scan(&auditCount); errCount != nil || auditCount != 1 {
		t.Fatal("user deletion removed audit history")
	}
	if errClose := s.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	if s.Health(ctx) == nil {
		t.Fatal("closed database should fail health check")
	}
}

func TestPostgresConcurrentBootstrap(t *testing.T) {
	dsn := integrationDSN(t)
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			s, errOpen := Open(context.Background(), Config{DSN: dsn})
			if errOpen != nil {
				t.Error(errOpen)
				return
			}
			if errClose := s.Close(); errClose != nil {
				t.Error(errClose)
			}
		})
	}
	workers.Wait()
}

func TestPostgresSchemaFailureRollsBack(t *testing.T) {
	dsn := integrationDSN(t)
	db, errOpen := sql.Open("pgx", dsn)
	if errOpen != nil {
		t.Fatal("open isolated test database failed")
	}
	t.Cleanup(func() {
		if errClose := db.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	// An incompatible preexisting table makes the expiry index fail late in the
	// bootstrap. Earlier CREATE statements must roll back together.
	if _, errCreate := db.Exec(`CREATE TABLE cpa_sessions (id INTEGER)`); errCreate != nil {
		t.Fatal(errCreate)
	}
	if opened, errBootstrap := Open(context.Background(), Config{DSN: dsn}); errBootstrap == nil {
		_ = opened.Close()
		t.Fatal("incompatible schema should reject startup")
	}
	var usersTableExists bool
	if errQuery := db.QueryRow(`SELECT to_regclass('cpa_users') IS NOT NULL`).Scan(&usersTableExists); errQuery != nil {
		t.Fatal(errQuery)
	}
	if usersTableExists {
		t.Fatal("schema failure left partially created domain tables")
	}
}
