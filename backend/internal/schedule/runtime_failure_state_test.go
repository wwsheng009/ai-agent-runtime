package schedule

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestDeliveryFailureIsPersistedAndDeadLetterBlocks 钉住失败可见性与停止空转：
// 每次失败都写 last_error/code/last_attempt_at；达到 MaxAttempts 后转 blocked，
// 不再被 due scanner 选中，且手动 resume 能恢复。
func TestDeliveryFailureIsPersistedAndDeadLetterBlocks(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	svc := NewService(store, clock)

	delay := int64(60)
	rec, err := svc.Create("session-1", CreateRequest{Prompt: "ping", Title: "Ping", AfterSeconds: &delay}, OriginModel)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	deliverer := &flakyDeliverer{failFirst: 10}
	rt := NewRuntime(RuntimeOptions{
		Store: store, Service: svc, Clock: clock, Deliverer: deliverer,
		MaxAttempts: 2, FallbackToMailbox: true,
	})
	defer rt.Stop()

	now = now.Add(2 * time.Minute)
	rt.RequestDrive()
	waitFor(t, 5*time.Second, func() bool {
		task, ok, getErr := store.Get(rec.ID)
		return getErr == nil && ok && task.AttemptCount == 1 && task.LastErrorCode == string(CodeDeliveryFailed)
	}, "first failure persisted")

	task, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get after first failure: ok=%v err=%v", ok, err)
	}
	if task.Status != StatusActive {
		t.Fatalf("first failure must keep retrying in active state, got %s", task.Status)
	}
	if !strings.Contains(task.LastError, "deadline exceeded") {
		t.Fatalf("last_error must carry the delivery failure, got %q", task.LastError)
	}
	if task.LastAttemptAt == nil {
		t.Fatal("last_attempt_at must be persisted")
	}

	now = now.Add(31 * time.Second)
	rt.RequestDrive()
	waitFor(t, 5*time.Second, func() bool {
		current, found, getErr := store.Get(rec.ID)
		return getErr == nil && found && current.Status == StatusBlocked
	}, "dead letter blocks task")

	task, ok, err = store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get after dead letter: ok=%v err=%v", ok, err)
	}
	if task.AttemptCount != 2 || task.DeadLetteredAt == nil {
		t.Fatalf("dead letter bookkeeping = attempts:%d deadLetteredAt:%v", task.AttemptCount, task.DeadLetteredAt)
	}
	due, err := store.ListActiveDue(now.Add(24 * time.Hour))
	if err != nil {
		t.Fatalf("ListActiveDue: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("blocked task must not be due, got %+v", due)
	}

	resumed, err := svc.Resume("session-1", rec.ID)
	if err != nil || !resumed.Updated || resumed.Status != StatusActive {
		t.Fatalf("resume blocked task = %+v, err=%v", resumed, err)
	}
	after, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get after resume: ok=%v err=%v", ok, err)
	}
	if after.LastError != "" || after.LastErrorCode != "" || after.AttemptCount != 0 || after.DeadLetteredAt != nil {
		t.Fatalf("resume must clear failure bookkeeping: %+v", after)
	}
}

// TestBlockedTaskRecoversWhenPendingDeliverySucceeds 验证死信后落库的兜底消息
// 一旦补投成功，任务会推进并清掉 blocked/错误标记，面板不会永久停在失败态。
func TestBlockedTaskRecoversWhenPendingDeliverySucceeds(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	svc := NewService(store, clock)
	delay := int64(60)
	rec, err := svc.Create("session-1", CreateRequest{Prompt: "ping", Title: "Ping", AfterSeconds: &delay}, OriginModel)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	deliverer := &flakyDeliverer{failFirst: 2}
	rt := NewRuntime(RuntimeOptions{
		Store: store, Service: svc, Clock: clock, Deliverer: deliverer,
		MaxAttempts: 2, FallbackToMailbox: true,
	})
	defer rt.Stop()

	now = now.Add(2 * time.Minute)
	rt.RequestDrive()
	waitFor(t, 5*time.Second, func() bool {
		task, ok, getErr := store.Get(rec.ID)
		return getErr == nil && ok && task.AttemptCount == 1
	}, "first failure")
	now = now.Add(31 * time.Second)
	rt.RequestDrive()
	waitFor(t, 5*time.Second, func() bool {
		task, ok, getErr := store.Get(rec.ID)
		return getErr == nil && ok && task.Status == StatusBlocked
	}, "blocked after dead letter")

	consumed, err := rt.DrainPendingMessages(context.Background(), "session-1", 10)
	if err != nil {
		t.Fatalf("DrainPendingMessages: %v", err)
	}
	if consumed == 0 {
		t.Fatal("expected at least one pending message to be consumed")
	}
	task, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get after drain: ok=%v err=%v", ok, err)
	}
	if task.Status == StatusBlocked || task.LastErrorCode != "" || task.LastError != "" {
		t.Fatalf("successful drain must clear blocked failure state, got %+v", task)
	}
}

// TestSuccessfulRetryClearsFailureMarker 确认失败后重试成功时不会残留红字。
func TestSuccessfulRetryClearsFailureMarker(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	svc := NewService(store, clock)
	delay := int64(60)
	rec, err := svc.Create("session-1", CreateRequest{Prompt: "ping", Title: "Ping", AfterSeconds: &delay}, OriginModel)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	deliverer := &flakyDeliverer{failFirst: 1}
	rt := NewRuntime(RuntimeOptions{
		Store: store, Service: svc, Clock: clock, Deliverer: deliverer,
		MaxAttempts: 3, FallbackToMailbox: false,
	})
	defer rt.Stop()

	now = now.Add(2 * time.Minute)
	rt.RequestDrive()
	waitFor(t, 5*time.Second, func() bool {
		task, ok, getErr := store.Get(rec.ID)
		return getErr == nil && ok && task.LastErrorCode == string(CodeDeliveryFailed)
	}, "first failure")
	now = now.Add(31 * time.Second)
	rt.RequestDrive()
	waitFor(t, 5*time.Second, func() bool {
		task, ok, getErr := store.Get(rec.ID)
		return getErr == nil && ok && task.Status == StatusInactive
	}, "successful retry commits")

	task, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get after success: ok=%v err=%v", ok, err)
	}
	if task.LastError != "" || task.LastErrorCode != "" {
		t.Fatalf("success must clear failure marker, got code=%q error=%q", task.LastErrorCode, task.LastError)
	}
	if task.LastDelivery == nil {
		t.Fatal("successful retry must record a delivery receipt")
	}
}
