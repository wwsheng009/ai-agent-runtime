package ui

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	outputpkg "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/output"
)

// ============================================================================
// Phase 3：legacy surface → gateway binding 集成测试（8.2 验收）
// ============================================================================

// surfaceBindingFixture：surface + registry + gateway + memory primary。
type surfaceBindingFixture struct {
	Registry *outputpkg.SessionBindingRegistry
	Gateway  *outputpkg.RenderOutputGateway
	Sink     *outputpkg.MemorySink
	Binding  *LegacySurfaceBinding
	Surface  *FixedBottomSurface
	Terminal *Terminal
}

func newSurfaceBindingFixture(t *testing.T) *surfaceBindingFixture {
	t.Helper()
	sink := outputpkg.NewMemorySink(outputpkg.TargetDescriptor{
		SinkID:             "mem-primary",
		Class:              outputpkg.TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	})
	gw, err := outputpkg.NewRenderOutputGateway("legacy-surf-"+testSessionSuffix(), outputpkg.RenderGatewayOptions{
		Clock:                 outputpkg.SystemClock{},
		CloseTimeout:          3 * time.Second,
		ReconfigureTimeout:    3 * time.Second,
		MaxIntentBytes:        1 << 20,
		MirrorQueueCapacity:   16,
		DeliveryJournalLimit:  outputpkg.JournalLimit{MaxItems: 64, MaxBytes: 1 << 20},
		EventJournalLimit:     outputpkg.JournalLimit{MaxItems: 64, MaxBytes: 1 << 20},
		MaxSubscriptions:      8,
		MaxSubscriptionBuffer: 32,
	}, outputpkg.RenderRouteConfig{
		Primary:            sink,
		PrimaryOwnership:   outputpkg.SinkOwned,
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
	registry := outputpkg.NewSessionBindingRegistry()
	binding := NewLegacySurfaceBinding(registry, "surf-session")
	term := NewTerminal()
	term.SetSizeForTest(40, 10)
	surface := &FixedBottomSurface{terminal: term}
	surface.SetLegacyBinding(binding)
	term.SetLegacyBinding(binding)
	return &surfaceBindingFixture{
		Registry: registry,
		Gateway:  gw,
		Sink:     sink,
		Binding:  binding,
		Surface:  surface,
		Terminal: term,
	}
}

// TestP3SurfaceTitleGoesThroughGateway：binding 后 Terminal.SetTitle 输出
// 经 gateway 提交（TransactionTitle），不写 TerminalOutput。
func TestP3SurfaceTitleGoesThroughGateway(t *testing.T) {
	fx := newSurfaceBindingFixture(t)
	fx.Binding.Bind(fx.Gateway)
	fx.Terminal.SetTitle("hello-title")
	batches := fx.Sink.SnapshotBatches()
	found := false
	for _, b := range batches {
		if b.Kind == outputpkg.TransactionTitle && strings.Contains(string(b.Bytes), "hello-title") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("title not submitted via gateway: %+v", batchKindSummaries(batches))
	}
}

// TestP3SurfaceBoundWritesOnePrimaryPerFlush：LegacyFlushRunner 一次 flush
// （多处 encode Write）只产生一笔 primary submission。
func TestP3SurfaceBoundWritesOnePrimaryPerFlush(t *testing.T) {
	fx := newSurfaceBindingFixture(t)
	fx.Binding.Bind(fx.Gateway)
	runner := NewLegacyFlushRunner(fx.Binding, outputpkg.TransactionLegacyFlush, "test-surface")
	runner.SetGeometry(GeometryState{Width: 40, Height: 10})
	receipt, err := runner.Run(context.Background(), func(w io.Writer) error {
		for i := 0; i < 4; i++ {
			if _, werr := w.Write([]byte("segment")); werr != nil {
				return werr
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if receipt.Admission.Decision != outputpkg.AdmissionAccepted || receipt.Primary == nil {
		t.Fatalf("receipt: %+v", receipt)
	}
	batches := fx.Sink.SnapshotBatches()
	if len(batches) != 1 {
		t.Fatalf("expected 1 primary batch per flush, got %d", len(batches))
	}
	if string(batches[0].Bytes) != "segmentsegmentsegmentsegment" {
		t.Fatalf("bytes: %q", batches[0].Bytes)
	}
}

// TestP3SurfaceLateBindingNoCrossSession：旧 binding 的 late goroutine 在
// 新 session 绑定后只得到 rejected，不串写。
func TestP3SurfaceLateBindingNoCrossSession(t *testing.T) {
	fx := newSurfaceBindingFixture(t)
	fx.Binding.Bind(fx.Gateway)
	// 正常写一笔。
	fx.Terminal.SetTitle("first")
	if n := len(fx.Sink.SnapshotBatches()); n != 1 {
		t.Fatalf("first write batches: %d", n)
	}
	// 保存旧 facade（模拟 late goroutine 持有的引用）。
	oldRef := fx.Binding.Registry.Bind("surf-session", fx.Gateway)
	// 新 session 绑定同 key → 递增 generation，旧 facade fenced。
	fx.Registry.Bind("surf-session", fx.Gateway)
	adapter := &outputpkg.LegacyImmediateAdapter{
		Binding:  oldRef,
		Kind:     outputpkg.TransactionLegacyImmediate,
		Source:   "late-goroutine",
		Terminal: outputpkg.RenderTerminalContext{},
	}
	n, err := adapter.Write([]byte("late"))
	if n != 0 || err == nil {
		t.Fatalf("late write must be fenced: n=%d err=%v", n, err)
	}
	// sink 里没有 late 内容。
	for _, b := range fx.Sink.SnapshotBatches() {
		if strings.Contains(string(b.Bytes), "late") {
			t.Fatalf("late write leaked into sink: %q", b.Bytes)
		}
	}
}

// TestP3SurfaceBindingUnbindFence：Unbind 后 surface writer 返回稳定错误，
// 不回落 TerminalOutput（无第二 physical writer 防护）。
func TestP3SurfaceBindingUnbindFence(t *testing.T) {
	fx := newSurfaceBindingFixture(t)
	fx.Binding.Bind(fx.Gateway)
	fx.Binding.Unbind()
	writer := NewLegacySurfaceWriter(fx.Binding, outputpkg.TransactionLegacyImmediate, "test")
	n, err := writer.Write([]byte("after-unbind"))
	if n != 0 || err == nil {
		t.Fatalf("unbound write: n=%d err=%v", n, err)
	}
	if !strings.Contains(err.Error(), "no bound") && !strings.Contains(err.Error(), "fenced") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// batchKindSummaries 汇总 kinds 供断言消息。
func batchKindSummaries(batches []outputpkg.RenderBatch) []outputpkg.TransactionKind {
	kinds := make([]outputpkg.TransactionKind, 0, len(batches))
	for _, b := range batches {
		kinds = append(kinds, b.Kind)
	}
	return kinds
}

// TestP3SurfaceFlushGoesThroughGateway：surface 的 flush 入口
// （flushLegacyANSIHoldingLock）有 binding 时提交一笔 legacy_flush primary
// transaction，不写 process writer。
func TestP3SurfaceFlushGoesThroughGateway(t *testing.T) {
	fx := newSurfaceBindingFixture(t)
	fx.Binding.Bind(fx.Gateway)
	seq := "\x1b[1;24r\x1b[24;1H"
	fx.Surface.flushLegacyANSIHoldingLock(seq)
	batches := fx.Sink.SnapshotBatches()
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}
	if batches[0].Kind != outputpkg.TransactionLegacyFlush {
		t.Fatalf("kind: %s", batches[0].Kind)
	}
	if string(batches[0].Bytes) != seq {
		t.Fatalf("bytes: %q", batches[0].Bytes)
	}
}

// TestP3SurfaceFlushUnboundSilentlyDrops：surface 的 legacy binding 失效后，
// flush 静默丢弃（不回落 process writer、不产生 batch）。
func TestP3SurfaceFlushUnboundSilentlyDrops(t *testing.T) {
	fx := newSurfaceBindingFixture(t)
	fx.Binding.Bind(fx.Gateway)
	fx.Surface.flushLegacyANSIHoldingLock("first")
	if len(fx.Sink.SnapshotBatches()) != 1 {
		t.Fatal("bound flush produced no batch")
	}
	fx.Binding.Unbind()
	before := len(fx.Sink.SnapshotBatches())
	fx.Surface.flushLegacyANSIHoldingLock("late")
	if got := len(fx.Sink.SnapshotBatches()); got != before {
		t.Fatalf("unbound flush must not submit: before=%d after=%d", before, got)
	}
}

// TestP3SurfaceFenceNoProcessWriter：binding 存在时，surface/title 输出经
// gateway，不写 process TerminalOutput（physical fence）。用
// SetTerminalOutputForTesting 捕获 process writer 验证无泄漏。
func TestP3SurfaceFenceNoProcessWriter(t *testing.T) {
	fx := newSurfaceBindingFixture(t)
	var captured strings.Builder
	restore := SetTerminalOutputForTesting(&capturedWriter{buf: &captured})
	defer restore()

	fx.Binding.Bind(fx.Gateway)
	fx.Terminal.SetTitle("fenced-title")
	fx.Surface.flushLegacyANSIHoldingLock("fenced-flush")
	if captured.Len() != 0 {
		t.Fatalf("process writer must not receive fenced output: %q", captured.String())
	}
	if len(fx.Sink.SnapshotBatches()) == 0 {
		t.Fatal("gateway must receive fenced output")
	}
}

// capturedWriter 把写入追加到 strings.Builder。
type capturedWriter struct {
	buf *strings.Builder
}

func (w *capturedWriter) Write(p []byte) (int, error) {
	return w.buf.Write(p)
}

// TestP3LegacyFlushObservableByMirror：legacy flush 经 gateway 后，capture
// mirror 能观察到同一笔 batch（统一/legacy 同 route 被 capture/virtual sink
// 观察——Phase 3 验收第 4 项）。
func TestP3LegacyFlushObservableByMirror(t *testing.T) {
	sink := outputpkg.NewMemorySink(outputpkg.TargetDescriptor{
		SinkID:             "mem-primary",
		Class:              outputpkg.TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	})
	capture := outputpkg.NewCaptureSink("pt-capture", outputpkg.CaptureOptions{
		MaxEntries:   8,
		MaxBytes:     1 << 20,
		StorePayload: true,
	})
	gw, err := outputpkg.NewRenderOutputGateway("legacy-mirror-"+testSessionSuffix(), outputpkg.RenderGatewayOptions{
		Clock:                 outputpkg.SystemClock{},
		CloseTimeout:          3 * time.Second,
		ReconfigureTimeout:    3 * time.Second,
		MaxIntentBytes:        1 << 20,
		MirrorQueueCapacity:   16,
		DeliveryJournalLimit:  outputpkg.JournalLimit{MaxItems: 64, MaxBytes: 1 << 20},
		EventJournalLimit:     outputpkg.JournalLimit{MaxItems: 64, MaxBytes: 1 << 20},
		MaxSubscriptions:      8,
		MaxSubscriptionBuffer: 32,
	}, outputpkg.RenderRouteConfig{
		Primary:            sink,
		PrimaryOwnership:   outputpkg.SinkOwned,
		ProjectionTargetID: "pt-primary",
		Mirrors: []outputpkg.RenderMirror{{
			Sink:      capture,
			Policy:    outputpkg.MirrorBestEffort,
			ApplyMode: outputpkg.MirrorApplyBytes,
			Ownership: outputpkg.SinkOwned,
			Timeout:   1 * time.Second,
		}},
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	gw.Run()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
	})
	registry := outputpkg.NewSessionBindingRegistry()
	binding := NewLegacySurfaceBinding(registry, "mirror-session")
	binding.Bind(gw)
	term := NewTerminal()
	term.SetSizeForTest(40, 10)
	surface := &FixedBottomSurface{terminal: term}
	surface.SetLegacyBinding(binding)
	// legacy surface flush 经 binding 提交。
	seq := "\x1b[1;24r\x1b[24;1H"
	surface.flushLegacyANSIHoldingLock(seq)
	// 等 mirror worker 完成。
	drainCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := gw.Drain(drainCtx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	// capture mirror 观察到同一笔 legacy_flush batch。
	entries := capture.Entries()
	if len(entries) != 1 {
		t.Fatalf("capture entries: %d", len(entries))
	}
	if entries[0].Transaction != outputpkg.TransactionLegacyFlush {
		t.Fatalf("capture transaction kind: %s", entries[0].Transaction)
	}
	if entries[0].BytesLength != len(seq) {
		t.Fatalf("capture bytes: %d", entries[0].BytesLength)
	}
}
