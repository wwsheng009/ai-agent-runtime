package schedule

import (
	"context"
	"sync"
	"testing"
	"time"
)

// manualClock 是可手动拨动的测试时钟（并发安全）。
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newManualClock(start time.Time) *manualClock { return &manualClock{now: start} }

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRuntimeClaimSkipsHeldTaskThenTakesOver 钉住 S4（§4.3）的核心窗口：
// 另一节点持有 claim 时不投递（计入 ClaimConflictsTotal、任务保持 active）；
// 其租约过期后本节点接管投递。claim 是冗余防线——不丢任务、不双投。
func TestRuntimeClaimSkipsHeldTaskThenTakesOver(t *testing.T) {
	store := newClaimTestStore(t)
	t0 := time.Date(2026, 10, 15, 13, 0, 0, 0, time.UTC)
	clock := newManualClock(t0)
	service := NewService(store, clock.Now)
	task := putClaimTestTask(t, store, "schedule-claim-runtime", t0.Add(-time.Minute))

	// 另一节点先抢占（租约到 t0+60s）。
	if claimed, err := store.ClaimTasks([]string{task.Record.ID}, "node-other", t0.Add(time.Minute), t0); err != nil || len(claimed) != 1 {
		t.Fatalf("pre-claim: %v, %v", claimed, err)
	}

	deliverer := &countingDeliverer{}
	runtime := NewRuntime(RuntimeOptions{
		Store: store, Service: service, Clock: clock.Now, Deliverer: deliverer,
		Mode: ModeDelegate, Logf: func(string, ...any) {},
	})
	runtime.Start(context.Background())
	defer runtime.Stop()

	waitFor(t, 5*time.Second, func() bool { return runtime.Metrics().ClaimConflictsTotal >= 1 }, "claim conflict metric")
	if calls := deliverer.calls.Load(); calls != 0 {
		t.Fatalf("deliverer calls = %d, want 0 while claim is held by another node", calls)
	}
	if got, _, _ := store.Get(task.Record.ID); got.Status != StatusActive {
		t.Fatalf("status = %q, want active", got.Status)
	}

	// 租约过期后本节点接管并投递。
	clock.Set(t0.Add(2 * time.Minute))
	runtime.RequestDrive()
	waitFor(t, 5*time.Second, func() bool {
		got, ok, err := store.Get(task.Record.ID)
		return err == nil && ok && got.Status == StatusInactive
	}, "task advanced after takeover")
	if calls := deliverer.calls.Load(); calls != 1 {
		t.Fatalf("deliverer calls = %d, want 1 after takeover", calls)
	}
}

// TestRuntimeHydratesPersistedAttempts 钉住 S4（§4.3 ③）：leader 易主后从
// 持久层恢复退避——attempt=1（30s 退避）未到期时不重试；到期后投递成功并
// 清空簿记。
func TestRuntimeHydratesPersistedAttempts(t *testing.T) {
	store := newClaimTestStore(t)
	t0 := time.Date(2026, 10, 15, 14, 0, 0, 0, time.UTC)
	clock := newManualClock(t0)
	service := NewService(store, clock.Now)
	task := putClaimTestTask(t, store, "schedule-hydrate", t0.Add(-time.Minute))

	// 上一任 leader 失败过一次（last=t0-5s → 退避至 t0+25s），随后退出。
	if _, err := store.RecordTaskAttempt(task.Record.ID, t0.Add(-5*time.Second), false); err != nil {
		t.Fatalf("RecordTaskAttempt: %v", err)
	}

	deliverer := &countingDeliverer{}
	runtime := NewRuntime(RuntimeOptions{
		Store: store, Service: service, Clock: clock.Now, Deliverer: deliverer,
		Mode: ModeDelegate, Logf: func(string, ...any) {},
	})
	runtime.Start(context.Background())
	defer runtime.Stop()
	time.Sleep(200 * time.Millisecond) // 给首次 drive 一个完成窗口
	if calls := deliverer.calls.Load(); calls != 0 {
		t.Fatalf("deliverer calls = %d, want 0 during hydrated backoff (until t0+25s)", calls)
	}

	// 退避窗口过后接管投递。
	clock.Set(t0.Add(31 * time.Second))
	runtime.RequestDrive()
	waitFor(t, 5*time.Second, func() bool {
		got, ok, err := store.Get(task.Record.ID)
		return err == nil && ok && got.Status == StatusInactive
	}, "delivery after hydrated backoff")
	if calls := deliverer.calls.Load(); calls != 1 {
		t.Fatalf("deliverer calls = %d, want 1", calls)
	}
	got, _, _ := store.Get(task.Record.ID)
	if got.AttemptCount != 0 || got.LastAttemptAt != nil || got.DeadLetteredAt != nil {
		t.Fatalf("attempts not cleared after success: %+v", got)
	}
}
