package ui

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	outputpkg "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/output"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
)

// ============================================================================
// Phase 1：TerminalSessionWithOutput 统一接入（11.2 模式）
// ============================================================================

// outputTestFixture 是 ui 侧最简 gateway fixture：memory primary sink +
// gateway + port。
type outputTestFixture struct {
	Gateway *outputpkg.RenderOutputGateway
	Sink    *outputpkg.MemorySink
}

func newOutputTestFixture(t *testing.T) *outputTestFixture {
	t.Helper()
	sink := outputpkg.NewMemorySink(outputpkg.TargetDescriptor{
		SinkID:             "memory-primary",
		Class:              outputpkg.TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	})
	gw, err := outputpkg.NewRenderOutputGateway("ui-test-"+testSessionSuffix(), outputpkg.RenderGatewayOptions{
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
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
	})
	return &outputTestFixture{Gateway: gw, Sink: sink}
}

var testSessionCounter uint64

func testSessionSuffix() string {
	testSessionCounter++
	return "s" + string(rune('a'+testSessionCounter))
}

// TestTerminalSessionWithOutputFlush：Flush 经 gateway port 提交，成功后 sink
// 收到完整 bytes，frame 结果与直写路径一致（committed 等价）。
func TestTerminalSessionWithOutputFlush(t *testing.T) {
	fx := newOutputTestFixture(t)
	session := NewTerminalSessionWithOutput(fx.Gateway)
	plan := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	result := session.Flush(plan)
	if result.Err != nil || result.Deferred || !result.FullRepaint || result.Frame != 1 {
		t.Fatalf("first flush = %#v", result)
	}
	batches := fx.Sink.SnapshotBatches()
	if len(batches) == 0 {
		t.Fatal("no batches submitted to gateway")
	}
	var total int
	for _, b := range batches {
		if b.Kind != outputpkg.TransactionFrame {
			t.Fatalf("expected frame kind, got %s", b.Kind)
		}
		total += len(b.Bytes)
	}
	if total == 0 {
		t.Fatal("no bytes reached physical sink")
	}
	// projection 状态与直写路径一致。
	state := session.ProjectionState()
	if state.Validity != renderengine.ProjectionKnown || state.Frame != 1 {
		t.Fatalf("state after gateway flush = %#v", state)
	}
}

// TestTerminalSessionWithOutputHistoryHandoff：CommitHistory 以 history_handoff
// kind 提交且成功。
func TestTerminalSessionWithOutputHistoryHandoff(t *testing.T) {
	fx := newOutputTestFixture(t)
	session := NewTerminalSessionWithOutput(fx.Gateway)
	initial := terminalSessionPlan(1, 24, 5, 3, LeaseState{})
	initial.Rows[0].Text = "outgoing history"
	initial.Rows[1].Text = "retained middle"
	initial.Rows[2].Text = "retained latest"
	initial.Rows[3].Text = "> prompt"
	initial.Rows[4].Text = "status"
	if result := session.Flush(initial); result.Err != nil || !result.FullRepaint {
		t.Fatalf("initial frame = %#v", result)
	}

	commit := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: "outgoing history"}}})
	result := session.FlushTransaction(TerminalTransactionPlan{Frame: initial, History: &commit})
	if result.Frame.Err != nil || result.History == nil || result.History.Err != nil || result.History.Deferred || result.History.Frame == 0 {
		t.Fatalf("transaction = %#v", result)
	}
	batches := fx.Sink.SnapshotBatches()
	var sawHistoryKind bool
	for _, b := range batches {
		if b.Kind == outputpkg.TransactionHistoryHandoff ||
			b.Kind == outputpkg.TransactionFrameAndHistory {
			sawHistoryKind = true
		}
	}
	if !sawHistoryKind {
		t.Fatalf("expected history-bearing batch kind, got %v kinds", batchKinds(batches))
	}
}

