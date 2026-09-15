package ui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func TestDeriveBottomPaneState_ReflowsPromptViewportFromSemanticInput(t *testing.T) {
	input := "one\ntwo\nthree\nfour\nfive"
	bottom := BottomPaneState{
		DynamicStatusModel:     &style.StatusLineModel{State: style.RunStreaming, StateText: "Working"},
		PromptLine:             "> ",
		PromptInput:            input,
		PromptCursor:           len([]rune(input)),
		PromptCursorKnown:      true,
		PromptVisible:          true,
		PromptReservedRows:     5,
		PromptNoticeLine:       "queue\nattachments",
		ActiveBandLines:        []string{"0", "1", "2", "3", "4", "5", "6", "7", "8"},
		ActiveBandMaxRows:      99,
		ActiveBandTopGapRows:   99,
		PromptTopMarginRows:    99,
		PromptBottomMarginRows: 99,
	}

	short := DeriveBottomPaneState(bottom, GeometryState{Width: 80, Height: 12})
	if short.ActiveBandMaxRows != ActiveBandRows(12) || short.ActiveBandMaxRows != 6 {
		t.Fatalf("active-band policy = %+v", short)
	}
	// The permanent second status row (session ID / --pprof / --debug) reserves
	// one more row than the historical single-status layout, so short terminals
	// collapse the band-top separator before sacrificing retained history rows.
	if short.ActiveBandTopGapRows != 0 || short.PromptTopMarginRows != 1 || short.PromptBottomMarginRows != 1 {
		t.Fatalf("geometry policy not applied: %+v", short)
	}
	if got := strings.Join(short.ActiveBandLines, ","); got != "3,4,5,6,7,8" {
		t.Fatalf("active-band tail = %q, want newest geometry-capped rows", got)
	}
	if short.PromptTotalRows != 5 || short.PromptReservedRows != 1 || short.PromptViewportStart != 4 || short.PromptCursorRow != 0 {
		t.Fatalf("short prompt viewport = %+v", short)
	}

	tall := DeriveBottomPaneState(bottom, GeometryState{Width: 80, Height: 24})
	if tall.ActiveBandTopGapRows != 1 {
		t.Fatalf("tall geometry policy lost the band-top gap: %+v", tall)
	}
	if tall.PromptTotalRows != 5 || tall.PromptReservedRows != 5 || tall.PromptViewportStart != 0 || tall.PromptCursorRow != 4 {
		t.Fatalf("tall prompt viewport = %+v", tall)
	}

	// The helper returns a detached display projection; it must not normalize or
	// clip the AppState-owned source in place.
	if bottom.ActiveBandMaxRows != 99 || len(bottom.ActiveBandLines) != 9 || bottom.PromptReservedRows != 5 {
		t.Fatalf("derive mutated semantic source: %+v", bottom)
	}
}

func TestLayoutAppState_PromptViewportReflowsWhenGeometryChanges(t *testing.T) {
	input := strings.Repeat("x", 10)
	state := AppState{
		Geometry: GeometryState{Width: 6, Height: 24, Generation: 1},
		Bottom: BottomPaneState{
			PromptLine:         "> ",
			PromptInput:        input,
			PromptCursor:       len([]rune(input)),
			PromptCursorKnown:  true,
			PromptVisible:      true,
			PromptReservedRows: 1,
		},
	}

	narrow := LayoutAppState(state)
	if narrow.Bottom.PromptTotalRows != 3 || narrow.Bottom.PromptRows != 3 || narrow.Bottom.PromptViewportStart != 0 || narrow.Bottom.State.PromptCursorRow != 2 {
		t.Fatalf("narrow prompt layout = %+v", narrow.Bottom)
	}
	if got := narrow.Bottom.VisiblePromptLines; !reflect.DeepEqual(got, []string{"> xxxx", "xxxxxx", ""}) {
		t.Fatalf("narrow prompt rows = %#v", got)
	}

	state.Geometry = GeometryState{Width: 80, Height: 24, Generation: 2}
	wide := LayoutAppState(state)
	if wide.Bottom.PromptTotalRows != 1 || wide.Bottom.PromptRows != 1 || wide.Bottom.PromptViewportStart != 0 || wide.Bottom.State.PromptCursorRow != 0 {
		t.Fatalf("wide prompt layout = %+v", wide.Bottom)
	}
	if got := wide.Bottom.VisiblePromptLines; !reflect.DeepEqual(got, []string{"> " + input}) {
		t.Fatalf("wide prompt rows = %#v", got)
	}
}

