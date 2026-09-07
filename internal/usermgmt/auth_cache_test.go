package usermgmt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
)

func unitAuthState(lookup func(context.Context, string) (store.KeyIdentity, error)) *authState {
	return &authState{
		cache: make(map[string]cachedIdentity), pending: make(map[string]time.Time),
		ttl: time.Minute, now: time.Now, lookup: lookup,
	}
}

func TestAuthCacheTTLAndMutationInvalidation(t *testing.T) {
	lookupCount := 0
	state := unitAuthState(func(context.Context, string) (store.KeyIdentity, error) {
		lookupCount++
		return store.KeyIdentity{UserID: "user1", KeyID: "key1"}, nil
	})
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	state.now = func() time.Time { return now }
	for range 2 {
		if _, errAuth := state.authenticate(context.Background(), "hash"); errAuth != nil {
			t.Fatal(errAuth)
		}
	}
	if lookupCount != 1 {
		t.Fatal("positive cache did not avoid repeated database lookup")
	}
	now = now.Add(time.Minute)
	if _, errAuth := state.authenticate(context.Background(), "hash"); errAuth != nil || lookupCount != 2 {
		t.Fatal("expired cache entry did not revalidate with database")
	}
	state.invalidate()
	if _, errAuth := state.authenticate(context.Background(), "hash"); errAuth != nil || lookupCount != 3 {
		t.Fatal("mutation did not invalidate active cached credential")
	}
	state.updateTTL(2 * time.Minute)
	if _, errAuth := state.authenticate(context.Background(), "hash"); errAuth != nil || lookupCount != 4 {
		t.Fatal("TTL config change retained an old cache deadline")
	}
}

func TestAuthCacheRejectsInFlightLookupAfterInvalidation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	state := unitAuthState(func(context.Context, string) (store.KeyIdentity, error) {
		close(started)
		<-release
		return store.KeyIdentity{UserID: "revoked-user", KeyID: "revoked-key"}, nil
	})
	result := make(chan error, 1)
	go func() {
		_, errAuth := state.authenticate(context.Background(), "hash")
		result <- errAuth
	}()
	<-started
	state.invalidate()
	close(release)
	if errAuth := <-result; !errors.Is(errAuth, store.ErrNotFound) {
		t.Fatal("stale lookup authorized a revoked identity")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.cache) != 0 || len(state.pending) != 0 {
		t.Fatal("stale lookup repopulated cache or last-used batch")
	}
}

func TestAuthCacheDoesNotCacheFailuresAndClearsOnDatabaseFailure(t *testing.T) {
	lookupCount := 0
	state := unitAuthState(func(_ context.Context, hash string) (store.KeyIdentity, error) {
		lookupCount++
		switch hash {
		case "valid":
			return store.KeyIdentity{UserID: "user1", KeyID: "key1"}, nil
		case "database-error":
			return store.KeyIdentity{}, errors.New("database unavailable")
		default:
			return store.KeyIdentity{}, store.ErrNotFound
		}
	})
	for range 2 {
		_, _ = state.authenticate(context.Background(), "missing")
	}
	if lookupCount != 2 {
		t.Fatal("negative credentials were cached")
	}
	_, _ = state.authenticate(context.Background(), "valid")
	_, _ = state.authenticate(context.Background(), "database-error")
	_, _ = state.authenticate(context.Background(), "valid")
	if lookupCount != 5 {
		t.Fatal("observed database error did not clear cached credentials")
	}
}

func TestLastUsedFlushCancellationPreservesBatchForFinalFlush(t *testing.T) {
	state := unitAuthState(nil)
	state.pending["key1"] = time.Now()
	calls := 0
	state.touch = func(ctx context.Context, batch map[string]time.Time) error {
		calls++
		if len(batch) != 1 {
			t.Fatal("lost canceled last-used batch")
		}
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	state.flush(ctx)
	state.flush(context.Background())
	if calls != 2 || len(state.pending) != 0 {
		t.Fatal("last-used batch was not retried on final flush")
	}
}

func TestLastUsedCloseCancelsBlockedFinalFlush(t *testing.T) {
	state := unitAuthState(nil)
	state.pending["key1"] = time.Now()
	state.done = make(chan struct{})
	// Zero cleanup budget is deterministically expired; the production budget
	// is two seconds. A blocked backend must observe cancellation and return.
	state.stopTimeout = 0
	called := make(chan struct{})
	state.touch = func(ctx context.Context, _ map[string]time.Time) error {
		close(called)
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	state.cancel = cancel
	go state.run(ctx)
	state.close()
	select {
	case <-called:
	default:
		t.Fatal("final flush did not attempt pending timestamp batch")
	}
}
