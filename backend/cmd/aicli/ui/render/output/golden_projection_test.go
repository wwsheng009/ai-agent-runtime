package output

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ============================================================================
// A1 虚拟投影 golden 测试
//
// 双 golden 契约：
//   - wire golden：memory primary 收到的字节序列（对终端状态/控制序列敏感）；
//   - text golden：VirtualTerminalSink.Projection().Rows（用户实际看到的屏幕
//     文本，回归价值更高）。
//
// 所有 golden 均为内联常量，不提供自动更新入口：任何变更都必须显式 review
// 并修改断言，不允许静默更新通过（验收标准）。
//
// 说明：fixture 的 virtual mirror 需要非零 geometry 才会应用 bytes，因此
// 所有 intent 都显式携带 Terminal 上下文（deterministic geometry/profile）。
// ============================================================================

var goldenGeometry = RenderTerminalContext{
	Geometry: TerminalGeometry{Width: 40, Height: 10},
	Profile:  TerminalProfileRef{ID: "ansi", Version: 1},
}

// goldenIntent 构造带确定性 terminal 上下文的完整 intent。
func goldenIntent(kind TransactionKind, bytes []byte) RenderIntent {
	return RenderIntent{
		IntentID: "golden-" + strings.ReplaceAll(string(bytes), "\n", "_"),
		Kind:     kind,
		Source:   "golden-test",
		Cause:    "a1-golden",
		Bytes:    bytes,
		Terminal: goldenGeometry,
	}
}

// submitGolden 提交 intent 并断言 primary committed。
func submitGolden(t *testing.T, fx *RenderTestFixture, intent RenderIntent) OutputReceipt {
	t.Helper()
	r := fx.Gateway.Submit(context.Background(), intent)
	if r.Primary == nil || r.Primary.Status != DeliveryCommitted {
		t.Fatalf("golden submit receipt: %+v", r)
	}
	return r
}

// drainGolden 排空 gateway 异步 mirror 路径，确保 virtual sink 已处理
// 所有已提交 entry（mirror 交付是异步的，断言前必须 Drain）。
func drainGolden(t *testing.T, fx *RenderTestFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := fx.Gateway.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
}

// goldenProjectionRows 断言虚拟终端投影与 text golden 完全一致。
func goldenProjectionRows(t *testing.T, v *VirtualTerminalSink, want []string) {
	t.Helper()
	snap := v.Projection()
	if snap.Validity != ProjectionValid {
		t.Fatalf("projection validity = %s, want valid", snap.Validity)
	}
	if len(snap.Rows) != len(want) {
		t.Fatalf("rows = %d, want %d\n--- got ---\n%s\n--- want ---\n%s",
			len(snap.Rows), len(want), strings.Join(snap.Rows, "\n"), strings.Join(want, "\n"))
	}
	for i := range want {
		if snap.Rows[i] != want[i] {
			t.Fatalf("row %d = %q, want %q\n--- got ---\n%s\n--- want ---\n%s",
				i, snap.Rows[i], want[i], strings.Join(snap.Rows, "\n"), strings.Join(want, "\n"))
		}
	}
}