func TestUIController_MeasuredResizeAdvancesGenerationOnlyOnGeometryChange(t *testing.T) {
	c, _ := newP1Controller(t, 0)
	go c.Run()
	defer c.Close()

	for _, action := range []UIAction{
		Resize{Width: 80, Height: 24, Applied: true},
		Resize{Width: 80, Height: 24, Applied: true},
		Resize{Width: 100, Height: 24, Applied: true},
		Resize{Width: 120, Height: 42, Generation: 9, Applied: true},
	} {
		if !c.Post(action) {
			t.Fatalf("Post(%+v) rejected", action)
		}
	}
	c.WaitIdle()

	state := c.AppState()
	if state.Geometry != (GeometryState{Width: 120, Height: 42, Generation: 9}) || state.LayoutGeneration != 9 {
		t.Fatalf("geometry/layout = %+v/%d", state.Geometry, state.LayoutGeneration)
	}
}

func TestUIController_PromptActionPreservesInputEventCursorUntilMeasuredGeometry(t *testing.T) {
	c, _ := newP1Controller(t, 0)
	go c.Run()
	defer c.Close()

	input := "abcdefgh"
	for _, action := range []UIAction{
		InputEvent{Text: input, Cursor: 8},
		TrackPromptInputAction{Line: "> ", Input: input, Rows: 2, CursorRow: 1, CursorCol: 4},
		Resize{Width: 6, Height: 24, Applied: true},
	} {
		if !c.Post(action) {
			t.Fatalf("Post(%T) rejected", action)
		}
	}
	c.WaitIdle()

	state := c.AppState()
	if !state.Bottom.PromptCursorKnown || state.Bottom.PromptCursor != 8 {
		t.Fatalf("logical cursor was replaced by an unmeasured visual guess: %+v", state.Bottom)
	}
	layout := LayoutAppState(state)
	if layout.Bottom.State.PromptCursorRow != 1 || layout.Bottom.State.PromptCursorCol != 4 {
		t.Fatalf("measured cursor layout = %+v", layout.Bottom.State)
	}
}

func TestLayoutBottomPaneRows_AllocatesOwnersAndPlainTextWithoutSurface(t *testing.T) {
	bottom := BottomPaneState{
		StatusModel:            &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
		DynamicStatusModel:     &style.StatusLineModel{State: style.RunStreaming, StateText: "Working"},
		PromptLine:             "> ",
		PromptInput:            "draft",
		PromptCursor:           5,
		PromptCursorKnown:      true,
		PromptVisible:          true,
		PromptReservedRows:     1,
		PromptNoticeLine:       "queued",
		PromptEditorStatusLine: "editing",
		ActiveBandLines:        []string{"live one", "live two"},
		PopupLines:             []string{"select one", "select two"},
		PopupBelowPrompt:       true,
	}
	plan := LayoutBottomPaneRows(bottom, GeometryState{Width: 40, Height: 24})
	if plan.OutputBottomRow != 12 || plan.StatusRow != 24 || len(plan.Rows) != 12 {
		t.Fatalf("row-plan bounds = %+v", plan)
	}

	want := []BottomPaneRow{
		{Row: 13, Owner: renderengine.RowOwnerGap},
		{Row: 14, Owner: renderengine.RowOwnerBand, Text: "live one"},
		{Row: 15, Owner: renderengine.RowOwnerBand, Text: "live two"},
		{Row: 16, Owner: renderengine.RowOwnerPrompt, Text: "queued"},
		{Row: 17, Owner: renderengine.RowOwnerPrompt, Text: "editing"},
		{Row: 18, Owner: renderengine.RowOwnerStatus, Text: "Working"},
		{Row: 19, Owner: renderengine.RowOwnerGap},
		{Row: 20, Owner: renderengine.RowOwnerPrompt, Text: "> draft"},
		{Row: 21, Owner: renderengine.RowOwnerGap},
		{Row: 22, Owner: renderengine.RowOwnerPopup, Text: "select one"},
		{Row: 23, Owner: renderengine.RowOwnerPopup, Text: "select two"},
		{Row: 24, Owner: renderengine.RowOwnerStatus, Text: "Ready"},
	}
	if !reflect.DeepEqual(plan.Rows, want) {
		t.Fatalf("row plan = %#v\nwant = %#v", plan.Rows, want)
	}

	// LayoutAppState exposes the same detached row plan instead of consulting a
	// live FixedBottomSurface after the snapshot has been captured.
	layout := LayoutAppState(AppState{Geometry: GeometryState{Width: 40, Height: 24}, Bottom: bottom})
	if !reflect.DeepEqual(layout.Bottom.RowPlan, plan) {
		t.Fatalf("AppLayout row plan = %#v\nwant = %#v", layout.Bottom.RowPlan, plan)
	}
}

