package schedule

import (
	"testing"
	"time"
)

func newClaimTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func putClaimTestTask(t *testing.T, store *SQLiteStore, id string, scheduledAt time.Time) Task {
	t.Helper()
	task := Task{
		SessionID: "session-claim",
		Record: Record{
			ID: id, Kind: KindAfter, Title: "claim", Prompt: "p",
			ScheduledAt: scheduledAt, AfterSeconds: 60,
		},
		Status:     StatusActive,
		TargetKind: TargetSession,
	}
	if err := store.Put(task); err != nil {
		t.Fatalf("Put(%s): %v", id, err)
	}
	got, ok, err := store.Get(id)
	if err != nil || !ok {
		t.Fatalf("Get(%s): ok=%v err=%v", id, ok, err)
	}
	return got
}

// TestClaimTasksCASLifecycle 钉住 §4.3 CAS 语义：首次抢占成功；租约存活期间
// 他人被拒；本人可重抢（刷新租约）；过期后他人可接管；释放后可再抢；claim
// 簿记不推进 state_version。
func TestClaimTasksCASLifecycle(t *testing.T) {
	store := newClaimTestStore(t)
	t0 := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	task := putClaimTestTask(t, store, "schedule-claim-a", t0.Add(-time.Minute))
	id := task.Record.ID

	claimed, err := store.ClaimTasks([]string{id}, "node-a", t0.Add(time.Minute), t0)
	if err != nil || len(claimed) != 1 || claimed[0] != id {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	if claimed, _ := store.ClaimTasks([]string{id}, "node-b", t0.Add(2*time.Minute), t0.Add(time.Second)); len(claimed) != 0 {
		t.Fatalf("foreign claim while lease alive = %v, want none", claimed)
	}
	if claimed, _ := store.ClaimTasks([]string{id}, "node-a", t0.Add(2*time.Minute), t0.Add(2*time.Second)); len(claimed) != 1 {
		t.Fatalf("owner re-claim = %v, want refresh", claimed)
	}
	// node-a 的租约到 t0+2m；t0+2m1s 时过期 → node-b 接管。
	if claimed, _ := store.ClaimTasks([]string{id}, "node-b", t0.Add(5*time.Minute), t0.Add(2*time.Minute+time.Second)); len(claimed) != 1 {
		t.Fatalf("expired-lease takeover = %v, want claimed", claimed)
	}
	if err := store.ReleaseTaskClaims([]string{id}, "node-b"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if claimed, _ := store.ClaimTasks([]string{id}, "node-c", t0.Add(time.Hour), t0.Add(3*time.Minute)); len(claimed) != 1 {
		t.Fatalf("after release = %v, want claimed", claimed)
	}
	after, _, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.StateVersion != task.StateVersion {
		t.Fatalf("state_version = %d after claims, want %d", after.StateVersion, task.StateVersion)
	}
}

// TestClaimTasksPartialAndNullLease 钉住部分成功与"owner 有值但租约为空"的
// 边界（旧行/异常写入不得永久锁死任务）。
func TestClaimTasksPartialAndNullLease(t *testing.T) {
	store := newClaimTestStore(t)
	t0 := time.Date(2026, 10, 15, 11, 0, 0, 0, time.UTC)
	taskA := putClaimTestTask(t, store, "schedule-claim-p1", t0.Add(-time.Minute))
	taskB := putClaimTestTask(t, store, "schedule-claim-p2", t0.Add(-time.Minute))
	if claimed, err := store.ClaimTasks([]string{taskA.Record.ID}, "node-b", t0.Add(10*time.Minute), t0); err != nil || len(claimed) != 1 {
		t.Fatalf("pre-claim: %v, %v", claimed, err)
	}
	claimed, err := store.ClaimTasks([]string{taskA.Record.ID, taskB.Record.ID}, "node-c", t0.Add(time.Minute), t0.Add(time.Second))
	if err != nil {
		t.Fatalf("partial claim: %v", err)
	}
	if len(claimed) != 1 || claimed[0] != taskB.Record.ID {
		t.Fatalf("partial claim = %v, want [%s]", claimed, taskB.Record.ID)
	}
	if err := store.ExecRaw(`UPDATE schedule_tasks SET owner_instance_id = 'stale-owner', lease_expires_at = NULL WHERE id = ?`, taskB.Record.ID); err != nil {
		t.Fatalf("ExecRaw: %v", err)
	}
	if claimed, _ := store.ClaimTasks([]string{taskB.Record.ID}, "node-d", t0.Add(2*time.Minute), t0.Add(2*time.Second)); len(claimed) != 1 {
		t.Fatalf("null-lease stale owner must be claimable, got %v", claimed)
	}
}

// TestRecordTaskAttemptPersistsAndClears 钉住 §4.3 ③：attempts/死信持久化
// 往返（含 ListActiveDue 扫描），簿记写不推进 state_version，清空归零。
func TestRecordTaskAttemptPersistsAndClears(t *testing.T) {
	store := newClaimTestStore(t)
	t0 := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	task := putClaimTestTask(t, store, "schedule-attempt-a", t0.Add(-time.Minute))

	if count, err := store.RecordTaskAttempt(task.Record.ID, t0, false); err != nil || count != 1 {
		t.Fatalf("attempt 1 = %d, %v", count, err)
	}
	if count, err := store.RecordTaskAttempt(task.Record.ID, t0.Add(30*time.Second), true); err != nil || count != 2 {
		t.Fatalf("attempt 2 = %d, %v", count, err)
	}
	// 再次标记死信：保留首次留痕时间。
	if count, err := store.RecordTaskAttempt(task.Record.ID, t0.Add(time.Minute), true); err != nil || count != 3 {
		t.Fatalf("attempt 3 = %d, %v", count, err)
	}

	got, ok, err := store.Get(task.Record.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.AttemptCount != 3 {
		t.Fatalf("attempt_count = %d, want 3", got.AttemptCount)
	}
	if got.LastAttemptAt == nil || !got.LastAttemptAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("last_attempt_at = %v, want %v", got.LastAttemptAt, t0.Add(time.Minute))
	}
	if got.DeadLetteredAt == nil || !got.DeadLetteredAt.Equal(t0.Add(30*time.Second)) {
		t.Fatalf("dead_lettered_at = %v, want first stamp %v", got.DeadLetteredAt, t0.Add(30*time.Second))
	}
	if got.StateVersion != task.StateVersion {
		t.Fatalf("state_version = %d after attempts, want %d", got.StateVersion, task.StateVersion)
	}

	// 到期扫描同样携带簿记字段（leader 接管路径依赖它做退避延续）。
	due, err := store.ListActiveDue(t0.Add(2 * time.Minute))
	if err != nil || len(due) != 1 {
		t.Fatalf("ListActiveDue = %d tasks, %v", len(due), err)
	}
	if due[0].AttemptCount != 3 || due[0].LastAttemptAt == nil {
		t.Fatalf("due task missing attempt bookkeeping: %+v", due[0])
	}

	if err := store.ClearTaskAttempts(task.Record.ID); err != nil {
		t.Fatalf("ClearTaskAttempts: %v", err)
	}
	cleared, _, err := store.Get(task.Record.ID)
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if cleared.AttemptCount != 0 || cleared.LastAttemptAt != nil || cleared.DeadLetteredAt != nil {
		t.Fatalf("attempts not cleared: %+v", cleared)
	}
}
