package usermgmt

import (
	"context"
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
)

func TestRuntimeDisabledDoesNotConnect(t *testing.T) {
	runtime := Runtime{pricingRefresh: func(context.Context, *store.Store) error { return nil }}
	cfg := config.UserManagementConfig{DSN: "invalid and unreachable", Session: config.UserManagementSessionConfig{TTL: "invalid"}}
	if errApply := runtime.Apply(context.Background(), cfg); errApply != nil {
		t.Fatal(errApply)
	}
	if db, _ := runtime.Snapshot(); db != nil {
		t.Fatal("disabled configuration opened a store")
	}
	if errClose := runtime.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	if errClose := runtime.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	cfg.Enabled = true
	if errApply := runtime.Apply(context.Background(), cfg); errApply == nil {
		t.Fatal("closed runtime accepted a new connection")
	}
}

func TestRuntimeFailedEnableLeavesDisabled(t *testing.T) {
	runtime := Runtime{pricingRefresh: func(context.Context, *store.Store) error { return nil }}
	cfg := config.UserManagementConfig{Enabled: true, DSN: "postgres://test:secret@localhost:%/test"}
	if errApply := runtime.Apply(context.Background(), cfg); errApply == nil {
		t.Fatal("invalid DSN should fail to enable")
	}
	if db, active := runtime.Snapshot(); db != nil || active.Enabled {
		t.Fatal("failed enable published partial runtime state")
	}
}

func TestPostgresRuntimeReloadAndClose(t *testing.T) {
	// This test only bootstraps the test database's idempotent schema and changes
	// no rows. Destructive schema/constraint checks use isolated schemas in store.
	dsn := os.Getenv("CLIPROXY_USERMGMT_TEST_DSN")
	if dsn == "" {
		t.Skip("set CLIPROXY_USERMGMT_TEST_DSN to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	runtime := Runtime{pricingRefresh: func(context.Context, *store.Store) error { return nil }}
	t.Cleanup(func() {
		if errClose := runtime.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	cfg := config.UserManagementConfig{Enabled: true, DSN: dsn, RequestActivity: config.UserManagementRequestActivityConfig{SpoolDirectory: t.TempDir()}}
	if errApply := runtime.Apply(ctx, cfg); errApply != nil {
		t.Fatal(errApply)
	}
	initial, _ := runtime.Snapshot()
	if initial == nil {
		t.Fatal("enabled runtime did not open a store")
	}
	cfg.Quota.DefaultMonthlyTokens = 123
	if errApply := runtime.Apply(ctx, cfg); errApply != nil {
		t.Fatal(errApply)
	}
	current, active := runtime.Snapshot()
	if current != initial || active.Quota.DefaultMonthlyTokens != 123 {
		t.Fatal("quota reload should reuse the connection and apply settings")
	}
	cfg.DSN = "postgres://test:secret@localhost:%/test"
	if errApply := runtime.Apply(ctx, cfg); errApply == nil {
		t.Fatal("invalid replacement should fail")
	}
	current, active = runtime.Snapshot()
	if current != initial || active.DSN != dsn || current.Health(ctx) != nil {
		t.Fatal("failed replacement changed the working runtime")
	}
	cfg.Enabled = false
	if errApply := runtime.Apply(ctx, cfg); errApply != nil {
		t.Fatal(errApply)
	}
	if current, _ = runtime.Snapshot(); current != nil || initial.Health(ctx) == nil {
		t.Fatal("disable did not close and remove the store")
	}
	cfg.Enabled, cfg.DSN = true, dsn
	if errApply := runtime.Apply(ctx, cfg); errApply != nil {
		t.Fatal(errApply)
	}
	current, _ = runtime.Snapshot()
	if current == nil || current == initial {
		t.Fatal("reenable did not open a replacement store")
	}
	if errClose := runtime.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	if current.Health(ctx) == nil {
		t.Fatal("shutdown left the connection open")
	}
}
