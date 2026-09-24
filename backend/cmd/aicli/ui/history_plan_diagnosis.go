package ui

import (
	"fmt"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// HistoryPlanDiagnosis answers the one question the ledger counters cannot:
// why did the reducer plan nothing at all? A resumed session can reach
// pending=0 / next=0 with a fully populated Scene, and the three causes are
// indistinguishable from the queue state alone:
//
//  1. the planner's input (AppState.Transcript) never received the cells the
//     Scene holds — the plan is empty because it is planning an empty
//     transcript;
//  2. the controller transcript holds the cells but the canonical frontier
//     collapsed to nothing — one non-finalized barrier cell stops the
//     contiguous finalized prefix walk, so no later finalized cell may enter
//     native history;
//  3. the layout produced no rows for the finalized cells.
//
// Everything reported here is cheap (cell walks only, no layout, no markdown):
// this is a debug-endpoint view, and the status path must never pay the
// O(entire history) layout cost it exists to diagnose.
type HistoryPlanDiagnosis struct {
	// TranscriptCells is len(UIControllerState.Transcript.Cells): what the
	// reducer installed from the last authoritative snapshot.
	TranscriptCells int
	// AppStateCells is len(AppState.Transcript.Cells): what the planner
	// actually reads. A zero here with a populated Transcript is the planner
	// planning an empty transcript.
	AppStateCells int
	// FrontierCells is the size of the contiguous finalized prefix that may
	// enter native history; FrontierActive reports whether the frontier also
	// admits the mutable active cell's overflow.
	FrontierCells  int
	FrontierActive bool
	// MutableCells counts transcript cells still in the mutable phase: the
	// first one is the frontier barrier.
	MutableCells int
	// ActivePhase / ActiveCellID describe the mounted active cell, which the
	// frontier walk requires to be exactly the barrier. The phase is rendered as
	// text here because the active cell and the transcript use different phase
	// enums, and a debug line must be readable without the type in hand.
	ActivePhase  string
	ActiveCellID scene.CellID
	// FirstCell* describe the head of the transcript, where a single
	// non-finalized cell collapses the whole frontier.
	FirstCellID        scene.CellID
	FirstCellPhase     string
	FirstCellFinalized bool
	FirstCellSourceLen int
	// LayoutRowsTotal / LayoutRowsScreened / LayoutRowsBudgeted / LayoutBudgetComplete
	// locate the collapse when the transcript and the frontier are both healthy
	// but no commit was ever planned: the planner lays out the finalized cells
	// and returns an empty candidate set when that layout yields no rows. Total
	// is the raw scene layout, Screened is the same rows after the AppScreenRow
	// projection with no deadline, and Budgeted is the projection under the real
	// historyCommitPlanningBudget — the planner's actual input.
	LayoutRowsTotal      int
	LayoutRowsScreened   int
	LayoutRowsBudgeted   int
	LayoutBudgetComplete bool
	// LayoutProbed reports whether the layout probe ran at all. False means
	// "this session is not in the pathological shape" (or the caller used the
	// cheap inputs-only variant) — it must never be read as "the layout was
	// empty".
	LayoutProbed bool
}

// DiagnoseHistoryPlanInputs derives only the planner's cheap inputs from a
// controller state: cell walks, no layout, no markdown. It never mutates
// state. This is the variant the bounded HTTP status path uses, because the
// layout probe below is the one part that can cost an O(entire history) pass.
func DiagnoseHistoryPlanInputs(state UIControllerState) HistoryPlanDiagnosis {
	diag := HistoryPlanDiagnosis{
		TranscriptCells: len(state.Transcript.Cells),
		AppStateCells:   len(state.AppState.Transcript.Cells),
		MutableCells:    len(mutableTranscriptCellIDs(state.Transcript)),
		ActivePhase:     fmt.Sprintf("%v", state.Active.Phase),
		ActiveCellID:    state.Active.CellID,
	}
	frontier, active := canonicalHistoryCommitFrontier(state.AppState)
	diag.FrontierCells = len(frontier)
	diag.FrontierActive = active
	if len(state.Transcript.Cells) > 0 {
		first := state.Transcript.Cells[0]
		diag.FirstCellID = first.ID
		diag.FirstCellPhase = first.Phase.String()
		diag.FirstCellFinalized = cellIsFinalizedForHistory(first)
		diag.FirstCellSourceLen = len(first.Source)
	}
	return diag
}

// DiagnoseHistoryPlan derives the planner's cheap inputs from a controller
// state and, only in the pathological shape it exists for, probes the layout.
func DiagnoseHistoryPlan(state UIControllerState) HistoryPlanDiagnosis {
	diag := DiagnoseHistoryPlanInputs(state)
	// Only probe the layout in the pathological case this diagnosis exists for —
	// a populated transcript with a live frontier that still planned nothing —
	// so a healthy session never pays for a second O(entire history) layout in a
	// debug endpoint.
	if diag.TranscriptCells > 0 && diag.FrontierCells > 0 && state.HistoryEffects.NextToken == 0 {
		diag.LayoutProbed = true
		appState := state.AppState
		byID := transcriptCellsByID(appState.Transcript)
		mutable := mutableTranscriptCellIDs(appState.Transcript)
		layoutRows := appState.Transcript.LayoutRows(appState.LayoutGeneration)
		diag.LayoutRowsTotal = len(layoutRows)
		screened, _ := layoutTranscriptScreenRowsWithin(
			layoutRows, byID, mutable, appState.Geometry.Width, time.Time{}, appState.Theme)
		diag.LayoutRowsScreened = len(screened)
		rows, complete := layoutTranscriptScreenRowsWithin(
			layoutRows, byID, mutable, appState.Geometry.Width,
			time.Now().Add(historyCommitPlanningBudget), appState.Theme)
		diag.LayoutRowsBudgeted = len(rows)
		diag.LayoutBudgetComplete = complete
	}
	return diag
}