func TestLayoutBottomPaneRows_PlainOwnerParityWithLegacySurfaceSnapshot(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(40, 24)
	surface.mu.Lock()
	surface.statusModel = &style.StatusLineModel{State: style.RunReady, StateText: "Ready"}
	surface.dynamicStatusModel = &style.StatusLineModel{State: style.RunStreaming, StateText: "Working"}
	surface.promptLine = "> "
	surface.promptInput = "draft"
	surface.promptReservedRows = 1
	surface.promptCursorRow = 0
	surface.promptCursorCol = 7
	surface.promptNoticeLine = "queued"
	surface.promptEditorStatusLine = "editing"
	surface.activeBandLines = []string{"live one", "live two"}
	surface.popupLines = []string{"select one", "select two"}
	surface.popupBelowPrompt = true
	state := surface.bottomPaneStateLocked()
	legacy := surface.bottomRowsWithOwnersLocked()
	surface.mu.Unlock()

	pure := LayoutBottomPaneRows(state, GeometryState{Width: 40, Height: 24})
	if len(pure.Rows) != len(legacy) {
		t.Fatalf("row count: pure=%d legacy=%d", len(pure.Rows), len(legacy))
	}
	for index, legacyRow := range legacy {
		got := pure.Rows[index]
		if got.Owner != legacyRow.Owner {
			t.Fatalf("row %d owner: pure=%s legacy=%s", got.Row, got.Owner, legacyRow.Owner)
		}
		wantText := strings.TrimRight(cellRowPlainText(legacyRow.Cells), " ")
		if got.Text != wantText {
			t.Fatalf("row %d text: pure=%q legacy=%q", got.Row, got.Text, wantText)
		}
	}
}

func TestLayoutBottomPaneRows_MultilinePromptOwnerParityWithLegacySurfaceSnapshot(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(40, 24)
	surface.mu.Lock()
	surface.statusModel = &style.StatusLineModel{State: style.RunReady, StateText: "Ready"}
	surface.promptLine = "> "
	surface.promptInput = "one\ntwo\nthree"
	surface.promptReservedRows = 3
	surface.promptCursorRow = 2
	surface.promptCursorCol = 5
	state := surface.bottomPaneStateLocked()
	legacy := surface.bottomRowsWithOwnersLocked()
	surface.mu.Unlock()

	pure := LayoutBottomPaneRows(state, GeometryState{Width: 40, Height: 24})
	if len(pure.Rows) != len(legacy) {
		t.Fatalf("row count: pure=%d legacy=%d", len(pure.Rows), len(legacy))
	}
	for index, legacyRow := range legacy {
		got := pure.Rows[index]
		if got.Owner != legacyRow.Owner {
			t.Fatalf("row %d owner: pure=%s legacy=%s", got.Row, got.Owner, legacyRow.Owner)
		}
		wantText := strings.TrimRight(cellRowPlainText(legacyRow.Cells), " ")
		if got.Text != wantText {
			t.Fatalf("row %d text: pure=%q legacy=%q", got.Row, got.Text, wantText)
		}
	}
}

