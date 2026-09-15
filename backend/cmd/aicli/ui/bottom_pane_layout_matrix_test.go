package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// bottomPaneMatrixConfig 是 popup 与 prompt 共用底部保留区时的一种组合形态。
// ask_user_question 的 merged answer prompt 只是其中一例（body-only popup +
// 输入行 + band + 动态状态行）；这里把其余组合一起纳入审计。
type bottomPaneMatrixConfig struct {
	composer   string
	band       []string
	dynamic    bool
	notice     string
	promptOn   bool
	promptRows int
	popup      []string
	below      bool
}

func (cfg bottomPaneMatrixConfig) name(width, height int) string {
	return fmt.Sprintf("w%d_h%d_composer%t_band%d_dynamic%t_notice%t_prompt%d_popup%d_below%t",
		width, height, cfg.composer != "", len(cfg.band), cfg.dynamic, cfg.notice != "", cfg.promptRows, len(cfg.popup), cfg.below)
}

func (cfg bottomPaneMatrixConfig) state() BottomPaneState {
	const input = "answer text"
	state := BottomPaneState{
		StatusModel:        &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
		PromptLine:         "> ",
		PromptInput:        input,
		PromptCursor:       len([]rune(input)),
		PromptCursorKnown:  true,
		PromptVisible:      cfg.promptOn,
		PromptReservedRows: cfg.promptRows,
		PromptNoticeLine:   cfg.notice,
		ActiveBandLines:    cfg.band,
		PopupLines:         cfg.popup,
		PopupOwner:         "question",
		PopupBelowPrompt:   cfg.below,
		ComposerLine:       cfg.composer,
	}
	if cfg.dynamic {
		state.DynamicStatusModel = &style.StatusLineModel{State: style.RunStreaming, StateText: "Waiting for answer"}
	}
	return state
}

// TestLayoutBottomPaneRows_PopupPromptReserveMatrix 对 popup/prompt 保留区的
// 组合矩阵做两项审计：
//  1. 纯布局与 legacy adapter 必须给出同一行分配（owner + 文本）；
//  2. popup 块必须是一段连续行，prompt 输入行整体位于其下。
//
// 第二项正是 ask_user_question 卡片被自己的 band/动态状态行切成两段的回归形态，
// 一旦某条路径重新按单行 gap 定位 popup，这里会在对应组合上直接失败。
func TestLayoutBottomPaneRows_PopupPromptReserveMatrix(t *testing.T) {
	geometries := []GeometryState{
		{Width: 120, Height: 30},
		{Width: 60, Height: 20},
		{Width: 36, Height: 12},
		{Width: 20, Height: 9},
	}
	configs := bottomPaneMatrixConfigs()
	if len(configs) == 0 {
		t.Fatal("empty matrix")
	}
	for _, geometry := range geometries {
		for _, cfg := range configs {
			t.Run(cfg.name(geometry.Width, geometry.Height), func(t *testing.T) {
				assertBottomPaneMatrixCase(t, cfg, geometry)
			})
		}
	}
}

func bottomPaneMatrixConfigs() []bottomPaneMatrixConfig {
	composers := []string{"", "> typed answer"}
	bands := [][]string{
		nil,
		{"Running [broker] ask_user_question prompt=…"},
		{"• Running grep", "  scanned 12 files"},
	}
	notices := []string{"", "queued input"}
	prompts := []struct {
		on   bool
		rows int
	}{{false, 0}, {true, 1}, {true, 3}}
	popups := [][]string{
		nil,
		{"[提问] 问题：这两个会话用户卡片希望怎么处理？"},
		{"[提问] 1. 保留现状", "[提问] 2. 增加说明", "[提问] 3. 增加可移除入口"},
	}
	var configs []bottomPaneMatrixConfig
	for _, composer := range composers {
		for _, band := range bands {
			for _, dynamic := range []bool{false, true} {
				for _, notice := range notices {
					for _, prompt := range prompts {
						for _, popup := range popups {
							for _, below := range []bool{false, true} {
								if below && len(popup) == 0 {
									// popup-below-prompt 需要 popup 行才可见。
									continue
								}
								configs = append(configs, bottomPaneMatrixConfig{
									composer:   composer,
									band:       band,
									dynamic:    dynamic,
									notice:     notice,
									promptOn:   prompt.on,
									promptRows: prompt.rows,
									popup:      popup,
									below:      below,
								})
							}
						}
					}
				}
			}
		}
	}
	return configs
}

