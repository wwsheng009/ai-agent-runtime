package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// 需求（真实用户报告）：**长文本**在终端宽度变化后会重新折行，同一份源文本被
// 切成不同的 fragment 身份。此前所有 resize 回归测试都使用短 marker（两种宽度
// 下都不换行），因此 SourceRange 不变、无法暴露该缺陷。
//
// 判据：每个语义标记在 scrollback + 可见屏中恰好出现一次。
func TestNativeScrollbackResizeReflowKeepsWrappedHistoryExactlyOnce(t *testing.T) {
	const (
		initialWidth  = 52
		resizedWidth  = 24
		initialHeight = 10
		resizedHeight = 14
	)
	// 每行放一个短标记，词间用空格分隔，使长行在两种宽度下都能安全折行，
	// 同时标记本身不会被拆断（便于精确计数）。
	markers := make([]string, 12)
	lines := make([]string, 12)
	for index := range markers {
		markers[index] = fmt.Sprintf("WRAP-REFLOW-%02d", index+1)
		lines[index] = markers[index] + " " + strings.Repeat("wrap ", 26) + "tail"
	}
	source := strings.Join(lines, "\n")
	cell := &scene.TranscriptCell{
		ID: 902, Revision: 1, Kind: scene.KindAssistant,
		Source: source, Phase: scene.CellCommitted,
	}

	h := newNativeScrollbackRegressionHarness(t)
	h.post(t,
		Resize{Width: initialWidth, Height: initialHeight, Generation: 1},
		ShowPromptAction{Line: "> "},
		ReplaceTranscriptAction{Snapshot: regressionCommittedSnapshot(1, cell)},
	)
	h.flush()

	initial := h.controller.State()
	if initial.HistoryEffects.HasPending() {
		t.Fatalf("initial delivery still pending: %+v", initial.HistoryEffects)
	}
	initialBytes := h.physical.Len()

	// 宽度变化：conhost/终端会 reflow 已交付行，布局也会重新折行。
	h.post(t, Resize{Width: resizedWidth, Height: resizedHeight, Generation: 2})
	h.flush()

	raw := h.physical.String()
	if h.physical.Len() <= initialBytes {
		t.Fatalf("width resize produced no repaint: %q", string([]rune(raw)))
	}
	// 关键断言：换行后的同一段内容不得被再次写入原生 scrollback。
	for _, marker := range markers {
		if count := strings.Count(raw, marker); count != 1 {
			t.Fatalf("wrapped history marker %q crossed the writer %d times after reflow, want exactly 1",
				marker, count)
		}
	}
	if state := h.controller.State(); state.HistoryEffects.ProjectionUnknown {
		t.Fatalf("resize left projection unknown: %+v", state.HistoryEffects)
	}
}
