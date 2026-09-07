package usermgmt

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func stringsContainAll(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(value, fragment) {
			return false
		}
	}
	return true
}

func usageContext(t *testing.T, runtime *Runtime, key string) context.Context {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+key)
	identity, errAuth := runtime.AccessProvider().Authenticate(request.Context(), request)
	if errAuth != nil {
		t.Fatal(errAuth)
	}
	return sdkaccess.WithResult(context.Background(), identity)
}

func unitAccountant(t *testing.T, capacity int, persist func(context.Context, store.UsageIncrement) error) *accountant {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	a := &accountant{queue: make(chan store.UsageIncrement, capacity), ctx: ctx, cancel: cancel, done: make(chan struct{}), progress: make(chan struct{}), quota: newQuotaCache(), persist: persist}
	go a.run()
	t.Cleanup(func() {
		if errClose := a.close(context.Background()); errClose != nil {
			t.Error(errClose)
		}
	})
	return a
}

func TestUsageIncrementNormalizesTotalsAndUTCPeriod(t *testing.T) {
	at := time.Date(2026, 10, 1, 1, 0, 0, 0, time.FixedZone("UTC+2", 2*3600))
	for _, testCase := range []struct {
		detail  usage.Detail
		total   int64
		invalid bool
	}{
		{detail: usage.Detail{InputTokens: 2, OutputTokens: 3}, total: 5},
		{detail: usage.Detail{InputTokens: 2, OutputTokens: 3, TotalTokens: 9}, total: 9},
		{detail: usage.Detail{}, total: 0},
		{detail: usage.Detail{TotalTokens: -1}, invalid: true},
		{detail: usage.Detail{InputTokens: math.MaxInt64, OutputTokens: 1}, invalid: true},
	} {
		delta, errDelta := usageIncrement("user", usage.Record{RequestedAt: at, Detail: testCase.detail}, time.Now())
		if testCase.invalid {
			if errDelta == nil {
				t.Fatal("invalid token usage accepted")
			}
			continue
		}
		if errDelta != nil || delta.TotalTokens != testCase.total || delta.Period != "2026-09" {
			t.Fatalf("wrong normalization: %+v (%v)", delta, errDelta)
		}
	}
	if delta, errDelta := usageIncrement("user", usage.Record{}, at); errDelta != nil || delta.Period != "2026-09" {
		t.Fatal("zero request timestamp did not use UTC fallback")
	}
}

func TestAccountingScopesCanceledContextsAndReporting(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	other, otherEngine, _ := testRuntime(t)
	user, _, key := createTestIdentity(t, engine)
	otherUser, _, _ := createTestIdentity(t, otherEngine)
	ctx, cancel := context.WithCancel(usageContext(t, runtime, key))
	cancel()
	at := time.Date(2026, 9, 30, 23, 59, 0, 0, time.UTC)
	record := usage.Record{RequestedAt: at, Stream: true, APIKey: "untrusted-record-principal", Detail: usage.Detail{InputTokens: 5, OutputTokens: 9, TotalTokens: 14}}
	runtime.HandleUsage(ctx, record)
	other.HandleUsage(ctx, record)
	legacy := sdkaccess.WithResult(context.Background(), &sdkaccess.Result{Provider: "config", Principal: user.ID})
	runtime.HandleUsage(legacy, record)
	if errFlush := runtime.FlushUsage(context.Background()); errFlush != nil {
		t.Fatal(errFlush)
	}
	db, _ := runtime.Snapshot()
	monthly, errGet := db.GetMonthlyUsage(context.Background(), user.ID, "2026-09")
	if errGet != nil || monthly.TotalTokens != 14 || monthly.RequestCount != 1 {
		t.Fatalf("canceled context lost or misattributed stream usage: %+v (%v)", monthly, errGet)
	}
	otherDB, _ := other.Snapshot()
	foreign, errForeign := otherDB.GetMonthlyUsage(context.Background(), otherUser.ID, "2026-09")
	if errForeign != nil || foreign.TotalTokens != 0 {
		t.Fatal("usage leaked across embedded runtimes")
	}
	response := requestJSON(t, engine, http.MethodGet, "/usage?period=2026-09&user_id="+user.ID, "", http.StatusOK)
	if !stringsContainAll(response.Body.String(), `"total_tokens":14`, `"request_count":1`, `"period":"2026-09"`) {
		t.Fatal("usage API omitted persisted counters")
	}
	requestJSON(t, engine, http.MethodGet, "/usage?period=2026-13", "", http.StatusBadRequest)
	requestJSON(t, engine, http.MethodGet, "/usage?period=2026-9", "", http.StatusBadRequest)
	requestJSON(t, engine, http.MethodGet, "/usage?user_id=invalid", "", http.StatusBadRequest)
	current := requestJSON(t, engine, http.MethodGet, "/users/"+user.ID, "", http.StatusOK)
	if !stringsContainAll(current.Body.String(), `"usage":`, `"period":"`+time.Now().UTC().Format("2006-01")+`"`) {
		t.Fatal("user fetch omitted current month usage")
	}
}

