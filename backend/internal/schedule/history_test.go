package schedule

import (
	"testing"
	"time"
)

func TestServiceHistoryPaginationAndCursor(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	svc := NewService(store, fixedClock(now))
	every := int64(60)
	rec, err := svc.Create("session-1", CreateRequest{Prompt: "tick", Title: "Tick", EverySeconds: &every}, OriginModel)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 模拟 5 次投递（每分钟）。
	clock := now
	for i := 0; i < 5; i++ {
		clock = clock.Add(time.Duration(i+1) * time.Minute)
		task, ok, err := store.Get(rec.ID)
		if err != nil || !ok {
			t.Fatalf("Get: ok=%v err=%v", ok, err)
		}
		occurrence, err := ResolveOccurrence(task.Record, clock)
		if err != nil {
			t.Fatalf("ResolveOccurrence: %v", err)
		}
		receipt := &DeliveryReceipt{
			ScheduledAt: occurrence.OccurrenceAt,
			DeliveredAt: clock,
			MessageID:   syntheticMessageID(rec.ID, occurrence.OccurrenceAt),
		}
		next := task
		next.LastDelivery = receipt
		next.DeliveryHistory = appendHistory(task, receipt)
		if occurrence.NextScheduledAt != nil {
			next.Record.ScheduledAt = occurrence.NextScheduledAt.UTC()
		}
		if applied, err := store.UpdateCAS(next, task.StateVersion); err != nil || !applied {
			t.Fatalf("UpdateCAS applied=%v err=%v", applied, err)
		}
		if err := store.AppendDeliveryRecord(rec.ID, DeliveryRecord{
			DeliveryReceipt: *receipt,
			Prompt:          task.Record.Prompt,
		}); err != nil {
			t.Fatalf("AppendDeliveryRecord: %v", err)
		}
	}

	// 第一页（limit=2，从新到旧）。
	page1, err := svc.History(HistoryRequest{SessionID: "session-1", ID: rec.ID, Limit: 2})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(page1.Records) != 2 || page1.NextBefore == "" {
		t.Fatalf("page1 = %+v", page1)
	}
	if !page1.Records[0].DeliveredAt.After(page1.Records[1].DeliveredAt) {
		t.Fatal("records must be newest-first")
	}

	// 第二页（before=第一页最早 message id）。
	page2, err := svc.History(HistoryRequest{SessionID: "session-1", ID: rec.ID, Limit: 2, Before: page1.NextBefore})
	if err != nil {
		t.Fatalf("History page2: %v", err)
	}
	if len(page2.Records) != 2 {
		t.Fatalf("page2 = %+v", page2)
	}
	if page1.Records[1].MessageID == page2.Records[0].MessageID {
		t.Fatal("pages must not overlap (cursor is exclusive)")
	}

	// 游标未知 → delivery_cursor_not_found。
	_, err = svc.History(HistoryRequest{SessionID: "session-1", ID: rec.ID, Limit: 2, Before: "schedule:x:0"})
	if input, ok := IsInputError(err); !ok || input.Code != CodeCursorNotFound {
		t.Fatalf("unknown cursor err = %v, want delivery_cursor_not_found", err)
	}

	// limit 越界。
	if _, err := svc.History(HistoryRequest{SessionID: "session-1", ID: rec.ID, Limit: 0}); err == nil {
		t.Fatal("limit=0 must be rejected")
	}
	if _, err := svc.History(HistoryRequest{SessionID: "session-1", ID: rec.ID, Limit: 101}); err == nil {
		t.Fatal("limit=101 must be rejected")
	}

	// 绑定不符 → not found。
	if _, err := svc.History(HistoryRequest{SessionID: "session-other", ID: rec.ID, Limit: 1}); err == nil {
		t.Fatal("session mismatch must be rejected")
	}
}
