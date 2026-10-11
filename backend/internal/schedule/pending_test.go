package schedule

import (
	"context"
	"sync"
	"testing"
	"time"
)

// flakyDeliverer 前几次失败，之后成功。
type flakyDeliverer struct {
	mu          sync.Mutex
	failFirst   int
	deliveries  int
	records     []string
	redelivered []string
}

func (d *flakyDeliverer) Deliver(_ context.Context, sessionID, text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deliveries++
	if d.deliveries <= d.failFirst {
		return context.DeadlineExceeded
	}
	d.records = append(d.records, sessionID+"|"+text)
	d.redelivered = append(d.redelivered, sessionID)
	return nil
}

func TestRuntimeFallbackToPendingAndDrain(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	svc := NewService(store, clock)

	delay := int64(60)
	rec, err := svc.Create("session-1", CreateRequest{Prompt: "offline check", Title: "Offline", AfterSeconds: &delay})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	deliverer := &flakyDeliverer{failFirst: 1}
	rt := NewRuntime(RuntimeOptions{
		Store: store, Service: svc, Clock: clock, Deliverer: deliverer,
		MaxAttempts: 3, FallbackToMailbox: true,
	})
	defer rt.Stop()

	// 第一次驱动：投递失败 → 落 pending。
	now = now.Add(2 * time.Minute)
	rt.RequestDrive()
	time.Sleep(80 * time.Millisecond)

	pending, err := store.ListPendingMessages("session-1", 10)
	if err != nil {
		t.Fatalf("ListPendingMessages: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected one pending message, got %d", len(pending))
	}
	if pending[0].ScheduleID != rec.ID || pending[0].Prompt != "offline check" {
		t.Fatalf("unexpected pending: %+v", pending[0])
	}
	if pending[0].Reason == "" {
		t.Fatal("pending must record the failure reason")
	}

	// 任务仍在（未达死信上限），目标未推进。
	task, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if task.Status != StatusActive {
		t.Fatalf("task must stay active after a failed delivery, got %s", task.Status)
	}

	// 恢复：DrainPendingMessages 消费兜底消息。
	consumed, err := rt.DrainPendingMessages(context.Background(), "session-1", 10)
	if err != nil {
		t.Fatalf("DrainPendingMessages: %v", err)
	}
	if consumed != 1 {
		t.Fatalf("consumed = %d, want 1", consumed)
	}

	// 消费后 pending 清空。
	pending, err = store.ListPendingMessages("session-1", 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after drain = %d, err = %v", len(pending), err)
	}
}

func TestRuntimeFallbackDisabled(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	svc := NewService(store, clock)

	delay := int64(60)
	if _, err := svc.Create("session-1", CreateRequest{Prompt: "x", Title: "t", AfterSeconds: &delay}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	deliverer := &flakyDeliverer{failFirst: 3}
	rt := NewRuntime(RuntimeOptions{
		Store: store, Service: svc, Clock: clock, Deliverer: deliverer,
		MaxAttempts: 3, FallbackToMailbox: false,
	})
	defer rt.Stop()

	now = now.Add(2 * time.Minute)
	rt.RequestDrive()
	time.Sleep(80 * time.Millisecond)

	pending, err := store.ListPendingMessages("session-1", 10)
	if err != nil {
		t.Fatalf("ListPendingMessages: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("fallback disabled must not write pending, got %d", len(pending))
	}
}
