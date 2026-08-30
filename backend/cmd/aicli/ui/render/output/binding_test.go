package output

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// ============================================================================
// Phase 3 legacy adapter 契约测试（8.2）
// ============================================================================

// bindingFixture 提供 registry + gateway + memory primary。
type bindingFixture struct {
	Registry *SessionBindingRegistry
	Gateway  *RenderOutputGateway
	Sink     *MemorySink
}

func newBindingFixture(t *testing.T) *bindingFixture {
	t.Helper()
	sink := NewMemorySink(TargetDescriptor{
		SinkID:             "mem-primary",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	})
	gw, err := NewRenderOutputGateway("legacy-"+randomID("s"), gatewayOptions(), RenderRouteConfig{
		Primary:            sink,
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: "pt-primary",
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
	})
	return &bindingFixture{
		Registry: NewSessionBindingRegistry(),
		Gateway:  gw,
		Sink:     sink,
	}
}

// TestLegacyTransactionOnePrimary：多次 Write 到 encode buffer 只产生一笔
// primary submission。
func TestLegacyTransactionOnePrimary(t *testing.T) {
	fx := newBindingFixture(t)
	ref := fx.Registry.Bind("ses-1", fx.Gateway)
	adapter := &LegacyTransactionAdapter{Binding: ref}
	// adapter 用 nil 绑定验证 fail-closed（见 TestLegacyAdapterNoPort）。
	receipt, err := adapter.Submit(context.Background(), TransactionLegacyFlush, "test",
		RenderTerminalContext{}, nil, func(w io.Writer) error {
			for i := 0; i < 3; i++ {
				if _, werr := w.Write([]byte("chunk")); werr != nil {
					return werr
				}
			}
			return nil
		})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if receipt.Admission.Decision != AdmissionAccepted || receipt.Primary == nil {
		t.Fatalf("receipt: %+v", receipt)
	}
	// sink 只收到一笔 batch（3 次 encode Write 合并为 1 次 primary）。
	batches := fx.Sink.SnapshotBatches()
	if len(batches) != 1 {
		t.Fatalf("expected exactly 1 primary batch, got %d", len(batches))
	}
	if string(batches[0].Bytes) != "chunkchunkchunk" {
		t.Fatalf("batch bytes: %q", batches[0].Bytes)
	}
	if batches[0].Kind != TransactionLegacyFlush {
		t.Fatalf("kind: %s", batches[0].Kind)
	}
}

// TestLegacyTransactionBufferLimit：encode 超 local limit → fail closed
// （gateway 调用前失败，无 receipt）。
func TestLegacyTransactionBufferLimit(t *testing.T) {
	fx := newBindingFixture(t)
	adapter := &LegacyTransactionAdapter{
		Binding:    fx.Registry.Bind("ses-2", fx.Gateway),
		LocalLimit: 8,
	}
	_, err := adapter.Submit(context.Background(), TransactionLegacyFlush, "test",
		RenderTerminalContext{}, nil, func(w io.Writer) error {
			_, werr := w.Write([]byte("0123456789ABCDEF"))
			return werr
		})
	if !errors.Is(err, ErrLegacyBufferLimit) {
		t.Fatalf("expected buffer limit error, got %v", err)
	}
	if len(fx.Sink.SnapshotBatches()) != 0 {
		t.Fatal("buffer-limit failure must not reach gateway")
	}
}

