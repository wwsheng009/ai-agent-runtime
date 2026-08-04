package ui

import (
	"reflect"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func composeFixtureState() AppState {
	return AppState{
		Revision:         41,
		LayoutGeneration: 9,
		Geometry:         GeometryState{Width: 80, Height: 24, Generation: 9},
		Lease:            LeaseState{ID: 7, Active: true},
		Transcript: NewTranscriptState(&scene.Snapshot{
			Revision: 12,
			Cells: []*scene.TranscriptCell{
				{ID: 1, Sequence: 1, Kind: scene.KindUser, Source: "question", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
				{ID: 2, Sequence: 2, Kind: scene.KindAssistant, Source: "answer", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
			},
		}),
		Bottom: BottomPaneState{
			StatusModel:        &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
			PromptLine:         "> ",
			PromptInput:        "draft",
			PromptCursor:       5,
			PromptCursorKnown:  true,
			PromptReservedRows: 1,
			PromptVisible:      true,
			Focus:              BottomFocusPrompt,
		},
	}
}

func TestComposeAppTextLayoutMatchesScreenRowsAndAddsPromptCursor(t *testing.T) {
	state := composeFixtureState()
	screen := LayoutAppScreen(state)
	frame := ComposeAppTextLayout(state)

	if frame.Width != 80 || frame.Height != 24 {
		t.Fatalf("frame geometry = %dx%d, want 80x24", frame.Width, frame.Height)
	}
	if len(frame.Rows) != 24 {
		t.Fatalf("frame rows = %d, want 24", len(frame.Rows))
	}
	// 行布局必须与 LayoutAppScreen 完全一致：Compose 只附加 cursor，绝不
	// 引入第二套行序/换行/宽度算法。
	if !reflect.DeepEqual(frame.Rows, screen.Rows) {
		t.Fatalf("compose rows diverge from screen layout\ncompose=%#v\nscreen =%#v", frame.Rows, screen.Rows)
	}

	// status 行固定在屏幕最后一行。
	if got := frame.Rows[23]; got.Owner != renderengine.RowOwnerStatus || got.Row != 24 {
		t.Fatalf("status row = %+v, want row 24 owner Status", got)
	}
	// transcript 行必须完整保留在视口内（2 语义行 + 1 boundary gap 行，
	// 尾部对齐 OutputBottomRow）。
	transcriptRows := 0
	for _, row := range frame.Rows {
		if row.Owner == renderengine.RowOwnerTranscript {
			transcriptRows++
		}
	}
	if transcriptRows != 3 {
		t.Fatalf("transcript rows = %d, want 3 (question, gap, answer)", transcriptRows)
	}

	if frame.Cursor == nil || frame.Cursor.Focus != BottomFocusPrompt {
		t.Fatalf("cursor = %+v, want Prompt focus", frame.Cursor)
	}
	if frame.Cursor.Row <= screen.OutputBottomRow || frame.Cursor.Row > 24 {
		t.Fatalf("prompt cursor row = %d, want inside prompt area above %d", frame.Cursor.Row, screen.OutputBottomRow)
	}
	// PromptCursorCol 是绝对 0-based 物理列（含 PromptLine 前缀 "> "）：
	// 2 + 5 = 7 → 1-based col 8。
	if frame.Cursor.Col != 8 {
		t.Fatalf("prompt cursor col = %d, want 8", frame.Cursor.Col)
	}
}

func TestComposeAppTextLayoutMultiRowPromptCursor(t *testing.T) {
	state := composeFixtureState()
	state.Bottom.PromptInput = "line one\nline two"
	state.Bottom.PromptCursor = len([]rune("line one\nline two")) // 第二行末尾
	state.Bottom.PromptCursorKnown = true

	frame := ComposeAppTextLayout(state)
	if frame.Cursor == nil || frame.Cursor.Focus != BottomFocusPrompt {
		t.Fatalf("cursor = %+v, want Prompt focus", frame.Cursor)
	}
	// PromptCursorRow 应落在第二行：cursor 行必须严格大于输入区第一行。
	firstPromptRow := 0
	for _, row := range frame.Rows {
		if row.Owner == renderengine.RowOwnerPrompt {
			firstPromptRow = row.Row
			break
		}
	}
	if firstPromptRow < 1 {
		t.Fatalf("no prompt rows in frame: %+v", frame.Rows)
	}
	if frame.Cursor.Row <= firstPromptRow {
		t.Fatalf("multi-row cursor row = %d, want below first prompt row %d", frame.Cursor.Row, firstPromptRow)
	}
	plan := LayoutAppState(state).Bottom.RowPlan
	if frame.Cursor.Row < plan.PromptInputStartRow || frame.Cursor.Row >= plan.PromptInputStartRow+plan.PromptInputRows {
		t.Fatalf("cursor row %d outside explicit prompt input range %d..%d", frame.Cursor.Row, plan.PromptInputStartRow, plan.PromptInputStartRow+plan.PromptInputRows-1)
	}
}

func TestComposeAppTextLayoutPopupCursor(t *testing.T) {
	state := composeFixtureState()
	state.Bottom.PromptVisible = false
	state.Bottom.PromptReservedRows = 0
	state.Bottom.Focus = BottomFocusPopup
	state.Bottom.PopupLines = []string{"choice A", "choice B"}

	frame := ComposeAppTextLayout(state)
	if frame.Cursor == nil || frame.Cursor.Focus != BottomFocusPopup {
		t.Fatalf("cursor = %+v, want Popup focus", frame.Cursor)
	}
	// popup 区：status=24，gap=1，popup 行 21-22（2 行可见）。
	if frame.Cursor.Row != 22 {
		t.Fatalf("popup cursor row = %d, want 22", frame.Cursor.Row)
	}
	if frame.Cursor.Col != 9 { // "choice B" 显示宽度 8 + 1
		t.Fatalf("popup cursor col = %d, want 9", frame.Cursor.Col)
	}
	// popup 行必须以 RowOwnerPopup 出现在帧中。
	popupRows := 0
	for _, row := range frame.Rows {
		if row.Owner == renderengine.RowOwnerPopup {
			popupRows++
		}
	}
	if popupRows != 2 {
		t.Fatalf("popup rows = %d, want 2", popupRows)
	}
}

func TestComposeAppTextLayoutCursorSuppressedWhenUnknown(t *testing.T) {
	state := composeFixtureState()
	state.Bottom.Focus = BottomFocusNone
	if cursor := ComposeAppTextLayout(state).Cursor; cursor != nil {
		t.Fatalf("cursor = %+v, want nil for BottomFocusNone", cursor)
	}

	state.Bottom.Focus = BottomFocusPrompt
	state.Bottom.PromptVisible = false
	state.Bottom.PromptCursorKnown = false
	if cursor := ComposeAppTextLayout(state).Cursor; cursor != nil {
		t.Fatalf("cursor = %+v, want nil when prompt hidden/unknown", cursor)
	}
}

func TestComposeAppTextLayoutDeterministicAndImmutable(t *testing.T) {
	state := composeFixtureState()
	first := ComposeAppTextLayout(state)
	second := ComposeAppTextLayout(state)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("compose not deterministic\nfirst=%+v\nsecond=%+v", first, second)
	}
	// Compose 不得保留调用方可变内存：篡改输入后帧保持不变。
	state.Bottom.PromptInput = "mutated"
	state.Bottom.PopupLines = []string{"mutated"}
	if got := ComposeAppTextLayout(state).Rows; reflect.DeepEqual(got, first.Rows) {
		t.Fatalf("compose retained caller-owned mutable memory")
	}
	if !reflect.DeepEqual(first.Rows, second.Rows) {
		t.Fatalf("first frame mutated after recompose")
	}
}
