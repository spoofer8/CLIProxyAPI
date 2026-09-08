package usermgmt

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	log "github.com/sirupsen/logrus"
)

type requestActivityJob struct {
	initial      *store.RequestActivity
	id, provider string
	status       int
	durationMS   int64
	completion   *store.RequestActivity
	usage        *store.UsageIncrement
	barrier      chan struct{}
}

// At most 64 sanitized 256KiB previews await persistence. Raw bodies never
// enter this queue, and overload drops diagnostics without delaying inference.
type requestActivityWriter struct {
	mu            sync.Mutex
	queue         chan requestActivityJob
	closed        bool
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	db            *store.Store
	retentionDays atomic.Int64
	lastWarning   time.Time
}

func newRequestActivityWriter(db *store.Store, days int) *requestActivityWriter {
	ctx, cancel := context.WithCancel(context.Background())
	w := &requestActivityWriter{queue: make(chan requestActivityJob, 64), ctx: ctx, cancel: cancel, done: make(chan struct{}), db: db}
	w.retentionDays.Store(int64(days))
	go w.run()
	return w
}

func (w *requestActivityWriter) run() {
	defer close(w.done)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	w.cleanup()
	for {
		select {
		case job, ok := <-w.queue:
			if !ok {
				return
			}
			if job.barrier != nil {
				close(job.barrier)
				continue
			}
			ctx, cancel := context.WithTimeout(w.ctx, 2*time.Second)
			var errWrite error
			switch {
			case job.initial != nil:
				errWrite = w.db.InsertRequestActivity(ctx, *job.initial)
			case job.completion != nil:
				errWrite = w.db.FinishRequestActivityPreview(ctx, *job.completion, job.status, job.durationMS)
			case job.usage != nil:
				errWrite = w.db.AddRequestActivityUsage(ctx, job.id, job.provider, *job.usage)
			}
			cancel()
			if errWrite != nil && w.ctx.Err() == nil {
				w.warn()
			}
		case <-ticker.C:
			w.cleanup()
		case <-w.ctx.Done():
			return
		}
	}
}

func (w *requestActivityWriter) warn() {
	if time.Since(w.lastWarning) < time.Minute {
		return
	}
	w.lastWarning = time.Now()
	// No body, database error text, credential, or request identifier is logged.
	log.Warn("user management request activity persistence unavailable; some activity may be missing")
}

func (w *requestActivityWriter) cleanup() {
	ctx, cancel := context.WithTimeout(w.ctx, 2*time.Second)
	defer cancel()
	cutoff := time.Now().UTC().Add(-time.Duration(w.retentionDays.Load()) * 24 * time.Hour)
	for ctx.Err() == nil {
		count, errDelete := w.db.DeleteExpiredRequestActivity(ctx, cutoff)
		if errDelete != nil {
			if w.ctx.Err() == nil {
				w.warn()
			}
			return
		}
		if count < 1000 {
			return
		}
	}
}

func (w *requestActivityWriter) enqueue(job requestActivityJob) bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return false
	}
	select {
	case w.queue <- job:
		return true
	default:
		return false
	}
}

func (w *requestActivityWriter) flush(ctx context.Context) error {
	if w == nil {
		return nil
	}
	barrier := make(chan struct{})
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		select {
		case <-w.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case w.queue <- requestActivityJob{barrier: barrier}:
		w.mu.Unlock()
	case <-ctx.Done():
		w.mu.Unlock()
		return ctx.Err()
	case <-w.done:
		w.mu.Unlock()
		return nil
	}
	select {
	case <-barrier:
		return nil
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *requestActivityWriter) close(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.queue)
	}
	w.mu.Unlock()
	select {
	case <-w.done:
		w.cancel()
		return nil
	case <-ctx.Done():
		w.cancel()
		<-w.done
		return ctx.Err()
	}
}
