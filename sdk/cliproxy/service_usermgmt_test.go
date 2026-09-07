package cliproxy

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type userManagementTokenProviderFunc func(context.Context, *config.Config) (*TokenClientResult, error)

func (f userManagementTokenProviderFunc) Load(ctx context.Context, cfg *config.Config) (*TokenClientResult, error) {
	return f(ctx, cfg)
}

func TestServiceUserManagementDisabledRunReachesProvider(t *testing.T) {
	providerFailure := errors.New("stop after user management initialization")
	service := &Service{cfg: &config.Config{AuthDir: t.TempDir()}}
	service.cfg.UserManagement.DSN = "invalid and unreachable"
	service.cfg.UserManagement.Session.TTL = "invalid"
	called := false
	service.tokenProvider = userManagementTokenProviderFunc(func(context.Context, *config.Config) (*TokenClientResult, error) {
		called = true
		if db, active := service.userManagement.Snapshot(); db != nil || active.Enabled {
			t.Error("disabled startup activated user management")
		}
		return nil, providerFailure
	})

	if errRun := service.Run(context.Background()); !errors.Is(errRun, providerFailure) {
		t.Fatalf("disabled startup did not reach normal provider initialization: %v", errRun)
	}
	if !called {
		t.Fatal("normal provider initialization was skipped")
	}
}

func TestServiceUserManagementRunRejectsFailedInitialization(t *testing.T) {
	service := &Service{cfg: &config.Config{AuthDir: t.TempDir()}}
	service.cfg.UserManagement = config.UserManagementConfig{
		Enabled: true,
		DSN:     "postgres://test:DO-NOT-LOG-THIS@localhost:%/test",
	}
	called := false
	service.tokenProvider = userManagementTokenProviderFunc(func(context.Context, *config.Config) (*TokenClientResult, error) {
		called = true
		return nil, errors.New("unexpected provider initialization")
	})

	errRun := service.Run(context.Background())
	if errRun == nil || !strings.Contains(errRun.Error(), "initialize user management") {
		t.Fatalf("startup did not report its user management initialization failure: %v", errRun)
	}
	if strings.Contains(errRun.Error(), "DO-NOT-LOG-THIS") {
		t.Fatal("startup error exposed the database password")
	}
	if called {
		t.Fatal("normal providers started after the required database failed")
	}
	if db, _ := service.userManagement.Snapshot(); db != nil {
		t.Fatal("failed startup left an active database")
	}
}

func TestServiceUserManagementRunClosesStoreOnStartupFailure(t *testing.T) {
	dsn := serviceUserManagementTestDSN(t)
	providerFailure := errors.New("later startup component failed")
	service := &Service{cfg: &config.Config{AuthDir: t.TempDir()}}
	service.cfg.UserManagement = config.UserManagementConfig{Enabled: true, DSN: dsn}
	var opened *store.Store
	service.tokenProvider = userManagementTokenProviderFunc(func(ctx context.Context, _ *config.Config) (*TokenClientResult, error) {
		var active config.UserManagementConfig
		opened, active = service.userManagement.Snapshot()
		if opened == nil || !active.Enabled {
			t.Error("database was not ready before provider initialization")
		} else if errHealth := opened.Health(ctx); errHealth != nil {
			t.Errorf("database was unhealthy during startup: %v", errHealth)
		}
		if providers := service.accessManager.Providers(); len(providers) != 1 || providers[0].Identifier() != "user" {
			t.Error("startup did not install the manager-local user provider")
		}
		return nil, providerFailure
	})

	if errRun := service.Run(context.Background()); !errors.Is(errRun, providerFailure) {
		t.Fatalf("unexpected startup result: %v", errRun)
	}
	if opened == nil {
		t.Fatal("startup did not initialize the database")
	}
	if current, _ := service.userManagement.Snapshot(); current != nil {
		t.Fatal("deferred service shutdown left user management active")
	}
	if errHealth := opened.Health(context.Background()); errHealth == nil {
		t.Fatal("deferred service shutdown left its connection pool open")
	}
	if len(service.accessManager.Providers()) != 0 {
		t.Fatal("shutdown left the manager-local user provider installed")
	}
}

func TestServiceUserManagementReloadEnableFailureDisable(t *testing.T) {
	dsn := serviceUserManagementTestDSN(t)
	ctx := context.Background()
	manager := sdkaccess.NewManager()
	service := &Service{cfg: &config.Config{}, accessManager: manager}
	t.Cleanup(func() {
		if errClose := service.userManagement.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	enabled := &config.Config{UserManagement: config.UserManagementConfig{Enabled: true, DSN: dsn}}
	if !service.applyConfigUpdateWithAuthSynthesis(ctx, enabled, false) {
		t.Fatal("enabling configuration was not applied")
	}
	initial, active := service.userManagement.Snapshot()
	if initial == nil || !active.Enabled {
		t.Fatal("configuration reload did not enable user management")
	}
	if providers := manager.Providers(); len(providers) != 1 || providers[0].Identifier() != "user" {
		t.Fatal("configuration reload did not install the user provider")
	}
	if errHealth := initial.Health(ctx); errHealth != nil {
		t.Fatalf("enabled database is unhealthy: %v", errHealth)
	}

	invalid := *enabled
	invalid.DisableCooling = true
	invalid.UserManagement.DSN = "postgres://test:secret@localhost:%/test"
	if !service.applyConfigUpdateWithAuthSynthesis(ctx, &invalid, false) {
		t.Fatal("failed database replacement prevented unrelated configuration from applying")
	}
	current, active := service.userManagement.Snapshot()
	if current != initial || active.DSN != dsn || current.Health(ctx) != nil {
		t.Fatal("failed reload replaced the working user management runtime")
	}
	if service.cfg != &invalid || !service.cfg.DisableCooling {
		t.Fatal("unrelated configuration did not apply after database replacement failed")
	}

	disabled := invalid
	disabled.UserManagement.Enabled = false
	if !service.applyConfigUpdateWithAuthSynthesis(ctx, &disabled, false) {
		t.Fatal("disable reload failed with an invalid database DSN")
	}
	if current, active = service.userManagement.Snapshot(); current != nil || active.Enabled {
		t.Fatal("disable reload left user management active")
	}
	if service.accessManager != manager || len(manager.Providers()) != 0 {
		t.Fatal("disable reload replaced the service manager or retained a user provider")
	}
	// Disabled scopes now drain producers and accounting asynchronously.
	if errClose := service.userManagement.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	if errHealth := initial.Health(ctx); errHealth == nil {
		t.Fatal("disable reload left the previous connection pool open")
	}
}

func serviceUserManagementTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("CLIPROXY_USERMGMT_TEST_DSN")
	if dsn == "" {
		t.Skip("set CLIPROXY_USERMGMT_TEST_DSN to run PostgreSQL integration tests")
	}
	// These lifecycle tests only bootstrap the idempotent schema in the supplied
	// test database; they do not modify or remove application rows.
	return dsn
}
