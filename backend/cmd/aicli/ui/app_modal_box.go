package ui

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// 审批 / 提问面板（modal:priority:*）是 body-only 的底部 popup：正文型面板不
// 拥有输入行（ComposerLine == ""），答案走底部 prompt。这类面板此前按原始行数
// 计入底区保留高度——审批面板 9 行就撑高底区 9 行，面板顶边因此被推到屏幕中部；
// 面板既没有边框也没有底色，行格式与 transcript 完全一致，视觉上像是插进了消息
// 流中间。
//
// 这里把这类面板改成边框盒子：
//   - 盒子高度随正文自动扩展：每条正文行独立成行，超宽行按盒子内宽就地折行，
//     折行行数计入总高度。正文行越多盒子越高，底区保留高度与正文行数一一对应；
//   - 盒子高度只受终端“放得下”的上界约束（modalBoxInteriorRows）：正文 + 上下
//     边框不能挤掉底部 prompt 输入行与状态行。正文超过该上界（极端矮终端 /
//     超长选项列表）时才退化为按首行 → 正文 → 末行提示的优先级挑选原始行；
//   - 不再把多行用 " | " 合并进一条、也不用 "…" 硬截断——提问卡片此前
//     正是因此把标题/问题文本、前几条选项挤在同一行且无法换行；
//   - 盒子整行仍落在底区保留范围内、且在 prompt 输入行之上，光标归属不变。
//
// 终端放不下（矮终端 / 窄终端）时返回 nil，调用方回退到原始 popup 行块行为。
const (
	// modalBoxFitReserve 是盒子正文上界的扣除项：除上下边框外，还要为底部
	// prompt 输入行与状态行各留至少一行，正文不能把这两层挤掉。
	modalBoxFitReserve = 3
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

// modalBoxInteriorRows 是盒子正文行数的终端上界（fit ceiling）：正文按原始
// 顺序逐条保留、超宽就地折行，折行后的总行数达到该上界即停。它只取决于终端
// 高度（扣掉底部 prompt / 状态行与上下边框），保证盒子永远不会挤掉 prompt
// 输入行与状态行。实际盒子高度 = min(折行后正文行数, 该上界) + 2 边框。
func modalBoxInteriorRows(height int) int {
	if height < modalBoxMinHeight {
		return 0
	}
	rows := height - modalBoxFitReserve - 2 // 2 = 上下边框
	if rows < 1 {
		rows = 1
	}
	return rows
}

// ModalBoxMaxRows 是 priority 正文面板盒子在终端内可占的最大行数（正文上界
// + 上下边框）。它是“盒子永远不挤掉底部 prompt 输入行与状态行”这条契约的
// 唯一来源，布局测试与调用方直接引用，避免在多个包里复制上界策略。实际盒子
// 高度随正文行数自动增长，直至该上界。
func ModalBoxMaxRows(height int) int {
	interior := modalBoxInteriorRows(height)
	if interior < 1 {
		return 0
	}
	return interior + 2
}

// modalBoxLines 把活动 popup 渲染成水平居中、带边框的盒子文本行。盒子高度
// 随正文自动扩展（每条正文行一行、超宽就地折行），只受终端放得下的上界约束。
// 返回 nil 表示当前几何或 owner 不适用盒子，调用方必须回退到原始 popup 行块。
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

// modalBoxContentLines 选择盒子正文：按原始顺序逐条保留（每条正文行独立成行，
// 超宽行就地折行），行数随正文自动增长；仅当折行后的总行数超过终端上界 rows
// （极端矮终端 / 超长选项列表）时才退化为 modalBoxPriorityFolds 按优先级挑选。
// 盒子不使用 PopupViewportSpec 的 " | " 合并压缩——提问卡片曾因此把标题/问题
// 文本、前几条选项挤在同一行且无法换行——也不再用 "…" 硬截断正文。
func modalBoxContentLines(bottom BottomPaneState, rows, maxWidth int) []string {
	lines := dropBlankModalBoxLines(cloneAndSanitizePopupLines(bottom.PopupLines))
	if len(lines) == 0 {
		return nil
	}
	folded := foldAllModalBoxLines(lines, maxWidth)
	if len(folded) <= rows {
		return folded
	}
	return modalBoxPriorityFolds(lines, rows, maxWidth)
}

// foldAllModalBoxLines 按原始顺序把每一行折到 maxWidth 以内并平铺进结果，不设
// 行数上限（自动高度：盒子正文行数 = 折行后的总行数）。空行不产出。
func foldAllModalBoxLines(lines []string, maxWidth int) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, wrapModalBoxLine(line, maxWidth)...)
	}
	return out
}

// foldModalBoxLines 按原始顺序把每一行折到 maxWidth 以内并平铺进结果，累计
// 行数达到 rows 即停；空行不产出。rows 是终端上界，仅在 fit 退化路径使用。
func foldModalBoxLines(lines []string, rows, maxWidth int) []string {
	if rows <= 0 {
		return nil
	}
	all := foldAllModalBoxLines(lines, maxWidth)
	if len(all) > rows {
		return all[:rows]
	}
	return all
}

// modalBoxPriorityFolds 终端上界不足（正文折行后仍放不下）时的挑选策略：
// 最后一行（回答提示 / 决策提示）固定保留并预留其折行行数；其余行按原始顺序
// 从头填充（标题 → 问题文本 → 选项），长行折行后占用更多行，靠后的选项自然
// 让位。返回行数不超过 rows。
func modalBoxPriorityFolds(lines []string, rows, maxWidth int) []string {
	if rows <= 0 || len(lines) == 0 {
		return nil
	}
	footer := wrapModalBoxLine(lines[len(lines)-1], maxWidth)
	reserve := len(footer)
	if reserve < 1 {
		reserve = 1
	}
	headBudget := rows - reserve
	if headBudget < 0 {
		headBudget = 0
	}
	out := foldModalBoxLines(lines[:len(lines)-1], headBudget, maxWidth)
	for _, folded := range footer {
		if len(out) >= rows {
			break
		}
		out = append(out, folded)
	}
	return out
}

// wrapModalBoxLine 按显示宽度把一行折成若干连续行（每行 ≤ maxWidth）。空行
// 返回 nil。折行按 rune 宽度累积，宽度为 0 的组合符跟随当前段。
func wrapModalBoxLine(line string, maxWidth int) []string {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	if maxWidth <= 0 {
		maxWidth = 80
	}
	if DisplayWidth(line) <= maxWidth {
		return []string{line}
	}
	out := make([]string, 0, 2)
	var builder strings.Builder
	current := 0
	flush := func() {
		if builder.Len() > 0 {
			out = append(out, builder.String())
			builder.Reset()
			current = 0
		}
	}
	for _, r := range line {
		width := render.RuneWidth(r)
		if width < 0 {
			width = 0
		}
		if current > 0 && current+width > maxWidth {
			flush()
		}
		builder.WriteRune(r)
		current += width
	}
	flush()
	return out
}

// dropBlankModalBoxLines 剔除空白行但保持相对顺序。
func dropBlankModalBoxLines(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	out := lines[:0]
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
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
