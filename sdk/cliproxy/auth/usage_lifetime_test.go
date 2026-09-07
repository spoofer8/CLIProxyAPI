package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type leasedStreamExecutor struct {
	ProviderExecutor
	chunks <-chan cliproxyexecutor.StreamChunk
}

func (e leasedStreamExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return &cliproxyexecutor.StreamResult{Chunks: e.chunks}, nil
}

type leaseUsageCapture struct{ total atomic.Int64 }

func (p *leaseUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	p.total.Add(record.Detail.TotalTokens)
}

func TestCanceledStreamRetainsLeaseUntilProducerEnds(t *testing.T) {
	for _, withUsage := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-usage", true: "final-usage"}[withUsage], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var leases atomic.Int64
			released := make(chan struct{})
			ctx = sdkaccess.WithRequestHooks(ctx, sdkaccess.RequestHooks{Begin: func(context.Context) (func(), error) {
				leases.Add(1)
				var once sync.Once
				return func() { once.Do(func() { leases.Add(-1); close(released) }) }, nil
			}})
			raw := make(chan cliproxyexecutor.StreamChunk)
			manager := usage.NewManager(1)
			capture := &leaseUsageCapture{}
			manager.Register(capture)
			t.Cleanup(func() {
				if errStop := manager.StopAndWait(context.Background()); errStop != nil {
					t.Error(errStop)
				}
			})
			stream, errStream := executeStreamWithUsageLease(ctx, leasedStreamExecutor{chunks: raw}, nil, cliproxyexecutor.Request{}, cliproxyexecutor.Options{})
			if errStream != nil {
				t.Fatal(errStream)
			}
			cancel()
			if leases.Load() != 1 {
				t.Fatal("cancellation released the lease before the raw producer finished")
			}
			producerMayFinish := make(chan struct{})
			go func() {
				<-producerMayFinish
				// Existing built-in executors enqueue final usage before raw close.
				// A no-usage exit must also release without waiting for a record.
				if withUsage {
					manager.Publish(ctx, usage.Record{Detail: usage.Detail{TotalTokens: 17}})
				}
				close(raw)
			}()
			select {
			case <-released:
				t.Fatal("lease ended while producer could still publish")
			default:
			}
			close(producerMayFinish)
			select {
			case <-released:
			case <-time.After(2 * time.Second):
				t.Fatal("raw producer ended but its lease leaked")
			}
			for range stream.Chunks {
			}
			if errFlush := manager.Flush(context.Background()); errFlush != nil {
				t.Fatal(errFlush)
			}
			want := int64(0)
			if withUsage {
				want = 17
			}
			if capture.total.Load() != want || leases.Load() != 0 {
				t.Fatalf("after producer drain: tokens=%d leases=%d", capture.total.Load(), leases.Load())
			}
		})
	}
}

func TestLegacyStreamDoesNotAddUsageLifetimeWrapper(t *testing.T) {
	raw := make(chan cliproxyexecutor.StreamChunk)
	close(raw)
	stream, errStream := executeStreamWithUsageLease(context.Background(), leasedStreamExecutor{chunks: raw}, nil, cliproxyexecutor.Request{}, cliproxyexecutor.Options{})
	if errStream != nil || stream.Chunks != raw {
		t.Fatal("legacy stream gained an unnecessary lifetime wrapper")
	}
}