func assertBottomPaneMatrixCase(t *testing.T, cfg bottomPaneMatrixConfig, geometry GeometryState) {
	t.Helper()
	semantic := cfg.state()
	derived := DeriveBottomPaneState(semantic, geometry)
	pure := LayoutBottomPaneRows(semantic, geometry)

	surface := newOwnedTestFixedBottomSurfaceWithSize(geometry.Width, geometry.Height)
	surface.mu.Lock()
	defer surface.mu.Unlock()
	applyBottomPaneStateForLegacyParityLocked(surface, derived)
	legacy := surface.bottomRowsWithOwnersLocked()

	if len(pure.Rows) == 0 {
		t.Fatalf("empty plan\nlegacy:\n%s", dumpLegacyBottomRows(legacy, geometry.Height-len(legacy)+1))
	}
	firstRow := pure.Rows[0].Row
	if len(pure.Rows) != len(legacy) {
		t.Fatalf("reserve rows: pure=%d legacy=%d\npure:\n%slegacy:\n%s",
			len(pure.Rows), len(legacy), dumpBottomPaneRows(pure.Rows), dumpLegacyBottomRows(legacy, firstRow))
	}
	for index, row := range pure.Rows {
		legacyRow := legacy[index]
		if row.Owner != legacyRow.Owner {
			t.Fatalf("row %d owner: pure=%s legacy=%s\npure:\n%slegacy:\n%s",
				row.Row, row.Owner, legacyRow.Owner, dumpBottomPaneRows(pure.Rows), dumpLegacyBottomRows(legacy, firstRow))
		}
		if want := strings.TrimRight(cellRowPlainText(legacyRow.Cells), " "); row.Text != want {
			t.Fatalf("row %d text: pure=%q legacy=%q\npure:\n%slegacy:\n%s",
				row.Row, row.Text, want, dumpBottomPaneRows(pure.Rows), dumpLegacyBottomRows(legacy, firstRow))
		}
	}
	assertBottomPanePopupBlockIntegrity(t, pure, derived)
	assertLegacyPromptBottomTargetLocked(t, surface, pure, derived)
}

// assertLegacyPromptBottomTargetLocked 断言 legacy 的 prompt 底部目标行（caret
// 定位与清行共用 promptBottomRowLocked）与实际绘制 prompt / composer 的末行一致。
// 两侧一旦按不同 gap 定位，清行与 caret 就会落到 active band、动态状态行上。
func assertLegacyPromptBottomTargetLocked(t *testing.T, surface *FixedBottomSurface, plan BottomPaneRowPlan, bottom BottomPaneState) {
	t.Helper()
	painted := -1
	switch {
	case plan.PromptInputRows > 0:
		painted = plan.PromptInputStartRow + plan.PromptInputRows - 1
	case bottom.composerVisibleRowCount() > 0:
		// composer 行画在 popup 块末尾（owner=popup），因此取 popup 块末行。
		for _, row := range plan.Rows {
			if row.Owner == renderengine.RowOwnerPopup {
				painted = row.Row
			}
		}
	default:
		return
	}
	if painted < 0 {
		return
	}
	if got := surface.promptBottomRowLocked(); got != painted {
		t.Fatalf("legacy prompt bottom target = %d, painted prompt/composer ends at row %d\n%s",
			got, painted, dumpBottomPaneRows(plan.Rows))
	}
}

// assertBottomPanePopupBlockIntegrity 断言 popup 块为一段连续行，且在 body-only
// popup 场景下 prompt 输入行整体位于 popup 之下：prompt 区域的行（band、notice、
// 动态状态、边距、输入行）一旦落进 popup 行区间，卡片就会被切断。
func assertBottomPanePopupBlockIntegrity(t *testing.T, plan BottomPaneRowPlan, bottom BottomPaneState) {
	t.Helper()
	first, last := -1, -1
	for index, row := range plan.Rows {
		if row.Owner != renderengine.RowOwnerPopup {
			continue
		}
		if first < 0 {
			first = index
		}
		last = index
	}
	if first < 0 {
		return
	}
	for index := first; index <= last; index++ {
		if plan.Rows[index].Owner != renderengine.RowOwnerPopup {
			t.Fatalf("popup block split at row %d by %s\n%s",
				plan.Rows[index].Row, plan.Rows[index].Owner, dumpBottomPaneRows(plan.Rows))
		}
	}
	if bottom.composerVisibleRowCount() > 0 || bottom.popupExpandsBelowPrompt() || plan.PromptInputRows < 1 {
		return
	}
	if plan.PromptInputStartRow <= plan.Rows[last].Row {
		t.Fatalf("prompt input starts at %d, at/above popup block ending at row %d\n%s",
			plan.PromptInputStartRow, plan.Rows[last].Row, dumpBottomPaneRows(plan.Rows))
	}
}

func dumpBottomPaneRows(rows []BottomPaneRow) string {
	var b strings.Builder
	for _, row := range rows {
		fmt.Fprintf(&b, "  %2d %-10s %q\n", row.Row, row.Owner, row.Text)
	}
	return b.String()
}

func dumpLegacyBottomRows(rows []renderengine.PlanRow, firstRow int) string {
	var b strings.Builder
	for index, row := range rows {
		fmt.Fprintf(&b, "  %2d %-10s %q\n", firstRow+index, row.Owner, strings.TrimRight(cellRowPlainText(row.Cells), " "))
	}
	return b.String()
}
