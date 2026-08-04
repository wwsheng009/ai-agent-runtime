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
	if short.ActiveBandTopGapRows != 1 || short.PromptTopMarginRows != 1 || short.PromptBottomMarginRows != 1 {
		t.Fatalf("geometry policy not applied: %+v", short)
	}
	if got := strings.Join(short.ActiveBandLines, ","); got != "3,4,5,6,7,8" {
		t.Fatalf("active-band tail = %q, want newest geometry-capped rows", got)
	}
	if short.PromptTotalRows != 5 || short.PromptReservedRows != 1 || short.PromptViewportStart != 4 || short.PromptCursorRow != 0 {
		t.Fatalf("short prompt viewport = %+v", short)
	}

	tall := DeriveBottomPaneState(bottom, GeometryState{Width: 80, Height: 24})
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
