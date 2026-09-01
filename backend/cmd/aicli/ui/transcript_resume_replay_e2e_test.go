package ui

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// resumeReplayE2EHarness drives the FULL production path for the resume
// replay-loop bug: real UIController.Run (canonical reduceUIControllerState),
// real TerminalSessionExecutor.runOne, real TerminalSession.FlushTransaction
// and a physical writer whose byte stream is inspected for duplicate replay.
//
// The controller-level transcript_resume_loop_test.go tests call
// reduceUIControllerState directly and never exercise the executor; the
// visible symptom (the terminal keeps replaying the whole transcript) only
// appears when the executor's scrollback-reconciliation path reacts to each
// streaming delta. This harness closes that gap.
type resumeReplayE2EHarness struct {
	controller *UIController
	executor   *TerminalSessionExecutor
	writer     *terminalSessionShortWriter
}

func newResumeReplayE2EHarness(t *testing.T) *resumeReplayE2EHarness {
	t.Helper()
	var executor *TerminalSessionExecutor
	// Mirror TerminalSessionPresenter wiring: the actor's render/history wake
	// intents are forwarded to the executor.
	controller := NewUIController(UIControllerConfig{}, nil, func(effect Effect) {
		if executor == nil {
			return
		}
		switch effect.(type) {
		case FlushEffect, HistoryCommitWakeEffect:
			executor.Request()
		}
	})
	go controller.Run()
	writer := &terminalSessionShortWriter{}
	session := NewTerminalSession(writer)
	executor = NewTerminalSessionExecutor(controller, session)
	t.Cleanup(func() {
		executor.Close()
		controller.Close()
		controller.WaitIdle()
	})
	return &resumeReplayE2EHarness{controller: controller, executor: executor, writer: writer}
}

func (h *resumeReplayE2EHarness) post(t *testing.T, actions ...UIAction) {
	t.Helper()
	for _, action := range actions {
		if !h.controller.Post(action) {
			t.Fatalf("post %T", action)
		}
	}
	h.controller.WaitIdle()
}

// flush lets the executor consume every pending wake and settle the actor.
func (h *resumeReplayE2EHarness) flush(t *testing.T) {
	t.Helper()
	h.executor.Request()
	h.executor.WaitIdle()
	h.controller.WaitIdle()
}

// assertNoReplay verifies the invariants that make the loop visible: the
// recovery flags stay clear, the reducer-confirmed terminal epoch never
// advances (no scrollback reset happened), the physical session never
// performed a scrollback reset, and the byte stream stays within the
// incremental-write bound (a reset+replay rewrites the whole transcript).
func (h *resumeReplayE2EHarness) assertNoReplay(t *testing.T, baselineEpoch, baselineResetCount, baselineBytes, delta int) {
	t.Helper()
	state := h.controller.State()
	if state.HistoryEffects.ReconciliationRequired {
		t.Fatalf("delta %d: ReconciliationRequired set; the executor would reset and "+
			"replay the ENTIRE transcript on the next wake (visible replay loop)", delta)
	}
	if state.HistoryEffects.ProjectionUnknown {
		t.Fatalf("delta %d: ProjectionUnknown set", delta)
	}
	if epoch := int(state.HistoryEffects.TerminalEpoch); epoch != baselineEpoch {
		t.Fatalf("delta %d: TerminalEpoch advanced %d -> %d; a reducer-confirmed scrollback "+
			"reset+replay was performed", delta, baselineEpoch, epoch)
	}
	if resetCount := int(h.executor.session.ProjectionState().ScrollbackResetCount); resetCount != baselineResetCount {
		t.Fatalf("delta %d: physical ScrollbackResetCount advanced %d -> %d; the terminal "+
			"actually reset and replayed native scrollback", delta, baselineResetCount, resetCount)
	}
	// Incremental appends only grow the stream by the new chunk. A reset+replay
	// re-emits the whole transcript, so the growth per delta explodes.
	if bytes := h.writer.bytes.Len(); bytes > baselineBytes+16*1024 {
		t.Fatalf("delta %d: physical stream grew to %d bytes from baseline %d; "+
			"growth %d exceeds the incremental bound (reset+replay churn)",
			delta, bytes, baselineBytes, bytes-baselineBytes)
	}
}