func TestLayoutBottomPaneRows_LegacyParityMatrix(t *testing.T) {
	tests := []struct {
		name      string
		width     int
		height    int
		setup     func(*FixedBottomSurface)
		normalize func(*FixedBottomSurface)
	}{
		{
			name:   "popup-above-output",
			width:  40,
			height: 24,
			setup: func(surface *FixedBottomSurface) {
				surface.statusModel = &style.StatusLineModel{State: style.RunReady, StateText: "Ready"}
				surface.popupLines = []string{"choice one", "choice two"}
				surface.popupOwner = "selection"
			},
		},
		{
			name:   "composer-with-popup",
			width:  40,
			height: 24,
			setup: func(surface *FixedBottomSurface) {
				surface.statusModel = &style.StatusLineModel{State: style.RunReady, StateText: "Ready"}
				surface.popupLines = []string{"completion one", "completion two"}
				surface.popupOwner = "slash_completion"
				surface.composerLine = "compose> draft"
			},
		},
		{
			name:   "short-terminal-overlay-pressure",
			width:  12,
			height: 10,
			setup: func(surface *FixedBottomSurface) {
				surface.statusModel = &style.StatusLineModel{State: style.RunReady, StateText: "Ready"}
				surface.dynamicStatusModel = &style.StatusLineModel{State: style.RunStreaming, StateText: "Working"}
				surface.promptLine = "> "
				surface.promptInput = "one\ntwo\nthree"
				surface.promptReservedRows = 3
				surface.promptCursorRow = 2
				surface.promptCursorCol = 5
				surface.promptNoticeLine = "queue"
				surface.promptEditorStatusLine = "editing"
				surface.activeBandLines = []string{"run 1", "run 2", "run 3", "run 4", "run 5", "run 6"}
				surface.popupLines = []string{"approve", "cancel"}
				surface.popupBelowPrompt = true
			},
			normalize: func(surface *FixedBottomSurface) {
				rows := interactiveInputDisplayRows([]rune(surface.promptInput), terminalVisibleWidth(surface.promptLine), surface.terminal.Width())
				surface.setPromptStateLocked(surface.promptLine, surface.promptInput, rows, 2, 5)
			},
		},
		{
			name:   "narrow-wide-popup-prompt",
			width:  16,
			height: 18,
			setup: func(surface *FixedBottomSurface) {
				surface.statusModel = &style.StatusLineModel{State: style.RunReady, StateText: "Ready"}
				surface.promptLine = "> "
				surface.promptInput = "abcdefghijklmnop"
				surface.promptReservedRows = 2
				surface.promptCursorRow = 1
				surface.promptCursorCol = 2
				surface.popupLines = []string{"first", "second", "third"}
				surface.popupBelowPrompt = true
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			surface := newOwnedTestFixedBottomSurfaceWithSize(test.width, test.height)
			surface.mu.Lock()
			test.setup(surface)
			if test.normalize != nil {
				test.normalize(surface)
			}
			assertBottomPaneRowPlanParityLocked(t, surface)
			surface.mu.Unlock()
		})
	}
}

