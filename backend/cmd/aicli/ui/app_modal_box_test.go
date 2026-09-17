package ui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
)

const modalBoxTestOwner = "modal:priority:agent_input"

func modalBoxTestLines() []string {
	return []string{
		"[审批] Agent 请求执行需要授权的操作",
		"[审批] 工具：write",
		"[审批] 风险等级：高（high）",
		"[审批] 上下文：permission_mode=default",
		"[审批] 参数：content=package main",
		"[审批] 原因：permission_mode_requires_approval",
		"[审批] 目标：main.go",
		"[审批] 会话：session-1",
		"[审批] 请选择 [1] 仅本次允许  [2] 拒绝  [3] 查看完整参数（兼容 y/n）：",
	}
}

func modalBoxTestViewport(lines []string) *PopupViewportSpec {
	return &PopupViewportSpec{
		HeaderLines: []string{lines[0] + " | " + lines[1]},
		BodyLines:   append([]string{strings.Join(lines[2:5], " | ")}, lines[5:]...),
		FooterLines: []string{lines[len(lines)-1]},
	}
}

// 盒子高度随正文自动扩展：正文行数翻倍时盒子必须跟着变高（每条正文行独立成
// 行，不再按固定预算截断），且高度不超过终端上界 ModalBoxMaxRows。
func TestModalBoxLinesHeightTracksContent(t *testing.T) {
	lines := modalBoxTestLines()
	short := BottomPaneState{PopupOwner: modalBoxTestOwner, PopupLines: lines, PopupViewport: modalBoxTestViewport(lines)}
	long := BottomPaneState{PopupOwner: modalBoxTestOwner, PopupLines: append(append([]string(nil), lines...), lines...), PopupViewport: modalBoxTestViewport(lines)}

	// width=100 → 内宽 96：modalBoxTestLines 最宽约 70 列，不会折行，因此
	// 每条正文行恰好占一行，便于逐行核对内容保留。
	shortBox := modalBoxLines(short, 24, 100)
	longBox := modalBoxLines(long, 24, 100)
	if len(shortBox) == 0 || len(longBox) == 0 {
		t.Fatalf("expected modal box for priority body popup, got short=%#v long=%#v", shortBox, longBox)
	}
	if len(longBox) <= len(shortBox) {
		t.Fatalf("box height did not grow with content: short=%d long=%d", len(shortBox), len(longBox))
	}
	if maxRows := ModalBoxMaxRows(24); len(longBox) > maxRows {
		t.Fatalf("box rows %d exceed terminal ceiling %d", len(longBox), maxRows)
	}
	if len(shortBox)-2 != len(lines) || len(longBox)-2 != 2*len(lines) {
		t.Fatalf("content rows (%d / %d) don't match lines (%d / %d) — width caused wrapping: %v %v",
			len(shortBox)-2, len(longBox)-2, len(lines), 2*len(lines), shortBox, longBox)
	}
	// 每条正文行都必须独立成行且完整保留（正文翻倍 → 内容出现两轮）。
	shortContent := shortBox[1 : len(shortBox)-1]
	longContent := longBox[1 : len(longBox)-1]
	stripRow := func(row string) string {
		interior := strings.TrimPrefix(strings.TrimLeft(row, " "), "│ ")
		return strings.TrimRight(strings.TrimSuffix(interior, " │"), " ")
	}
	for i, want := range modalBoxTestLines() {
		if stripRow(shortContent[i]) != strings.TrimSpace(want) {
			t.Fatalf("short box row %d = %q, want %q\n%s", i, stripRow(shortContent[i]), want, strings.Join(shortBox, "\n"))
		}
		if stripRow(longContent[i]) != strings.TrimSpace(want) || stripRow(longContent[i+len(lines)]) != strings.TrimSpace(want) {
			t.Fatalf("long box lost round of %q\n%s", want, strings.Join(longBox, "\n"))
		}
	}
}

