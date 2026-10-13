package schedule

import (
	"testing"
	"time"
)

func TestMetricsSnapshotReflectsCounters(t *testing.T) {
	var m metrics
	m.deliveredTotal.Add(3)
	m.deliveryFailedTotal.Add(1)
	m.embeddedTotal.Add(1)
	m.embeddedSkippedTotal.Add(2)
	m.retriesScheduled.Add(4)
	m.deadLetterTotal.Add(1)

	snapshot := m.Snapshot()
	if snapshot.DeliveredTotal != 3 ||
		snapshot.DeliveryFailedTotal != 1 ||
		snapshot.EmbeddedTotal != 1 ||
		snapshot.EmbeddedSkippedTotal != 2 ||
		snapshot.RetriesScheduled != 4 ||
		snapshot.DeadLetterTotal != 1 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}

func TestRuntimeMetricsReflectDrive(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	svc := NewService(store, clock)

	delay := int64(60)
	if _, err := svc.Create("session-1", CreateRequest{Prompt: "x", Title: "t", AfterSeconds: &delay}, OriginModel); err != nil {
		t.Fatalf("Create: %v", err)
	}

	deliverer := &flakyDeliverer{failFirst: 1}
	arbiter := newTestArbiter()
	rt := NewRuntime(RuntimeOptions{
		Store: store, Service: svc, Clock: clock, Deliverer: deliverer,
		MaxAttempts: 5, FallbackToMailbox: true,
		Executor: &embeddedExecutor{}, Arbiter: arbiter, Mode: ModeEmbedded,
	})
	defer rt.Stop()

	now = now.Add(2 * time.Minute)
	rt.RequestDrive()
	time.Sleep(80 * time.Millisecond)

	snapshot := rt.Metrics()
	// 第 1 次 delegate 失败即被内嵌接住（EmbeddedTotal +1, DeliveredTotal +1）；
	// deliveryFailedTotal 只计"未被内嵌接住的失败"，保持 0。
	if snapshot.EmbeddedTotal != 1 || snapshot.DeliveredTotal != 1 {
		t.Fatalf("success metrics = %+v", snapshot)
	}
	if snapshot.DeliveryFailedTotal != 0 || snapshot.RetriesScheduled != 0 {
		t.Fatalf("failure metrics must stay zero when embedded catches: %+v", snapshot)
	}
}
