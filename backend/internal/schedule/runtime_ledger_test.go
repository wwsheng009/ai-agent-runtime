package schedule

import (
	"context"
	"testing"
	"time"
)

// TestRuntimeSkipsLedgerHitAndCommits 钉住 S2（§12.1）的核心窗口：
// 上一任 leader"已投递、未 commit"（账本命中）时，新 leader **不再重复投递**，
// 直接推进任务（一次性任务转 inactive），并把命中计入 DuplicateSkippedTotal。
func TestRuntimeSkipsLedgerHitAndCommits(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()
	service := NewService(store, SystemClock)

	task := Task{
		SessionID: "session_ledger_test",
		Record: Record{
			ID:           "schedule-ledger-test",
			Kind:         KindAfter,
			Title:        "ledger",
			Prompt:       "ping",
			ScheduledAt:  time.Now().UTC().Add(-time.Minute),
			AfterSeconds: 300,
		},
		Status:     StatusActive,
		TargetKind: TargetSession,
	}
	if err := store.Put(task); err != nil {
		t.Fatalf("Put: %v", err)
	}
	keys := []string{OccurrenceKey(task.Record.ID, task.Record.ScheduledAt)}
	if err := store.MarkOccurrencesDelivered(keys, time.Now()); err != nil {
		t.Fatalf("Mark: %v", err)
	}

	deliverer := &countingDeliverer{}
	runtime := NewRuntime(RuntimeOptions{
		Store:     store,
		Service:   service,
		Deliverer: deliverer,
		Mode:      ModeDelegate,
		Logf:      func(string, ...any) {},
	})
	runtime.Start(context.Background())
	defer runtime.Stop()

	deadline := time.Now().Add(5 * time.Second)
	for {
		got, ok, err := store.Get(task.Record.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if ok && got.Status == StatusInactive {
			break
		}
		if time.Now().After(deadline) {
			status := Status("")
			if ok {
				status = got.Status
			}
			t.Fatalf("task not advanced by ledger hit; status=%q", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if calls := deliverer.calls.Load(); calls != 0 {
		t.Fatalf("deliverer calls = %d, want 0 (ledger hit must skip delivery)", calls)
	}
	if got := runtime.Metrics().DuplicateSkippedTotal; got != 1 {
		t.Fatalf("DuplicateSkippedTotal = %d, want 1", got)
	}
}
