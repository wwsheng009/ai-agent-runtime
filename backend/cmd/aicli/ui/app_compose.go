package ui

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
)

// AppCursor 是帧的输入光标意图。Row 为 1-based 物理行；Col 为 1-based
// 物理列，0 表示"未知/尚未派生"。它只来自 Bottom.Focus 与已派生的视口
// 位置，绝不猜测 streaming 的 Stable/Enqueued/Acked 区间。
type AppCursor struct {
	Row   int
	Col   int
	Focus BottomFocus
}

// AppTextLayout 是 ComposeAppTextLayout 的输出：完整屏幕文本帧
// （transcript + active/bottom/overlay）+ 光标意图。行数恒等于 Height，
// 行序与 legacy composedPlanLocked 相同（1..Height 升序），因此两者可以
// 直接按行比较。它是纯内存 shadow 产物，不包含任何终端写。
type AppTextLayout struct {
	Width  int
	Height int
	Rows   []AppScreenRow
	Cursor *AppCursor
}

// ComposeAppTextLayout 从 immutable AppState snapshot 派生完整文本帧。
// 纯函数：不读 surface mutex、不写 terminal、不推进 effect、不触碰任何
// 实时状态（Layout 铁律）。行布局完全复用 LayoutAppScreen（含 transcript
// wrap、mutable cell 排除、bottom pane RowPlan），本层只附加光标意图，
// 避免出现第二套宽度/换行/行序算法。
func ComposeAppTextLayout(state AppState) AppTextLayout {
	screen := LayoutAppScreen(state)
	return composeAppTextLayoutFromScreen(screen)
}

func composeAppTextLayoutFromScreen(screen AppScreenLayout) AppTextLayout {
	out := AppTextLayout{
		Width:  screen.Geometry.Width,
		Height: screen.Geometry.Height,
		Rows:   screen.Rows,
	}
	if out.Width < 1 {
		out.Width = 80
	}
	if out.Height < 1 {
		return out
	}
	out.Cursor = composeAppCursor(screen.Rows, screen.bottom.RowPlan, screen.bottom.State, screen.CursorFocus)
	return out
}

// composeAppCursor 派生输入光标意图（纯布局，无终端读）。
//
// Prompt 焦点：prompt 输入区位置来自 RowPlan 的显式 metadata，不能从
// RowOwnerPrompt 反推，因为 notice/editor context 也使用 Prompt owner。
// 在输入首行上叠加 PromptCursorRow（0-based 视口内行）得到物理行。Col 输出
// PromptCursorCol+1（1-based 物理列）；未知列保持 0，由未来 Presenter
// 阶段补齐物理光标放置。
//
// Popup 焦点：光标落在最后一条可见 popup 行，列 = 该行显示宽度 + 1
// （与 legacy moveToPopupInputLocked 一致）。
func composeAppCursor(rows []AppScreenRow, plan BottomPaneRowPlan, bottom BottomPaneState, focus BottomFocus) *AppCursor {
	switch focus {
	case BottomFocusPrompt:
		if !bottom.PromptCursorKnown || !bottom.PromptVisible {
			return nil
		}
		if plan.PromptInputStartRow < 1 || plan.PromptInputRows < 1 {
			return nil
		}
		row := plan.PromptInputStartRow + bottom.PromptCursorRow
		if row < plan.PromptInputStartRow {
			row = plan.PromptInputStartRow
		}
		if last := plan.PromptInputStartRow + plan.PromptInputRows - 1; row > last {
			row = last
		}
		col := 0
		if bottom.PromptCursorCol >= 0 {
			col = bottom.PromptCursorCol + 1
		}
		return &AppCursor{Row: row, Col: col, Focus: BottomFocusPrompt}
	case BottomFocusPopup:
		popupLast := 0
		text := ""
		for _, row := range rows {
			if row.Owner == renderengine.RowOwnerPopup {
				popupLast = row.Row
				text = row.Text
			}
		}
		if popupLast < 1 {
			return nil
		}
		return &AppCursor{Row: popupLast, Col: DisplayWidth(text) + 1, Focus: BottomFocusPopup}
	default:
		return nil
	}
}

// FrameParityWithAppLayout 比较 legacy owned frame（本 surface 的 composed
// plan）与 AppState snapshot 派生帧，返回逐行差异的纯文本报告；完全一致时
// 返回 "parity: identical"。仅做内存比较，不写 terminal，不影响生产渲染。
//
// 这是任务 7 的 shadow 钩子：/debug 或测试调用它验证 AppState 快照是否
// 完整捕获了 legacy 布局；报告的差异即快照覆盖缺口（例如 legacy history
// buffer 与 AppState.Transcript 尚未同源时的行数/文本差异，或 legacy
// ActiveBand 的样式化渲染与纯文本投影的差异）。
func (s *FixedBottomSurface) FrameParityWithAppLayout(state AppState) string {
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
	derived := ComposeAppTextLayout(state)
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
