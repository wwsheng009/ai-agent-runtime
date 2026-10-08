package commands

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// 本文件是 FixedBottomSurface 保留区布局中性（layout neutral）契约的测试。
//
// L3-2 起物理绘制退役，旧观察面（把 stdout 字节回放到 vt.Screen 再看行）
// 恒为空屏；新的权威观察面是 ComposedFrameForTest()：历史 + 底部保留区 +
// 提示行的合成帧，与生产快照路径同源。布局中性 = 一次 band / popup /
// settle / 软尾重写周期结束后，合成帧必须与「从未展示过该保留区」的基线
// 逐行一致，且已提交的历史行不得被覆盖、重复或乱序。

// assertFrameCommittedRowsIntact 断言每个已提交 marker 在合成帧里恰好出现
// 一次且严格按序，返回最后一个 marker 的行号。
func assertFrameCommittedRowsIntact(t *testing.T, surface *ui.FixedBottomSurface, markers []string, stage string) int {
	t.Helper()
	lines := composedFrameLines(surface)
	last := 0
	for _, marker := range markers {
		rows := frameRowsContaining(lines, marker)
		if len(rows) != 1 {
			t.Fatalf("%s: expected %q exactly once in composed frame, got rows %v\n%s",
				stage, marker, rows, strings.Join(lines, "\n"))
		}
		if rows[0] <= last {
			t.Fatalf("%s: %q landed on frame row %d, out of order after row %d\n%s",
				stage, marker, rows[0], last, strings.Join(lines, "\n"))
		}
		last = rows[0]
	}
	return last
}

func surfaceBandLines(count int) []string {
	lines := make([]string, 0, count)
	for i := 1; i <= count; i++ {
		lines = append(lines, fmt.Sprintf("band-%02d", i))
	}
	return lines
}

func writeSurfaceTranscriptRow(t *testing.T, surface *ui.FixedBottomSurface, marker string) {
	t.Helper()
	if _, err, ok := surface.WriteOutput(io.Discard, marker+" committed transcript row\n"); !ok || err != nil {
		t.Fatalf("WriteOutput(%s): ok=%t err=%v", marker, ok, err)
	}
}

// surfaceLayoutBaselineFrame 是布局 oracle：同一批已提交行写在一个从未
// 扩展过底部保留区的新 surface 上，返回其合成帧。任何 band / popup /
// settle 周期都必须把布局留在与这份基线相同的行上。
func surfaceLayoutBaselineFrame(t *testing.T, width, height int, markers []string) []string {
	t.Helper()
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.ShowPrompt("> ")
	surface.ClearPromptRows(1)
	for _, marker := range markers {
		writeSurfaceTranscriptRow(t, surface, marker)
	}
	surface.ShowPrompt("> ")
	assertFrameCommittedRowsIntact(t, surface, markers, "baseline")
	return composedFrameLines(surface)
}

// TestFixedBottomSurface_ActiveBandIsLayoutNeutral 用合成帧观察一次流式
// 回合：band 增长时已提交行不可逆（不得被覆盖或重复），band 释放后整体
// 布局必须与从未出现过 band 的基线一致。
func TestFixedBottomSurface_ActiveBandIsLayoutNeutral(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width = 80

	for _, height := range []int{24, 40} {
		t.Run(fmt.Sprintf("height=%d", height), func(t *testing.T) {
			markers := make([]string, 0, 8)
			for i := 1; i <= 8; i++ {
				markers = append(markers, fmt.Sprintf("L%02d", i))
			}

			baseFrame := surfaceLayoutBaselineFrame(t, width, height, markers)

			// Live: the band grows over the trailing blank output row, more rows
			// commit underneath it, then the band is released and the prompt
			// returns.
			surface := ui.NewFixedBottomSurface(ui.NewTerminal())
			surface.EnableForTest(width, height)
			surface.ShowPrompt("> ")
			surface.ClearPromptRows(1)
			for _, marker := range markers[:5] {
				writeSurfaceTranscriptRow(t, surface, marker)
			}
			surface.SetActiveBand(surfaceBandLines(6))
			for _, marker := range markers[5:] {
				writeSurfaceTranscriptRow(t, surface, marker)
			}
			bandLast := assertFrameCommittedRowsIntact(t, surface, markers, "band growth")
			for _, band := range surfaceBandLines(6) {
				assertFrameMarkerOnce(t, "band growth", surface, band)
			}
			// One blank row is expected: the output region bottom row is the
			// position every writer targets, so it stays empty between writes.
			// Anything larger is a reserve-growth hole.
			if bandStart := frameRowOf(t, surface, "band-01"); bandStart-bandLast > 2 {
				t.Fatalf("transcript left a %d-row hole above the active band\n%s",
					bandStart-bandLast-1, composedFrameText(surface))
			}

			surface.ClearActiveBand()
			surface.ShowPrompt("> ")
			assertFrameCommittedRowsIntact(t, surface, markers, "band release")
			for _, band := range surfaceBandLines(6) {
				if rows := frameRowsContaining(composedFrameLines(surface), band); len(rows) != 0 {
					t.Fatalf("released band row %q still in composed frame at %v\n%s",
						band, rows, composedFrameText(surface))
				}
			}
			assertComposedFramesEqual(t, "band cycle", composedFrameLines(surface), baseFrame)
		})
	}
}