// goldenWireBatches 断言 memory primary 收到的 wire bytes 与 wire golden 一致。
func goldenWireBatches(t *testing.T, primary *MemorySink, want []string) {
	t.Helper()
	batches := primary.SnapshotBatches()
	var got []string
	for _, b := range batches {
		got = append(got, string(b.Bytes))
	}
	if len(got) != len(want) {
		t.Fatalf("batches = %d, want %d\n--- got ---\n%s\n--- want ---\n%s",
			len(got), len(want), strings.Join(got, "|"), strings.Join(want, "|"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("batch %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestA1GoldenFrameWireAndText：frame 场景 wire/text 双 golden。
// 注意：fixture 的 fakeEmulator 是简单累积器（按 \n 拆分、不清屏），
// 因此多帧时第二帧以 \n 开头、末行不带尾换行，行 golden 才干净。
func TestA1GoldenFrameWireAndText(t *testing.T) {
	fx := NewRenderTestFixture(t, WithVirtualTerminal("pt-virtual"))

	// frame 1：状态行 + 内容行（真实渲染器产出的屏幕行）。
	wire1 := "status: ready\nhello world"
	submitGolden(t, fx, goldenIntent(TransactionFrame, []byte(wire1)))

	// frame 2：内容更新（以 \n 与前一帧分隔）。
	wire2 := "\nstatus: running\nhello world\nprogress 50%"
	submitGolden(t, fx, goldenIntent(TransactionFrame, []byte(wire2)))

	drainGolden(t, fx)

	// wire golden：primary 顺序收到两帧字节。
	goldenWireBatches(t, fx.Primary, []string{wire1, wire2})

	// text golden：虚拟终端屏幕行（累积器语义：全部行按序拼接）。
	goldenProjectionRows(t, fx.Virtual, []string{
		"status: ready",
		"hello world",
		"status: running",
		"hello world",
		"progress 50%",
	})
}

// TestA1GoldenHistoryHandoff：history_handoff 场景。
func TestA1GoldenHistoryHandoff(t *testing.T) {
	fx := NewRenderTestFixture(t, WithVirtualTerminal("pt-virtual"))

	// 先一帧普通内容。
	submitGolden(t, fx, goldenIntent(TransactionFrame, []byte("transcript line 1\ntranscript line 2")))

	// history handoff：把历史行交付给虚拟终端。
	handoff := "\nhistory line A\nhistory line B"
	submitGolden(t, fx, goldenIntent(TransactionHistoryHandoff, []byte(handoff)))

	drainGolden(t, fx)

	goldenWireBatches(t, fx.Primary, []string{
		"transcript line 1\ntranscript line 2",
		handoff,
	})
	goldenProjectionRows(t, fx.Virtual, []string{
		"transcript line 1",
		"transcript line 2",
		"history line A",
		"history line B",
	})
}

// TestA1GoldenBarrier：context barrier（空 payload）不产生可见行，
// 但推进 context。
func TestA1GoldenBarrier(t *testing.T) {
	fx := NewRenderTestFixture(t, WithVirtualTerminal("pt-virtual"))

	// barrier 前先应用一帧，确认投影 valid。
	submitGolden(t, fx, goldenIntent(TransactionFrame, []byte("visible")))

	barrier := goldenIntent(TransactionContextBarrier, nil)
	barrier.Terminal = RenderTerminalContext{
		Geometry: TerminalGeometry{Width: 60, Height: 12},
		Profile:  TerminalProfileRef{ID: "ansi", Version: 1},
	}
	r := fx.Gateway.Submit(context.Background(), barrier)
	if r.Primary == nil || r.Primary.Status != DeliveryCommitted {
		t.Fatalf("barrier receipt: %+v", r)
	}

	drainGolden(t, fx)

	// barrier 不解释 bytes：行数不变。
	goldenProjectionRows(t, fx.Virtual, []string{"visible"})
	// barrier 推进了 geometry。
	if snap := fx.Virtual.Projection(); snap.Width != 60 || snap.Height != 12 {
		t.Fatalf("barrier geometry = %dx%d, want 60x12", snap.Width, snap.Height)
	}
}

// TestA1GoldenLeaseContext：HistoryEpoch 随 intent 透传（lease 域）。
func TestA1GoldenLeaseContext(t *testing.T) {
	fx := NewRenderTestFixture(t, WithVirtualTerminal("pt-virtual"))

	epoch := uint64(7)
	lease := goldenIntent(TransactionFrameAndHistory, []byte("lease-frame\nhistory-lease"))
	lease.HistoryEpoch = &epoch
	submitGolden(t, fx, lease)

	drainGolden(t, fx)

	goldenProjectionRows(t, fx.Virtual, []string{"lease-frame", "history-lease"})
}

// TestA1GoldenResize：geometry 变更后重新投影，行内容保持。
func TestA1GoldenResize(t *testing.T) {
	fx := NewRenderTestFixture(t, WithVirtualTerminal("pt-virtual"))

	submitGolden(t, fx, goldenIntent(TransactionFrame, []byte("before-resize")))

	// resize：新 geometry 上下文，以 \n 与前一帧分隔。
	resize := goldenIntent(TransactionFrame, []byte("\nafter-resize"))
	resize.Terminal = RenderTerminalContext{
		Geometry: TerminalGeometry{Width: 60, Height: 12},
		Profile:  TerminalProfileRef{ID: "ansi", Version: 1},
	}
	submitGolden(t, fx, resize)

	drainGolden(t, fx)

	snap := fx.Virtual.Projection()
	if snap.Width != 60 || snap.Height != 12 {
		t.Fatalf("geometry after resize = %dx%d, want 60x12", snap.Width, snap.Height)
	}
	goldenProjectionRows(t, fx.Virtual, []string{"before-resize", "after-resize"})
}

// TestA1GoldenSequenceStamp：gateway 为 golden 帧分配单调 sequence，
// 投影携带最新 sequence。
func TestA1GoldenSequenceStamp(t *testing.T) {
	fx := NewRenderTestFixture(t, WithVirtualTerminal("pt-virtual"))

	r1 := submitGolden(t, fx, goldenIntent(TransactionFrame, []byte("seq-1")))
	r2 := submitGolden(t, fx, goldenIntent(TransactionFrame, []byte("seq-2")))
	drainGolden(t, fx)
	if r2.Sequence <= r1.Sequence {
		t.Fatalf("sequence not monotonic: %d <= %d", r2.Sequence, r1.Sequence)
	}
	snap := fx.Virtual.Projection()
	if snap.LastSequence != r2.Sequence {
		t.Fatalf("last sequence = %d, want %d", snap.LastSequence, r2.Sequence)
	}
	if snap.LastBatchID == "" {
		t.Fatal("last batch id must be stamped")
	}
}