package ui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// TestBottomReserveShrinkRestoresHistoryWithoutBlankingTop is the production
// regression for the former CSI-T compensation bug. A full output viewport is
// composed from retained history while ActiveBand grows and shrinks; the older
// top rows must return instead of being replaced by inserted blanks.
//
// 语义说明（Phase 0，§4.2 规则 3）：本场景**不做**"语义行 ≤1 次"断言——
// band 出现导致可见区收缩时，顶部行可能被物理滚出（进入终端 scrollback），
// band 消失时从模型恢复上屏；这两段字节流中同一行出现两次是**设计允许**
// 的（语义上未 handoff，恢复上屏不算重放）。本测试的语义断言是：
// (1) 恢复后屏幕 1..9 行必须完整还原 L1..L9（不得顶部空白）；
// (2) 不得发出 CSI-T 滚动补偿；
// (3) 生产帧与 vt.Screen 回放零差异。
func TestBottomReserveShrinkRestoresHistoryWithoutBlankingTop(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width, height = 20, 10
	surface := newOwnedTestFixedBottomSurfaceWithSize(width, height)
	screen := vt.NewScreen(width, height)
	feed := func(paint func()) string {
		t.Helper()
		output := captureUIStdout(t, paint)
		screen.Feed(output)
		return output
	}

	feed(func() {
		lines := make([]string, height-1)
		for i := range lines {
			lines[i] = fmt.Sprintf("L%d", i+1)
		}
		if _, err, ok := surface.WriteOutput(os.Stdout, strings.Join(lines, "\n")+"\n"); !ok || err != nil {
			t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
		}
	})
	if got := strings.TrimSpace(screen.Line(1)); got != "L1" {
		t.Fatalf("precondition: row1=%q want L1\n%s", got, screen.Dump())
	}

	feed(func() {
		surface.SetActiveBand([]string{"B1", "B2", "B3"})
	})
	surface.mu.Lock()
	shortState := surface.bottomPaneStateLocked()
	if got := shortState.activeBandTopGapRowCount(); got != 0 {
		surface.mu.Unlock()
		t.Fatalf("height %d must collapse ActiveBand top gap, got %d", height, got)
	}
	if got := surface.bottomRowsLocked(); got != 4 {
		surface.mu.Unlock()
		t.Fatalf("short bottom rows=%d want status(1)+band(3)", got)
	}
	surface.mu.Unlock()
	shrinkOutput := feed(func() {
		surface.ClearActiveBand()
	})
	if strings.Contains(shrinkOutput, terminalScrollDownSequence(1)) ||
		strings.Contains(shrinkOutput, terminalScrollDownSequence(2)) ||
		strings.Contains(shrinkOutput, terminalScrollDownSequence(3)) {
		t.Fatalf("owned shrink must not emit terminal scroll-down compensation: %q", shrinkOutput)
	}
	for i := 1; i <= height-1; i++ {
		want := fmt.Sprintf("L%d", i)
		if got := strings.TrimSpace(screen.Line(i)); got != want {
			t.Fatalf("row %d=%q want %q after grow/shrink\n%s", i, got, want, screen.Dump())
		}
	}
	if differences := frameCellDifferences(surface.ComposedFrameForTest(), screen.CellRows(1, height)); differences != 0 {
		t.Fatalf("production frame differs from owned composition: differences=%d\n%s", differences, screen.Dump())
	}
}

// TestOwnedSettleOutputDebtIsPureRecompose pins that SettleOutputDebt on the
// production owned path does no legacy CSI-T compensation.
func TestOwnedSettleOutputDebtIsPureRecompose(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected prompt")
		}
		if !surface.ClearPromptRows(1) {
			t.Fatal("expected prompt clear")
		}
		surface.BeginOutput()
	})
	if surface.LegacyReserveStateForTest().PendingScrollDownRows != 0 {
		t.Fatalf("owned SettleOutputDebt must not accumulate pending compensation, got %d", surface.LegacyReserveStateForTest().PendingScrollDownRows)
	}
	settled := captureUIStdout(t, func() {
		surface.SettleOutputDebt()
	})
	if strings.Contains(settled, terminalScrollDownSequence(1)) ||
		strings.Contains(settled, terminalScrollDownSequence(2)) ||
		strings.Contains(settled, terminalScrollDownSequence(3)) {
		t.Fatalf("owned SettleOutputDebt must not emit scroll-down compensation: %q", settled)
	}
	if surface.LegacyReserveStateForTest().CursorOnBlankRow {
		t.Fatal("owned settle must not set blank-row flag")
	}
}
