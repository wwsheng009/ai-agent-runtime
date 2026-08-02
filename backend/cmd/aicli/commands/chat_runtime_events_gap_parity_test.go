package commands

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// parityItem 是双跑对比的语义块：旧路径以真实 coordinator 方法驱动，
// 新路径以等价 ChangeSet（append → completed upsert）驱动。
type parityItem struct {
	id   int
	kind encoding.ItemKind
	head string
}

// TestRenderLayer_GapParity_LegacyCoordinatorVsLayoutTranscript 固化渲染层
// 切换可行性的核心等价性：旧路径 gap 状态机（completeBlockOutput 置位 +
// writePromptGapLocked 消费）产生的空行序列 == 新路径 LayoutTranscript
// （boundary.ResolveGap 规则表）的 gap 行序列。
//
// 会话序列：user → assistant → supplement → user → assistant → assistant，
// 覆盖旧路径全部 gap 决策点：gapForTopLevelMessage（assistant/error 块）、
// gapForEventBlock（supplement/tool 块）、prompt 消费（writePromptGapLocked）、
// user echo 的 gapNone 提交。
func TestRenderLayer_GapParity_LegacyCoordinatorVsLayoutTranscript(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	// 旧路径：真实 coordinator + 真实公开方法。writePromptGapLocked 是
	// PrintPrompt 在用户输入前消费上一块残留 gap 的真实实现（unified plan
	// §7.3 记录的唯一消费点），此处直接调用以模拟 prompt 重绘的 gap 部分。
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var out bytes.Buffer
	coord.SetWriter(&out)
	coord.RenderSubmittedUserInput("U1")
	coord.RenderAssistant("A1")
	coord.RenderAsyncLine("S1")
	coord.writePromptGapLocked()
	coord.RenderSubmittedUserInput("U2")
	coord.RenderAssistant("A2")
	coord.RenderAssistant("A3")

	legacyLines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	legacyGaps := blankLinePositions(legacyLines)
	legacyContent := len(legacyLines) - len(legacyGaps)

	// 新路径：ChangeSetMapper + LayoutTranscript（真实实现；事件编码层到
	// ChangeSet 的链路已由 TestChatRuntimeEventBridge_SceneFollowsModelOrder
	// 覆盖，此处直接构造等价 ChangeSet）。
	items := []parityItem{
		{1, encoding.KindUser, "U1"},
		{2, encoding.KindAssistant, "A1"},
		{3, encoding.KindReasoning, "S1"},
		{4, encoding.KindUser, "U2"},
		{5, encoding.KindAssistant, "A2"},
		{6, encoding.KindAssistant, "A3"},
	}
	s := scene.New()
	m := scene.NewChangeSetMapper(s)
	for _, it := range items {
		id := fmt.Sprintf("item-%d", it.id)
		// append 即终态提交（INV-SCENE-04：finalize 同一 cell 且不可变），
		// 与事件序列中“流式结束 → EventAssistantMessage 提交”等价。
		item := &encoding.Item{ID: id, Seq: uint64(it.id), Kind: it.kind, Status: encoding.StatusCompleted, Head: it.head}
		if _, _, err := m.Apply(&encoding.ChangeSet{Changes: []encoding.ItemChange{{Op: encoding.OpAppend, Item: item, Revision: 1}}}); err != nil {
			t.Fatalf("append %s: %v", id, err)
		}
	}
	rows := scene.LayoutTranscript(s.Cells(), s.Revision())
	var newGaps []int
	for i, row := range rows {
		if row.Gap > 0 {
			newGaps = append(newGaps, i)
		}
	}

	if got := len(rows) - len(newGaps); got != legacyContent {
		t.Fatalf("content lines: legacy=%d layout=%d\nlegacy=%q\nlayout=%v", legacyContent, got, legacyLines, rows)
	}
	if len(legacyGaps) != len(newGaps) {
		t.Fatalf("gap count: legacy=%d layout=%d\nlegacy lines=%q\nlayout rows=%v", len(legacyGaps), len(newGaps), legacyLines, rows)
	}
	for i := range legacyGaps {
		if legacyGaps[i] != newGaps[i] {
			t.Fatalf("gap position %d: legacy=%d layout=%d\nlegacy lines=%q\nlayout rows=%v", i, legacyGaps[i], newGaps[i], legacyLines, rows)
		}
	}
	t.Logf("parity ok: %d content lines, %d gaps at %v", legacyContent, len(legacyGaps), newGaps)
}

// blankLinePositions 返回行数组中空行（gap）的索引序列。
func blankLinePositions(lines []string) []int {
	var pos []int
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			pos = append(pos, i)
		}
	}
	return pos
}