func TestAccountingQueueSaturationAndWriteFailure(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	a := unitAccountant(t, 1, func(ctx context.Context, _ store.UsageIncrement) error {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return errors.New("database unavailable")
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	delta := store.UsageIncrement{UserID: "user", Period: "2026-09", TotalTokens: 1}
	a.enqueue(delta)
	<-started
	a.enqueue(delta)
	a.enqueue(delta) // must drop immediately even while the DB worker is blocked
	if a.dropped.Load() != 1 {
		t.Fatal("saturated queue did not drop exactly one record")
	}
	close(release)
	if errFlush := a.flush(context.Background()); errFlush != nil {
		t.Fatal(errFlush)
	}
	if a.dropped.Load() != 3 {
		t.Fatal("database failures were not isolated as dropped increments")
	}
}

func TestAccountingCloseCancelsBlockedDatabaseWrite(t *testing.T) {
	started := make(chan struct{})
	a := unitAccountant(t, 1, func(ctx context.Context, _ store.UsageIncrement) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	a.enqueue(store.UsageIncrement{UserID: "user", Period: "2026-09", TotalTokens: 1})
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if errClose := a.close(ctx); !errors.Is(errClose, context.Canceled) {
		t.Fatalf("blocked database close did not honor cancellation: %v", errClose)
	}
}

func TestRetiredScopeDrainsOriginalDatabaseAfterProducerLeases(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	_, _, replacementDSN := testRuntime(t)
	user, _, key := createTestIdentity(t, engine)
	ctx := usageContext(t, runtime, key)
	outerRelease, errBegin := runtime.BeginRequest(ctx)
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	producerRelease, errProducer := runtime.BeginRequest(ctx)
	if errProducer != nil {
		t.Fatal(errProducer)
	}
	oldDB, cfg := runtime.Snapshot()
	identity, _ := sdkaccess.ResultFromContext(ctx)
	scope := runtime.scopes[identity.Metadata[UsageScopeMetadataKey]]
	dispatchStarted, dispatchRelease := make(chan struct{}), make(chan struct{})
	var once sync.Once
	runtime.SetUsageFlusher(func(ctx context.Context) error {
		once.Do(func() { close(dispatchStarted) })
		select {
		case <-dispatchRelease:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	cfg.DSN = replacementDSN
	if errApply := runtime.Apply(context.Background(), cfg); errApply != nil {
		t.Fatal(errApply)
	}
	outerRelease()
	if errHealth := oldDB.Health(context.Background()); errHealth != nil {
		t.Fatal("reload closed database while raw producer was still alive")
	}
	runtime.HandleUsage(ctx, usage.Record{RequestedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Detail: usage.Detail{TotalTokens: 14}})
	producerRelease()
	producerRelease() // release is idempotent
	<-dispatchStarted
	if errHealth := oldDB.Health(context.Background()); errHealth != nil {
		t.Fatal("database closed before SDK dispatcher barrier")
	}
	if errFlush := runtime.FlushUsage(context.Background()); errFlush != nil {
		t.Fatal(errFlush)
	}
	monthly, errGet := oldDB.GetMonthlyUsage(context.Background(), user.ID, "2026-09")
	if errGet != nil || monthly.TotalTokens != 14 {
		t.Fatal("late stream record did not reach its original database")
	}
	newDB, _ := runtime.Snapshot()
	if _, errGet := newDB.GetUser(context.Background(), user.ID); !errors.Is(errGet, store.ErrNotFound) {
		t.Fatal("scope transition copied user or usage to replacement database")
	}
	close(dispatchRelease)
	<-scope.done
	if oldDB.Health(context.Background()) == nil {
		t.Fatal("retired database remained open after drain")
	}
	if _, errLate := runtime.BeginRequest(ctx); errLate == nil {
		t.Fatal("closed scope accepted a late producer")
	}
	runtime.HandleUsage(ctx, usage.Record{Detail: usage.Detail{TotalTokens: 100}}) // safely ignored
}

func TestCloseContextObservesDeadlineBeforeApplyMutex(t *testing.T) {
	runtime, _, _ := testRuntime(t)
	runtime.applyMu.Lock() // model a configuration operation still unwinding
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	closed := make(chan error, 1)
	go func() { closed <- runtime.CloseContext(ctx) }()
	if errClose := <-closed; !errors.Is(errClose, context.Canceled) {
		t.Fatalf("close waited on apply mutex before cancellation: %v", errClose)
	}
	runtime.applyMu.Unlock()
	if errClose := runtime.Close(); errClose != nil {
		t.Fatal(errClose)
	}
}
