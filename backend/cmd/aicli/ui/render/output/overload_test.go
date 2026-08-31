package output

import (
	"context"
	"testing"
	"time"
)

// TestSustainedOverloadBoundedMirrorAndPrimaryUnaffected：
// 17.5/11.6——持续 overload 下（慢 mirror 阻塞、queue 打满、journal 淘汰
// 持续发生）：
//   - physical primary 不被慢 mirror 阻塞（每笔 Submit 都返回有界延迟）；
//   - mirror 队列有界：超出容量的 entry 被 drop 而非无界堆积；
//   - snapshot 统计守恒：MirrorsScheduled+Drops 恒等于登记 entry 数
//     （无泄漏）；EntriesUnsealed 在排空后归零；
//   - journal/capture 有界（淘汰生效且不进 new batch 路径）。
func TestSustainedOverloadBoundedMirrorAndPrimaryUnaffected(t *testing.T) {
	blockSink := &queueBlockingMirrorSink{release: make(chan struct{})}
	opts := gatewayOptions()
	opts.MirrorQueueCapacity = 4
	opts.Clock = SystemClock{}
	primaryDesc := TargetDescriptor{
		SinkID:             "ovl-primary",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	}
	gw, err := NewRenderOutputGateway("ovl-"+randomID("s"), opts, RenderRouteConfig{
		Primary:            NewMemorySink(primaryDesc),
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: primaryDesc.ProjectionTargetID,
		Mirrors: []RenderMirror{{
			Sink:      blockSink,
			Policy:    MirrorBestEffort,
			ApplyMode: MirrorApplyBytes,
			Ownership: SinkBorrowed,
			Timeout:   10 * time.Second,
		}},
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	gw.Run()
	releaseOnce := func() {
		select {
		case <-blockSink.release:
		default:
			close(blockSink.release)
		}
	}
	t.Cleanup(func() {
		releaseOnce()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
	})

	const n = 60
	// 第一笔把唯一 in-flight 槽占住；随后尽快连续提交 n 笔（queue 容量 4，
	// 其余全部 drop），每笔都必须有界返回（primary 不被慢 mirror 阻塞）。
	first := gw.Submit(context.Background(), RenderIntent{
		IntentID: "ovl-0",
		Kind:     TransactionFrame,
		Bytes:    []byte("x"),
	})
	if first.Admission.Decision != AdmissionAccepted {
		t.Fatalf("first admission: %+v", first.Admission)
	}
	// 等 callback 已 dispatch（in-flight 占住，queue 空）。
	deadline := time.Now().Add(2 * time.Second)
	for blockSink.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if blockSink.calls.Load() == 0 {
		t.Fatal("first mirror callback never dispatched")
	}

	start := time.Now()
	for i := 1; i < n; i++ {
		r := gw.Submit(context.Background(), RenderIntent{
			IntentID: "ovl-" + string(rune('a'+i%26)) + string(rune('0'+i%10)),
			Kind:     TransactionFrame,
			Bytes:    []byte("payload"),
		})
		if r.Admission.Decision != AdmissionAccepted {
			t.Fatalf("submit %d admission: %+v", i, r.Admission)
		}
	}
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("primary submits blocked by slow mirror: %v for %d batches", elapsed, n)
	}

	// 放行并排空——EntriesUnsealed（pending+in-flight）必须归零。
	releaseOnce()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := gw.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	snap := gw.Snapshot()
	if len(snap.Mirrors) != 1 {
		t.Fatalf("mirror snapshot: %d", len(snap.Mirrors))
	}
	ms := snap.Mirrors[0]
	if ms.EntriesUnsealed != 0 {
		t.Fatalf("entries unsealed after drain: %d (pending=%d in-flight=%d)",
			ms.EntriesUnsealed, ms.Pending, ms.InFlight)
	}
	// 守恒：Scheduled + ScheduleDrops 必须等于 n（每笔登记恰好一种终态）。
	registered := ms.Scheduled + ms.ScheduleDrops
	if registered != uint64(n) {
		t.Fatalf("mirror accounting mismatch: scheduled=%d drops=%d total=%d want %d",
			ms.Scheduled, ms.ScheduleDrops, registered, n)
	}
	if ms.ScheduleDrops < uint64(n-4-1) {
		t.Fatalf("expected heavy queue-full drops, got %d", ms.ScheduleDrops)
	}
	if snap.AdmissionAccepted != n {
		t.Fatalf("admission accepted: %d want %d", snap.AdmissionAccepted, n)
	}
	if snap.PrimaryCommitted != n {
		t.Fatalf("primary committed: %d want %d (slow mirror must not degrade primary)",
			snap.PrimaryCommitted, n)
	}
}

// TestSustainedOverloadJournalBounded：持续提交下 delivery journal 保持有界
// （淘汰生效），EventsUnsealed 排空归零。
func TestSustainedOverloadJournalBounded(t *testing.T) {
	opts := gatewayOptions()
	opts.DeliveryJournalLimit = JournalLimit{MaxItems: 8, MaxBytes: 1 << 20}
	gw := mustGatewayWithOptions(t, opts)
	ctx := context.Background()
	const n = 30
	for i := 0; i < n; i++ {
		r := gw.Submit(ctx, RenderIntent{
			IntentID: "j-" + string(rune('a'+i%26)),
			Kind:     TransactionFrame,
			Bytes:    []byte("j"),
		})
		if r.Admission.Decision != AdmissionAccepted {
			t.Fatalf("submit %d: %+v", i, r.Admission)
		}
	}
	drainCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := gw.Drain(drainCtx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	recs := gw.RecentDeliveries(64)
	if len(recs) > 8 {
		t.Fatalf("journal not bounded: %d records retained (limit 8)", len(recs))
	}
	if len(recs) == 0 {
		t.Fatal("journal empty")
	}
	snap := gw.Snapshot()
	if snap.DeliveryRecordsSealed != n {
		t.Fatalf("sealed records: %d want %d", snap.DeliveryRecordsSealed, n)
	}
}