// TestLegacyImmediateMapping：committed→n=len；unknown→UncertainWriteError；
// pre-admission→n=0 稳定错误。
func TestLegacyImmediateMapping(t *testing.T) {
	fx := newBindingFixture(t)
	ref := fx.Registry.Bind("ses-3", fx.Gateway)
	adapter := &LegacyImmediateAdapter{
		Binding:  ref,
		Kind:     TransactionLegacyImmediate,
		Source:   "test",
		Terminal: RenderTerminalContext{},
	}
	payload := []byte("hello-immediate")
	n, err := adapter.Write(payload)
	if err != nil || n != len(payload) {
		t.Fatalf("committed mapping: n=%d err=%v", n, err)
	}

	// unknown：用 PhysicalSink 短写 writer 构造 unknown primary。
	shortWriter := &shortWriteWriter{n: 2}
	phys := NewPhysicalSink(TargetDescriptor{
		SinkID:             "short-primary",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-short",
	}, shortWriter, PhysicalSinkOptions{})
	gw2, err := NewRenderOutputGateway("legacy-short-"+randomID("s"), gatewayOptions(), RenderRouteConfig{
		Primary:            phys,
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: "pt-short",
	})
	if err != nil {
		t.Fatalf("gateway2: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = gw2.Close(ctx)
	})
	adapter2 := &LegacyImmediateAdapter{
		Binding:  fx.Registry.Bind("ses-4", gw2),
		Kind:     TransactionLegacyImmediate,
		Source:   "test",
		Terminal: RenderTerminalContext{},
	}
	_, err = adapter2.Write([]byte("partial"))
	if err == nil {
		t.Fatal("unknown write must return error")
	}
	ue := EnsureUncertainWriteError(err)
	if ue == nil {
		t.Fatalf("expected UncertainWriteError, got %v", err)
	}
	if ue.AcceptedBytes() != 2 {
		t.Fatalf("accepted: %d", ue.AcceptedBytes())
	}
	if ue.ProjectionTargetID() != "pt-short" {
		t.Fatalf("target: %s", ue.ProjectionTargetID())
	}
	if ue.BatchID() == "" {
		t.Fatal("batch id missing")
	}
}

// TestLegacyImmediatePreAdmission：binding 失效 → n=0 稳定错误，不降级
// process writer。
func TestLegacyImmediatePreAdmission(t *testing.T) {
	fx := newBindingFixture(t)
	adapter := &LegacyImmediateAdapter{
		Binding:  fx.Registry.Bind("ses-5", fx.Gateway),
		Kind:     TransactionLegacyImmediate,
		Source:   "test",
		Terminal: RenderTerminalContext{},
	}
	fx.Registry.Unbind("ses-5")
	n, err := adapter.Write([]byte("late"))
	if n != 0 || err == nil {
		t.Fatalf("fenced binding: n=%d err=%v", n, err)
	}
	var ce ClassifiedDeliveryError
	if !errors.As(err, &ce) || ce.DeliveryClass() != DeliveryErrorClosed {
		t.Fatalf("expected closed classified error, got %v", err)
	}
}

// TestBindingLateWriteNoCrossSession：旧 binding 的 late goroutine 在新
// session 建立后只得到 rejected，不会串写。
func TestBindingLateWriteNoCrossSession(t *testing.T) {
	fx := newBindingFixture(t)
	refOld := fx.Registry.Bind("ses-old-1", fx.Gateway)
	adapterOld := &LegacyImmediateAdapter{
		Binding:  refOld,
		Kind:     TransactionLegacyImmediate,
		Source:   "old",
		Terminal: RenderTerminalContext{},
	}
	// 写一笔成功。
	if _, err := adapterOld.Write([]byte("before")); err != nil {
		t.Fatalf("before: %v", err)
	}
	// 新 session 绑定同 key（递增 generation）→ 旧 facade fenced。
	fx.Registry.Bind("ses-old-1", fx.Gateway)
	n, err := adapterOld.Write([]byte("late"))
	if n != 0 || err == nil {
		t.Fatalf("late write must be fenced: n=%d err=%v", n, err)
	}
	// 只有 before 一笔在 sink；late 未串写。
	batches := fx.Sink.SnapshotBatches()
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}
}

