package usermgmt

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	log "github.com/sirupsen/logrus"
)

const maxCachedKeys = 4096

type cachedIdentity struct {
	identity store.KeyIdentity
	expires  time.Time
}

// authState belongs to one store lifetime. Swaps never carry cached identity or
// last-used writes across databases. Only successful authentications are cached.
type authState struct {
	mu          sync.Mutex
	cache       map[string]cachedIdentity
	pending     map[string]time.Time
	generation  uint64
	ttl         time.Duration
	closed      bool
	now         func() time.Time
	lookup      func(context.Context, string) (store.KeyIdentity, error)
	touch       func(context.Context, map[string]time.Time) error
	flushMu     sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
	stopTimeout time.Duration
}

func newAuthState(db *store.Store, ttl time.Duration) *authState {
	state := &authState{
		cache: make(map[string]cachedIdentity), pending: make(map[string]time.Time),
		ttl: ttl, now: time.Now, lookup: db.LookupActiveKey, touch: db.TouchKeys,
		done: make(chan struct{}), stopTimeout: 2 * time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	state.cancel = cancel
	go state.run(ctx)
	return state
}

func (s *authState) run(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.flush(ctx)
		case <-ctx.Done():
			// Last-used timestamps are best-effort metadata. Bound database
			// cleanup so row locks or an outage cannot prevent disable/shutdown.
			cleanupCtx, cancel := context.WithTimeout(context.Background(), s.stopTimeout)
			s.flush(cleanupCtx)
			cancel()
			return
		}
	}
}

func (s *authState) authenticate(ctx context.Context, hash string) (store.KeyIdentity, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return store.KeyIdentity{}, store.ErrNotFound
	}
	now := s.now()
	if cached, exists := s.cache[hash]; exists && now.Before(cached.expires) {
		s.recordUsedLocked(cached.identity.KeyID, now)
		s.mu.Unlock()
		return cached.identity, nil
	}
	delete(s.cache, hash)
	generation := s.generation
	s.mu.Unlock()
	identity, errLookup := s.lookup(ctx, hash)
	if errLookup != nil {
		if !errors.Is(errLookup, store.ErrNotFound) {
			// Once a database failure is observed, cached credentials must not
			// continue authenticating while the backend is unavailable.
			s.invalidate()
		}
		return store.KeyIdentity{}, errLookup
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || generation != s.generation {
		// A concurrent mutation can revoke/disable this identity. Do not admit
		// the stale result, or allow it to repopulate the cache after invalidation.
		return store.KeyIdentity{}, store.ErrNotFound
	}
	if len(s.cache) >= maxCachedKeys {
		clear(s.cache)
	}
	s.cache[hash] = cachedIdentity{identity: identity, expires: s.now().Add(s.ttl)}
	s.recordUsedLocked(identity.KeyID, now)
	return identity, nil
}

func (s *authState) recordUsedLocked(keyID string, at time.Time) {
	if _, exists := s.pending[keyID]; exists || len(s.pending) < maxCachedKeys {
		s.pending[keyID] = at
	}
}

func (s *authState) invalidate() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.generation++
	clear(s.cache)
	s.mu.Unlock()
}

func (s *authState) updateTTL(ttl time.Duration) {
	s.mu.Lock()
	if s.ttl != ttl {
		s.ttl = ttl
		s.generation++
		clear(s.cache)
	}
	s.mu.Unlock()
}

func (s *authState) flush(ctx context.Context) {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.mu.Lock()
	batch := s.pending
	s.pending = make(map[string]time.Time)
	s.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	if errTouch := s.touch(ctx, batch); errTouch != nil {
		if ctx.Err() != nil {
			// Shutdown may interrupt a periodic flush. Preserve that batch for
			// the worker's final flush before its pool is closed.
			s.mu.Lock()
			for id, at := range batch {
				if pending, exists := s.pending[id]; !exists || pending.Before(at) {
					s.recordUsedLocked(id, at)
				}
			}
			s.mu.Unlock()
			return
		}
		s.invalidate()
		log.WithError(errTouch).Warn("user management API key last-used batch could not be saved")
	}
}

func (s *authState) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	s.generation++
	clear(s.cache)
	s.mu.Unlock()
	s.cancel()
	<-s.done
}
