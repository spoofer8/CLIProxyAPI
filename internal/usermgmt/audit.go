package usermgmt

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	log "github.com/sirupsen/logrus"
)

type AuditActor struct{ ID, ClientIP string }
type AuditEvent struct {
	Action, Target string
	Detail         map[string]any
}
type auditActorKey struct{}
type auditScopeKey struct{}

func WithAuditActor(ctx context.Context, actor AuditActor) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, auditActorKey{}, actor)
}
func AuditActorFromContext(ctx context.Context) AuditActor {
	if ctx == nil {
		return AuditActor{}
	}
	actor, _ := ctx.Value(auditActorKey{}).(AuditActor)
	return actor
}

// Audit details accept only fixed operational fields; arbitrary request data,
// credentials, hashes, cookies and configuration values cannot be serialized.
func sanitizedAudit(ctx context.Context, event AuditEvent) store.AuditEvent {
	actor := AuditActorFromContext(ctx)
	detail := make(map[string]any)
	for key, value := range event.Detail {
		switch key {
		case "fields":
			if values, ok := value.([]string); ok {
				detail[key] = append([]string(nil), values...)
			}
		case "cleared", "replacement":
			if value, ok := value.(bool); ok {
				detail[key] = value
			}
		case "rule_count", "status_code":
			if value, ok := value.(int); ok {
				detail[key] = value
			}
		case "key_prefix":
			if value, ok := value.(string); ok && strings.HasPrefix(value, "sk-cpa-") && len(value) <= 15 {
				detail[key] = value
			}
		case "method", "route", "reason":
			if value, ok := value.(string); ok && len(value) <= 256 && !strings.ContainsAny(value, "\r\n?\x00") {
				detail[key] = value
			}
		}
	}
	return store.AuditEvent{Actor: actor.ID, ClientIP: actor.ClientIP, Action: event.Action, Target: event.Target, Detail: detail}
}

func (r *Runtime) mutate(ctx context.Context, event AuditEvent, operation func(*store.Store) error) error {
	return r.withStore(true, func(db *store.Store) error {
		return db.Transaction(ctx, func(txStore *store.Store) error {
			if errOperation := operation(txStore); errOperation != nil {
				return errOperation
			}
			return txStore.AppendAudit(ctx, sanitizedAudit(ctx, event))
		})
	})
}

func (r *Runtime) RecordAudit(ctx context.Context, event AuditEvent) error {
	if ctx != nil {
		if scope, ok := ctx.Value(auditScopeKey{}).(*usageScope); ok && scope != nil {
			return scope.store.AppendAudit(ctx, sanitizedAudit(ctx, event))
		}
	}
	return r.withStore(false, func(db *store.Store) error { return db.AppendAudit(ctx, sanitizedAudit(ctx, event)) })
}

func (r *Runtime) BeginAudit(ctx context.Context) (context.Context, func(), error) {
	if r == nil {
		return ctx, nil, ErrDisabled
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	scope := r.current
	if scope == nil {
		r.mu.Unlock()
		return ctx, nil, ErrDisabled
	}
	scope.leases++
	r.mu.Unlock()
	var once sync.Once
	return context.WithValue(ctx, auditScopeKey{}, scope), func() { once.Do(func() { r.releaseScope(scope) }) }, nil
}

func (r *Runtime) listAudit(c *gin.Context) {
	limit, offset, ok := parsePagination(c)
	if !ok {
		return
	}
	if offset != 0 {
		badRequest(c, "Audit pagination uses before, not offset")
		return
	}
	before := int64(0)
	if raw, exists := c.GetQuery("before"); exists {
		var err error
		before, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || before <= 0 {
			badRequest(c, "before must be a positive audit event ID")
			return
		}
	}
	actor, action := c.Query("actor"), c.Query("action")
	if !validText(actor, 256) || !validText(action, 128) {
		badRequest(c, "Invalid audit filter")
		return
	}
	var events []store.AuditEvent
	errList := r.withStore(false, func(db *store.Store) error {
		var err error
		events, err = db.ListAudit(c.Request.Context(), limit, before, actor, action)
		return err
	})
	if errList != nil {
		respondError(c, errList)
		return
	}
	next := int64(0)
	if len(events) == limit {
		next = events[len(events)-1].ID
	}
	c.JSON(http.StatusOK, gin.H{"events": events, "next_before": next})
}

type invalidKeyLimiter struct {
	mu     sync.Mutex
	last   map[string]time.Time
	global time.Time
}

func (l *invalidKeyLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.global.IsZero() && now.Sub(l.global) < time.Second {
		return false
	}
	if at, exists := l.last[ip]; exists && now.Sub(at) < time.Minute {
		return false
	}
	if l.last == nil {
		l.last = make(map[string]time.Time)
	}
	if len(l.last) >= 4096 {
		clear(l.last)
	}
	l.last[ip] = now
	l.global = now
	return true
}

// RecordInvalidKey is called only after all access providers reject the request.
// It contains no credential material and never blocks a proxy response on SQL.
func (r *Runtime) RecordInvalidKey(clientIP string) {
	if r == nil || !r.invalidKeys.allow(clientIP, time.Now()) {
		return
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.current == nil {
		return
	}
	r.current.audit.enqueue(store.AuditEvent{Action: "auth.invalid_key", ClientIP: clientIP, Detail: map[string]any{}})
}

type auditWriter struct {
	mu     sync.Mutex
	queue  chan store.AuditEvent
	closed bool
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	db     *store.Store
}

func newAuditWriter(db *store.Store) *auditWriter {
	ctx, cancel := context.WithCancel(context.Background())
	w := &auditWriter{queue: make(chan store.AuditEvent, 256), ctx: ctx, cancel: cancel, done: make(chan struct{}), db: db}
	go func() {
		defer close(w.done)
		for event := range w.queue {
			if err := db.AppendAudit(ctx, event); err != nil && !errors.Is(err, context.Canceled) {
				log.WithError(err).Warn("user management authentication audit could not be saved")
			}
		}
	}()
	return w
}
func (w *auditWriter) enqueue(event store.AuditEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		select {
		case w.queue <- event:
		default:
		}
	}
}
func (w *auditWriter) close(ctx context.Context) error {
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
