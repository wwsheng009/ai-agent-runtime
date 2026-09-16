package ui

import (
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

// 面板高度必须只由终端高度决定：正文行数翻倍也不得让盒子变高，否则底区保留
// 高度会被正文撑高，面板顶边再次上移到屏幕中部。
func TestModalBoxLinesBudgetIsIndependentOfContentLength(t *testing.T) {
	lines := modalBoxTestLines()
	short := BottomPaneState{PopupOwner: modalBoxTestOwner, PopupLines: lines, PopupViewport: modalBoxTestViewport(lines)}
	long := BottomPaneState{PopupOwner: modalBoxTestOwner, PopupLines: append(append([]string(nil), lines...), lines...), PopupViewport: modalBoxTestViewport(lines)}

	shortBox := modalBoxLines(short, 24, 80)
	longBox := modalBoxLines(long, 24, 80)
	if len(shortBox) == 0 || len(longBox) == 0 {
		t.Fatalf("expected modal box for priority body popup, got short=%#v long=%#v", shortBox, longBox)
	}
	if len(longBox) != len(shortBox) {
		t.Fatalf("box height grew with content: short=%d long=%d", len(shortBox), len(longBox))
	}
	budget := modalBoxInteriorRows(24)
	if len(shortBox) > budget+2 {
		t.Fatalf("box rows %d exceed budget %d (+2 borders)", len(shortBox), budget)
	}
	if len(shortBox) < 3 {
		t.Fatalf("box rows %d too small to carry a border and content", len(shortBox))
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

// 端到端：审批正文面板走盒子后，底区保留高度不再等于原始正文行数，且 popup 行
// 仍整体落在 prompt 输入行之上（既有不变量）。
func TestLayoutBottomPaneRowsBoundsPriorityPanelReserve(t *testing.T) {
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
	legacy := LayoutBottomPaneRows(bottomFor("modal:selection"), geometry)

	if boxed.OutputBottomRow <= legacy.OutputBottomRow {
		t.Fatalf("boxed reserve did not shrink: boxed outputBottom=%d legacy=%d",
			boxed.OutputBottomRow, legacy.OutputBottomRow)
	}

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
	if !sawTopBorder || !sawBottomBorder {
		t.Fatalf("expected bordered box rows, got %#v", boxed.Rows)
	}
	t.Logf("approval box: rows=%d first=%d outputBottom=%d (legacy reserve outputBottom=%d)",
		popupRows, firstPopupRow, boxed.OutputBottomRow, legacy.OutputBottomRow)
	// 用户报告的缺陷是面板出现在屏幕中部：预算盒子必须停在屏幕下半部分，
	// 而不是把顶边推到中线以上。
	if firstPopupRow <= geometry.Height/2 {
		t.Fatalf("approval box starts at row %d, want below the screen midpoint %d (rows=%#v)",
			firstPopupRow, geometry.Height/2, boxed.Rows)
	}
}
