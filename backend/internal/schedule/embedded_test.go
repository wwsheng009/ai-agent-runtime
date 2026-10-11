package schedule

import (
	"context"
	"sync"
	"testing"
	"time"
)

// embeddedExecutor 记录内嵌执行调用。
type embeddedExecutor struct {
	mu    sync.Mutex
	calls []string
	fail  bool
}

func (e *embeddedExecutor) ExecuteTurn(_ context.Context, sessionID, _ string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fail {
		return context.DeadlineExceeded
	}
	e.calls = append(e.calls, sessionID)
	return nil
}

func (e *embeddedExecutor) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

// testArbiter 是租约仲裁测试替身：held 集合里的会话拿不到租约。
type testArbiter struct {
	mu       sync.Mutex
	held     map[string]bool
	acquired []string
}

func newTestArbiter() *testArbiter { return &testArbiter{held: map[string]bool{}} }

func (a *testArbiter) AcquireSession(_ context.Context, sessionID string) (func(), bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.held[sessionID] {
		return func() {}, false
	}
	a.acquired = append(a.acquired, sessionID)
	return func() {}, true
}

func TestTryEmbeddedExecutesWhenLeaseGranted(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	svc := NewService(store, clock)

	delay := int64(60)
	rec, err := svc.Create("session-1", CreateRequest{Prompt: "x", Title: "t", AfterSeconds: &delay})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	deliverer := &flakyDeliverer{failFirst: 3} // delegate 恒失败
	executor := &embeddedExecutor{}
	arbiter := newTestArbiter()
	rt := NewRuntime(RuntimeOptions{
		Store: store, Service: svc, Clock: clock, Deliverer: deliverer,
		MaxAttempts: 3, FallbackToMailbox: true,
		Executor: executor, Arbiter: arbiter,
	})
	defer rt.Stop()

	now = now.Add(2 * time.Minute)
	rt.RequestDrive()
	time.Sleep(80 * time.Millisecond)

	if executor.count() != 1 {
		t.Fatalf("embedded turn must execute once, got %d", executor.count())
	}
	// 内嵌成功 → 任务推进（一次性转 inactive）。
	task, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if task.Status != StatusInactive || task.LastDelivery == nil {
		t.Fatalf("task must be committed after embedded execution: %+v", task)
	}
	// 兜底消息不应写入（已成功执行）。
	pending, _ := store.ListPendingMessages("session-1", 10)
	if len(pending) != 0 {
		t.Fatalf("no pending expected after embedded success, got %d", len(pending))
	}
}

func TestTryEmbeddedSkipsWhenLeaseHeld(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	svc := NewService(store, clock)

	delay := int64(60)
	if _, err := svc.Create("session-busy", CreateRequest{Prompt: "x", Title: "t", AfterSeconds: &delay}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	deliverer := &flakyDeliverer{failFirst: 3}
	executor := &embeddedExecutor{}
	arbiter := newTestArbiter()
	arbiter.held["session-busy"] = true // 活跃会话：交互宿主持有
	rt := NewRuntime(RuntimeOptions{
		Store: store, Service: svc, Clock: clock, Deliverer: deliverer,
		MaxAttempts: 3, FallbackToMailbox: true,
		Executor: executor, Arbiter: arbiter,
	})
	defer rt.Stop()

	now = now.Add(2 * time.Minute)
	rt.RequestDrive()
	time.Sleep(80 * time.Millisecond)

	if executor.count() != 0 {
		t.Fatal("held session must never be preempted by embedded execution")
	}
	// 退回兜底：pending 落库。
	pending, _ := store.ListPendingMessages("session-busy", 10)
	if len(pending) != 1 {
		t.Fatalf("fallback pending expected, got %d", len(pending))
	}
}

func TestTryEmbeddedNoopWithoutExecutor(t *testing.T) {
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
	arbiter := newTestArbiter()
	rt := NewRuntime(RuntimeOptions{
		Store: store, Service: svc, Clock: clock, Deliverer: deliverer,
		MaxAttempts: 3, FallbackToMailbox: true,
		Executor: nil, Arbiter: arbiter, // 未注入 Executor → 恒不内嵌
	})
	defer rt.Stop()

	now = now.Add(2 * time.Minute)
	rt.RequestDrive()
	time.Sleep(80 * time.Millisecond)

	pending, _ := store.ListPendingMessages("session-1", 10)
	if len(pending) != 1 {
		t.Fatalf("without executor the fallback path must run, got %d pending", len(pending))
	}
}