// TestResumeStreamingDeltasNoScrollbackResetReplayE2E reproduces the reported
// `aicli resume` symptom through the FULL production path (controller actor,
// executor worker, real TerminalSession flush and physical writer):
// a resumed session already has an acked prefix in native scrollback, then a
// stream of ReplaceTranscriptAction deltas grows the active cell append-only.
//
// If ANY step of the production path (reducer, planner, executor, session)
// treats that append-only growth as invalidating the acked scrollback, the
// executor resets the scrollback and replays the whole transcript for every
// delta — the terminal epoch advances and the physical session performs a
// scrollback reset per delta. The controller-level transcript_resume_loop_test
// cannot see that: it never runs the executor or the session.
func TestResumeStreamingDeltasNoScrollbackResetReplayE2E(t *testing.T) {
	const cellID = scene.CellID(7)

	h := newResumeReplayE2EHarness(t)

	// Resume: one committed prefix cell plus a mutable active cell whose first
	// five bytes were already acknowledged in a previous session run.
	h.post(t,
		Resize{Width: 60, Height: 8, Generation: 1},
		SetSemanticActiveCellProjectionAction{Enabled: true},
		ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
			SceneID: 1, Revision: 1, ContentVersion: 1,
			Cells: []*scene.TranscriptCell{
				{ID: 3, Kind: scene.KindUser, Phase: scene.CellCommitted, Source: "prefix", Revision: 1},
				{ID: cellID, Kind: scene.KindAssistant, Phase: scene.CellMutable, Source: "hello", Revision: 1},
			},
		}},
		SetActiveCellAction{Active: ActiveCellState{
			CellID: cellID, Revision: 1, Kind: scene.KindAssistant,
			Phase: ActiveCellMutable, Source: "hello",
			Stable: SourceRange{Start: 0, End: 5}, Enqueued: SourceRange{Start: 0, End: 5},
			Acked: SourceRange{Start: 0, End: 5},
		}},
	)
	// Let the executor write the restored transcript once and settle.
	h.flush(t)
	for _, entry := range h.controller.State().HistoryEffects.Entries() {
		if entry.State != HistoryCommitAcked {
			t.Fatalf("resume restore left history unresolved: %#v", entry)
		}
	}
	if h.controller.State().HistoryEffects.ReconciliationRequired {
		t.Fatal("resume restore already set ReconciliationRequired")
	}

	baselineEpoch := int(h.controller.State().HistoryEffects.TerminalEpoch)
	baselineResetCount := int(h.executor.session.ProjectionState().ScrollbackResetCount)
	baselineBytes := h.writer.bytes.Len()

	// A stream of deltas grows the active cell append-only; the acked prefix
	// bytes stay identical, only the live tail changes.
	stream := "hello"
	for index := 1; index <= 12; index++ {
		stream += " chunk-" + strings.Repeat("x", index)
		h.post(t, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
			SceneID: 1, Revision: uint64(index + 1), ContentVersion: uint64(index + 1),
			Cells: []*scene.TranscriptCell{
				{ID: 3, Kind: scene.KindUser, Phase: scene.CellCommitted, Source: "prefix", Revision: 1},
				{ID: cellID, Kind: scene.KindAssistant, Phase: scene.CellMutable, Source: stream, Revision: uint64(index + 1)},
			},
		}})
		h.flush(t)
		h.assertNoReplay(t, baselineEpoch, baselineResetCount, baselineBytes, index)
	}
}

