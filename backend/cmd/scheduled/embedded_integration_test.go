package main

import (
	"context"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

// failingDeliverer 模拟 delegate 目标（runtime-server）不可达。
type failingDeliverer struct{}

func (failingDeliverer) Deliver(context.Context, string, string) error {
	return context.DeadlineExceeded
}

// testClock 是可手动推进的时钟（daemon 集成测试用）。
type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

// newEmbeddedRuntime 装配真实 daemon 组件：schedule.Runtime + 同源会话租约 arbiter
// + scheduleExecutorAdapter（fake runtimehost.Executor）。
func newEmbeddedRuntime(t *testing.T, sessionID string, exec *fakeExecutor, leaseStore chat.SessionLeaseStore) (*schedule.Runtime, *schedule.SQLiteStore, schedule.Record, *testClock, func()) {
	t.Helper()
	store, err := schedule.NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	clock := &testClock{now: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	svc := schedule.NewService(store, clock.Now)
	delay := int64(60)
	rec, err := svc.Create(sessionID, schedule.CreateRequest{Prompt: "check", Title: "t", AfterSeconds: &delay}, schedule.OriginModel)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	rt := schedule.NewRuntime(schedule.RuntimeOptions{
		Store: store, Service: svc, Clock: clock.Now, Deliverer: failingDeliverer{},
		MaxAttempts: 3, FallbackToMailbox: true,
		Mode:     schedule.ModeEmbedded,
		Executor: scheduleExecutorAdapter{exec: exec, profile: "scheduled"},
		Arbiter:  newDaemonSessionArbiter(leaseStore),
	})
	return rt, store, rec, clock, func() { rt.Stop(); _ = store.Close() }
}

// TestEmbeddedExecutionEndToEnd 验证 daemon 内嵌执行链路：
// delegate 失败 → 取（同一把 chat）会话租约 → 执行 turn → 任务推进、不落 pending。
func TestEmbeddedExecutionEndToEnd(t *testing.T) {
	exec := &fakeExecutor{}
	rt, store, rec, clock, cleanup := newEmbeddedRuntime(t, "session-embedded", exec, chat.NewInMemoryRuntimeStore(16))
	defer cleanup()

	clock.now = clock.now.Add(2 * time.Minute) // 到点
	rt.RequestDrive()
	time.Sleep(150 * time.Millisecond)

	if len(exec.reqs) != 1 {
		t.Fatalf("embedded turn must execute exactly once, got %d", len(exec.reqs))
	}
	if exec.reqs[0].SessionID != "session-embedded" {
		t.Fatalf("unexpected embedded session: %q", exec.reqs[0].SessionID)
	}
	task, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if task.Status != schedule.StatusInactive {
		t.Fatalf("one-time task must be inactive after embedded execution, got %s", task.Status)
	}
	if pending, _ := store.ListPendingMessages("session-embedded", 10); len(pending) != 0 {
		t.Fatalf("no pending expected after embedded success, got %d", len(pending))
	}
}

// TestEmbeddedSkippedWhenSessionHeld 验证：会话被宿主以**同一把** chat 租约持有时，
// daemon 绝不抢占，退回持久 mailbox（这正是 E1 的核心保证）。
func TestEmbeddedSkippedWhenSessionHeld(t *testing.T) {
	leaseStore := chat.NewInMemoryRuntimeStore(16)
	// 模拟交互宿主正在跑该会话的 turn。
	holder, err := chat.AcquireSessionLease(context.Background(), leaseStore, chat.LeaseRequest{
		SessionID: "session-held",
		OwnerID:   "interactive-host",
		OwnerKind: "host",
		PID:       4242,
	})
	if err != nil {
		t.Fatalf("hold lease: %v", err)
	}
	defer func() { _ = holder.Release(context.Background()) }()

	exec := &fakeExecutor{}
	rt, store, _, clock, cleanup := newEmbeddedRuntime(t, "session-held", exec, leaseStore)
	defer cleanup()

	clock.now = clock.now.Add(2 * time.Minute) // 到点
	rt.RequestDrive()
	time.Sleep(150 * time.Millisecond)

	if len(exec.reqs) != 0 {
		t.Fatalf("held session must never be preempted by embedded execution, got %d turns", len(exec.reqs))
	}
	if pending, _ := store.ListPendingMessages("session-held", 10); len(pending) != 1 {
		t.Fatalf("must fall back to durable mailbox, got %d pending", len(pending))
	}
}