// A command popup reserves rows the same way an active band does, but closing it
// deliberately defers the shrink compensation: the slash-completion popup is
// rebuilt on every keystroke, and flushing on close would bounce the whole
// transcript up and down while the user types. The contract is therefore
// "repaid at the next output write" — and after that write the layout must be
// indistinguishable from a run that never opened a popup.
func TestFixedBottomSurface_PopupIsLayoutNeutral(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width = 80

	for _, height := range []int{24, 40} {
		t.Run(fmt.Sprintf("height=%d", height), func(t *testing.T) {
			markers := make([]string, 0, 6)
			for i := 1; i <= 6; i++ {
				markers = append(markers, fmt.Sprintf("L%02d", i))
			}
			popup := []string{"popup-01", "popup-02", "popup-03", "popup-04"}

			// Baseline: the same turn boundary sequence, no popup.
			baseSurface := ui.NewFixedBottomSurface(ui.NewTerminal())
			baseSurface.EnableForTest(width, height)
			baseSurface.ShowPrompt("> ")
			baseSurface.ClearPromptRows(1)
			for _, marker := range markers[:5] {
				writeSurfaceTranscriptRow(t, baseSurface, marker)
			}
			baseSurface.ShowPrompt("> ")
			baseSurface.ClearPromptRows(1)
			writeSurfaceTranscriptRow(t, baseSurface, markers[5])
			baseSurface.ShowPrompt("> ")
			assertFrameCommittedRowsIntact(t, baseSurface, markers, "baseline")
			baseFrame := composedFrameLines(baseSurface)

			surface := ui.NewFixedBottomSurface(ui.NewTerminal())
			surface.EnableForTest(width, height)
			surface.ShowPrompt("> ")
			surface.ClearPromptRows(1)
			for _, marker := range markers[:5] {
				writeSurfaceTranscriptRow(t, surface, marker)
			}
			surface.ShowPrompt("> ")
			surface.ShowPopup(popup)
			assertFrameCommittedRowsIntact(t, surface, markers[:5], "popup open")
			for _, line := range popup {
				assertFrameMarkerOnce(t, "popup open", surface, line)
			}

			surface.ClearPopup()
			assertFrameCommittedRowsIntact(t, surface, markers[:5], "popup close")
			for _, line := range popup {
				if rows := frameRowsContaining(composedFrameLines(surface), line); len(rows) != 0 {
					t.Fatalf("closed popup row %q still in composed frame at %v\n%s",
						line, rows, composedFrameText(surface))
				}
			}

			// Next turn: submitting input clears the prompt and the reply write
			// pays the deferred popup compensation.
			surface.ClearPromptRows(1)
			writeSurfaceTranscriptRow(t, surface, markers[5])
			surface.ShowPrompt("> ")
			assertFrameCommittedRowsIntact(t, surface, markers, "popup debt repaid")
			assertComposedFramesEqual(t, "popup cycle", composedFrameLines(surface), baseFrame)
		})
	}
}

// History / resume replay runs after the prompt rows were cleared, which leaves
// deferred shrink compensation. SettleOutputDebt has to absorb that debt before
// the first already-final message is written, so the replayed transcript ends up
// exactly where a plain run would put it.
func TestFixedBottomSurface_HistorySettleIsLayoutNeutral(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width = 80

	for _, height := range []int{24, 40} {
		t.Run(fmt.Sprintf("height=%d", height), func(t *testing.T) {
			markers := make([]string, 0, 6)
			for i := 1; i <= 6; i++ {
				markers = append(markers, fmt.Sprintf("L%02d", i))
			}
			baseFrame := surfaceLayoutBaselineFrame(t, width, height, markers)

			surface := ui.NewFixedBottomSurface(ui.NewTerminal())
			surface.EnableForTest(width, height)
			surface.ShowPrompt("> ")
			for _, marker := range markers[:2] {
				writeSurfaceTranscriptRow(t, surface, marker)
			}
			// /resume: prompt rows are cleared, then already-final history is
			// replayed after settling layout debt.
			surface.ClearPromptRows(1)
			surface.SettleOutputDebt()
			for _, marker := range markers[2:] {
				writeSurfaceTranscriptRow(t, surface, marker)
			}
			surface.ShowPrompt("> ")
			assertFrameCommittedRowsIntact(t, surface, markers, "history settle")
			assertComposedFramesEqual(t, "history settle", composedFrameLines(surface), baseFrame)
		})
	}
}

