// Package usermgmt owns optional user management, separate from upstream auth.
package usermgmt

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

// Runtime owns the current domain store and configuration. Its zero value is
// disabled and creates no connections or workers. Future request handlers should
// obtain the current store from Snapshot instead of retaining a replaced store.
type Runtime struct {
	applyMu        sync.Mutex
	mu             sync.RWMutex
	store          *store.Store
	auth           *authState
	cfg            config.UserManagementConfig
	dsn            string
	closed         bool
	current        *usageScope
	scopes         map[string]*usageScope
	usageFlusher   func(context.Context) error
	lifetimeOnce   sync.Once
	lifetimeCtx    context.Context
	cancelLifetime context.CancelFunc
	shutdownOnce   sync.Once
	shutdownDone   chan struct{}
	abortShutdown  chan struct{}
	abortOnce      sync.Once
}

const UsageScopeMetadataKey = "usermgmt_scope"

type usageScope struct {
	id            string
	store         *store.Store
	auth          *authState
	accountant    *accountant
	cfg           config.UserManagementConfig
	leases        int
	retired       bool
	idle          chan struct{}
	done          chan struct{}
	cleanupCtx    context.Context
	cancelCleanup context.CancelFunc
}

// Apply opens a replacement completely before publishing it. A failed enable or
// DSN change leaves the last working runtime unchanged. Disabling never needs a
// database connection; the old pool retires after admitted producers and usage
// queues finish. Configuration reloads are serial.
func (r *Runtime) Apply(ctx context.Context, cfg config.UserManagementConfig) error {
	if r == nil {
		return ErrDisabled
	}
	r.ensureLifetime()
	if r.lifetimeCtx.Err() != nil {
		return errors.New("user management: runtime is closed")
	}
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	if r.closed || r.lifetimeCtx.Err() != nil {
		return errors.New("user management: runtime is closed")
	}
	cfg = cfg.WithDefaults()
	if errValidate := cfg.Validate(); errValidate != nil {
		return errValidate
	}
	r.mu.RLock()
	oldStore, oldDSN, oldAuth, oldScope := r.store, r.dsn, r.auth, r.current
	r.mu.RUnlock()
	if !cfg.Enabled {
		if oldStore == nil {
			return nil
		}
		r.mu.Lock()
		r.store, r.cfg, r.dsn, r.auth, r.current = nil, cfg, "", nil, nil
		r.mu.Unlock()
		r.retire(oldScope)
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
		oldScope.cfg = cfg
		ttl, _ := time.ParseDuration(cfg.Cache.TTL) // validated above
		oldAuth.updateTTL(ttl)
		r.mu.Unlock()
		return nil
	}
	openCtx, cancelOpen := r.operationContext(ctx)
	defer cancelOpen()
	replacement, errOpen := store.Open(openCtx, store.Config{DSN: dsn})
	if errOpen != nil {
		return errOpen
	}
	if r.lifetimeCtx.Err() != nil {
		_ = replacement.Close()
		return errors.New("user management: runtime is closed")
	}
	scopeID, errScopeID := newID()
	if errScopeID != nil {
		_ = replacement.Close()
		return errScopeID
	}
	ttl, _ := time.ParseDuration(cfg.Cache.TTL) // validated above
	replacementAuth := newAuthState(replacement, ttl)
	replacementAuth.scopeID = scopeID
	cleanupCtx, cancelCleanup := context.WithCancel(context.Background())
	scope := &usageScope{
		id: scopeID, store: replacement, auth: replacementAuth, accountant: newAccountant(replacement), cfg: cfg,
		idle: make(chan struct{}), done: make(chan struct{}), cleanupCtx: cleanupCtx, cancelCleanup: cancelCleanup,
	}
	r.mu.Lock()
	if r.scopes == nil {
		r.scopes = make(map[string]*usageScope)
	}
	r.scopes[scopeID] = scope
	r.store, r.cfg, r.dsn, r.auth, r.current = replacement, cfg, dsn, replacementAuth, scope
	r.mu.Unlock()
	r.retire(oldScope)
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

// Close is idempotent and drains with a default five-second cleanup budget.
// Services with their own shutdown budget should call CloseContext instead.
func (r *Runtime) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.CloseContext(ctx)
}

// CloseContext waits for admitted usage producers and both dispatcher/domain
// queues before closing stores. Exhausting the cleanup budget cancels persistence
// and rejects late records; it cannot hold the service shutdown indefinitely.
func (r *Runtime) CloseContext(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.ensureLifetime()
	r.cancelLifetime()
	r.shutdownOnce.Do(func() { go r.shutdown() })
	select {
	case <-r.shutdownDone:
		return nil
	case <-ctx.Done():
		r.abortOnce.Do(func() { close(r.abortShutdown) })
		return ctx.Err()
	}
}

func (r *Runtime) ensureLifetime() {
	r.lifetimeOnce.Do(func() {
		r.lifetimeCtx, r.cancelLifetime = context.WithCancel(context.Background())
		r.shutdownDone = make(chan struct{})
		r.abortShutdown = make(chan struct{})
	})
}

func (r *Runtime) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	r.ensureLifetime()
	if ctx == nil {
		ctx = context.Background()
	}
	operationCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(r.lifetimeCtx, cancel)
	return operationCtx, func() { stop(); cancel() }
}

