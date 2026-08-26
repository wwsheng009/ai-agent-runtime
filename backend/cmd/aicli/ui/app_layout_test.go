package ui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func TestLayoutAppStateDerivesTranscriptAndBottomWithoutAliases(t *testing.T) {
	state := AppState{
		Revision:         31,
		LayoutGeneration: 7,
		Geometry:         GeometryState{Width: 80, Height: 24, Generation: 7},
		Lease:            LeaseState{ID: 5, Active: true},
		Transcript: NewTranscriptState(&scene.Snapshot{
			Revision: 12,
			Cells: []*scene.TranscriptCell{
				{ID: 1, Sequence: 1, Kind: scene.KindUser, Source: "question", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
				{ID: 2, Sequence: 2, Kind: scene.KindAssistant, Source: "answer", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
			},
		}),
		Active: ActiveCellState{CellID: 3, Revision: 4, Phase: ActiveCellMutable, Source: "live source"},
		Bottom: BottomPaneState{
			StatusModel:        &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
			PromptLine:         "> ",
			PromptInput:        "draft",
			PromptReservedRows: 1,
			PromptVisible:      true,
			Focus:              BottomFocusPrompt,
			PopupLines:         []string{"one choice"},
			ActiveBandLines:    []string{"legacy transient row"},
		},
	}

	first := LayoutAppState(state)
	second := LayoutAppState(state)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("pure layout is not deterministic\nfirst=%+v\nsecond=%+v", first, second)
	}
	if first.Revision != 31 || first.LayoutGeneration != 7 || first.Geometry != state.Geometry || first.Lease != state.Lease {
		t.Fatalf("layout metadata = %+v", first)
	}
	if len(first.Transcript) != 3 || first.Transcript[0].Text != "question" || first.Transcript[1].Gap != boundary.GapOne || first.Transcript[2].Text != "answer" {
		t.Fatalf("transcript layout = %+v", first.Transcript)
	}
	if first.Active.Source != "live source" || !first.Bottom.LegacyBandProjection || first.Bottom.StatusRows != 1 || first.Bottom.PromptRows < 1 || first.Bottom.ActiveBandRows < 1 || len(first.Bottom.VisiblePopupLines) != 1 {
		t.Fatalf("bottom layout = %+v", first.Bottom)
	}

	// Layout must detach all nested source slices and pointers from the caller.
	state.Transcript.Cells[0].Source = "mutated"
	state.Bottom.PopupLines[0] = "mutated"
	state.Bottom.ActiveBandLines[0] = "mutated"
	if first.Transcript[0].Text != "question" || first.Bottom.VisiblePopupLines[0] != "one choice" || first.Bottom.State.ActiveBandLines[0] != "legacy transient row" {
		t.Fatalf("layout retained caller-owned mutable memory: %+v", first)
	}
}

func TestLayoutAppScreenCombinesTranscriptTailAndBottomWithoutTerminal(t *testing.T) {
	state := AppState{
		Revision:         17,
		LayoutGeneration: 4,
		Geometry:         GeometryState{Width: 5, Height: 7, Generation: 4},
		Transcript: NewTranscriptState(&scene.Snapshot{Cells: []*scene.TranscriptCell{
			{ID: 1, Sequence: 1, Kind: scene.KindUser, Source: "abcdeF", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
			{ID: 2, Sequence: 2, Kind: scene.KindAssistant, Source: "甲乙xy", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
		}}),
		Bottom: BottomPaneState{
			StatusModel: &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
		},
	}

	plan := LayoutAppScreen(state)
	if plan.Revision != 17 || plan.LayoutGeneration != 4 || plan.OutputBottomRow != 6 || len(plan.Rows) != 7 {
		t.Fatalf("screen metadata = %+v", plan)
	}
	want := []AppScreenRow{
		{Row: 1, Owner: renderengine.RowOwnerGap},
		{Row: 2, Owner: renderengine.RowOwnerTranscript, Text: "abc", CellID: 1, UserMessage: true},
		{Row: 3, Owner: renderengine.RowOwnerTranscript, Text: "deF", CellID: 1, UserMessage: true},
		{Row: 4, Owner: renderengine.RowOwnerTranscript, CellID: 2, TranscriptGap: true},
		{Row: 5, Owner: renderengine.RowOwnerTranscript, Text: "甲乙x", CellID: 2},
		{Row: 6, Owner: renderengine.RowOwnerTranscript, Text: "y", CellID: 2},
		{Row: 7, Owner: renderengine.RowOwnerStatus, Text: "Ready"},
	}
	if !reflect.DeepEqual(plan.Rows, want) {
		t.Fatalf("screen rows = %#v\nwant = %#v", plan.Rows, want)
	}
	if plan.ActiveProjectionPending || plan.LegacyBandProjection {
		t.Fatalf("committed-only plan unexpectedly reports active projection: %+v", plan)
	}
}

func TestLayoutAppScreenExcludesMutableTranscriptFromRetainedRows(t *testing.T) {
	state := AppState{
		Geometry: GeometryState{Width: 20, Height: 5},
		Transcript: NewTranscriptState(&scene.Snapshot{Cells: []*scene.TranscriptCell{
			{ID: 1, Sequence: 1, Kind: scene.KindUser, Source: "committed", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
			{ID: 2, Sequence: 2, Kind: scene.KindAssistant, Source: "mutable source", Phase: scene.CellMutable, Boundary: boundary.BoundaryNormal},
		}}),
		Active: ActiveCellState{CellID: 2, Revision: 3, Phase: ActiveCellMutable, Source: "mutable source"},
		Bottom: BottomPaneState{
			StatusModel:     &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
			ActiveBandLines: []string{"legacy active projection"},
		},
	}

	plan := LayoutAppScreen(state)
	if !plan.ActiveProjectionPending || !plan.LegacyBandProjection {
		t.Fatalf("active migration markers = %+v", plan)
	}
	var committed, mutable, band bool
	for _, row := range plan.Rows {
		if row.Owner == renderengine.RowOwnerBand {
			band = true
		}
		switch row.Text {
		case "committed":
			committed = row.Owner == renderengine.RowOwnerTranscript && row.CellID == 1
		case "mutable source":
			mutable = true
		}
	}
	if !committed || mutable || !band {
		t.Fatalf("screen layout mixed retained and mutable sources: %+v", plan.Rows)
	}
}

func TestLayoutAppScreenExcludesCanonicalSuffixAfterMutableBarrier(t *testing.T) {
	state := AppState{
		Geometry: GeometryState{Width: 40, Height: 8},
		Transcript: NewTranscriptState(&scene.Snapshot{Cells: []*scene.TranscriptCell{
			{ID: 1, Sequence: 1, Kind: scene.KindUser, Source: "visible prefix", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
			{ID: 2, Sequence: 2, Kind: scene.KindReasoning, Source: "reasoning barrier", Phase: scene.CellMutable, Boundary: boundary.BoundaryNormal},
			{ID: 3, Sequence: 3, Kind: scene.KindAssistant, Source: "blocked finalized assistant", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
			{ID: 4, Sequence: 4, Kind: scene.KindSystem, Source: "blocked system", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
		}}),
		Active: ActiveCellState{CellID: 2, Revision: 1, Kind: scene.KindReasoning, Phase: ActiveCellMutable, Source: "reasoning barrier"},
	}

	plan := LayoutAppScreen(state)
	for _, row := range plan.Rows {
		if row.CellID == 2 || row.CellID == 3 || row.CellID == 4 ||
			row.Text == "blocked finalized assistant" || row.Text == "blocked system" {
			t.Fatalf("canonical suffix crossed mutable barrier: %+v", plan.Rows)
		}
	}
	seenPrefix := false
	for _, row := range plan.Rows {
		seenPrefix = seenPrefix || row.CellID == 1 && row.Text == "visible prefix"
	}
	if !seenPrefix {
		t.Fatalf("finalized prefix was not retained: %+v", plan.Rows)
	}
}

func TestActiveCellFromTranscriptSelectsFirstCanonicalMutableCell(t *testing.T) {
	transcript := NewTranscriptState(&scene.Snapshot{Cells: []*scene.TranscriptCell{
		{ID: 10, Sequence: 1, Kind: scene.KindReasoning, Revision: 2, Source: "reasoning first", Phase: scene.CellMutable},
		{ID: 11, Sequence: 2, Kind: scene.KindAssistant, Revision: 3, Source: "assistant later", Phase: scene.CellMutable},
	}})

	active, ok := ActiveCellFromTranscript(transcript)
	if !ok || active.CellID != 10 || active.Kind != scene.KindReasoning || active.Source != "reasoning first" {
		t.Fatalf("active = %+v, ok=%v; want first canonical mutable cell", active, ok)
	}
}

func TestLayoutAppScreenSemanticActiveProjectionRejectsLegacyBandPayload(t *testing.T) {
	state := AppState{
		Geometry:                     GeometryState{Width: 40, Height: 8},
		SemanticActiveCellProjection: true,
		Transcript: NewTranscriptState(&scene.Snapshot{Cells: []*scene.TranscriptCell{
			{ID: 17, Sequence: 1, Kind: scene.KindAssistant, Revision: 2, Source: "scene mutable body", Phase: scene.CellMutable, Boundary: boundary.BoundaryNormal},
		}}),
		Active: ActiveCellState{
			CellID: 17, Revision: 2, Kind: scene.KindAssistant,
			Phase: ActiveCellMutable, Source: "scene mutable body",
		},
		Bottom: BottomPaneState{
			StatusModel:     &style.StatusLineModel{State: style.RunStreaming, StateText: "Streaming"},
			ActiveBandLines: []string{"legacy display payload"},
		},
	}

	plan := LayoutAppScreen(state)
	if plan.LegacyBandProjection {
		t.Fatalf("unified plan selected legacy ActiveBand: %+v", plan)
	}
	var sceneBody, legacyBody bool
	for _, row := range plan.Rows {
		if row.Owner != renderengine.RowOwnerBand {
			continue
		}
		sceneBody = sceneBody || row.Text == "scene mutable body"
		legacyBody = legacyBody || row.Text == "legacy display payload"
	}
	if !sceneBody || legacyBody {
		t.Fatalf("band source was not exclusive: rows=%+v", plan.Rows)
	}
}

func TestLayoutAppScreen_PlainOwnerTextParityWithLegacyOwnedViewport(t *testing.T) {
	const (
		width  = 16
		height = 24
	)
	bottom := BottomPaneState{
		StatusModel:            &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
		DynamicStatusModel:     &style.StatusLineModel{State: style.RunStreaming, StateText: "Working"},
		PromptLine:             "> ",
		PromptInput:            "draft",
		PromptCursor:           5,
		PromptCursorKnown:      true,
		PromptVisible:          true,
		PromptNoticeLine:       "queued",
		PromptEditorStatusLine: "editing",
		ActiveBandLines:        []string{"live row"},
		PopupLines:             []string{"choice one", "choice two"},
		PopupBelowPrompt:       true,
	}
	snapshot := &scene.Snapshot{Revision: 9, Cells: []*scene.TranscriptCell{
		{ID: 1, Sequence: 1, Kind: scene.KindUser, Source: "question", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
		{ID: 2, Sequence: 2, Kind: scene.KindAssistant, Source: "answer\n甲乙xy", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
		{ID: 3, Sequence: 3, Kind: scene.KindUser, Source: "follow up", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
	}}
	state := AppState{
		Revision:         13,
		LayoutGeneration: 3,
		Geometry:         GeometryState{Width: width, Height: height, Generation: 3},
		Transcript:       NewTranscriptState(snapshot),
		Bottom:           bottom,
	}

	surface := newOwnedTestFixedBottomSurfaceWithSize(width, height)
	surface.mu.Lock()
	defer surface.mu.Unlock()
	applyBottomPaneStateForLegacyParityLocked(surface, DeriveBottomPaneState(bottom, state.Geometry))
	// This is the legacy retained source corresponding exactly to the Scene
	// cells above: one semantic gap is materialized between top-level cells.
	surface.historyWindow = []string{"question", "", "answer", "甲乙xy", "", "follow up"}

	assertAppScreenLegacyParityLocked(t, surface, state)
}

func TestLayoutAppScreen_PlainOwnerTextParityKeepsNewestTranscriptTail(t *testing.T) {
	const (
		width  = 8
		height = 4
	)
	snapshot := &scene.Snapshot{Cells: []*scene.TranscriptCell{
		{ID: 1, Sequence: 1, Kind: scene.KindAssistant, Source: "row-1\nrow-2\nrow-3\nrow-4", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
	}}
	state := AppState{
		Geometry:   GeometryState{Width: width, Height: height},
		Transcript: NewTranscriptState(snapshot),
		Bottom: BottomPaneState{
			StatusModel: &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
		},
	}
	surface := newOwnedTestFixedBottomSurfaceWithSize(width, height)
	surface.mu.Lock()
	defer surface.mu.Unlock()
	applyBottomPaneStateForLegacyParityLocked(surface, DeriveBottomPaneState(state.Bottom, state.Geometry))
	surface.historyWindow = []string{"row-1", "row-2", "row-3", "row-4"}

	assertAppScreenLegacyParityLocked(t, surface, state)
	plan := LayoutAppScreen(state)
	if got := []string{plan.Rows[0].Text, plan.Rows[1].Text, plan.Rows[2].Text}; !reflect.DeepEqual(got, []string{"row-2", "row-3", "row-4"}) {
		t.Fatalf("visible transcript tail = %#v", got)
	}
}

func TestLayoutAppScreen_PlainOwnerTextParityMatrix(t *testing.T) {
	ready := func() *style.StatusLineModel {
		return &style.StatusLineModel{State: style.RunReady, StateText: "Ready"}
	}
	cases := []struct {
		name    string
		width   int
		height  int
		bottom  BottomPaneState
		cells   []scene.TranscriptCell
		history []string
	}{
		{
			name:   "indentation-tab-and-wide-runes",
			width:  8,
			height: 6,
			bottom: BottomPaneState{StatusModel: ready()},
			cells: []scene.TranscriptCell{{
				ID: 1, Sequence: 1, Kind: scene.KindAssistant,
				Source: "  start\n\tX\n甲乙", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal,
			}},
			history: []string{"  start", "\tX", "甲乙"},
		},
		{
			name:   "one-column-wide-rune",
			width:  1,
			height: 4,
			bottom: BottomPaneState{StatusModel: ready()},
			cells: []scene.TranscriptCell{{
				ID: 1, Sequence: 1, Kind: scene.KindAssistant,
				Source: "甲", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal,
			}},
			history: []string{"甲"},
		},
		{
			name:   "semantic-boundary-with-combining-mark",
			width:  5,
			height: 6,
			bottom: BottomPaneState{StatusModel: ready()},
			cells: []scene.TranscriptCell{
				{ID: 1, Sequence: 1, Kind: scene.KindUser, Source: "abcde", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
				{ID: 2, Sequence: 2, Kind: scene.KindAssistant, Source: "\u0301e", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
			},
			// 用户消息在 unified 布局按 width-2=3 预算 wrap（为渲染层 "> "
			// 前缀预留宽度），故 legacy retained 对比数据为 wrap 片段。
			history: []string{"abc", "de", "", "\u0301e"},
		},
		{
			name:   "popup-with-unowned-input-gap",
			width:  12,
			height: 8,
			bottom: BottomPaneState{
				StatusModel: ready(), PopupLines: []string{"choose", "cancel"}, PopupOwner: "selection",
			},
			cells: []scene.TranscriptCell{{
				ID: 1, Sequence: 1, Kind: scene.KindAssistant,
				Source: "history", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal,
			}},
			history: []string{"history"},
		},
		{
			name:   "multiline-prompt-and-popup",
			width:  10,
			height: 12,
			bottom: BottomPaneState{
				StatusModel:            ready(),
				DynamicStatusModel:     &style.StatusLineModel{State: style.RunStreaming, StateText: "Working"},
				PromptLine:             "> ",
				PromptInput:            "one\ntwo\nthree",
				PromptCursor:           len([]rune("one\ntwo\nthree")),
				PromptCursorKnown:      true,
				PromptVisible:          true,
				PromptNoticeLine:       "queued",
				PromptEditorStatusLine: "editing",
				ActiveBandLines:        []string{"running"},
				PopupLines:             []string{"approve", "reject"},
				PopupBelowPrompt:       true,
			},
			cells: []scene.TranscriptCell{{
				ID: 1, Sequence: 1, Kind: scene.KindAssistant,
				Source: "history", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal,
			}},
			history: []string{"history"},
		},
		{
			name:   "one-row-terminal-has-no-output-region",
			width:  8,
			height: 1,
			bottom: BottomPaneState{StatusModel: ready()},
			cells: []scene.TranscriptCell{{
				ID: 1, Sequence: 1, Kind: scene.KindAssistant,
				Source: "hidden", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal,
			}},
			history: []string{"hidden"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := &scene.Snapshot{Cells: make([]*scene.TranscriptCell, 0, len(tc.cells))}
			for index := range tc.cells {
				cell := tc.cells[index]
				snapshot.Cells = append(snapshot.Cells, &cell)
			}
			state := AppState{
				Geometry:   GeometryState{Width: tc.width, Height: tc.height},
				Transcript: NewTranscriptState(snapshot),
				Bottom:     tc.bottom,
			}
			surface := newOwnedTestFixedBottomSurfaceWithSize(tc.width, tc.height)
			surface.mu.Lock()
			defer surface.mu.Unlock()
			applyBottomPaneStateForLegacyParityLocked(surface, DeriveBottomPaneState(tc.bottom, state.Geometry))
			surface.historyWindow = append([]string(nil), tc.history...)
			assertAppScreenLegacyParityLocked(t, surface, state)
		})
	}
}

func TestLayoutAppScreen_PlainOwnerTextParityAcrossGeometryChanges(t *testing.T) {
	bottom := BottomPaneState{
		StatusModel:            &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
		DynamicStatusModel:     &style.StatusLineModel{State: style.RunStreaming, StateText: "Working"},
		PromptLine:             "> ",
		PromptInput:            "one two three four five six",
		PromptCursor:           len([]rune("one two three four five six")),
		PromptCursorKnown:      true,
		PromptVisible:          true,
		PromptNoticeLine:       "queued",
		PromptEditorStatusLine: "editing",
		ActiveBandLines:        []string{"run one", "run two", "run three"},
		PopupLines:             []string{"choice one", "choice two"},
		PopupBelowPrompt:       true,
	}
	state := AppState{
		Transcript: NewTranscriptState(&scene.Snapshot{Cells: []*scene.TranscriptCell{
			{ID: 1, Sequence: 1, Kind: scene.KindUser, Source: "first transcript row is intentionally long", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
			{ID: 2, Sequence: 2, Kind: scene.KindAssistant, Source: "甲乙 mixed width tail", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
		}}),
		Bottom: bottom,
	}
	geometries := []GeometryState{
		{Width: 12, Height: 10, Generation: 1},
		{Width: 36, Height: 22, Generation: 2},
		{Width: 8, Height: 8, Generation: 3},
		{Width: 12, Height: 10, Generation: 4},
	}
	surface := newOwnedTestFixedBottomSurfaceWithSize(geometries[0].Width, geometries[0].Height)
	for _, geometry := range geometries {
		t.Run("geometry", func(t *testing.T) {
			state.Geometry = geometry
			surface.mu.Lock()
			defer surface.mu.Unlock()
			surface.terminal.SetSizeForTest(geometry.Width, geometry.Height)
			applyBottomPaneStateForLegacyParityLocked(surface, DeriveBottomPaneState(bottom, geometry))
			// historyWindow is logical source, so each composition must re-expand
			// it at the current width just as the pure screen layout does.
			surface.historyWindow = []string{
				"first transcript row is intentionally long",
				"",
				"甲乙 mixed width tail",
			}
			assertAppScreenLegacyParityLocked(t, surface, state)
		})
	}
}

func assertAppScreenLegacyParityLocked(t *testing.T, surface *FixedBottomSurface, state AppState) {
	t.Helper()
	width, height := surface.terminal.Width(), surface.terminal.Height()
	pure := LayoutAppScreen(state)
	legacy := surface.composedPlanLocked(width, height, false)
	if len(pure.Rows) != len(legacy) {
		t.Fatalf("screen row count: pure=%d legacy=%d", len(pure.Rows), len(legacy))
	}
	// 用户消息在 unified 布局按 width-2 wrap（为渲染层 "> " 前缀预留 2 列），
	// legacy 侧按 width wrap；词边界可能差 2 列，逐行文本不匹配但内容流一致。
	// 对连续的用户消息行做拼接等价比较，其余行严格逐行比较。
	userPure, userLegacy := "", ""
	flushUserStream := func() {
		if userPure == "" && userLegacy == "" {
			return
		}
		if userPure != userLegacy {
			t.Fatalf("user message content stream: pure=%q legacy=%q", userPure, userLegacy)
		}
		userPure, userLegacy = "", ""
	}
	for index, legacyRow := range legacy {
		got := pure.Rows[index]
		if got.Row != index+1 || got.Owner != legacyRow.Owner {
			t.Fatalf("row %d ownership: pure=%+v legacy=%s", index+1, got, legacyRow.Owner)
		}
		wantText := appScreenCellRowText(legacyRow.Cells)
		if got.UserMessage {
			userPure += got.Text
			userLegacy += strings.TrimPrefix(wantText, userMessagePrefix)
			continue
		}
		flushUserStream()
		if got.Text != wantText {
			t.Fatalf("row %d text: pure=%q legacy=%q", index+1, got.Text, wantText)
		}
	}
	flushUserStream()
}

func TestWrapAppScreenTextMatchesOwnedVTExpansion(t *testing.T) {
	cases := []struct {
		name  string
		width int
		text  string
		want  []string
	}{
		{name: "deferred exact width", width: 4, text: "abcd", want: []string{"abcd"}},
		{name: "deferred wrap", width: 4, text: "abcde", want: []string{"abcd", "e"}},
		{name: "wide rune wraps", width: 3, text: "甲乙", want: []string{"甲", "乙"}},
		{name: "wide rune on one column", width: 1, text: "甲", want: []string{"", "甲"}},
		{name: "leading combining mark", width: 5, text: "\u0301e", want: []string{"e"}},
		{name: "tab stop", width: 8, text: "\tX", want: []string{"       X"}},
		{name: "sgr sequence", width: 5, text: "\x1b[31mred\x1b[0m", want: []string{"red"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wrapAppScreenText(tc.text, tc.width); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("rows = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestWrapPlainAppScreenTextMatchesVTExpansion(t *testing.T) {
	cases := []struct {
		name  string
		width int
		text  string
	}{
		{name: "ascii wraps", width: 4, text: "abcdefghijkl"},
		{name: "single column ascii", width: 1, text: "abc"},
		{name: "wide runes", width: 4, text: "甲乙丙丁"},
		{name: "wide rune wraps", width: 3, text: "甲乙丙"},
		{name: "combining marks", width: 4, text: "e\u0301e\u0301e\u0301"},
		{name: "leading combining mark", width: 4, text: "\u0301e"},
		{name: "emoji sequence", width: 5, text: "👩‍💻x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := wrapPlainAppScreenText(tc.text, tc.width)
			if !ok {
				t.Fatal("plain path unexpectedly declined plain text")
			}
			if want := wrapVTAppScreenText(tc.text, tc.width); !reflect.DeepEqual(got, want) {
				t.Fatalf("rows = %#v, want VT %#v", got, want)
			}
		})
	}
}

func BenchmarkWrapAppScreenTextPlainTranscript(b *testing.B) {
	text := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 32)
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		_ = wrapAppScreenText(text, 80)
	}
}

func BenchmarkWrapVTAppScreenTextPlainTranscript(b *testing.B) {
	text := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 32)
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		_ = wrapVTAppScreenText(text, 80)
	}
}

func TestAppScreenScratchHeightUsesDisplayWidthInsteadOfRuneCount(t *testing.T) {
	text := strings.Repeat("x", 10_000)
	if got, want := appScreenScratchHeight(text, 80), 127; got != want {
		t.Fatalf("scratch height = %d, want %d", got, want)
	}
}

func TestUserMessagePrefixReservesWidthAndPrefixesEveryLine(t *testing.T) {
	// 超宽用户消息：宽度 10 时按 width-2=8 预算 wrap，渲染层每行加 "> "
	// 前缀后仍不超宽（避免满宽行加前缀后被终端截断）。
	width := 10
	source := "abcdefghijklmnopqrstuvwxyz" // 26 个 ASCII 字符
	state := AppState{
		Revision:         1,
		LayoutGeneration: 1,
		Geometry:         GeometryState{Width: width, Height: 8, Generation: 1},
		Transcript: NewTranscriptState(&scene.Snapshot{Cells: []*scene.TranscriptCell{
			{ID: 1, Sequence: 1, Kind: scene.KindUser, Source: source, Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
		}}),
		Bottom: BottomPaneState{
			StatusModel: &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
		},
	}

	layout := LayoutAppScreen(state)
	var userRows []AppScreenRow
	for _, row := range layout.Rows {
		if row.UserMessage {
			userRows = append(userRows, row)
		}
	}
	if len(userRows) == 0 {
		t.Fatalf("layout contains no user message rows: %+v", layout.Rows)
	}
	joined := ""
	for _, row := range userRows {
		if strings.HasPrefix(row.Text, userMessagePrefix) {
			t.Fatalf("layout text must stay unprefixed (parity contract), got %q", row.Text)
		}
		if render.Width(row.Text) > width-2 {
			t.Fatalf("user layout row %q exceeds content width %d", row.Text, width-2)
		}
		joined += row.Text
	}
	if joined != source {
		t.Fatalf("wrapped user content %q != source %q", joined, source)
	}

	// 渲染层：每个显示行带 "> " 前缀，且渲染后不超宽。
	frame := ComposeAppRenderFrame(state)
	rendered := ""
	for index := range frame.Rows {
		row := frame.Rows[index]
		if !row.Screen.UserMessage {
			continue
		}
		plain := (render.PlainBackend{}).Render(render.LinesDoc(row.Line))
		want := userMessagePrefix + row.Screen.Text
		if plain != want {
			t.Fatalf("row %d render plain = %q, want %q", index+1, plain, want)
		}
		if render.Width(plain) > width {
			t.Fatalf("row %d render width %d exceeds terminal width %d: %q", index+1, render.Width(plain), width, plain)
		}
		rendered += strings.TrimPrefix(plain, userMessagePrefix)
	}
	if rendered != source {
		t.Fatalf("rendered user content %q != source %q", rendered, source)
	}
}