// TestBindingRebindWaitsForInFlightOldGeneration verifies that Bind's return
// is the generation fence: an old submission that already owns the facade's
// read-side lease finishes before rebind returns, and no later old submission
// can enter the underlying port.
func TestBindingRebindWaitsForInFlightOldGeneration(t *testing.T) {
	registry := NewSessionBindingRegistry()
	port := &blockingSubmitPort{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	old := registry.Bind("ses-linearized", port)

	submitDone := make(chan struct{})
	go func() {
		defer close(submitDone)
		old.Port.Submit(context.Background(), RenderIntent{
			Kind:  TransactionFrame,
			Bytes: []byte("old"),
		})
	}()
	select {
	case <-port.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("old submission did not enter underlying port")
	}

	rebindDone := make(chan struct{})
	go func() {
		registry.Bind("ses-linearized", port)
		close(rebindDone)
	}()
	select {
	case <-rebindDone:
		t.Fatal("rebind returned while an old-generation submission was still active")
	case <-time.After(25 * time.Millisecond):
	}

	close(port.release)
	select {
	case <-submitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("old submission did not finish")
	}
	select {
	case <-rebindDone:
	case <-time.After(2 * time.Second):
		t.Fatal("rebind did not finish after old submission returned")
	}

	late := old.Port.Submit(context.Background(), RenderIntent{
		Kind:  TransactionFrame,
		Bytes: []byte("late"),
	})
	if late.Admission.Decision != AdmissionRejected ||
		late.Admission.ErrorClass != DeliveryErrorClosed {
		t.Fatalf("late old-generation submit was not fenced: %+v", late.Admission)
	}
}

type blockingSubmitPort struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingSubmitPort) Submit(context.Context, RenderIntent) OutputReceipt {
	p.once.Do(func() { close(p.entered) })
	<-p.release
	return OutputReceipt{
		Admission: AdmissionReceipt{Decision: AdmissionAccepted},
	}
}

// TestLegacyImmediateUncertainFence：unknown 的 accepted 数被 clamp。
func TestLegacyImmediateUncertainClamp(t *testing.T) {
	fx := newBindingFixture(t)
	phys := NewPhysicalSink(TargetDescriptor{
		SinkID:             "over-primary",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-over",
	}, &oversizedWriter{n: 999}, PhysicalSinkOptions{})
	gw, err := NewRenderOutputGateway("legacy-over-"+randomID("s"), gatewayOptions(), RenderRouteConfig{
		Primary:            phys,
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: "pt-over",
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
	})
	adapter := &LegacyImmediateAdapter{
		Binding:  fx.Registry.Bind("ses-6", gw),
		Kind:     TransactionLegacyImmediate,
		Source:   "test",
		Terminal: RenderTerminalContext{},
	}
	n, err := adapter.Write([]byte("six"))
	if n > 3 || err == nil {
		t.Fatalf("clamp: n=%d err=%v", n, err)
	}
	ue := EnsureUncertainWriteError(err)
	if ue != nil && ue.AcceptedBytes() > 3 {
		t.Fatalf("accepted clamp: %d", ue.AcceptedBytes())
	}
}

// shortWriteWriter 写一半。
type shortWriteWriter struct{ n int }

func (w *shortWriteWriter) Write(p []byte) (int, error) {
	if w.n > len(p) {
		w.n = len(p)
	}
	return w.n, nil
}

// oversizedWriter 声称写了超长（违反 writer 契约→unknown）。
type oversizedWriter struct{ n int }

func (w *oversizedWriter) Write(p []byte) (int, error) { return w.n, nil }

// TestLegacyTransactionBarrier：空 barrier 经 adapter 提交只推进 context。
func TestLegacyTransactionBarrier(t *testing.T) {
	fx := newBindingFixture(t)
	adapter := &LegacyTransactionAdapter{Binding: fx.Registry.Bind("ses-7", fx.Gateway)}
	receipt, err := adapter.Submit(context.Background(), TransactionContextBarrier, "test",
		RenderTerminalContext{Geometry: TerminalGeometry{Width: 20, Height: 6}}, nil,
		func(w io.Writer) error { return nil })
	if err != nil {
		t.Fatalf("barrier submit: %v", err)
	}
	if receipt.Admission.Decision != AdmissionAccepted || receipt.Primary == nil {
		t.Fatalf("barrier receipt: %+v", receipt)
	}
}
