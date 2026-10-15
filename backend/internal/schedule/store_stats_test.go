package schedule

import (
	"testing"
	"time"
)

// TestStoreStatsCounts 钉住 S4b 对账口径：任务状态计数 + pending 兜底 +
// 交付账本条目（daemon /status.stats 的数据源）。
func TestStoreStatsCounts(t *testing.T) {
	store := newClaimTestStore(t)
	t0 := time.Date(2026, 10, 15, 17, 0, 0, 0, time.UTC)
	active := putClaimTestTask(t, store, "schedule-stats-a", t0.Add(time.Hour))
	inactive := putClaimTestTask(t, store, "schedule-stats-b", t0.Add(time.Hour))
	inactive.Status = StatusInactive
	if applied, err := store.UpdateCAS(inactive, inactive.StateVersion); err != nil || !applied {
		t.Fatalf("UpdateCAS: applied=%v err=%v", applied, err)
	}
	if err := store.AppendPendingMessage(active.Record.ID, active.SessionID, "p", "failed", t0); err != nil {
		t.Fatalf("AppendPendingMessage: %v", err)
	}
	if err := store.MarkOccurrencesDelivered([]string{OccurrenceKey(active.Record.ID, t0)}, t0); err != nil {
		t.Fatalf("MarkOccurrencesDelivered: %v", err)
	}

	stats, err := store.StoreStats()
	if err != nil {
		t.Fatalf("StoreStats: %v", err)
	}
	if stats.ActiveTasks != 1 || stats.InactiveTasks != 1 || stats.PendingMessages != 1 || stats.DeliveryIntents != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}
