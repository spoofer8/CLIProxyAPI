// Package usermgmt owns optional user management, separate from upstream auth.
package usermgmt

import (
	"context"
	"errors"
	"sync"
	"time"

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
	auth    *authState
	cfg     config.UserManagementConfig
	dsn     string
	closed  bool
}

// Apply opens a replacement completely before publishing it. A failed enable or
// DSN change leaves the last working runtime unchanged. Disabling never needs a
// database connection and closes the old pool. Configuration reloads are serial.
func (r *Runtime) Apply(ctx context.Context, cfg config.UserManagementConfig) error {
	if r == nil {
		return ErrDisabled
	}
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
	oldStore, oldDSN, oldAuth := r.store, r.dsn, r.auth
	r.mu.RUnlock()
	if !cfg.Enabled {
		if oldStore == nil {
			return nil
		}
		r.mu.Lock()
		r.store, r.cfg, r.dsn, r.auth = nil, cfg, "", nil
		r.mu.Unlock()
		oldAuth.close()
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
		ttl, _ := time.ParseDuration(cfg.Cache.TTL) // validated above
		oldAuth.updateTTL(ttl)
		r.mu.Unlock()
		return nil
	}
	replacement, errOpen := store.Open(ctx, store.Config{DSN: dsn})
	if errOpen != nil {
		return errOpen
	}
	ttl, _ := time.ParseDuration(cfg.Cache.TTL) // validated above
	replacementAuth := newAuthState(replacement, ttl)
	r.mu.Lock()
	r.store, r.cfg, r.dsn, r.auth = replacement, cfg, dsn, replacementAuth
	r.mu.Unlock()
	oldAuth.close()
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
	if r == nil {
		return nil, config.UserManagementConfig{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.store, r.cfg
}

// Close is idempotent and prevents a concurrent configuration reload from
// reopening the pool after shutdown. Call after request and accounting workers
// have finished; accounting drain is added with the accounting component.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	r.closed = true
	r.mu.Lock()
	oldStore, oldAuth := r.store, r.auth
	r.store, r.cfg, r.dsn, r.auth = nil, config.UserManagementConfig{}, "", nil
	r.mu.Unlock()
	oldAuth.close()
	return oldStore.Close()
}

var ErrDisabled = errors.New("user management is disabled")

// withStore keeps a mutation in its original store lifetime until its cache is
// invalidated. Reload/disable cannot publish a replacement midway through it.
func (r *Runtime) withStore(mutation bool, operation func(*store.Store) error) error {
	if r == nil {
		return ErrDisabled
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.store == nil {
		return ErrDisabled
	}
	errOperation := operation(r.store)
	if mutation && errOperation == nil {
		r.auth.invalidate()
	}
	return errOperation
}
