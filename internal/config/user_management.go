package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	DefaultUserManagementSessionTTL = "12h"
	DefaultUserManagementCacheTTL   = "30s"
)

// UserManagementConfig controls user accounts independently of the config/auth store.
// DSN retains environment references when configuration is saved and is omitted
// from management API JSON responses. Only ResolvedDSN expands references.
type UserManagementConfig struct {
	Enabled         bool                                `yaml:"enabled" json:"enabled"`
	DSN             string                              `yaml:"dsn" json:"-"`
	Quota           UserManagementQuotaConfig           `yaml:"quota" json:"quota"`
	Session         UserManagementSessionConfig         `yaml:"session" json:"session"`
	Cache           UserManagementCacheConfig           `yaml:"cache" json:"cache"`
	RequestActivity UserManagementRequestActivityConfig `yaml:"request-activity" json:"request-activity"`
}

// Request previews are bounded and redacted before entering the persistence queue.
type UserManagementRequestActivityConfig struct {
	Enabled       *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	RetentionDays int   `yaml:"retention-days" json:"retention-days"`
}

func (cfg UserManagementRequestActivityConfig) CaptureEnabled() bool {
	return cfg.Enabled == nil || *cfg.Enabled
}

type UserManagementQuotaConfig struct {
	// DefaultMonthlyTokens <= 0 means unlimited unless a user has an explicit limit.
	DefaultMonthlyTokens int64 `yaml:"default-monthly-tokens" json:"default-monthly-tokens"`
	// Enforce defaults to false so initial deployment can verify accounting first.
	Enforce bool `yaml:"enforce" json:"enforce"`
}

type UserManagementSessionConfig struct {
	TTL string `yaml:"ttl" json:"ttl"`
}

type UserManagementCacheConfig struct {
	TTL string `yaml:"ttl" json:"ttl"`
}

// WithDefaults returns a normalized copy without expanding or exposing the DSN.
func (cfg UserManagementConfig) WithDefaults() UserManagementConfig {
	cfg.DSN = strings.TrimSpace(cfg.DSN)
	cfg.Session.TTL = strings.TrimSpace(cfg.Session.TTL)
	cfg.Cache.TTL = strings.TrimSpace(cfg.Cache.TTL)
	if cfg.Session.TTL == "" {
		cfg.Session.TTL = DefaultUserManagementSessionTTL
	}
	if cfg.Cache.TTL == "" {
		cfg.Cache.TTL = DefaultUserManagementCacheTTL
	}
	if cfg.RequestActivity.RetentionDays == 0 {
		cfg.RequestActivity.RetentionDays = 7
	}
	return cfg
}

// Validate ignores disabled feature settings, including unset DSN variables, so
// the rollback switch never depends on the database or feature configuration.
func (cfg UserManagementConfig) Validate() error {
	if !cfg.Enabled {
		return nil
	}
	cfg = cfg.WithDefaults()
	if cfg.RequestActivity.RetentionDays < 1 || cfg.RequestActivity.RetentionDays > 365 {
		return fmt.Errorf("user-management.request-activity.retention-days must be 1..365")
	}
	if _, errResolve := cfg.ResolvedDSN(); errResolve != nil {
		return errResolve
	}
	for _, setting := range []struct{ name, value string }{
		{"session.ttl", cfg.Session.TTL},
		{"cache.ttl", cfg.Cache.TTL},
	} {
		duration, errParse := time.ParseDuration(setting.value)
		if errParse != nil || duration <= 0 {
			return fmt.Errorf("user-management.%s must be a positive duration", setting.name)
		}
	}
	return nil
}

var userManagementEnvReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ResolvedDSN expands ${ENV_VAR} references once, without interpreting literal $
// characters in passwords or recursively expanding the environment value.
func (cfg UserManagementConfig) ResolvedDSN() (string, error) {
	missing := false
	dsn := userManagementEnvReference.ReplaceAllStringFunc(cfg.DSN, func(reference string) string {
		value, present := os.LookupEnv(reference[2 : len(reference)-1])
		if !present || value == "" {
			missing = true
		}
		return value
	})
	if missing {
		return "", fmt.Errorf("user-management.dsn references an unset or empty environment variable")
	}
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return "", fmt.Errorf("user-management.dsn is required when enabled")
	}
	return dsn, nil
}
