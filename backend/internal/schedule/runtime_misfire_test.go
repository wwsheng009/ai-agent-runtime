package schedule

import (
	"context"
	"testing"
	"time"
)

// TestRuntimeMisfireGraceSkipsExpiredOneShot 钉住 S4b（§4.4）：一次性任务
// 逾期超过宽限窗口 → 不投递、转 inactive、计入 MisfireSkippedTotal 并清簿记。
func TestRuntimeMisfireGraceSkipsExpiredOneShot(t *testing.T) {
	store := newClaimTestStore(t)
	t0 := time.Date(2026, 10, 15, 15, 0, 0, 0, time.UTC)
	clock := newManualClock(t0)
	service := NewService(store, clock.Now)
	task := putClaimTestTask(t, store, "schedule-misfire-a", t0.Add(-10*time.Minute))
	// 上一任失败簿记，应随 misfire 清理（任务转终态）。
	if _, err := store.RecordTaskAttempt(task.Record.ID, t0.Add(-9*time.Minute), false); err != nil {
		t.Fatalf("RecordTaskAttempt: %v", err)
	}

	deliverer := &countingDeliverer{}
	runtime := NewRuntime(RuntimeOptions{
		Store: store, Service: service, Clock: clock.Now, Deliverer: deliverer,
		Mode: ModeDelegate, MisfireGrace: 2 * time.Minute, Logf: func(string, ...any) {},
	})
	runtime.Start(context.Background())
	defer runtime.Stop()
	waitFor(t, 5*time.Second, func() bool {
		got, ok, err := store.Get(task.Record.ID)
		return err == nil && ok && got.Status == StatusInactive
	}, "misfired task to turn inactive")
	if calls := deliverer.calls.Load(); calls != 0 {
		t.Fatalf("deliverer calls = %d, want 0 for misfired task", calls)
	}
	if got := runtime.Metrics().MisfireSkippedTotal; got != 1 {
		t.Fatalf("MisfireSkippedTotal = %d, want 1", got)
	}
	got, _, _ := store.Get(task.Record.ID)
	if got.AttemptCount != 0 || got.LastAttemptAt != nil {
		t.Fatalf("bookkeeping not cleared after misfire skip: %+v", got)
	}
}

// TestRuntimeMisfireGraceDeliversWithinWindow：窗口内照常补发；未配置（0）
// 时兼容既有行为（总是补发）。
func TestRuntimeMisfireGraceDeliversWithinWindow(t *testing.T) {
	cases := []struct {
		name  string
		grace time.Duration
		delay time.Duration
	}{
		{"within window", 2 * time.Minute, time.Minute},
		{"grace disabled falls back to always-deliver", 0, 10 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newClaimTestStore(t)
			t0 := time.Date(2026, 10, 15, 15, 30, 0, 0, time.UTC)
			clock := newManualClock(t0)
			service := NewService(store, clock.Now)
			task := putClaimTestTask(t, store, "schedule-misfire-ok", t0.Add(-tc.delay))

			deliverer := &countingDeliverer{}
			runtime := NewRuntime(RuntimeOptions{
				Store: store, Service: service, Clock: clock.Now, Deliverer: deliverer,
				Mode: ModeDelegate, MisfireGrace: tc.grace, Logf: func(string, ...any) {},
			})
			runtime.Start(context.Background())
			defer runtime.Stop()
			waitFor(t, 5*time.Second, func() bool {
				got, ok, err := store.Get(task.Record.ID)
				return err == nil && ok && got.Status == StatusInactive
			}, "delivery")
			if calls := deliverer.calls.Load(); calls != 1 {
				t.Fatalf("deliverer calls = %d, want 1", calls)
			}
			if got := runtime.Metrics().MisfireSkippedTotal; got != 0 {
				t.Fatalf("MisfireSkippedTotal = %d, want 0", got)
			}
		})
	}
}

// TestRuntimeRecurringOverdueDeliversLatestOccurrenceOnly 钉住 S4/§4.4 既定口径：
// 周期任务停机一天后只补发最近一次（不追补历史周期），并推进到未来目标。
func TestRuntimeRecurringOverdueDeliversLatestOccurrenceOnly(t *testing.T) {
	store := newClaimTestStore(t)
	t0 := time.Date(2026, 10, 15, 16, 0, 0, 0, time.UTC)
	clock := newManualClock(t0)
	service := NewService(store, clock.Now)
	task := Task{
		SessionID: "session-misfire-every",
		Record: Record{
			ID: "schedule-every-overdue", Kind: KindEvery, Title: "every", Prompt: "p",
			ScheduledAt: t0.Add(-24 * time.Hour), EverySeconds: 3600,
		},
		Status:     StatusActive,
		TargetKind: TargetSession,
	}
	if err := store.Put(task); err != nil {
		t.Fatalf("Put: %v", err)
	}
	deliverer := &countingDeliverer{}
	runtime := NewRuntime(RuntimeOptions{
		Store: store, Service: service, Clock: clock.Now, Deliverer: deliverer,
		Mode: ModeDelegate, MisfireGrace: time.Minute, Logf: func(string, ...any) {},
	})
	runtime.Start(context.Background())
	defer runtime.Stop()
	waitFor(t, 5*time.Second, func() bool { return deliverer.calls.Load() == 1 }, "single catch-up delivery")
	waitFor(t, 5*time.Second, func() bool {
		got, ok, err := store.Get(task.Record.ID)
		return err == nil && ok && got.Status == StatusActive && got.Record.ScheduledAt.After(t0)
	}, "task advanced to a future target")
	if calls := deliverer.calls.Load(); calls != 1 {
		t.Fatalf("deliverer calls = %d, want exactly 1 (no historical backfill)", calls)
	}
	if got := runtime.Metrics().MisfireSkippedTotal; got != 0 {
		t.Fatalf("MisfireSkippedTotal = %d, want 0 for recurring kinds", got)
	}
}