// TestTerminalSessionWithOutputAlternate：alternate screen 经 gateway 以
// alternate_enter/write/exit 提交。
func TestTerminalSessionWithOutputAlternate(t *testing.T) {
	fx := newOutputTestFixture(t)
	session := NewTerminalSessionWithOutput(fx.Gateway)
	plan := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	if r := session.Flush(plan); r.Err != nil {
		t.Fatalf("flush: %v", r.Err)
	}
	if err := session.EnterAlternateScreen(7); err != nil {
		t.Fatalf("enter: %v", err)
	}
	if err := session.WriteAlternateScreen(7, "\x1b[1;1Hpager-content"); err != nil {
		t.Fatalf("write alt: %v", err)
	}
	batches := fx.Sink.SnapshotBatches()
	var sawEnter, sawWrite bool
	for _, b := range batches {
		switch b.Kind {
		case outputpkg.TransactionAlternateEnter:
			sawEnter = true
		case outputpkg.TransactionAlternateWrite:
			sawWrite = true
		}
	}
	if !sawEnter || !sawWrite {
		t.Fatalf("expected alternate_enter+alternate_write batches, enter=%v write=%v", sawEnter, sawWrite)
	}
	if err := session.ExitAlternateScreen(7); err != nil {
		t.Fatalf("exit: %v", err)
	}
	batches = fx.Sink.SnapshotBatches()
	var sawExit bool
	for _, b := range batches {
		if b.Kind == outputpkg.TransactionAlternateExit {
			sawExit = true
		}
	}
	if !sawExit {
		t.Fatal("expected alternate_exit batch")
	}
}

// TestTerminalSessionWithOutputAbort：gateway-backed AbortTerminalWrite 关闭
// port；之后 Flush 被拒（frame err），不进入 sink。
func TestTerminalSessionWithOutputAbort(t *testing.T) {
	fx := newOutputTestFixture(t)
	session := NewTerminalSessionWithOutput(fx.Gateway)
	if err := session.AbortTerminalWrite(); err != nil {
		t.Fatalf("abort: %v", err)
	}
	result := session.Flush(terminalSessionPlan(1, 20, 6, 4, LeaseState{}))
	if result.Err == nil {
		t.Fatal("expected frame error after abort")
	}
}

// TestTerminalSessionWithOutputZeroProof：n==0,nil writer 产生 zero proof；
// session 消费为 Err != nil 且 MayHavePartiallyWritten=false（可安全重建
// 语义，与直写路径一致）。
func TestTerminalSessionWithOutputZeroProof(t *testing.T) {
	phys := outputpkg.NewPhysicalSink(outputpkg.TargetDescriptor{
		SinkID:             "zero-primary",
		Class:              outputpkg.TargetClassPhysical,
		ProjectionTargetID: "pt-zero",
	}, &zeroWriter{}, outputpkg.PhysicalSinkOptions{})
	gw, err := outputpkg.NewRenderOutputGateway("zero-session-"+testSessionSuffix(), outputpkg.RenderGatewayOptions{
		Clock:                 outputpkg.SystemClock{},
		CloseTimeout:          3 * time.Second,
		ReconfigureTimeout:    3 * time.Second,
		MaxIntentBytes:        1 << 20,
		MirrorQueueCapacity:   8,
		DeliveryJournalLimit:  outputpkg.JournalLimit{MaxItems: 64, MaxBytes: 1 << 20},
		EventJournalLimit:     outputpkg.JournalLimit{MaxItems: 64, MaxBytes: 1 << 20},
		MaxSubscriptions:      8,
		MaxSubscriptionBuffer: 32,
	}, outputpkg.RenderRouteConfig{
		Primary:            phys,
		PrimaryOwnership:   outputpkg.SinkOwned,
		ProjectionTargetID: "pt-zero",
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
	})
	session := NewTerminalSessionWithOutput(gw)
	result := session.Flush(terminalSessionPlan(1, 20, 6, 4, LeaseState{}))
	if result.Err == nil {
		t.Fatal("expected error for zero-proof write")
	}
	// zero proof：session 消费为可安全重建（不标为 partial）；尝试恢复重绘
	// 应成功（writer 只是返回 n==0,nil，恢复 frame 不影响证明语义）。
	plan := terminalSessionPlan(2, 20, 6, 4, LeaseState{})
	result2 := session.Flush(plan)
	if result2.Err != nil {
		t.Fatalf("recovery flush should succeed after zero-proof: %v", result2.Err)
	}
}

// zeroWriter 首次 Write 返回 n==0,nil（零写），后续正常写入。
type zeroWriter struct {
	mu   sync.Mutex
	call int
}

func (w *zeroWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.call++
	if w.call == 1 {
		return 0, nil
	}
	return len(p), nil
}

// batchKinds 汇总已提交 batch 的 kinds（断言辅助）。
func batchKinds(batches []outputpkg.RenderBatch) []outputpkg.TransactionKind {
	kinds := make([]outputpkg.TransactionKind, 0, len(batches))
	for _, b := range batches {
		kinds = append(kinds, b.Kind)
	}
	return kinds
}
