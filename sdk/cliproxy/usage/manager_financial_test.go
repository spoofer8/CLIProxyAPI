package usage

import (
	"context"
	"sync"
	"testing"
)

type synchronousTestPlugin struct {
	mu      sync.Mutex
	records []Record
}

func (p *synchronousTestPlugin) SynchronousUsage() {}
func (p *synchronousTestPlugin) HandleUsage(_ context.Context, r Record) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.records = append(p.records, r)
}
func TestSynchronousFinancialPluginPrecedesQueueAndNeverDoubleDispatches(t *testing.T) {
	m := NewManager(1)
	plugin := &synchronousTestPlugin{}
	m.Register(plugin)
	m.Publish(context.Background(), Record{Model: "one"})
	plugin.mu.Lock()
	if len(plugin.records) != 1 || plugin.records[0].EventID == "" {
		t.Fatal("financial record was not durably dispatched before Publish returned")
	}
	plugin.mu.Unlock()
	if e := m.Flush(context.Background()); e != nil {
		t.Fatal(e)
	}
	plugin.mu.Lock()
	if len(plugin.records) != 1 {
		t.Fatal("sync plugin also consumed asynchronous duplicate")
	}
	plugin.mu.Unlock()
	m.Stop()
	m.Publish(context.Background(), Record{EventID: "late-stable-event", Model: "late"})
	plugin.mu.Lock()
	defer plugin.mu.Unlock()
	if len(plugin.records) != 2 || plugin.records[1].EventID != "late-stable-event" {
		t.Fatal("accepted producer financial record lost after analytics dispatcher stopped")
	}
}
