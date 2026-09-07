package usermgmt

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const accountingQueueSize = 1024

// accountant has one bounded queue per store lifetime. It never uses a request's
// canceled context for persistence, and saturation/write failures are best-effort
// drops rather than errors returned to the already completed upstream request.
type accountant struct {
	mu        sync.Mutex
	queue     chan store.UsageIncrement
	closed    bool
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	persist   func(context.Context, store.UsageIncrement) error
	quota     *quotaCache
	dropped   atomic.Uint64
	enqueued  uint64
	completed uint64
	progress  chan struct{}
}

func newAccountant(db *store.Store) *accountant {
	ctx, cancel := context.WithCancel(context.Background())
	a := &accountant{
		queue: make(chan store.UsageIncrement, accountingQueueSize), ctx: ctx, cancel: cancel,
		done: make(chan struct{}), persist: db.IncrementUsage, quota: newQuotaCache(),
		progress: make(chan struct{}),
	}
	go a.run()
	return a
}

func usageIncrement(userID string, record usage.Record, now time.Time) (store.UsageIncrement, error) {
	detail := record.Detail
	if detail.InputTokens < 0 || detail.OutputTokens < 0 || detail.TotalTokens < 0 || detail.InputTokens > math.MaxInt64-detail.OutputTokens {
		return store.UsageIncrement{}, store.ErrInvalidUsage
	}
	total := detail.TotalTokens
	if total == 0 {
		total = detail.InputTokens + detail.OutputTokens
	}
	at := record.RequestedAt
	if at.IsZero() {
		at = now
	}
	return store.UsageIncrement{
		UserID: userID, Period: at.UTC().Format("2006-01"),
		InputTokens: detail.InputTokens, OutputTokens: detail.OutputTokens, TotalTokens: total,
	}, nil
}

func (a *accountant) enqueue(delta store.UsageIncrement) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		a.drop("accounting scope closed")
		return
	}
	select {
	case a.queue <- delta:
		a.enqueued++
	default:
		a.drop("accounting queue full")
	}
}

func (a *accountant) drop(reason string) {
	count := a.dropped.Add(1)
	// Report first/powers-of-two drops, avoiding an unbounded outage log flood.
	if count&(count-1) == 0 {
		log.WithFields(log.Fields{"dropped_records": count, "reason": reason}).Warn("user management usage record dropped")
	}
}

func (a *accountant) run() {
	defer close(a.done)
	for delta := range a.queue {
		if errPersist := a.persist(a.ctx, delta); errPersist != nil {
			a.drop("database write failed")
		}
		// Cache only committed counters. Invalidating after persistence avoids
		// double-counting a delta that a concurrent database read already saw.
		a.quota.invalidate()
		a.mu.Lock()
		a.completed++
		close(a.progress)
		a.progress = make(chan struct{})
		a.mu.Unlock()
	}
}

// flush waits for all items queued before its barrier, without stopping delivery.
func (a *accountant) flush(ctx context.Context) error {
	a.mu.Lock()
	target := a.enqueued
	for a.completed < target {
		progress := a.progress
		a.mu.Unlock()
		select {
		case <-progress:
		case <-ctx.Done():
			return ctx.Err()
		}
		a.mu.Lock()
	}
	a.mu.Unlock()
	return nil
}

func (a *accountant) close(ctx context.Context) error {
	a.mu.Lock()
	if !a.closed {
		a.closed = true
		close(a.queue)
	}
	a.mu.Unlock()
	select {
	case <-a.done:
		a.cancel()
		return nil
	case <-ctx.Done():
		a.cancel()
		<-a.done // database/sql honors worker cancellation for a blocked query
		return ctx.Err()
	}
}

// FlushUsage is useful for consistency-sensitive reporting, lifecycle barriers,
// and tests. It drains domain queues; the SDK dispatcher must be flushed first.
func (r *Runtime) FlushUsage(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	states := make([]*accountant, 0, len(r.scopes))
	for _, scope := range r.scopes {
		states = append(states, scope.accountant)
	}
	r.mu.RUnlock()
	var errs []error
	for _, state := range states {
		if errFlush := state.flush(ctx); errFlush != nil {
			errs = append(errs, errFlush)
		}
	}
	return errors.Join(errs...)
}
