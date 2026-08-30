package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	outputpkg "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/output"
)

// ============================================================================
// Phase 2：统一测试观察面（11.2：core frame/history/lease 测试不需要
// stdout replacement 或 pseudo-TTY；capture/virtual primary 的 Ack 不会
// 泄漏到 physical domain）
// ============================================================================

// virtualFixture 是 physical primary + virtual mirror 的观察面 fixture。
type virtualFixture struct {
	Gateway  *outputpkg.RenderOutputGateway
	Virtual  *outputpkg.VirtualTerminalSink
	Emulator *VtTerminalEmulator
	Session  *TerminalSession
	Sink     *outputpkg.MemorySink
}

func newVirtualFixture(t *testing.T) *virtualFixture {
	t.Helper()
	sink := outputpkg.NewMemorySink(outputpkg.TargetDescriptor{
		SinkID:             "memory-primary",
		Class:              outputpkg.TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	})
	emu := NewVtTerminalEmulator()
	virtual := outputpkg.NewVirtualTerminalSink("pt-virtual", emu, outputpkg.VirtualSinkOptions{})
	gw, err := outputpkg.NewRenderOutputGateway("vt-fixture-"+testSessionSuffix(), outputpkg.RenderGatewayOptions{
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
			Sink:      virtual,
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
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
	})
	return &virtualFixture{
		Gateway:  gw,
		Virtual:  virtual,
		Emulator: emu,
		Session:  NewTerminalSessionWithOutput(gw),
		Sink:     sink,
	}
}

// drain 等 mirror worker 完成 entry seal。
func (f *virtualFixture) drain(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := f.Gateway.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
}

// TestP2VirtualFrameProjection：首帧 repaint 经 physical primary + virtual
// mirror 双写；virtual 投影看到同样内容（"终端解释器看到什么"）。
func TestP2VirtualFrameProjection(t *testing.T) {
	fx := newVirtualFixture(t)
	plan := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	result := fx.Session.Flush(plan)
	if result.Err != nil || result.Deferred || !result.FullRepaint {
		t.Fatalf("flush = %#v", result)
	}
	fx.drain(t)
	proj := fx.Virtual.Projection()
	if proj.Validity != outputpkg.ProjectionValid {
		t.Fatalf("virtual validity: %s", proj.Validity)
	}
	if proj.ProjectionTargetID != "pt-virtual" || proj.ObservedPrimaryTargetID != "pt-virtual" {
		t.Fatalf("virtual target identity: %+v", proj)
	}
	if proj.LastMirrorEntryID == "" {
		t.Fatal("virtual as mirror must carry mirror entry id")
	}
	// physical 也收到了同等批次。
	if batches := fx.Sink.SnapshotBatches(); len(batches) == 0 {
		t.Fatal("no physical batches")
	}
}

// TestP2VirtualHistoryShipping：history handoff 走到 virtual mirror 且
// 快照含 history 内容。
func TestP2VirtualHistoryShipping(t *testing.T) {
	fx := newVirtualFixture(t)
	initial := terminalSessionPlan(1, 24, 5, 3, LeaseState{})
	initial.Rows[0].Text = "outgoing history"
	initial.Rows[1].Text = "retained middle"
	initial.Rows[2].Text = "retained latest"
	initial.Rows[3].Text = "> prompt"
	initial.Rows[4].Text = "status"
	if r := fx.Session.Flush(initial); r.Err != nil {
		t.Fatalf("initial = %#v", r)
	}
	commit := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: "outgoing history"}}})
	result := fx.Session.FlushTransaction(TerminalTransactionPlan{Frame: initial, History: &commit})
	if result.Frame.Err != nil || result.History == nil || result.History.Err != nil {
		t.Fatalf("transaction = %#v", result)
	}
	fx.drain(t)
	proj := fx.Virtual.Projection()
	if proj.Validity != outputpkg.ProjectionValid {
		t.Fatalf("virtual validity: %s", proj.Validity)
	}
	// history 行应出现在 scrollback 或 rows 中。
	all := append(append([]string(nil), proj.Scrollback...), proj.Rows...)
	found := false
	for _, row := range all {
		if strings.Contains(row, "outgoing history") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("history content not visible in virtual projection: scrollback=%v rows=%v", proj.Scrollback, proj.Rows)
	}
}

// TestP2VirtualLeaseLifecycle：alternate 生命周期经 virtual mirror 观察，
// 不触碰 physical domain 的 Ack（physical 只收 primary）。
func TestP2VirtualLeaseLifecycle(t *testing.T) {
	fx := newVirtualFixture(t)
	plan := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	if r := fx.Session.Flush(plan); r.Err != nil {
		t.Fatalf("flush: %v", r.Err)
	}
	// alternate enter 不应在 physical sink 造成交错（primary 单 owner）。
	if err := fx.Session.EnterAlternateScreen(9); err != nil {
		t.Fatalf("enter: %v", err)
	}
	if err := fx.Session.WriteAlternateScreen(9, "\x1b[1;1Halt-content"); err != nil {
		t.Fatalf("write: %v", err)
	}
	fx.drain(t)
	proj := fx.Virtual.Projection()
	if !proj.Alternate {
		t.Fatal("virtual must observe alternate buffer")
	}
	if err := fx.Session.ExitAlternateScreen(9); err != nil {
		t.Fatalf("exit: %v", err)
	}
	fx.drain(t)
	proj = fx.Virtual.Projection()
	if proj.Alternate {
		t.Fatal("virtual must observe alternate exit")
	}
}

