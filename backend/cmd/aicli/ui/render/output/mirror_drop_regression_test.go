package output

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestMirrorQueueFullDropIsFailedNotSkipped：queue-full drop 的 record 条目
// 必须是 MirrorFailed+DeliveryErrorQueueFull（6.5：Scheduled=false 且
// ErrorClass 非 None 时是失败不是跳过）。
func TestMirrorQueueFullDropIsFailedNotSkipped(t *testing.T) {
	blockSink := &queueBlockingMirrorSink{release: make(chan struct{})}
	opts := gatewayOptions()
	opts.MirrorQueueCapacity = 1
	opts.Clock = SystemClock{}
	gw, err := NewRenderOutputGateway("qfd-"+randomID("s"), opts, RenderRouteConfig{
		Primary:            NewDiscardSink("pt-primary"),
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: "pt-primary",
		Mirrors: []RenderMirror{{
			Sink:      blockSink,
			Policy:    MirrorBestEffort,
			ApplyMode: MirrorApplyBytes,
			Ownership: SinkBorrowed,
			Timeout:   5 * time.Second,
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
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
	})

	// 第一笔占住唯一队列槽（callback 阻塞）。
	r1 := gw.Submit(context.Background(), RenderIntent{
		IntentID: "q1",
		Kind:     TransactionFrame,
		Bytes:    []byte("one"),
	})
	if r1.Admission.Decision != AdmissionAccepted {
		t.Fatalf("r1 admission: %+v", r1.Admission)
	}
	// 等 callback dispatch 后（queue 空但 in-flight），第二笔可入队（占用槽）；
	// 第三笔即 queue-full drop。
	deadline := time.Now().Add(2 * time.Second)
	for blockSink.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	r2 := gw.Submit(context.Background(), RenderIntent{
		IntentID: "q2",
		Kind:     TransactionFrame,
		Bytes:    []byte("two"),
	})
	r3 := gw.Submit(context.Background(), RenderIntent{
		IntentID: "q3",
		Kind:     TransactionFrame,
		Bytes:    []byte("three"),
	})
	if r2.Admission.Decision != AdmissionAccepted || r3.Admission.Decision != AdmissionAccepted {
		t.Fatalf("admissions: r2=%+v r3=%+v", r2.Admission, r3.Admission)
	}
	// 放行并排空。
	releaseOnce()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := gw.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	// r3 的 record：drop 条目必须 MirrorFailed + QueueFull（非 Skipped）。
	records := gw.RecentDeliveries(10)
	for _, rec := range records {
		if rec.Batch.BatchID != r3.BatchID || len(rec.Mirrors) != 1 {
			continue
		}
		m := rec.Mirrors[0]
		if m.Status != MirrorFailed {
			t.Fatalf("queue-full drop must be MirrorFailed, got %s: %+v", m.Status, m)
		}
		if m.ErrorClass != DeliveryErrorQueueFull {
			t.Fatalf("queue-full drop error class: %s", m.ErrorClass)
		}
		if m.Scheduled {
			t.Fatal("queue-full drop must have Scheduled=false")
		}
		if m.SkipReason != "" {
			t.Fatalf("drop must not carry skip reason: %q", m.SkipReason)
		}
		return
	}
	t.Fatal("r3 record not found")
}

// queueBlockingMirrorSink 阻塞 SubmitMirror 直到 release（便于 queue-full
// 测试控制时序）。
type queueBlockingMirrorSink struct {
	release chan struct{}
	calls   atomic.Int64
}

func (b *queueBlockingMirrorSink) Descriptor() TargetDescriptor {
	return TargetDescriptor{SinkID: "queue-block-mirror", Class: TargetClassVirtual, ProjectionTargetID: "pt-queue-block"}
}

func (b *queueBlockingMirrorSink) SubmitMirror(_ context.Context, _ MirrorEnvelope) MirrorSinkResult {
	b.calls.Add(1)
	<-b.release
	return MirrorSinkResult{Status: MirrorApplied, ErrorClass: DeliveryErrorNone}
}

func (b *queueBlockingMirrorSink) Snapshot() SinkSnapshot {
	return SinkSnapshot{Descriptor: b.Descriptor(), State: SinkLifecycleOpen}
}

func (b *queueBlockingMirrorSink) Abort(AbortProof) error { return nil }
func (b *queueBlockingMirrorSink) Close(context.Context) error {
	select {
	case <-b.release:
	default:
		close(b.release)
	}
	return nil
}