// TestResumeSceneRebuildNewSceneIDNoScrollbackResetReplayE2E covers the other
// resume shape: the runtime event-log replay rebuilds the Scene under a new
// process-local SceneID while the actor still holds the old transcript. The
// full transcript-replacement path (not activeOnly) must still treat
// append-only growth as non-invalidating once the executor is in the loop.
func TestResumeSceneRebuildNewSceneIDNoScrollbackResetReplayE2E(t *testing.T) {
	const cellID = scene.CellID(9)

	h := newResumeReplayE2EHarness(t)

	h.post(t,
		Resize{Width: 60, Height: 8, Generation: 1},
		SetSemanticActiveCellProjectionAction{Enabled: true},
		ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
			SceneID: 1, Revision: 1, ContentVersion: 1,
			Cells: []*scene.TranscriptCell{
				{ID: 3, Kind: scene.KindUser, Phase: scene.CellCommitted, Source: "prefix", Revision: 1},
				{ID: cellID, Kind: scene.KindAssistant, Phase: scene.CellMutable, Source: "hello", Revision: 1},
			},
		}},
		SetActiveCellAction{Active: ActiveCellState{
			CellID: cellID, Revision: 1, Kind: scene.KindAssistant,
			Phase: ActiveCellMutable, Source: "hello",
			Stable: SourceRange{Start: 0, End: 5}, Enqueued: SourceRange{Start: 0, End: 5},
			Acked: SourceRange{Start: 0, End: 5},
		}},
	)
	h.flush(t)
	for _, entry := range h.controller.State().HistoryEffects.Entries() {
		if entry.State != HistoryCommitAcked {
			t.Fatalf("resume restore left history unresolved: %#v", entry)
		}
	}

	baselineEpoch := int(h.controller.State().HistoryEffects.TerminalEpoch)
	baselineResetCount := int(h.executor.session.ProjectionState().ScrollbackResetCount)
	baselineBytes := h.writer.bytes.Len()

	// Every delta arrives under a NEW SceneID (fresh event-log rebuild).
	stream := "hello"
	for index := 1; index <= 10; index++ {
		stream += "-part"
		h.post(t, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
			SceneID: uint64(100 + index), Revision: uint64(index + 1), ContentVersion: uint64(index + 1),
			Cells: []*scene.TranscriptCell{
				{ID: 3, Kind: scene.KindUser, Phase: scene.CellCommitted, Source: "prefix", Revision: 1},
				{ID: cellID, Kind: scene.KindAssistant, Phase: scene.CellMutable, Source: stream, Revision: uint64(index + 1)},
			},
		}})
		h.flush(t)
		h.assertNoReplay(t, baselineEpoch, baselineResetCount, baselineBytes, index)
	}
}
// TestResumeFailingWriterReconciliationStopsWithinBoundE2E reproduces the
// "persistently failing writer" flicker loop described in the executor.go
// comment (lines 256-259). A writer that returns short writes on every
// scrollback-reset flush causes the reconciliation to fail. The backoff guard
// must stop the executor after the first failed reset, yielding the worker
// instead of resetting+replaying the full transcript on every external wake.
//
// The existing unit test (TestTerminalSessionExecutorFailedReconciliationArmsBackoff)
// uses postHistoryEffectFixture (only committed cells). This test exercises
// the full resume path with a mutable active cell and acked prefix, through
// the real controller, executor, and session — the exact shape the user sees.
func TestResumeFailingWriterReconciliationStopsWithinBoundE2E(t *testing.T) {
	const cellID = scene.CellID(7)

	h := newResumeReplayE2EHarness(t)

	// Resume: one committed prefix cell plus a mutable active cell with acked prefix.
	h.post(t,
		Resize{Width: 60, Height: 8, Generation: 1},
		SetSemanticActiveCellProjectionAction{Enabled: true},
		ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
			SceneID: 1, Revision: 1, ContentVersion: 1,
			Cells: []*scene.TranscriptCell{
				{ID: 3, Kind: scene.KindUser, Phase: scene.CellCommitted, Source: "prefix", Revision: 1},
				{ID: cellID, Kind: scene.KindAssistant, Phase: scene.CellMutable, Source: "hello", Revision: 1},
			},
		}},
		SetActiveCellAction{Active: ActiveCellState{
			CellID: cellID, Revision: 1, Kind: scene.KindAssistant,
			Phase: ActiveCellMutable, Source: "hello",
			Stable: SourceRange{Start: 0, End: 5}, Enqueued: SourceRange{Start: 0, End: 5},
			Acked: SourceRange{Start: 0, End: 5},
		}},
	)
	h.flush(t)
	for _, entry := range h.controller.State().HistoryEffects.Entries() {
		if entry.State != HistoryCommitAcked {
			t.Fatalf("resume restore left history unresolved: %#v", entry)
		}
	}

	// Now make the writer short: every write returns a partial write, forcing
	// the session's flush to fail. The first reconciliation failure must arm
	// the backoff; subsequent external requests within the same revision must
	// yield without writing.
	h.writer.short = true
	h.writer.zeroError = nil
	h.writer.failZero = 0

	// Post a streaming delta that triggers ProjectionUnknown + ReconciliationRequired
	// (if the code had the bug), or just a frame-only delta that the executor
	// processes normally.
	stream := "hello chunk-1"
	h.post(t, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
		SceneID: 1, Revision: 2, ContentVersion: 2,
		Cells: []*scene.TranscriptCell{
			{ID: 3, Kind: scene.KindUser, Phase: scene.CellCommitted, Source: "prefix", Revision: 1},
			{ID: cellID, Kind: scene.KindAssistant, Phase: scene.CellMutable, Source: stream, Revision: 2},
		},
	}})

	// First Request: the executor runs one reconciliation attempt. With a short
	// writer this fails; the backoff is armed.
	h.executor.Request()
	h.executor.WaitIdle()
	h.controller.WaitIdle()

	// Record the settled revision after the executor's own failure posts.
	writesAfterFirst := h.writer.writes
	if writesAfterFirst == 0 {
		t.Fatal("short writer never wrote; the reconciliation was not attempted")
	}
	if st := h.controller.State(); !st.HistoryEffects.ProjectionUnknown {
		t.Fatalf("short writer did not leave an unknown projection: %#v", st.HistoryEffects)
	}

	// Issue many external requests within the backoff window. The backoff must
	// yield before any new write, keeping the writer count in check.
	for i := 0; i < 20; i++ {
		// Simulate an external wake: the controller itself may not post a new
		// action, so we drive the executor directly.
		h.executor.Request()
		h.executor.WaitIdle()
		h.controller.WaitIdle()
	}

	if writes := h.writer.writes; writes > writesAfterFirst+2 {
		// Allow at most 2 extra writes: one for the initial failed attempt
		// and possibly one more if the backoff window expired and the
		// revision advanced (the executor's own failure posts advance the
		// actor revision, so the guard checks lastResetRevision == stateRevision;
		// if no external action arrived, the revision matches and backoff
		// engages. A single extra write is acceptable as a race; anything
		// more is an unbounded loop.
		t.Fatalf("short writer wrote %d times after the first reconciliation "+
			"failure (initial %d); the backoff should have blocked most writes, "+
			"allowing at most 2 extra = %d total",
			writes, writesAfterFirst, writesAfterFirst+2)
	}
}