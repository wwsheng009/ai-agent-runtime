package ui

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
)

// AppTextRow 是 ComposeAppTextLayout 输出的一行纯文本帧。Row 为 1-based
// 终端行号（与 legacy owned frame 的物理行一致），Owner 标记该行归属，
// Text 为纯文本（无 ANSI、无调试标签）。
//
// 它是任务 7 的对齐目标：legacy FixedBottomSurface 的 composed frame 与
// AppState snapshot 派生帧在此处做纯内存 parity，不涉及任何终端写。
type AppTextRow struct {
	Row   int
	Owner renderengine.RowOwner
	Text  string
}

// AppCursor 是帧的输入光标意图。Row 为 1-based 物理行；Col 为 1-based
// 物理列，0 表示"未知/尚未派生"。它只来自 Bottom.Focus 与已派生的视口
// 位置，绝不猜测 streaming 的 Stable/Enqueued/Acked 区间。
type AppCursor struct {
	Row   int
	Col   int
	Focus BottomFocus
}

// AppTextLayout 是 ComposeAppTextLayout 的输出：完整屏幕文本帧
// （transcript + active/bottom/overlay）。行数恒等于 Height，行序与
// legacy composedPlanLocked 相同（1..Height 升序），因此两者可以直接
// 按行比较。
type AppTextLayout struct {
	Width  int
	Height int
	Rows   []AppTextRow
	Cursor *AppCursor
}

// ComposeAppTextLayout 从 immutable AppLayout 派生完整文本帧。纯函数：
// 不读 surface mutex、不写 terminal、不推进 effect、不触碰任何实时状态
// （Layout 铁律）。AppLayout 本身已深拷贝，调用方可安全复用。
//
// 行序 bottom-up 对齐 legacy 权威（fixed_bottom_surface_snapshot.go
// composedPlanLocked / promptPaintPlanLocked / popupPaintPlanLocked）：
//   - 第 Height 行 = status（RowPlan 已含默认 model 文本）；
//   - status 之上为 popup 区（VisiblePopupLines + composer 行）；
//   - 再向上为 prompt 区（band → notice → dynamic status → margins →
//     prompt 输入行，composer 可见时整体跳过）；
//   - 剩余 1..OutputBottomRow 由 transcript 语义行尾部对齐填充，
//     boundary gap 行输出为空行（Owner=Gap）。
func ComposeAppTextLayout(layout AppLayout) AppTextLayout {
	out := AppTextLayout{
		Width:  layout.Geometry.Width,
		Height: layout.Geometry.Height,
	}
	if out.Width < 1 {
		out.Width = 80
	}
	if out.Height < 1 {
		return out
	}

	frame := make([]AppTextRow, out.Height)
	for row := range frame {
		frame[row] = AppTextRow{Row: row + 1, Owner: renderengine.RowOwnerGap}
	}

	// Bottom pane：直接复用纯布局 RowPlan（文本行 + Gap 填充行）。
	plan := layout.Bottom.RowPlan
	firstRow := plan.OutputBottomRow + 1
	if firstRow < 1 {
		firstRow = 1
	}
	promptStartRow := 0
	popupLastRow := 0
	for _, row := range plan.Rows {
		if row.Row < firstRow || row.Row > out.Height {
			continue
		}
		if row.Owner == renderengine.RowOwnerPrompt && promptStartRow == 0 {
			promptStartRow = row.Row
		}
		if row.Owner == renderengine.RowOwnerPopup {
			popupLastRow = row.Row
		}
		frame[row.Row-1] = AppTextRow{Row: row.Row, Owner: row.Owner, Text: row.Text}
	}

	// Transcript：尾部对齐 OutputBottomRow，从下往上填充；超出的最旧
	// 语义行被丢弃（与 legacy 滚动语义一致）。
	bottomRow := plan.OutputBottomRow
	if bottomRow > out.Height-1 {
		bottomRow = out.Height - 1
	}
	if bottomRow < 0 {
		bottomRow = 0
	}
	transcriptRows := layout.Transcript
	start := len(transcriptRows) - bottomRow
	if start < 0 {
		start = 0
	}
	for row := 1; row <= bottomRow; row++ {
		index := start + row - 1
		owner := renderengine.RowOwnerTranscript
		text := ""
		if index >= 0 && index < len(transcriptRows) {
			text = transcriptRows[index].Text
			if transcriptRows[index].Gap > 0 {
				owner = renderengine.RowOwnerGap
			}
		}
		frame[row-1] = AppTextRow{Row: row, Owner: owner, Text: text}
	}

	out.Rows = frame
	out.Cursor = composeAppCursor(layout, promptStartRow, popupLastRow)
	return out
}