// The composite path users actually hit: /resume replays history, then the next
// turn streams a reply behind an active band. Both paths carry their own layout
// debt (settle before replay, absorb + deferred shrink during the turn), so the
// interesting failure mode is the interaction — a debt from the replay being
// paid inside the streaming turn, or vice versa.
func TestFixedBottomSurface_HistoryReplayThenStreamingTurnIsLayoutNeutral(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width = 80

	for _, height := range []int{24, 40} {
		t.Run(fmt.Sprintf("height=%d", height), func(t *testing.T) {
			history := []string{"H01", "H02", "H03"}
			reply := []string{"R01", "R02", "R03"}
			all := append(append([]string{}, history...), reply...)
			baseFrame := surfaceLayoutBaselineFrame(t, width, height, all)

			surface := ui.NewFixedBottomSurface(ui.NewTerminal())
			surface.EnableForTest(width, height)

			// /resume: clear the prompt, settle layout debt, replay final history.
			surface.ShowPrompt("> ")
			surface.ClearPromptRows(1)
			surface.SettleOutputDebt()
			for _, marker := range history {
				writeSurfaceTranscriptRow(t, surface, marker)
			}
			surface.ShowPrompt("> ")
			assertFrameCommittedRowsIntact(t, surface, history, "history replay")

			// Next turn: submit, stream behind an active band, release, restore.
			surface.ClearPromptRows(1)
			surface.SetActiveBand(surfaceBandLines(4))
			for _, marker := range reply {
				writeSurfaceTranscriptRow(t, surface, marker)
			}
			surface.ClearActiveBand()
			surface.ShowPrompt("> ")
			assertFrameCommittedRowsIntact(t, surface, all, "streaming turn")
			for _, band := range surfaceBandLines(4) {
				if rows := frameRowsContaining(composedFrameLines(surface), band); len(rows) != 0 {
					t.Fatalf("released band row %q still in composed frame at %v\n%s",
						band, rows, composedFrameText(surface))
				}
			}
			assertComposedFramesEqual(t, "history replay + streaming turn", composedFrameLines(surface), baseFrame)
		})
	}
}

// Progressive markdown streaming does not append line by line: it writes a soft
// tail and rewrites it in place on every stable-commit cut. That rewrite has to
// locate the rows the tail currently occupies, which depends on whether the
// output cursor is parked on a blank row — exactly the state band growth mutates
// when it absorbs that blank. The composed frame is the only place where a
// one-row error there is visible after the physical paint retired.
func TestFixedBottomSurface_SoftTailRewriteUnderActiveBandIsLayoutNeutral(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width = 80
	row := func(marker string) string { return marker + " committed transcript row" }

	for _, height := range []int{24, 40} {
		t.Run(fmt.Sprintf("height=%d", height), func(t *testing.T) {
			markers := []string{"C01", "S01", "S02"}
			baseFrame := surfaceLayoutBaselineFrame(t, width, height, markers)

			surface := ui.NewFixedBottomSurface(ui.NewTerminal())
			surface.EnableForTest(width, height)
			surface.ShowPrompt("> ")
			surface.ClearPromptRows(1)
			writeSurfaceTranscriptRow(t, surface, "C01")
			// Band growth absorbs the trailing blank row here.
			surface.SetActiveBand(surfaceBandLines(4))
			if _, err, ok := surface.WriteSoftTrackedOutput(io.Discard, row("D01-draft")+"\n"); !ok || err != nil {
				t.Fatalf("WriteSoftTrackedOutput: ok=%t err=%v", ok, err)
			}
			assertFrameCommittedRowsIntact(t, surface, []string{"C01", "D01-draft"}, "soft draft")

			// Stable commit: the draft tail is replaced by two final rows.
			if !surface.RewriteSoftOutputTail(io.Discard, []string{row("S01"), row("S02")}) {
				t.Fatal("expected soft tail rewrite to be accepted")
			}
			if rows := frameRowsContaining(composedFrameLines(surface), "D01-draft"); len(rows) != 0 {
				t.Fatalf("stale draft row still in composed frame at %v\n%s", rows, composedFrameText(surface))
			}
			assertFrameCommittedRowsIntact(t, surface, markers, "soft rewrite")

			surface.ClearActiveBand()
			surface.ShowPrompt("> ")
			assertFrameCommittedRowsIntact(t, surface, markers, "band release")
			assertComposedFramesEqual(t, "soft tail rewrite under band", composedFrameLines(surface), baseFrame)
		})
	}
}
