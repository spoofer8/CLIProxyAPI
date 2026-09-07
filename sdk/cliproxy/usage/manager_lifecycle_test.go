package usage

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

type usagePluginFunc func(context.Context, Record)

func (f usagePluginFunc) HandleUsage(ctx context.Context, record Record) { f(ctx, record) }

func TestFlushWaitsForCallbacksAndSupportsCanceledWait(t *testing.T) {
	manager := NewManager(1)
	entered, release := make(chan struct{}), make(chan struct{})
	var callbacks atomic.Int64
	manager.Register(usagePluginFunc(func(_ context.Context, _ Record) {
		if callbacks.Add(1) == 1 {
			close(entered)
			<-release
		}
	}))
	manager.Publish(context.Background(), Record{})
	<-entered
	manager.Publish(context.Background(), Record{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if errFlush := manager.Flush(ctx); !errors.Is(errFlush, context.Canceled) {
		t.Fatalf("blocked dispatcher flush = %v, want canceled", errFlush)
	}
	close(release)
	if errFlush := manager.Flush(context.Background()); errFlush != nil {
		t.Fatal(errFlush)
	}
	if callbacks.Load() != 2 {
		t.Fatal("flush returned before all previously published callbacks completed")
	}
	if errStop := manager.StopAndWait(context.Background()); errStop != nil {
		t.Fatal(errStop)
	}
}

func TestUnregisterNamedPreservesOtherPluginsAndWaitsViaFlush(t *testing.T) {
	manager := NewManager(1)
	entered, release := make(chan struct{}), make(chan struct{})
	var first, second atomic.Int64
	manager.RegisterNamed("first", usagePluginFunc(func(context.Context, Record) {
		first.Add(1)
		close(entered)
		<-release
	}))
	manager.RegisterNamed("second", usagePluginFunc(func(context.Context, Record) { second.Add(1) }))
	manager.Publish(context.Background(), Record{})
	<-entered
	manager.UnregisterNamed("first")
	manager.UnregisterNamed("missing")
	close(release)
	if errFlush := manager.Flush(context.Background()); errFlush != nil {
		t.Fatal(errFlush)
	}
	manager.RegisterNamed("second", usagePluginFunc(func(context.Context, Record) { second.Add(10) }))
	manager.Publish(context.Background(), Record{})
	if errFlush := manager.Flush(context.Background()); errFlush != nil {
		t.Fatal(errFlush)
	}
	if first.Load() != 1 || second.Load() != 11 {
		t.Fatalf("named unregister/replacement corrupted delivery: first=%d second=%d", first.Load(), second.Load())
	}
	if errStop := manager.StopAndWait(context.Background()); errStop != nil {
		t.Fatal(errStop)
	}
}

func TestStopDrainsAcceptedRecordsAndRejectsLatePublication(t *testing.T) {
	manager := NewManager(1)
	entered, release := make(chan struct{}), make(chan struct{})
	var callbacks atomic.Int64
	manager.Register(usagePluginFunc(func(context.Context, Record) {
		if callbacks.Add(1) == 1 {
			close(entered)
			<-release
		}
	}))
	for range 32 {
		manager.Publish(context.Background(), Record{})
	}
	<-entered
	manager.Stop()
	manager.Publish(context.Background(), Record{})
	close(release)
	if errStop := manager.StopAndWait(context.Background()); errStop != nil {
		t.Fatal(errStop)
	}
	if callbacks.Load() != 32 {
		t.Fatalf("stop delivered %d callbacks, want the 32 accepted records", callbacks.Load())
	}
}

func TestManagerConcurrentStartStopAndNamedRegistration(t *testing.T) {
	manager := NewManager(1)
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 20 {
				manager.Start(context.Background())
				manager.RegisterNamed("same", usagePluginFunc(func(context.Context, Record) {}))
				manager.Publish(context.Background(), Record{})
				manager.UnregisterNamed("same")
			}
			manager.Stop()
		}()
	}
	workers.Wait()
	if errStop := manager.StopAndWait(context.Background()); errStop != nil {
		t.Fatal(errStop)
	}
}