// ask_user_question 走 merged answer prompt 时，问题正文是一个 body-only
// popup，回答输入复用底部 prompt。此时 popup 与 prompt 区域（active band、
// 动态状态、上下边距、输入行）共用同一段底部保留区，prompt 区域必须整体
// 落在 popup 之下；一旦 popup 只按 input-gap 单行定位，prompt 区域的行就会
// 覆盖卡片尾部，把问题卡片切成上下两段。
func TestLayoutBottomPaneRows_KeepsMergedQuestionPopupAbovePromptArea(t *testing.T) {
	card := []string{
		"[提问] Agent 需要你的补充信息",
		"[提问] 问题：这两个「会话用户」卡片希望怎么处理？",
		"[提问] 1. 保留现状：继续按 runtime 用户过滤会话列表",
		"[提问] 2. 增加说明：卡片上标注过滤范围",
		"[提问] 3. 增加可移除入口：卡片上提供隐藏/删除，并持久化到本地",
		"[提问] 请输入回答，可输入建议编号（必答）：",
	}
	band := "Running [broker] ask_user_question prompt=… required=true"
	semantic := BottomPaneState{
		StatusModel:        &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
		DynamicStatusModel: &style.StatusLineModel{State: style.RunStreaming, StateText: "Waiting for answer"},
		PromptLine:         "> ",
		PromptVisible:      true,
		PromptReservedRows: 1,
		ActiveBandLines:    []string{band},
		PopupLines:         append([]string(nil), card...),
		PopupOwner:         "question",
	}
	geometry := GeometryState{Width: 120, Height: 30}
	plan := LayoutBottomPaneRows(semantic, geometry)
	if plan.PromptInputRows != 1 {
		t.Fatalf("prompt input rows = %d, want 1: %+v", plan.PromptInputRows, plan)
	}

	popupRows := make([]BottomPaneRow, 0, len(card))
	for _, row := range plan.Rows {
		if row.Owner == renderengine.RowOwnerPopup {
			popupRows = append(popupRows, row)
		}
	}
	if len(popupRows) != len(card) {
		t.Fatalf("question card lost rows: popup=%d painted=%d\nplan=%#v", len(card), len(popupRows), plan.Rows)
	}
	for index, row := range popupRows {
		if index > 0 && row.Row != popupRows[index-1].Row+1 {
			t.Fatalf("question card is not contiguous: %#v", popupRows)
		}
		if row.Text != card[index] {
			t.Fatalf("card row %d = %q, want %q", row.Row, row.Text, card[index])
		}
	}

	// 卡片之下依次是：band 顶分隔、band 行、动态状态行、上边距、prompt 输入行、
	// 下边距、状态行。任何一行插进卡片范围内都会再次切断问题卡片。
	cardEnd := popupRows[len(popupRows)-1].Row
	below := []BottomPaneRow{
		{Row: cardEnd + 1, Owner: renderengine.RowOwnerGap},
		{Row: cardEnd + 2, Owner: renderengine.RowOwnerBand, Text: band},
		{Row: cardEnd + 3, Owner: renderengine.RowOwnerStatus},
		{Row: cardEnd + 4, Owner: renderengine.RowOwnerGap},
		{Row: cardEnd + 5, Owner: renderengine.RowOwnerPrompt},
		{Row: cardEnd + 6, Owner: renderengine.RowOwnerGap},
		{Row: cardEnd + 7, Owner: renderengine.RowOwnerStatus},
	}
	firstRow := plan.Rows[0].Row
	for _, want := range below {
		got := plan.Rows[want.Row-firstRow]
		if got.Row != want.Row || got.Owner != want.Owner {
			t.Fatalf("row %d = %+v, want %+v\nplan=%#v", want.Row, got, want, plan.Rows)
		}
	}
	if !strings.Contains(plan.Rows[below[2].Row-firstRow].Text, "Waiting for answer") {
		t.Fatalf("dynamic status row lost its text: %+v", plan.Rows[below[2].Row-firstRow])
	}
	if !strings.Contains(plan.Rows[below[6].Row-firstRow].Text, "Ready") {
		t.Fatalf("status row lost its text: %+v", plan.Rows[below[6].Row-firstRow])
	}
	if strings.TrimSpace(plan.Rows[below[4].Row-firstRow].Text) != ">" {
		t.Fatalf("prompt row = %+v, want the answer input row", plan.Rows[below[4].Row-firstRow])
	}
	if plan.PromptInputStartRow != below[4].Row {
		t.Fatalf("prompt input start = %d, want %d", plan.PromptInputStartRow, below[4].Row)
	}
	if plan.StatusRow != geometry.Height {
		t.Fatalf("status row = %d, want %d", plan.StatusRow, geometry.Height)
	}

	// 纯布局与 legacy adapter 必须给出同一行分配：本次回归正是两者在
	// body-only popup 上出现了分歧（legacy 已按整个 bottom gap 定位）。
	derived := DeriveBottomPaneState(semantic, geometry)
	surface := newOwnedTestFixedBottomSurfaceWithSize(geometry.Width, geometry.Height)
	surface.mu.Lock()
	defer surface.mu.Unlock()
	applyBottomPaneStateForLegacyParityLocked(surface, derived)
	assertBottomPaneRowPlanParityLocked(t, surface)
}

func TestLayoutBottomPaneRows_LegacyParityAcrossGeometryChanges(t *testing.T) {
	semantic := BottomPaneState{
		StatusModel:            &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
		DynamicStatusModel:     &style.StatusLineModel{State: style.RunStreaming, StateText: "Working"},
		PromptLine:             "> ",
		PromptInput:            "one two three four five six seven eight",
		PromptCursor:           len([]rune("one two three four five six seven eight")),
		PromptCursorKnown:      true,
		PromptVisible:          true,
		PromptNoticeLine:       "queued",
		PromptEditorStatusLine: "editing",
		ActiveBandLines:        []string{"run one", "run two", "run three"},
		PopupLines:             []string{"choice one", "choice two", "choice three"},
		PopupOwner:             "selection",
		PopupBelowPrompt:       true,
	}
	geometries := []GeometryState{
		{Width: 16, Height: 18},
		{Width: 40, Height: 24},
		{Width: 12, Height: 10},
		{Width: 16, Height: 18},
	}
	surface := newOwnedTestFixedBottomSurfaceWithSize(geometries[0].Width, geometries[0].Height)

	for _, geometry := range geometries {
		t.Run("geometry", func(t *testing.T) {
			derived := DeriveBottomPaneState(semantic, geometry)
			surface.mu.Lock()
			defer surface.mu.Unlock()
			surface.terminal.SetSizeForTest(geometry.Width, geometry.Height)
			applyBottomPaneStateForLegacyParityLocked(surface, derived)
			assertBottomPaneRowPlanParityLocked(t, surface)
		})
	}
}

