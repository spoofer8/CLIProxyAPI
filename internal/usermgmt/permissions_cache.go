package usermgmt

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
)

type cachedPermissions struct {
	policy  *compiledPermissions
	expires time.Time
}

type permissionCache struct {
	mu         sync.Mutex
	entries    map[string]cachedPermissions
	generation uint64
	ttl        time.Duration
	now        func() time.Time
	load       func(context.Context, string) ([]store.Permission, error)
}

func newPermissionCache(db *store.Store, ttl time.Duration) *permissionCache {
	return &permissionCache{entries: make(map[string]cachedPermissions), ttl: ttl, now: time.Now, load: db.ListPermissions}
}

func (p *permissionCache) invalidate() {
	p.mu.Lock()
	p.generation++
	clear(p.entries)
	p.mu.Unlock()
}

func (p *permissionCache) updateTTL(ttl time.Duration) {
	p.mu.Lock()
	if p.ttl != ttl {
		p.ttl = ttl
		p.generation++
		clear(p.entries)
	}
	p.mu.Unlock()
}

func (p *permissionCache) get(ctx context.Context, userID string) (*compiledPermissions, error) {
	p.mu.Lock()
	if cached, exists := p.entries[userID]; exists && p.now().Before(cached.expires) {
		p.mu.Unlock()
		return cached.policy, nil
	}
	generation := p.generation
	p.mu.Unlock()
	rules, errLoad := p.load(ctx, userID)
	if errLoad != nil {
		return nil, errLoad
	}
	policy, errCompile := compilePermissions(rules)
	if errCompile != nil {
		return nil, errCompile
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if generation != p.generation {
		// Returning the old policy here could admit a just-denied model even
		// without caching it. Fail closed; a subsequent request can retry.
		return nil, errors.New("account permissions changed during evaluation")
	}
	if len(p.entries) >= maxCachedKeys {
		clear(p.entries)
	}
	p.entries[userID] = cachedPermissions{policy: policy, expires: p.now().Add(p.ttl)}
	return policy, nil
}