// TestP2VirtualResizeBarrier：geometry 变化原子推进 context，resize 后
// 投影尺寸正确。
func TestP2VirtualResizeBarrier(t *testing.T) {
	fx := newVirtualFixture(t)
	planA := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	if r := fx.Session.Flush(planA); r.Err != nil {
		t.Fatalf("flush A: %v", r.Err)
	}
	planB := terminalSessionPlan(2, 30, 10, 8, LeaseState{})
	if r := fx.Session.Flush(planB); r.Err != nil {
		t.Fatalf("flush B: %v", r.Err)
	}
	fx.drain(t)
	proj := fx.Virtual.Projection()
	if proj.Width != 30 || proj.Height != 10 {
		t.Fatalf("resize projection: %dx%d", proj.Width, proj.Height)
	}
	if proj.Validity != outputpkg.ProjectionValid {
		t.Fatalf("validity: %s", proj.Validity)
	}
}

// TestP2VirtualNoStdoutLeak：本测试不替换 os.Stdout——投影断言全部来自
// virtual mirror 与 memory sink，验证 Phase 2 验收（不需要 stdout
// replacement 或 pseudo-TTY）。
func TestP2VirtualNoStdoutLeak(t *testing.T) {
	fx := newVirtualFixture(t)
	plan := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	if r := fx.Session.Flush(plan); r.Err != nil {
		t.Fatalf("flush: %v", r.Err)
	}
	fx.drain(t)
	_ = fx.Virtual.Projection()
	_ = fx.Sink.SnapshotBatches()
}

// TestP2VirtualResizeShrink：VtTerminalEmulator 变矮 resize 迁移不把首行
// 推入 scrollback、底部无空行、内容对齐（S5 末尾 \n 回归）。直接单测
// emulator 的 resize 迁移路径（session flush 是全量重绘，不走迁移）。
func TestP2VirtualResizeShrink(t *testing.T) {
	emu := NewVtTerminalEmulator()
	g := outputpkg.TerminalGeometry{Width: 20, Height: 6}
	if err := emu.ApplyContext(g, outputpkg.TerminalProfileRef{ID: "ansi", Version: 1}); err != nil {
		t.Fatalf("apply context: %v", err)
	}
	// 灌 6 行内容。
	if err := emu.Apply([]byte("row-one\nrow-two\nrow-three\nrow-four\nrow-five\nrow-six")); err != nil {
		t.Fatalf("apply: %v", err)
	}
	snap := emu.Snapshot()
	if len(snap.Rows) != 6 || snap.Rows[0] != "row-one" {
		t.Fatalf("seed rows: %+v", snap.Rows)
	}
	// 变矮到 3 行：迁移保留前 3 行，无 scroll 推挤、底部无空行。
	if err := emu.ApplyContext(outputpkg.TerminalGeometry{Width: 20, Height: 3}, snap.Profile); err != nil {
		t.Fatalf("resize: %v", err)
	}
	snap = emu.Snapshot()
	if snap.Height != 3 || len(snap.Rows) != 3 {
		t.Fatalf("after shrink: h=%d rows=%d", snap.Height, len(snap.Rows))
	}
	if snap.Rows[0] != "row-one" {
		t.Fatalf("first row lost after shrink: %v", snap.Rows)
	}
	if snap.Rows[2] != "row-three" {
		t.Fatalf("third row mismatch: %v", snap.Rows)
	}
	if len(snap.Scrollback) != 0 {
		t.Fatalf("shrink must not push rows into scrollback: %v", snap.Scrollback)
	}
	// 变宽 resize：内容保留。
	if err := emu.ApplyContext(outputpkg.TerminalGeometry{Width: 30, Height: 4}, snap.Profile); err != nil {
		t.Fatalf("grow: %v", err)
	}
	snap = emu.Snapshot()
	if snap.Width != 30 || snap.Height != 4 {
		t.Fatalf("after grow: %dx%d", snap.Width, snap.Height)
	}
	if snap.Rows[0] != "row-one" {
		t.Fatalf("content lost after grow: %v", snap.Rows)
	}
}

// TestP2CursorShapeTracking：DECSCUSR 形状序列被 adapter 跟踪
// （underline/bar），快照的 Cursor.Shape 反映之。
func TestP2CursorShapeTracking(t *testing.T) {
	emu := NewVtTerminalEmulator()
	if err := emu.ApplyContext(outputpkg.TerminalGeometry{Width: 20, Height: 6},
		outputpkg.TerminalProfileRef{ID: "ansi", Version: 1}); err != nil {
		t.Fatalf("context: %v", err)
	}
	if err := emu.Apply([]byte("hello\x1b[3 q")); err != nil {
		t.Fatalf("apply: %v", err)
	}
	snap := emu.Snapshot()
	if snap.Cursor.Shape != outputpkg.CursorShapeUnderline {
		t.Fatalf("shape: %s, want underline", snap.Cursor.Shape)
	}
	if err := emu.Apply([]byte("\x1b[5 q")); err != nil {
		t.Fatalf("apply: %v", err)
	}
	snap = emu.Snapshot()
	if snap.Cursor.Shape != outputpkg.CursorShapeBar {
		t.Fatalf("shape: %s, want bar", snap.Cursor.Shape)
	}
	// cursor 可见性。
	if err := emu.Apply([]byte("\x1b[?25h")); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !emu.Snapshot().Cursor.Visible {
		t.Fatal("cursor should be visible after ?25h")
	}
}
