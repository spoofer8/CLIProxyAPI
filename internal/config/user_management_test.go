package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserManagementConfigDefaultsAndValidation(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		input   string
		wantErr string
	}{
		{name: "absent", input: "port: 8317"},
		{name: "disabled ignores feature settings", input: "user-management:\n  enabled: false\n  dsn: '${UNSET_USERMGMT_TEST_DSN}'\n  session: {ttl: invalid}"},
		{name: "enabled requires DSN", input: "user-management: {enabled: true}", wantErr: "dsn is required"},
		{name: "session duration", input: "user-management: {enabled: true, dsn: postgres://example/db, session: {ttl: wrong}}", wantErr: "session.ttl"},
		{name: "zero cache duration", input: "user-management: {enabled: true, dsn: postgres://example/db, cache: {ttl: 0s}}", wantErr: "cache.ttl"},
		{name: "negative session duration", input: "user-management: {enabled: true, dsn: postgres://example/db, session: {ttl: -1h}}", wantErr: "session.ttl"},
		{name: "enabled defaults", input: "user-management: {enabled: true, dsn: postgres://example/db}"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if errWrite := os.WriteFile(path, []byte(testCase.input), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			for name, loader := range map[string]func() (*Config, error){
				"file":    func() (*Config, error) { return LoadConfig(path) },
				"payload": func() (*Config, error) { return ParseConfigBytes([]byte(testCase.input)) },
			} {
				cfg, errLoad := loader()
				if testCase.wantErr != "" {
					if errLoad == nil || !strings.Contains(errLoad.Error(), testCase.wantErr) {
						t.Fatalf("%s: expected %q error, got %v", name, testCase.wantErr, errLoad)
					}
					continue
				}
				if errLoad != nil {
					t.Fatalf("%s: %v", name, errLoad)
				}
				if testCase.name == "absent" || testCase.name == "enabled defaults" {
					if cfg.UserManagement.Session.TTL != "12h" || cfg.UserManagement.Cache.TTL != "30s" {
						t.Fatalf("%s: wrong duration defaults", name)
					}
					if cfg.UserManagement.Quota.Enforce || cfg.UserManagement.Quota.DefaultMonthlyTokens != 0 {
						t.Fatalf("%s: expected accounting-only/unlimited defaults", name)
					}
				}
			}
		})
	}
}

func TestUserManagementDSNExpansionAndPersistence(t *testing.T) {
	const reference = "${CLIPROXY_TEST_ACCOUNT_DSN}"
	const secretDSN = "postgres://user:literal$pass@localhost/test?sslmode=disable"
	t.Setenv("CLIPROXY_TEST_ACCOUNT_DSN", secretDSN)
	input := "port: 8317\nuser-management:\n  enabled: true\n  dsn: '" + reference + "'\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte(input), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	dsn, errResolve := cfg.UserManagement.ResolvedDSN()
	if errResolve != nil || dsn != secretDSN {
		t.Fatalf("DSN did not resolve once with literal password preserved (error %v)", errResolve)
	}
	if errSave := SaveConfigPreserveComments(path, cfg); errSave != nil {
		t.Fatal(errSave)
	}
	persisted, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if strings.Contains(string(persisted), "literal$pass") || !strings.Contains(string(persisted), reference) {
		t.Fatal("configuration save expanded the environment reference")
	}
	cfg.UserManagement.DSN = secretDSN
	encoded, errMarshal := json.Marshal(cfg)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if strings.Contains(string(encoded), "literal$pass") || strings.Contains(string(encoded), `"dsn"`) {
		t.Fatal("DSN leaked into management JSON")
	}
}

func TestUserManagementDSNMissingEnvironmentDoesNotLeak(t *testing.T) {
	t.Setenv("CLIPROXY_TEST_MISSING_ACCOUNT_DSN", "")
	cfg := UserManagementConfig{Enabled: true, DSN: "postgres://user:secret@localhost/${CLIPROXY_TEST_MISSING_ACCOUNT_DSN}"}
	if errValidate := cfg.Validate(); errValidate == nil || strings.Contains(errValidate.Error(), "secret") {
		t.Fatal("expected safe missing environment variable validation error")
	}
}

func TestUserManagementDefaultsDoNotPolluteExistingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte("port: 8317\n"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if errSave := SaveConfigPreserveComments(path, cfg); errSave != nil {
		t.Fatal(errSave)
	}
	persisted, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if strings.Contains(string(persisted), "user-management") {
		t.Fatal("disabled defaults added user management configuration")
	}
}
