package ui

import (
	"strings"
)

// 审批 / 提问面板（modal:priority:*）是 body-only 的底部 popup：正文型面板不
// 拥有输入行（ComposerLine == ""），答案走底部 prompt。这类面板此前按原始行数
// 计入底区保留高度——审批面板 9 行就撑高底区 9 行，面板顶边因此被推到屏幕中部；
// 面板既没有边框也没有底色，行格式与 transcript 完全一致，视觉上像是插进了消息
// 流中间。
//
// 这里把这类面板改成固定预算的边框盒子：
//   - 盒子高度只由终端高度决定（modalBoxInteriorRows），不随正文行数增长，
//     底区保留高度因此有上界，面板不会再因为正文变长而顶到屏幕中部；
//   - 正文超出预算时按 PopupViewportSpec 语义压缩（header/body/footer + anchor），
//     与 VisiblePopupLines 既有的压缩语义一致；
//   - 盒子整行仍落在底区保留范围内、且在 prompt 输入行之上，光标归属不变。
//
// 预算放不下（矮终端 / 窄终端）时返回 nil，调用方回退到原始 popup 行块行为。
const (
	// modalBoxMinInteriorRows / modalBoxMaxInteriorRows 是盒子正文行的固定预算。
	modalBoxMinInteriorRows = 2
	modalBoxMaxInteriorRows = 5
	// modalBoxMinHeight 是启用盒子的最小终端高度。更矮的终端保留原始行块，
	// 避免盒子挤掉 prompt 与状态行。
	modalBoxMinHeight = 12
	// modalBoxMinWidth 是启用盒子的最小终端宽度。
	modalBoxMinWidth = 28
	// modalBoxMinInteriorWidth 是盒子正文列的最小宽度。
	modalBoxMinInteriorWidth = 16
)

// popupRendersAsModalBox 报告当前活动 popup 是否按边框盒子呈现。只有正文型
// （无 ComposerLine）的 priority 面板适用：拥有输入行的 popup 仍然需要按底区
// 输入行排布，不能改成纯正文盒子。
func popupRendersAsModalBox(bottom BottomPaneState) bool {
	if strings.TrimSpace(bottom.ComposerLine) != "" || len(bottom.PopupLines) == 0 {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(bottom.PopupOwner)), "modal:priority:")
}

// modalBoxInteriorRows 是盒子正文行的固定预算，只取决于终端高度，与正文行数
// 无关。这是“面板不再撑高底区”的关键：正文再多也只占这个预算。
func modalBoxInteriorRows(height int) int {
	if height < modalBoxMinHeight {
		return 0
	}
	rows := height / 3
	if rows > modalBoxMaxInteriorRows {
		rows = modalBoxMaxInteriorRows
	}
	if rows < modalBoxMinInteriorRows {
		rows = modalBoxMinInteriorRows
	}
	return rows
}

// ModalBoxMaxRows 是 priority 正文面板盒子的行数上界（正文预算 + 上下边框）。
// 它是“面板高度与正文行数无关”这条契约的唯一来源，布局测试与调用方直接引用，
// 避免在多个包里复制预算策略。
func ModalBoxMaxRows(height int) int {
	interior := modalBoxInteriorRows(height)
	if interior < 1 {
		return 0
	}
	return interior + 2
}

// modalBoxLines 把活动 popup 渲染成水平居中、带边框的盒子文本行。返回 nil
// 表示当前几何或 owner 不适用盒子，调用方必须回退到原始 popup 行块。
func modalBoxLines(bottom BottomPaneState, height, width int) []string {
	if !popupRendersAsModalBox(bottom) {
		return nil
	}
	interior := modalBoxInteriorRows(height)
	if interior < 1 || width < modalBoxMinWidth {
		return nil
	}
	content := modalBoxContentLines(bottom, interior, width-4)
	if len(content) == 0 {
		return nil
	}
	boxWidth := modalBoxWidth(content, width)
	if boxWidth < modalBoxMinInteriorWidth+4 {
		return nil
	}
	inner := boxWidth - 4
	left := (width - boxWidth) / 2
	if left < 0 {
		left = 0
	}
	prefix := strings.Repeat(" ", left)

	lines := make([]string, 0, len(content)+2)
	lines = append(lines, prefix+"┌"+strings.Repeat("─", boxWidth-2)+"┐")
	for _, line := range content {
		lines = append(lines, prefix+"│ "+padModalBoxLine(truncateFixedPopupLine(line, inner), inner)+" │")
	}
	lines = append(lines, prefix+"└"+strings.Repeat("─", boxWidth-2)+"┘")
	return lines
}

// modalBoxContentLines 选择盒子正文：预算足够时保留全部行，否则先按 viewport
// 语义压缩，再退回首尾压缩。
func modalBoxContentLines(bottom BottomPaneState, rows, maxWidth int) []string {
	lines := cloneAndSanitizePopupLines(bottom.PopupLines)
	if len(lines) == 0 {
		return nil
	}
	if len(lines) > rows {
		if semantic := visibleSemanticPopupLines(bottom.PopupViewport, rows); len(semantic) > 0 {
			lines = semantic
		} else {
			lines = compactPopupHeadTail(lines, rows)
		}
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, truncateFixedPopupLine(line, maxWidth))
		if len(out) >= rows {
			break
		}
	}
	return out
}

// compactPopupHeadTail 是 VisiblePopupLines 的纯文本压缩分支，独立出来供盒子
// 使用：首行 + 省略号 + 末尾若干行。
func compactPopupHeadTail(lines []string, rows int) []string {
	if rows <= 0 {
		return nil
	}
	if len(lines) <= rows {
		return lines
	}
	if rows == 1 {
		return []string{lines[len(lines)-1]}
	}
	if rows == 2 {
		return []string{lines[0], lines[len(lines)-1]}
	}
	out := make([]string, 0, rows)
	out = append(out, lines[0], "...")
	tail := rows - 2
	start := len(lines) - tail
	if start < 1 {
		start = 1
	}
	out = append(out, lines[start:]...)
	return out
}

// modalBoxWidth 取最宽正文行加边框与内边距，并限制在终端宽度内。
func modalBoxWidth(content []string, width int) int {
	maxContent := 0
	for _, line := range content {
		if w := DisplayWidth(line); w > maxContent {
			maxContent = w
		}
	}
	boxWidth := maxContent + 4
	if boxWidth > width {
		boxWidth = width
	}
	return boxWidth
}

func padModalBoxLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	if pad := width - DisplayWidth(line); pad > 0 {
		return line + strings.Repeat(" ", pad)
	}
	return line
}