// composeAppCursor 派生输入光标意图。promptStartRow 是 RowPlan 中 prompt
// 输入区第一行（notice 行虽同为 RowOwnerPrompt，但 RowPlan 顺序保证首个
// Prompt owner 行即 promptStart，见 layoutBottomPanePromptRows）。
//
// 列坐标语义：PromptCursorCol 来自纯布局的视觉位置派生；Col 输出
// PromptCursorCol+1（1-based 物理列）。若 PromptCursorKnown 为假或列未知，
// Col 保持 0（未知），由未来 Presenter 阶段补齐物理光标放置。
func composeAppCursor(layout AppLayout, promptStartRow, popupLastRow int) *AppCursor {
	bottom := layout.Bottom
	switch bottom.CursorFocus {
	case BottomFocusPrompt:
		row := promptStartRow
		col := 0
		if bottom.State.PromptCursorKnown && bottom.State.PromptCursorRow >= 0 {
			row += bottom.State.PromptCursorRow
		}
		if row < 1 {
			row = 1
		}
		if bottom.State.PromptCursorKnown && bottom.State.PromptCursorCol >= 0 {
			col = bottom.State.PromptCursorCol + 1
		}
		return &AppCursor{Row: row, Col: col, Focus: BottomFocusPrompt}
	case BottomFocusPopup:
		if popupLastRow < 1 {
			return nil
		}
		text := ""
		for _, row := range bottom.RowPlan.Rows {
			if row.Row == popupLastRow {
				text = row.Text
			}
		}
		return &AppCursor{Row: popupLastRow, Col: DisplayWidth(text) + 1, Focus: BottomFocusPopup}
	default:
		return nil
	}
}

// FrameParityWithAppLayout 比较 legacy owned frame（本 surface 的 composed
// plan）与 AppState snapshot 派生帧，返回逐行差异的纯文本报告；完全一致时
// 返回 "parity: identical"。仅做内存比较，不写 terminal，不影响生产渲染。
//
// 这是任务 7 的 shadow 钩子：/debug 或测试可调用它验证 AppState 快照是否
// 完整捕获了 legacy 布局；报告的差异即快照覆盖缺口（例如 legacy history
// buffer 与 AppState.Transcript 数据源尚未同源时的行数差异）。
func (s *FixedBottomSurface) FrameParityWithAppLayout(layout AppLayout) string {
	if s == nil || s.terminal == nil {
		return "parity: surface unavailable"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || !s.ownedViewport {
		return "parity: owned viewport inactive"
	}
	width, height := s.terminal.Width(), s.terminal.Height()
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 24
	}
	legacy := s.composedPlanLocked(width, height, false)
	derived := ComposeAppTextLayout(layout)
	var b strings.Builder
	count := 0
	for row := 1; row <= height; row++ {
		var legacyText string
		if row-1 < len(legacy) {
			legacyText = cellRowPlainText(legacy[row-1].Cells)
		}
		var derivedText string
		if row-1 < len(derived.Rows) {
			derivedText = derived.Rows[row-1].Text
		}
		if legacyText != derivedText {
			count++
			if count <= 40 {
				fmt.Fprintf(&b, "row %3d legacy=%q derived=%q\n", row, legacyText, derivedText)
			}
		}
	}
	if count == 0 {
		return "parity: identical"
	}
	if count > 40 {
		fmt.Fprintf(&b, "... %d more differing rows\n", count-40)
	}
	return fmt.Sprintf("parity: %d differing row(s)\n%s", count, b.String())
}
