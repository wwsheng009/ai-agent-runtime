package schedule

import (
	"context"
	"sync"
	"testing"
	"time"
)

type recordingDeliverer struct {
	mu      sync.Mutex
	records []string
	ch      chan string
}

func newRecordingDeliverer() *recordingDeliverer {
	return &recordingDeliverer{ch: make(chan string, 8)}
}

func (d *recordingDeliverer) Deliver(_ context.Context, sessionID, text string) error {
	d.mu.Lock()
	d.records = append(d.records, sessionID+"|"+text)
	d.mu.Unlock()
	select {
	case d.ch <- sessionID:
	default:
	}
	return nil
}

func (d *recordingDeliverer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.records)
}

func TestRuntimeDeliversDueOneShotAndDeactivates(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	svc := NewService(store, clock)

	delay := int64(60)
	rec, err := svc.Create("session-1", CreateRequest{Prompt: "check", Title: "Check", AfterSeconds: &delay}, OriginModel)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	deliverer := newRecordingDeliverer()
	rt := NewRuntime(RuntimeOptions{Store: store, Service: svc, Clock: clock, Deliverer: deliverer, MaxAttempts: 3})
	defer rt.Stop()

	// 到点前驱动：不投递。
	rt.RequestDrive()
	time.Sleep(50 * time.Millisecond)
	if deliverer.count() != 0 {
		t.Fatalf("no delivery expected before due, got %d", deliverer.count())
	}

	// 到点后驱动：投递一次并转 inactive。
	now = now.Add(2 * time.Minute)
	rt.RequestDrive()
	select {
	case <-deliverer.ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for delivery")
	}
	time.Sleep(50 * time.Millisecond)

	task, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if task.Status != StatusInactive {
		t.Fatalf("one-shot must become inactive, got %s", task.Status)
	}
	if task.LastDelivery == nil {
		t.Fatal("expected a delivery receipt")
	}
	if task.LastDelivery.MessageID == "" {
		t.Fatal("expected a message id on the receipt")
	}
}

func TestRuntimeAdvancesEveryTask(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	svc := NewService(store, clock)

	every := int64(300)
	rec, err := svc.Create("session-1", CreateRequest{Prompt: "queue", Title: "Queue", EverySeconds: &every}, OriginModel)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	deliverer := newRecordingDeliverer()
	rt := NewRuntime(RuntimeOptions{Store: store, Service: svc, Clock: clock, Deliverer: deliverer, MaxAttempts: 3})
	defer rt.Stop()

	now = now.Add(25 * time.Minute)
	rt.RequestDrive()
	select {
	case <-deliverer.ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for delivery")
	}
	time.Sleep(50 * time.Millisecond)

	task, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if task.Status != StatusActive {
		t.Fatalf("every task must stay active, got %s", task.Status)
	}
	wantNext := now.Add(5 * time.Minute)
	if !task.Record.ScheduledAt.Equal(wantNext) {
		t.Fatalf("next target = %s, want %s", task.Record.ScheduledAt, wantNext)
	}
}
