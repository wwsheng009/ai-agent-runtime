package schedule

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// countingDeliverer 记录调用；实现 legacy Deliverer（无 occurrence 键）。
type countingDeliverer struct {
	calls       atomic.Int64
	err         error
	lastSession string
	lastText    string
}

func (d *countingDeliverer) Deliver(_ context.Context, sessionID, text string) error {
	d.calls.Add(1)
	d.lastSession = sessionID
	d.lastText = text
	return d.err
}

func TestOccurrenceKeyRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 14, 13, 43, 42, 0, time.UTC)
	key := OccurrenceKey("schedule-abc", at)
	if want := "schedule-abc@2026-10-14T13:43:42Z"; key != want {
		t.Fatalf("key = %q, want %q", key, want)
	}
	if got := ScheduleIDFromOccurrenceKey(key); got != "schedule-abc" {
		t.Fatalf("ScheduleIDFromOccurrenceKey = %q, want schedule-abc", got)
	}
	if got := ScheduleIDFromOccurrenceKey("no-separator"); got != "no-separator" {
		t.Fatalf("no-separator round trip = %q", got)
	}
}

func TestDeliveryLedgerRoundTrip(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	k1 := OccurrenceKey("s1", now)
	k2 := OccurrenceKey("s2", now.Add(time.Minute))

	if delivered, err := store.AllOccurrencesDelivered(nil); err != nil || delivered {
		t.Fatalf("empty keys = (%v, %v), want (false, nil)", delivered, err)
	}
	if delivered, _ := store.AllOccurrencesDelivered([]string{k1, k2}); delivered {
		t.Fatal("fresh keys must not read as delivered")
	}
	if err := store.MarkOccurrencesDelivered([]string{k1}, now); err != nil {
		t.Fatalf("mark k1: %v", err)
	}
	if delivered, _ := store.AllOccurrencesDelivered([]string{k1}); !delivered {
		t.Fatal("k1 must read as delivered after mark")
	}
	if delivered, _ := store.AllOccurrencesDelivered([]string{k1, k2}); delivered {
		t.Fatal("partial ledger must not read as all delivered")
	}
	// 幂等：重复落账不报错、不改变结论。
	if err := store.MarkOccurrencesDelivered([]string{k1, k2}, now.Add(time.Second)); err != nil {
		t.Fatalf("re-mark: %v", err)
	}
	if delivered, _ := store.AllOccurrencesDelivered([]string{k1, k2}); !delivered {
		t.Fatal("all keys must read as delivered after full mark")
	}
}

func TestGuardedDelivererSkipsDeliveredOccurrences(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	keys := []string{OccurrenceKey("s1", time.Now().UTC())}
	if err := store.MarkOccurrencesDelivered(keys, time.Now()); err != nil {
		t.Fatalf("mark: %v", err)
	}
	inner := &countingDeliverer{}
	guarded := GuardedDeliverer{Inner: inner, Ledger: store}
	if err := guarded.DeliverEnvelope(context.Background(), DeliveryEnvelope{
		SessionID: "sess", Text: "text", Occurrences: keys,
	}); err != nil {
		t.Fatalf("DeliverEnvelope: %v", err)
	}
	if calls := inner.calls.Load(); calls != 0 {
		t.Fatalf("inner calls = %d, want 0 (ledger hit must skip delivery)", calls)
	}
}

func TestGuardedDelivererMarksOnlyAfterSubmit(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	keys := []string{OccurrenceKey("s1", now)}
	inner := &countingDeliverer{}
	guarded := GuardedDeliverer{Inner: inner, Ledger: store}
	if err := guarded.DeliverEnvelope(context.Background(), DeliveryEnvelope{
		SessionID: "sess", Text: "text", Occurrences: keys,
	}); err != nil {
		t.Fatalf("DeliverEnvelope: %v", err)
	}
	if calls := inner.calls.Load(); calls != 1 {
		t.Fatalf("inner calls = %d, want 1", calls)
	}
	if delivered, _ := store.AllOccurrencesDelivered(keys); !delivered {
		t.Fatal("successful submit must be marked delivered")
	}

	// 提交失败 → 不得落账（下次重试仍会投递，绝不丢消息）。
	failedKeys := []string{OccurrenceKey("s2", now)}
	failing := &countingDeliverer{err: errors.New("boom")}
	guarded2 := GuardedDeliverer{Inner: failing, Ledger: store}
	if err := guarded2.DeliverEnvelope(context.Background(), DeliveryEnvelope{
		SessionID: "sess", Text: "text", Occurrences: failedKeys,
	}); err == nil {
		t.Fatal("submit failure must surface as error")
	}
	if delivered, _ := store.AllOccurrencesDelivered(failedKeys); delivered {
		t.Fatal("failed submit must not be marked delivered")
	}
}

// TestServiceDriverWakerFiresOnMutations 钉住 S2b 实测发现的修复：控制面
// 创建/删除任务后必须唤醒调度驱动（否则空闲期新任务要等下一次 drive 才可见）。
func TestServiceDriverWakerFiresOnMutations(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()
	service := NewService(store, SystemClock)

	var wakes atomic.Int64
	service.SetDriverWaker(func() { wakes.Add(1) })

	after := int64(60)
	rec, err := service.Create("session_waker", CreateRequest{
		Title: "waker", Prompt: "ping", AfterSeconds: &after,
	}, OriginManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if wakes.Load() == 0 {
		t.Fatal("create must wake the driver")
	}

	beforeDelete := wakes.Load()
	if _, err := service.Delete("session_waker", rec.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if wakes.Load() <= beforeDelete {
		t.Fatal("delete must wake the driver")
	}

	service.SetDriverWaker(nil)
	beforeCleared := wakes.Load()
	after2 := int64(60)
	if _, err := service.Create("session_waker2", CreateRequest{
		Title: "waker2", Prompt: "ping", AfterSeconds: &after2,
	}, OriginManual); err != nil {
		t.Fatalf("Create after clearing: %v", err)
	}
	if wakes.Load() != beforeCleared {
		t.Fatal("cleared waker must not fire")
	}
}