// 盒子必须是有边框、等宽、水平居中的整体，否则它又会读成 transcript 文本。
func TestModalBoxLinesAreBorderedCenteredAndRectangular(t *testing.T) {
	lines := modalBoxTestLines()
	state := BottomPaneState{PopupOwner: modalBoxTestOwner, PopupLines: lines, PopupViewport: modalBoxTestViewport(lines)}
	box := modalBoxLines(state, 24, 80)
	if len(box) < 3 {
		t.Fatalf("expected a box, got %#v", box)
	}

	top := strings.TrimLeft(box[0], " ")
	if !strings.HasPrefix(top, "┌") || !strings.HasSuffix(top, "┐") {
		t.Fatalf("top border = %q", box[0])
	}
	bottom := strings.TrimLeft(box[len(box)-1], " ")
	if !strings.HasPrefix(bottom, "└") || !strings.HasSuffix(bottom, "┘") {
		t.Fatalf("bottom border = %q", box[len(box)-1])
	}
	for index, line := range box[1 : len(box)-1] {
		trimmed := strings.TrimLeft(line, " ")
		if !strings.HasPrefix(trimmed, "│ ") || !strings.HasSuffix(trimmed, " │") {
			t.Fatalf("content row %d = %q, want side borders", index, line)
		}
	}

	width := DisplayWidth(box[0])
	for index, line := range box {
		if got := DisplayWidth(line); got != width {
			t.Fatalf("row %d width = %d, want %d (%q)", index, got, width, line)
		}
	}
	left := len(box[0]) - len(top)
	if want := (80 - (width - left)) / 2; left != want {
		t.Fatalf("box left padding = %d, want centered %d (box=%#v)", left, want, box)
	}
}

// 不满足盒子条件时必须回退到原始行块：拥有输入行的 popup、非 priority owner、
// 矮终端、窄终端。
func TestModalBoxLinesFallBackWhenGeometryOrOwnerDisqualifies(t *testing.T) {
	lines := modalBoxTestLines()
	viewport := modalBoxTestViewport(lines)

	cases := []struct {
		name   string
		state  BottomPaneState
		height int
		width  int
	}{
		{
			name:   "popup with composer row keeps the legacy input row",
			state:  BottomPaneState{PopupOwner: modalBoxTestOwner, PopupLines: lines, PopupViewport: viewport, ComposerLine: "approval> "},
			height: 24, width: 80,
		},
		{
			name:   "selection popup keeps the legacy block",
			state:  BottomPaneState{PopupOwner: "modal:selection", PopupLines: lines, PopupViewport: viewport},
			height: 24, width: 80,
		},
		{
			name:   "short terminal keeps the legacy block",
			state:  BottomPaneState{PopupOwner: modalBoxTestOwner, PopupLines: lines, PopupViewport: viewport},
			height: 8, width: 80,
		},
		{
			name:   "narrow terminal keeps the legacy block",
			state:  BottomPaneState{PopupOwner: modalBoxTestOwner, PopupLines: lines, PopupViewport: viewport},
			height: 24, width: 20,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if box := modalBoxLines(tc.state, tc.height, tc.width); len(box) != 0 {
				t.Fatalf("expected legacy fallback, got box %#v", box)
			}
		})
	}
}

// 端到端：审批正文面板走盒子后，底区保留高度 = 盒子实际高度（随正文自动扩展：
// 每条正文行独立成行），且盒子不超过终端上界、整块落在 prompt 输入行之上
// （既有不变量）。
func TestLayoutBottomPaneRowsPriorityPanelBoxTracksContent(t *testing.T) {
	lines := modalBoxTestLines()

	bottomFor := func(owner string) BottomPaneState {
		return BottomPaneState{
			PopupOwner:         owner,
			PopupLines:         lines,
			PopupViewport:      modalBoxTestViewport(lines),
			PromptLine:         "> ",
			PromptInput:        "1",
			PromptVisible:      true,
			PromptReservedRows: 1,
			PromptCursorKnown:  true,
		}
	}
	geometry := GeometryState{Width: 80, Height: 24, Generation: 1}

	boxed := LayoutBottomPaneRows(bottomFor(modalBoxTestOwner), geometry)

	popupRows := 0
	firstPopupRow := 0
	sawTopBorder, sawBottomBorder := false, false
	for _, row := range boxed.Rows {
		if row.Owner != renderengine.RowOwnerPopup {
			continue
		}
		popupRows++
		if firstPopupRow == 0 {
			firstPopupRow = row.Row
		}
		if row.Row >= boxed.PromptInputStartRow && boxed.PromptInputStartRow > 0 {
			t.Fatalf("approval box row %d (%q) overlaps the bottom prompt rows starting at %d",
				row.Row, row.Text, boxed.PromptInputStartRow)
		}
		if strings.Contains(row.Text, "┌") {
			sawTopBorder = true
		}
		if strings.Contains(row.Text, "└") {
			sawBottomBorder = true
		}
	}
	if maxRows := ModalBoxMaxRows(geometry.Height); popupRows == 0 || popupRows > maxRows {
		t.Fatalf("boxed popup rows = %d, want 1..%d (rows=%#v)", popupRows, maxRows, boxed.Rows)
	}
	// 每条正文行都必须渲染进盒子（宽度 80 内宽 76，测试行全部短于 76 列，
	// 不会折行）——高度随正文自动扩展而不是被预算截断。
	if want := len(lines) + 2; popupRows != want {
		t.Fatalf("boxed popup rows = %d, want %d (each body line on its own row; rows=%#v)",
			popupRows, want, boxed.Rows)
	}
	if firstPopupRow < 1 {
		t.Fatalf("approval box starts at row %d, want >= 1 (must fit on screen; rows=%#v)",
			firstPopupRow, boxed.Rows)
	}
	if !sawTopBorder || !sawBottomBorder {
		t.Fatalf("expected bordered box rows, got %#v", boxed.Rows)
	}
	t.Logf("approval box: rows=%d first=%d outputBottom=%d", popupRows, firstPopupRow, boxed.OutputBottomRow)
}

