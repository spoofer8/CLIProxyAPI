// Package usermgmt owns optional user management, separate from upstream auth.
package usermgmt

import (
	"context"
	"errors"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	log "github.com/sirupsen/logrus"
)

// Runtime owns the current domain store and configuration. Its zero value is
// disabled and creates no connections or workers. Future request handlers should
// obtain the current store from Snapshot instead of retaining a replaced store.
type Runtime struct {
	applyMu sync.Mutex
	mu      sync.RWMutex
	store   *store.Store
	cfg     config.UserManagementConfig
	dsn     string
	closed  bool
}

// Apply opens a replacement completely before publishing it. A failed enable or
// DSN change leaves the last working runtime unchanged. Disabling never needs a
// database connection and closes the old pool. Configuration reloads are serial.
func (r *Runtime) Apply(ctx context.Context, cfg config.UserManagementConfig) error {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	if r.closed {
		return errors.New("user management: runtime is closed")
	}
	cfg = cfg.WithDefaults()
	if errValidate := cfg.Validate(); errValidate != nil {
		return errValidate
	}
	r.mu.RLock()
	oldStore, oldDSN := r.store, r.dsn
	r.mu.RUnlock()
	if !cfg.Enabled {
		if oldStore == nil {
			return nil
		}
		r.mu.Lock()
		r.store, r.cfg, r.dsn = nil, cfg, ""
		r.mu.Unlock()
		if errClose := oldStore.Close(); errClose != nil {
			log.WithError(errClose).Warn("user management database close failed after disabling")
		}
		log.Info("user management disabled")
		return nil
	}
	dsn, errResolve := cfg.ResolvedDSN()
	if errResolve != nil {
		return errResolve
	}
	if oldStore != nil && oldDSN == dsn {
		r.mu.Lock()
		r.cfg = cfg
		r.mu.Unlock()
		return nil
	}
	replacement, errOpen := store.Open(ctx, store.Config{DSN: dsn})
	if errOpen != nil {
		return errOpen
	}
	r.mu.Lock()
	r.store, r.cfg, r.dsn = replacement, cfg, dsn
	r.mu.Unlock()
	if oldStore != nil {
		if errClose := oldStore.Close(); errClose != nil {
			log.WithError(errClose).Warn("previous user management database close failed")
		}
	}
	log.Info("user management PostgreSQL connection established and schema ready")
	return nil
}

// Snapshot returns the currently active store and settings. A nil store means
// the feature is disabled. Store owns its connection; callers must not close it.
func (r *Runtime) Snapshot() (*store.Store, config.UserManagementConfig) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.store, r.cfg
}

// Close is idempotent and prevents a concurrent configuration reload from
// reopening the pool after shutdown. Call after request and accounting workers
// have finished; accounting drain is added with the accounting component.
func (r *Runtime) Close() error {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	r.closed = true
	r.mu.Lock()
	oldStore := r.store
	r.store, r.cfg, r.dsn = nil, config.UserManagementConfig{}, ""
	r.mu.Unlock()
	return oldStore.Close()
}