func (r *Runtime) shutdown() {
	defer close(r.shutdownDone)
	r.applyMu.Lock()
	r.closed = true
	r.mu.Lock()
	active := r.current
	r.store, r.cfg, r.dsn, r.auth, r.current = nil, config.UserManagementConfig{}, "", nil, nil
	states := make([]*usageScope, 0, len(r.scopes))
	for _, scope := range r.scopes {
		states = append(states, scope)
	}
	r.mu.Unlock()
	r.applyMu.Unlock()
	r.retireAsync(active)
	for _, scope := range states {
		select {
		case <-scope.done:
		case <-r.abortShutdown:
			for _, pending := range states {
				pending.cancelCleanup()
			}
			<-scope.done
		}
	}
}

var ErrDisabled = errors.New("user management is disabled")

// withStore leases the original store without holding the global mutex during
// database work. Cache invalidation remains attached to that store lifetime.
func (r *Runtime) withStore(mutation bool, operation func(*store.Store) error) error {
	if r == nil {
		return ErrDisabled
	}
	r.mu.Lock()
	scope := r.current
	if scope == nil {
		r.mu.Unlock()
		return ErrDisabled
	}
	scope.leases++
	r.mu.Unlock()
	defer r.releaseScope(scope)
	errOperation := operation(scope.store)
	if mutation && errOperation == nil {
		scope.auth.invalidate()
		scope.accountant.quota.invalidate()
	}
	return errOperation
}

// UsagePluginName is stable and unique among live embedded service runtimes.
func (r *Runtime) UsagePluginName() string { return fmt.Sprintf("user-management:%p", r) }

func (r *Runtime) SetUsageFlusher(flush func(context.Context) error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.usageFlusher = flush
	r.mu.Unlock()
}

// BeginRequest leases the authenticated store lifetime. The SDK releases leases
// when actual usage producers finish, including raw streaming producers after
// client cancellation. Already-admitted work can acquire nested producer leases
// on a retired scope while its outer execution lease is still held.
func (r *Runtime) BeginRequest(ctx context.Context) (func(), error) {
	identity, exists := sdkaccess.ResultFromContext(ctx)
	if !exists || identity.Provider != AccessProviderName {
		return func() {}, nil
	}
	if r == nil {
		return nil, unavailableScopeError()
	}
	r.mu.Lock()
	scope := r.scopes[identity.Metadata[UsageScopeMetadataKey]]
	if scope == nil || (scope.retired && scope.leases == 0) {
		r.mu.Unlock()
		return nil, unavailableScopeError()
	}
	scope.leases++
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.releaseScope(scope)
		})
	}, nil
}

func (r *Runtime) releaseScope(scope *usageScope) {
	r.mu.Lock()
	scope.leases--
	if scope.retired && scope.leases == 0 {
		close(scope.idle)
	}
	r.mu.Unlock()
}

// HandleUsage implements usage.Plugin and only accepts this runtime's immutable
// user identity/scope. Record.APIKey and recycled Gin contexts are never used to
// select a user or database. Foreign/legacy records are simply ignored.
func (r *Runtime) HandleUsage(ctx context.Context, record usage.Record) {
	if r == nil {
		return
	}
	identity, exists := sdkaccess.ResultFromContext(ctx)
	if !exists || identity.Provider != AccessProviderName || identity.Principal == "" {
		return
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	scope := r.scopes[identity.Metadata[UsageScopeMetadataKey]]
	if scope == nil {
		return
	}
	delta, errDelta := usageIncrement(identity.Principal, record, time.Now())
	if errDelta != nil {
		scope.accountant.drop("invalid token counters")
		return
	}
	scope.accountant.enqueue(delta)
}

func (r *Runtime) retire(scope *usageScope) {
	if scope == nil {
		return
	}
	idle := r.retireAsync(scope)
	if idle {
		<-scope.done
	}
}

func (r *Runtime) retireAsync(scope *usageScope) bool {
	if scope == nil {
		return true
	}
	r.mu.Lock()
	idle := scope.leases == 0
	if scope.retired {
		r.mu.Unlock()
		return idle
	}
	scope.retired = true
	if idle {
		close(scope.idle)
	}
	r.mu.Unlock()
	go r.finishRetirement(scope)
	return idle
}

func (r *Runtime) finishRetirement(scope *usageScope) {
	defer close(scope.done)
	select {
	case <-scope.idle:
	case <-scope.cleanupCtx.Done():
	}
	cleanupCtx, cancel := context.WithTimeout(scope.cleanupCtx, 5*time.Second)
	defer cancel()
	defer scope.cancelCleanup()
	r.mu.RLock()
	flush := r.usageFlusher
	r.mu.RUnlock()
	if flush != nil {
		if errFlush := flush(cleanupCtx); errFlush != nil {
			log.WithError(errFlush).Warn("user management usage dispatcher drain did not complete")
		}
	}
	if errClose := scope.accountant.close(cleanupCtx); errClose != nil {
		log.WithError(errClose).Warn("user management accounting drain did not complete")
	}
	scope.auth.close()
	if errClose := scope.store.Close(); errClose != nil {
		log.WithError(errClose).Warn("user management retired database close failed")
	}
	r.mu.Lock()
	delete(r.scopes, scope.id)
	r.mu.Unlock()
}