// 回归：用户报告提问卡片“问题列表没有换行”——标题/问题文本、前几条选项被
// " | " 合并进同一行（priorityPromptViewport 的语义摘要），超宽正文又被 "…"
// 截断。盒子必须保持每条问题一行：超宽行就地折行（续行计入盒子总高度，高度
// 随正文自动扩展），不得再合并或截断。
func TestModalBoxLinesWrapsQuestionListWithoutMerging(t *testing.T) {
	lines := []string{
		"[提问] Agent 需要你的补充信息",
		"[提问] 问题：状态汇报：监督（supervision）子代理控制优化这一支线已收尾",
		"[提问] 1. 启动 P1（M9）：先实现 P1-1 progress_note + report_progress",
		"[提问] 2. 把本轮实测证据（probe1 生命周期闭环、控制面清零）补录进 plan 文档",
		"[提问] 4. 其他（我来说明）",
		"[提问] 请输入回答，可输入建议编号（必答）：",
	}
	// 复刻生产者的语义 viewport（priorityPromptViewport 用 " | " 合并 Header/
	// Body）：盒子必须忽略这份合并压缩，逐条折行。
	state := BottomPaneState{
		PopupOwner:    modalBoxTestOwner,
		PopupLines:    lines,
		PopupViewport: modalBoxTestViewport(lines),
	}
	const width = 52 // 内宽 48：问题文本与选项都超宽，必须折行而不是截断
	box := modalBoxLines(state, 24, width)
	if len(box) < 3 {
		t.Fatalf("expected a bordered modal box, got %#v", box)
	}
	content := box[1 : len(box)-1]
	if maxRows := ModalBoxMaxRows(24); len(content)+2 > maxRows {
		t.Fatalf("box rows = %d, exceed terminal ceiling %d", len(content)+2, maxRows)
	}
	joined := strings.Join(content, "\n")
	// 折行后的纯文本（去掉边框与行内补白）：用于核对问题原文被完整保留。
	var plainBuilder strings.Builder
	for _, row := range content {
		interior := strings.TrimPrefix(strings.TrimLeft(row, " "), "│ ")
		interior = strings.TrimRight(strings.TrimSuffix(interior, " │"), " ")
		plainBuilder.WriteString(interior)
	}
	foldedPlain := plainBuilder.String()
	if strings.Contains(joined, " | ") {
		t.Fatalf("question lines were merged with separator:\n%s", strings.Join(box, "\n"))
	}
	// 每条编号选项独立成行：同一行不得出现两个 "[提问] N. "。
	numbered := regexp.MustCompile(`\[提问\] \d\. `)
	for _, row := range content {
		if len(numbered.FindAllStringIndex(row, -1)) > 1 {
			t.Fatalf("row merges multiple questions: %q\n%s", row, strings.Join(box, "\n"))
		}
	}
	// 高度随正文自动扩展：标题、问题、每条选项与回答提示都必须完整保留
	// （逐条独立成行，不因预算被丢弃）。
	for _, want := range lines {
		if !strings.Contains(foldedPlain, strings.TrimSpace(want)) {
			t.Fatalf("question line lost (auto-height dropped it): %q\n%s", want, strings.Join(box, "\n"))
		}
	}
	// 超宽问题行被折行保留（续行可见其尾部），而不是 "…" 截断。折行边界可能
	// 落在子串中间，因此按去掉边框/换行的整体纯文本核对。
	if !strings.Contains(foldedPlain, "子代理控制优化这一支线已收尾") {
		t.Fatalf("long question line was not wrapped (tail lost):\n%s", strings.Join(box, "\n"))
	}
	if strings.Contains(joined, "...") {
		t.Fatalf("question text truncated with ..., want wrapping:\n%s", strings.Join(box, "\n"))
	}
	if !strings.Contains(content[len(content)-1], "请输入回答") {
		t.Fatalf("answer prompt lost: %#v\n%s", content, strings.Join(box, "\n"))
	}
}