// applyBottomPaneStateForLegacyParityLocked projects the same semantic bottom
// snapshot into the legacy adapter. It is test-only: production Layout must
// never recover its state from FixedBottomSurface.
func applyBottomPaneStateForLegacyParityLocked(surface *FixedBottomSurface, state BottomPaneState) {
	surface.statusModel = cloneStatusLineModel(state.StatusModel)
	surface.dynamicStatusModel = cloneStatusLineModel(state.DynamicStatusModel)
	surface.promptLine = state.PromptLine
	surface.promptInput = state.PromptInput
	surface.promptReservedRows = state.PromptReservedRows
	surface.promptViewportStart = state.PromptViewportStart
	surface.promptCursorRow = state.PromptCursorRow
	surface.promptCursorCol = state.PromptCursorCol
	surface.promptNoticeLine = state.PromptNoticeLine
	surface.promptEditorStatusLine = state.PromptEditorStatusLine
	surface.popupLines = append([]string(nil), state.PopupLines...)
	surface.popupOwner = state.PopupOwner
	surface.popupBelowPrompt = state.PopupBelowPrompt
	surface.popupReservedRows = state.PopupReservedRows
	surface.popupViewport = clonePopupViewportSpec(state.PopupViewport)
	surface.composerLine = state.ComposerLine
	surface.activeBandLines = append([]string(nil), state.ActiveBandLines...)
	surface.activeBandStyled = cloneRenderLines(state.ActiveBandStyled)
}

func assertBottomPaneRowPlanParityLocked(t *testing.T, surface *FixedBottomSurface) {
	t.Helper()
	state := surface.bottomPaneStateLocked()
	legacy := surface.bottomRowsWithOwnersLocked()
	pure := LayoutBottomPaneRows(state, GeometryState{
		Width:  surface.terminal.Width(),
		Height: surface.terminal.Height(),
	})
	if len(pure.Rows) != len(legacy) {
		t.Fatalf("row count: pure=%d legacy=%d", len(pure.Rows), len(legacy))
	}
	for index, legacyRow := range legacy {
		got := pure.Rows[index]
		if got.Owner != legacyRow.Owner {
			t.Fatalf("row %d owner: pure=%s legacy=%s", got.Row, got.Owner, legacyRow.Owner)
		}
		wantText := strings.TrimRight(cellRowPlainText(legacyRow.Cells), " ")
		if got.Text != wantText {
			t.Fatalf("row %d text: pure=%q legacy=%q", got.Row, got.Text, wantText)
		}
	}
}

// promptNoticeLinesRowCount must equal len(promptNoticeLines()) for every
// notice shape, because both feed the same bottom-pane row math: the width
// planner asks for the ungated count (allocation-free) while the row-count
// chain asks for the gated one. A divergence here would silently mis-size the
// prompt viewport instead of failing loudly.
func TestPromptNoticeRowCountsMatchLineList(t *testing.T) {
	notices := []string{
		"",
		"single",
		"queue\nattachments",
		"a\r\nb\r\nc",
		"trailing\n",
		"  \n \n",
		"中文通知\nsecond line",
	}
	statuses := []string{"", "   ", "Saved"}
	composers := []string{"", "draft"}
	reserved := []int{0, 1, 3, -1}

	for _, notice := range notices {
		for _, status := range statuses {
			for _, composer := range composers {
				for _, reservedRows := range reserved {
					bottom := BottomPaneState{
						PromptNoticeLine:       notice,
						PromptEditorStatusLine: status,
						ComposerLine:           composer,
						PromptReservedRows:     reservedRows,
					}
					if got, want := bottom.promptNoticeLinesRowCount(), len(bottom.promptNoticeLines()); got != want {
						t.Fatalf("notice=%q status=%q composer=%q reserved=%d: ungated count = %d, want %d",
							notice, status, composer, reservedRows, got, want)
					}
					want := 0
					if strings.TrimSpace(composer) == "" && reservedRows >= 1 {
						want = len(bottom.promptNoticeLines())
					}
					if got := bottom.promptNoticeVisibleRowCount(); got != want {
						t.Fatalf("notice=%q status=%q composer=%q reserved=%d: gated count = %d, want %d",
							notice, status, composer, reservedRows, got, want)
					}
				}
			}
		}
	}
}
