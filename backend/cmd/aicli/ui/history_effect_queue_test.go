package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func TestHistoryEffectQueue_FreezeBlocksBeginButNotAck(t *testing.T) {
	queue := HistoryEffectQueueState{}
	commit := testHistoryCommit(0, 51, 7)
	if err := queue.enqueue(commit); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	pending := queue.Pending()
	if len(pending) != 1 || pending[0].Token != 1 {
		t.Fatalf("pending = %#v", pending)
	}
	queue.Frozen = true
	if err := queue.markInFlight(1, 7); !errors.Is(err, ErrHistoryCommitFrozen) {
		t.Fatalf("mark while frozen = %v, want ErrHistoryCommitFrozen", err)
	}
	queue.Frozen = false
	if err := queue.markInFlight(1, 7); err != nil {
		t.Fatalf("mark after release: %v", err)
	}
	// A lease may be acquired after the terminal transaction completed but
	// before its barrier Ack reaches the actor. Ack remains valid for this
	// already-in-flight token; only starting a new write is frozen.
	queue.Frozen = true
	if err := queue.ack(1, 9, 7); err != nil {
		t.Fatalf("ack in-flight commit during freeze: %v", err)
	}
	entries := queue.Entries()
	if len(entries) != 1 || entries[0].State != HistoryCommitAcked {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestHistoryEffectQueue_RebasePreservesToken(t *testing.T) {
	queue := HistoryEffectQueueState{}
	commit := testHistoryCommit(0, 61, 3)
	if err := queue.enqueue(commit); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	replacement := testHistoryCommit(0, 61, 4)
	replacement.DisplayRange = DisplayRange{Start: 8, End: 13}
	if err := queue.rebasePending(replacement); err != nil {
		t.Fatalf("rebase: %v", err)
	}
	entry := queue.Entries()[0]
	if entry.Commit.Token != 1 || entry.Commit.LayoutGeneration != 4 || entry.Commit.DisplayRange != replacement.DisplayRange {
		t.Fatalf("rebased entry = %#v", entry)
	}
	if err := queue.markInFlight(1, 3); !errors.Is(err, ErrStaleLayoutGeneration) {
		t.Fatalf("old generation begin = %v, want stale generation", err)
	}
	if err := queue.markInFlight(1, 4); err != nil {
		t.Fatalf("new generation begin: %v", err)
	}
}

func TestHistoryEffectQueue_OrdersBeginsAndBlocksUnknownProjection(t *testing.T) {
	queue := HistoryEffectQueueState{}
	if err := queue.enqueue(testHistoryCommit(0, 71, 3)); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	if err := queue.enqueue(testHistoryCommit(0, 72, 3)); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}
	if err := queue.markInFlight(2, 3); !errors.Is(err, ErrHistoryCommitOutOfOrder) {
		t.Fatalf("begin second before first = %v, want ErrHistoryCommitOutOfOrder", err)
	}
	if err := queue.markInFlight(1, 3); err != nil {
		t.Fatalf("begin first: %v", err)
	}
	if err := queue.markInFlight(2, 3); !errors.Is(err, ErrHistoryCommitOutOfOrder) {
		t.Fatalf("begin second while first in flight = %v, want ErrHistoryCommitOutOfOrder", err)
	}
	if err := queue.ack(1, 9, 3); err != nil {
		t.Fatalf("ack first: %v", err)
	}
	if err := queue.markInFlight(2, 3); err != nil {
		t.Fatalf("begin second after first ack: %v", err)
	}
	queue.ProjectionUnknown = true
	if pending := queue.Pending(); pending != nil {
		t.Fatalf("unknown projection exposed pending work: %#v", pending)
	}
	if err := queue.markInFlight(2, 3); !errors.Is(err, ErrHistoryProjectionUnknown) {
		t.Fatalf("begin during unknown projection = %v, want ErrHistoryProjectionUnknown", err)
	}
}

func TestHistoryEffectQueue_UnresolvedFailureBlocksLaterTokensAfterRecovery(t *testing.T) {
	queue := HistoryEffectQueueState{}
	if err := queue.enqueue(testHistoryCommit(0, 81, 3)); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	if err := queue.enqueue(testHistoryCommit(0, 82, 3)); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}
	if err := queue.markInFlight(1, 3); err != nil {
		t.Fatalf("begin first: %v", err)
	}
	if err := queue.fail(1, 3, errors.New("writer failed"), false); err != nil {
		t.Fatalf("fail first: %v", err)
	}
	// A full viewport repaint may make the primary projection known again, but
	// it cannot prove the native-scrollback outcome for the failed first token.
	queue.markProjectionKnown()
	if pending := queue.Pending(); pending != nil {
		t.Fatalf("recovered queue exposed later pending token: %#v", pending)
	}
	if err := queue.markInFlight(2, 3); !errors.Is(err, ErrHistoryCommitRecoveryPending) {
		t.Fatalf("begin later after unresolved failure = %v, want recovery pending", err)
	}
}

func TestHistoryEffectQueue_ReconcileScrollbackRetiresOnlyAProvenTerminalEpoch(t *testing.T) {
	queue := HistoryEffectQueueState{}
	if err := queue.enqueue(testHistoryCommit(0, 91, 3)); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	if err := queue.enqueue(testHistoryCommit(0, 92, 3)); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}
	if err := queue.markInFlight(1, 3); err != nil {
		t.Fatalf("begin first: %v", err)
	}
	if err := queue.fail(1, 3, errors.New("writer failed"), true); err != nil {
		t.Fatalf("fail first: %v", err)
	}
	nextToken := queue.NextToken
	if queue.reconcileScrollback(0) {
		t.Fatal("zero terminal epoch was accepted")
	}
	if !queue.reconcileScrollback(1) {
		t.Fatalf("new terminal epoch was rejected: epoch=%d entries=%#v", queue.TerminalEpoch, queue.Entries())
	}
	if queue.TerminalEpoch != 1 || queue.NextToken != nextToken || len(queue.Entries()) != 0 {
		t.Fatalf("reconciliation did not retire old delivery ledger: %#v", queue)
	}
	if queue.reconcileScrollback(1) {
		t.Fatal("duplicate terminal epoch retired ledger twice")
	}
}

func TestHistoryEffectsReducer_BootstrapBatchAcknowledgesOrderedPendingRangesAtomically(t *testing.T) {
	state := historyEffectTestState(t, 2)
	entries := state.HistoryEffects.Entries()
	if len(entries) < 3 {
		t.Fatalf("fixture entries = %#v, want at least three", entries)
	}
	commits := []HistoryCommit{
		entries[0].Commit.Clone(),
		entries[1].Commit.Clone(),
		entries[2].Commit.Clone(),
	}
	state = reduceUIControllerState(state, BeginHistoryCommit{
		Token: commits[0].Token, LayoutGeneration: state.LayoutGeneration,
	}, 3)
	state = reduceUIControllerState(state, HistoryCommitsAcknowledged{
		Commits: commits, Frame: 9, LayoutGeneration: state.LayoutGeneration,
	}, 4)
	for _, commit := range commits {
		entry := historyCommitEntry(t, state, commit.Token)
		if entry.State != HistoryCommitAcked || entry.AckFrame != 9 || entry.Commit.Lines != nil {
			t.Fatalf("bootstrap token %d was not atomically acknowledged: %#v", commit.Token, entry)
		}
	}
	if state.HistoryEffects.ProjectionUnknown {
		t.Fatalf("valid bootstrap batch invalidated projection: %#v", state.HistoryEffects)
	}
}

func TestHistoryEffectsReducer_BootstrapBatchMismatchQuarantinesWholeDeliveredBatch(t *testing.T) {
	state := historyEffectTestState(t, 2)
	entries := state.HistoryEffects.Entries()
	if len(entries) < 3 {
		t.Fatalf("fixture entries = %#v, want at least three", entries)
	}
	commits := []HistoryCommit{
		entries[0].Commit.Clone(),
		entries[1].Commit.Clone(),
		entries[2].Commit.Clone(),
	}
	state = reduceUIControllerState(state, BeginHistoryCommit{
		Token: commits[0].Token, LayoutGeneration: state.LayoutGeneration,
	}, 3)

	// Simulate a semantic/layout action rebasing a later token while the old
	// bootstrap snapshot is already crossing the terminal writer.
	rebased := commits[1].Clone()
	rebased.Lines = []render.Line{{Spans: []render.Span{{Text: "rebased payload"}}}}
	if err := state.HistoryEffects.rebasePending(rebased); err != nil {
		t.Fatalf("rebase pending bootstrap token: %v", err)
	}
	state = reduceUIControllerState(state, HistoryCommitsAcknowledged{
		Commits: commits, Frame: 9, LayoutGeneration: state.LayoutGeneration,
	}, 4)

	for _, commit := range commits {
		entry := historyCommitEntry(t, state, commit.Token)
		if entry.State != HistoryCommitStateFailed || entry.AckFrame != 0 ||
			!entry.MayHavePartiallyWritten || !errors.Is(entry.Failure, ErrCommitSourceChanged) {
			t.Fatalf("mismatched delivered token %d was not quarantined: %#v", commit.Token, entry)
		}
	}
	if !state.HistoryEffects.ProjectionUnknown || len(state.HistoryEffects.Pending()) != 0 {
		t.Fatalf("mismatched batch remained retryable: %#v", state.HistoryEffects)
	}

	state = reduceUIControllerState(state, HistoryProjectionRecovered{LayoutGeneration: state.LayoutGeneration}, 5)
	if state.HistoryEffects.ProjectionUnknown || state.HistoryEffects.HasPending() {
		t.Fatalf("viewport-only recovery exposed an unresolved batch: %#v", state.HistoryEffects)
	}
}

func TestPlanEligibleHistoryCommits_UsesPhysicalWrappedRows(t *testing.T) {
	state := AppState{
		Geometry:         GeometryState{Width: 4, Height: 4, Generation: 1},
		LayoutGeneration: 1,
		Transcript: NewTranscriptState(&scene.Snapshot{Cells: []*scene.TranscriptCell{
			{ID: 1, Revision: 1, Kind: scene.KindAssistant, Source: "123456", Phase: scene.CellCommitted},
			{ID: 2, Revision: 1, Kind: scene.KindAssistant, Source: "abcdef", Phase: scene.CellCommitted},
		}}),
	}
	commits := planEligibleHistoryCommits(state)
	if len(commits) != 4 {
		t.Fatalf("physical wrapped eligibility = %#v, want all four finalized row fragments", commits)
	}
	want := []struct {
		source  SourceRange
		display DisplayRange
		text    string
	}{
		{source: SourceRange{Start: 0, End: 4}, display: DisplayRange{Start: 0, End: 1}, text: "1234"},
		{source: SourceRange{Start: 4, End: 6}, display: DisplayRange{Start: 1, End: 2}, text: "56"},
	}
	for index, expected := range want {
		commit := commits[index]
		if commit.CellID != 1 || commit.SourceRange != expected.source || commit.DisplayRange != expected.display {
			t.Fatalf("history row fragment %d = %#v, want source=%+v display=%+v", index, commit, expected.source, expected.display)
		}
		if len(commit.Lines) != 1 || len(commit.Lines[0].Spans) != 1 || commit.Lines[0].Spans[0].Text != expected.text {
			t.Fatalf("history row fragment %d payload = %#v, want %q", index, commit.Lines, expected.text)
		}
		line := commit.Lines[0]
		if len(line.Spans) != 1 || line.Spans[0].Style.Role != string(style.RoleAssistant) {
			t.Fatalf("history display line %d lost assistant render role: %#v", index+1, line)
		}
	}
}

func TestPlanEligibleHistoryCommits_SplitsUnbrokenPlainLineAtPrimaryBoundary(t *testing.T) {
	state := AppState{
		Geometry:         GeometryState{Width: 4, Height: 3, Generation: 1},
		LayoutGeneration: 1,
		Transcript: NewTranscriptState(&scene.Snapshot{Cells: []*scene.TranscriptCell{
			{ID: 1, Revision: 1, Kind: scene.KindAssistant, Source: "abcdefghijkl", Phase: scene.CellCommitted},
		}}),
	}

	commits := planEligibleHistoryCommits(state)
	if len(commits) != 3 {
		t.Fatalf("unbroken finalized commits = %#v, want every wrapped row", commits)
	}
	got := commits[0]
	if got.SourceRange != (SourceRange{Start: 0, End: 4}) || got.DisplayRange != (DisplayRange{Start: 0, End: 1}) {
		t.Fatalf("unbroken overflow identity = %#v", got)
	}
	if len(got.Lines) != 1 || len(got.Lines[0].Spans) != 1 || got.Lines[0].Spans[0].Text != "abcd" {
		t.Fatalf("unbroken overflow payload = %#v", got.Lines)
	}
}

func TestHistoryEffectsReducer_UnbrokenLineHandoffsEachRowOnce(t *testing.T) {
	cell := &scene.TranscriptCell{
		ID: 1, Revision: 1, Kind: scene.KindAssistant,
		Source: "abcdefghijkl", Phase: scene.CellCommitted,
	}
	state := UIControllerState{}
	state = reduceUIControllerState(state, Resize{Width: 4, Height: 3, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{Cells: []*scene.TranscriptCell{cell}}}, 2)
	entries := state.HistoryEffects.Entries()
	if len(entries) != 3 || entries[0].Commit.SourceRange != (SourceRange{Start: 0, End: 4}) {
		t.Fatalf("initial unbroken-line history = %#v", entries)
	}
	first := entries[0].Commit
	state = reduceUIControllerState(state, BeginHistoryCommit{Token: first.Token, LayoutGeneration: 1}, 3)
	state = reduceUIControllerState(state, HistoryCommitAcknowledged{Token: first.Token, Frame: 1, LayoutGeneration: 1}, 4)

	state = reduceUIControllerState(state, Resize{Width: 4, Height: 4, Generation: 2}, 5)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{Cells: []*scene.TranscriptCell{
		cell,
		{ID: 2, Revision: 1, Kind: scene.KindAssistant, Source: "tail", Phase: scene.CellCommitted},
	}}}, 6)

	entries = state.HistoryEffects.Entries()
	rowRanges := make(map[SourceRange]int)
	for _, entry := range entries {
		if entry.Commit.CellID == 1 {
			rowRanges[entry.Commit.SourceRange]++
		}
	}
	if rowRanges[SourceRange{Start: 0, End: 4}] != 1 || rowRanges[SourceRange{Start: 4, End: 8}] != 1 ||
		rowRanges[SourceRange{Start: 8, End: 12}] != 1 {
		t.Fatalf("unbroken line did not advance by one non-duplicated row: %#v", entries)
	}
	if len(rowRanges) != 3 {
		t.Fatalf("unbroken line minted duplicate or skipped ranges: %#v", entries)
	}
}

func TestPlanEligibleHistoryCommits_SplitsCJKWrappedRowsAtByteBoundaries(t *testing.T) {
	state := AppState{
		Geometry:         GeometryState{Width: 4, Height: 3, Generation: 1},
		LayoutGeneration: 1,
		Transcript: NewTranscriptState(&scene.Snapshot{Cells: []*scene.TranscriptCell{
			{ID: 1, Revision: 1, Kind: scene.KindAssistant, Source: "甲乙丙丁己", Phase: scene.CellCommitted},
		}}),
	}
	commits := planEligibleHistoryCommits(state)
	if len(commits) != 3 {
		t.Fatalf("CJK finalized commits = %#v, want every display row", commits)
	}
	got := commits[0]
	if got.SourceRange != (SourceRange{Start: 0, End: len("甲乙")}) || got.DisplayRange != (DisplayRange{Start: 0, End: 1}) {
		t.Fatalf("CJK source/display boundary = %#v", got)
	}
	if len(got.Lines) != 1 || len(got.Lines[0].Spans) != 1 || got.Lines[0].Spans[0].Text != "甲乙" {
		t.Fatalf("CJK display payload = %#v", got.Lines)
	}
}

func TestPlanEligibleHistoryCommits_SplitsMarkdownRowsAtPrimaryBoundary(t *testing.T) {
	const source = "# markdown heading\n\n- **markdown-history-01**\n- **markdown-history-02**\n- **markdown-history-03**\n- **markdown-history-04**\n- **markdown-history-05**"
	state := AppState{
		Geometry:         GeometryState{Width: 48, Height: 4, Generation: 1},
		LayoutGeneration: 1,
		Transcript: NewTranscriptState(&scene.Snapshot{Cells: []*scene.TranscriptCell{
			{ID: 1, Revision: 1, Kind: scene.KindAssistant, Source: source, Phase: scene.CellCommitted},
		}}),
	}

	commits := planEligibleHistoryCommits(state)
	if len(commits) == 0 {
		t.Fatal("overflowed Markdown did not create history fragments")
	}
	seenFragments := make(map[uint64]struct{}, len(commits))
	for _, commit := range commits {
		if commit.SourceRange != (SourceRange{Start: 0, End: len(source)}) || commit.FragmentID == 0 {
			t.Fatalf("Markdown fragment identity = %#v", commit)
		}
		if _, duplicate := seenFragments[commit.FragmentID]; duplicate {
			t.Fatalf("duplicate Markdown renderer fragment %d: %#v", commit.FragmentID, commits)
		}
		seenFragments[commit.FragmentID] = struct{}{}
		payload := render.PlainBackend{}.Render(render.LinesDoc(commit.Lines...))
		if strings.Contains(payload, "**") || strings.Contains(payload, "# markdown") {
			t.Fatalf("Markdown history payload leaked raw source: %q", payload)
		}
	}
}

// A finalized cell can be taller than the retained primary viewport. Its
// invisible prefix must still become tokenized history; limiting eligibility
// to whole cells leaves that prefix in neither the primary frame nor native
// scrollback. Plain source lines provide stable source boundaries for the
// initial segmented-handoff implementation.
func TestPlanEligibleHistoryCommits_SegmentsOversizedFinalizedPlainCell(t *testing.T) {
	const source = "first\nsecond\nthird\nfourth\nfifth\nsixth\nseventh"
	state := AppState{
		Geometry:         GeometryState{Width: 80, Height: 6, Generation: 1},
		LayoutGeneration: 1,
		Transcript: NewTranscriptState(&scene.Snapshot{Cells: []*scene.TranscriptCell{
			{ID: 1, Revision: 1, Kind: scene.KindAssistant, Source: source, Phase: scene.CellCommitted},
		}}),
	}

	commits := planEligibleHistoryCommits(state)
	if len(commits) != 7 {
		t.Fatalf("oversized finalized cell commits = %#v, want all source-line segments", commits)
	}
	wantRanges := []struct {
		source  SourceRange
		display DisplayRange
		text    string
	}{
		{source: SourceRange{Start: 0, End: 6}, display: DisplayRange{Start: 0, End: 1}, text: "first"},
		{source: SourceRange{Start: 6, End: 13}, display: DisplayRange{Start: 1, End: 2}, text: "second"},
	}
	for index, want := range wantRanges {
		got := commits[index]
		if got.CellID != 1 || got.Revision != 1 || got.SourceRange != want.source || got.DisplayRange != want.display {
			t.Fatalf("commit[%d] identity = %#v, want source=%+v display=%+v", index, got, want.source, want.display)
		}
		if len(got.Lines) != 1 || len(got.Lines[0].Spans) != 1 || got.Lines[0].Spans[0].Text != want.text {
			t.Fatalf("commit[%d] render payload = %#v, want %q", index, got.Lines, want.text)
		}
	}
}

func BenchmarkPlanEligibleHistoryCommitsPlainTranscript(b *testing.B) {
	cells := make([]*scene.TranscriptCell, 0, 96)
	for id := scene.CellID(1); id <= 96; id++ {
		cells = append(cells, &scene.TranscriptCell{
			ID: id, Revision: 1, Kind: scene.KindAssistant,
			Source: "The quick brown fox jumps over the lazy dog.", Phase: scene.CellCommitted,
		})
	}
	state := AppState{
		Geometry:         GeometryState{Width: 100, Height: 24, Generation: 1},
		LayoutGeneration: 1,
		Transcript:       NewTranscriptState(&scene.Snapshot{Cells: cells}),
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		_ = planEligibleHistoryCommits(state)
	}
}

func TestHistoryEffectsReducer_TranscriptOnlyMintsAndResizeOnlyRebases(t *testing.T) {
	state := UIControllerState{}
	state = reduceUIControllerState(state, Resize{Width: 80, Height: 10, Generation: 5}, 1)
	cells := make([]*scene.TranscriptCell, 0, 24)
	for id := scene.CellID(1); id <= 24; id++ {
		cells = append(cells, &scene.TranscriptCell{
			ID: id, Revision: 1, Kind: scene.KindAssistant,
			Source: "final", Phase: scene.CellCommitted,
		})
	}
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{Revision: 2, Cells: cells}}, 2)
	before := state.HistoryEffects.Entries()
	if len(before) == 0 {
		t.Fatal("expected finalized cells above viewport to enqueue history effects")
	}
	nextToken := state.HistoryEffects.NextToken
	state = reduceUIControllerState(state, Resize{Width: 100, Height: 10, Generation: 6}, 3)
	after := state.HistoryEffects.Entries()
	if state.HistoryEffects.NextToken != nextToken || len(after) != len(before) {
		t.Fatalf("resize minted or discarded tokens: next %d->%d entries %d->%d", nextToken, state.HistoryEffects.NextToken, len(before), len(after))
	}
	for index := range after {
		if after[index].Commit.Token != before[index].Commit.Token {
			t.Fatalf("resize replaced token at %d: before=%#v after=%#v", index, before[index], after[index])
		}
		if after[index].State == HistoryCommitPending && after[index].Commit.LayoutGeneration != 6 {
			t.Fatalf("pending commit was not rebased: %#v", after[index])
		}
	}
}

func TestHistoryEffectsReducer_StaleBeginClaimDoesNotInvalidateProjection(t *testing.T) {
	state := historyEffectTestState(t, 2)
	pending := state.HistoryEffects.Pending()
	if len(pending) == 0 {
		t.Fatal("fixture has no pending history commit")
	}
	token := pending[0].Token

	state = reduceUIControllerState(state, BeginHistoryCommit{
		Token: token, LayoutGeneration: state.LayoutGeneration - 1,
	}, 3)
	entry := historyCommitEntry(t, state, token)
	if entry.State != HistoryCommitPending {
		t.Fatalf("stale begin changed pending token state: %#v", entry)
	}
	if state.HistoryEffects.ProjectionUnknown || state.HistoryEffects.ReconciliationRequired {
		t.Fatalf("non-writing stale claim invalidated projection: %#v", state.HistoryEffects)
	}
}

func TestHistoryEffectsReducer_LeaseFreezesAndReplacementInvalidatesPending(t *testing.T) {
	state := UIControllerState{}
	state = reduceUIControllerState(state, Resize{Width: 80, Height: 10, Generation: 2}, 1)
	cells := make([]*scene.TranscriptCell, 0, 20)
	for id := scene.CellID(1); id <= 20; id++ {
		cells = append(cells, &scene.TranscriptCell{ID: id, Revision: 1, Kind: scene.KindAssistant, Source: "final", Phase: scene.CellCommitted})
	}
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{Revision: 1, Cells: cells}}, 2)
	entries := state.HistoryEffects.Entries()
	if len(entries) == 0 {
		t.Fatal("expected pending history effects")
	}
	token := entries[0].Commit.Token
	state = reduceUIControllerState(state, LeaseAcquired{LeaseID: 12}, 3)
	state = reduceUIControllerState(state, BeginHistoryCommit{Token: token, LayoutGeneration: 2}, 4)
	if entry := state.HistoryEffects.Entries()[0]; entry.State != HistoryCommitPending {
		t.Fatalf("lease allowed pending token to enter flight: %#v", entry)
	}
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{Revision: 2, Cells: cells[len(cells)-1:]}}, 5)
	invalidated := false
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.Commit.Token == token {
			invalidated = entry.State == HistoryCommitInvalidated
		}
	}
	if !invalidated {
		t.Fatalf("replacement did not invalidate removed pending token %d: %#v", token, state.HistoryEffects.Entries())
	}
	state = reduceUIControllerState(state, LeaseReleased{LeaseID: 12}, 6)
	if state.HistoryEffects.Frozen {
		t.Fatal("lease release left history queue frozen")
	}
}

func TestHistoryEffectsReducer_TranscriptBoundaryChangeRebasesPendingHandoff(t *testing.T) {
	state, token := historyEffectBoundaryChangeState(t)
	before := historyCommitEntry(t, state, token)
	if before.State != HistoryCommitPending || len(before.Commit.Lines) != 2 || len(before.Commit.Lines[0].Spans) != 0 {
		t.Fatalf("fixture did not retain boundary gap in pending payload: %#v", before)
	}

	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: historyEffectBoundaryChangedSnapshot(state)}, 3)
	after := historyCommitEntry(t, state, token)
	if after.State != HistoryCommitPending || after.Commit.Token != token {
		t.Fatalf("boundary-only replacement changed pending delivery identity: before=%#v after=%#v", before, after)
	}
	if after.Commit.DisplayRange == before.Commit.DisplayRange || len(after.Commit.Lines) != 1 ||
		len(after.Commit.Lines[0].Spans) != 1 || after.Commit.Lines[0].Spans[0].Text != "candidate" {
		t.Fatalf("pending payload retained stale boundary rows: before=%#v after=%#v", before, after)
	}
	if state.HistoryEffects.ProjectionUnknown {
		t.Fatalf("unstarted payload rebase unnecessarily invalidated projection: %#v", state.HistoryEffects)
	}
}

func TestHistoryEffectsReducer_TranscriptBoundaryChangeInvalidatesInFlightHandoff(t *testing.T) {
	state, token := historyEffectBoundaryChangeState(t)
	if !state.HistoryEffects.HasPending() {
		t.Fatal("fixture did not expose pending history")
	}
	// Native scrollback is ordered; retire any older eligible cells so the
	// candidate can be the token currently in flight for this test.
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.Commit.Token >= token || entry.State != HistoryCommitPending {
			continue
		}
		state = reduceUIControllerState(state, BeginHistoryCommit{Token: entry.Commit.Token, LayoutGeneration: state.LayoutGeneration}, 3)
		state = reduceUIControllerState(state, HistoryCommitAcknowledged{Token: entry.Commit.Token, Frame: entry.Commit.Token, LayoutGeneration: state.LayoutGeneration}, 3)
	}
	state = reduceUIControllerState(state, BeginHistoryCommit{Token: token, LayoutGeneration: state.LayoutGeneration}, 3)
	if entry := historyCommitEntry(t, state, token); entry.State != HistoryCommitInFlight {
		t.Fatalf("begin history = %#v", entry)
	}
	beforeToken := state.HistoryEffects.NextToken

	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: historyEffectBoundaryChangedSnapshot(state)}, 4)
	entry := historyCommitEntry(t, state, token)
	if entry.State != HistoryCommitInvalidated || !entry.MayHavePartiallyWritten || !state.HistoryEffects.ProjectionUnknown {
		t.Fatalf("changed in-flight payload was not fail-closed: entry=%#v state=%#v", entry, state.HistoryEffects)
	}
	if state.HistoryEffects.NextToken != beforeToken || state.HistoryEffects.HasPending() {
		t.Fatalf("in-flight invalidation minted or exposed a duplicate handoff: next=%d->%d pending=%#v", beforeToken, state.HistoryEffects.NextToken, state.HistoryEffects.Pending())
	}
}

func TestHistoryEffectsReducer_ResizeInvalidatesInFlightAndRequiresRecovery(t *testing.T) {
	state := historyEffectTestState(t, 2)
	entries := state.HistoryEffects.Entries()
	token := entries[0].Commit.Token
	state = reduceUIControllerState(state, BeginHistoryCommit{Token: token, LayoutGeneration: 2}, 3)
	if entry := historyCommitEntry(t, state, token); entry.State != HistoryCommitInFlight {
		t.Fatalf("begin entry = %#v, want in flight", entry)
	}
	count, nextToken := len(entries), state.HistoryEffects.NextToken
	state = reduceUIControllerState(state, Resize{Width: 100, Height: 10, Generation: 3}, 4)
	entry := historyCommitEntry(t, state, token)
	if entry.State != HistoryCommitInvalidated || !entry.MayHavePartiallyWritten || !state.HistoryEffects.ProjectionUnknown {
		t.Fatalf("resize did not invalidate in-flight projection: entry=%#v unknown=%t", entry, state.HistoryEffects.ProjectionUnknown)
	}
	if len(state.HistoryEffects.Entries()) != count || state.HistoryEffects.NextToken != nextToken {
		t.Fatalf("resize changed history token inventory: entries=%d/%d token=%d/%d", len(state.HistoryEffects.Entries()), count, state.HistoryEffects.NextToken, nextToken)
	}
	state = reduceUIControllerState(state, HistoryCommitAcknowledged{Token: token, Frame: 8, LayoutGeneration: 2}, 5)
	if entry := historyCommitEntry(t, state, token); entry.State != HistoryCommitInvalidated {
		t.Fatalf("stale ack advanced invalidated token: %#v", entry)
	}
	state = reduceUIControllerState(state, HistoryProjectionRecovered{LayoutGeneration: 3}, 6)
	if state.HistoryEffects.ProjectionUnknown {
		t.Fatal("matching recovery did not restore known projection")
	}
}

func TestHistoryEffectsReducer_BatchAckAcceptedWhenLeaseArrivesAfterTerminalWrite(t *testing.T) {
	state := historyEffectTestState(t, 2)
	pending := state.HistoryEffects.Pending()
	if len(pending) < 2 {
		t.Fatalf("fixture pending commits = %d, want at least 2", len(pending))
	}
	state = reduceUIControllerState(state, BeginHistoryCommit{
		Token: pending[0].Token, LayoutGeneration: state.LayoutGeneration,
	}, 3)
	state = reduceUIControllerState(state, LeaseAcquired{LeaseID: 44}, 4)
	state = reduceUIControllerState(state, HistoryCommitsAcknowledged{
		Commits: pending, Frame: 9, LayoutGeneration: state.LayoutGeneration,
	}, 5)

	if !state.Lease.Active || !state.HistoryEffects.Frozen || state.HistoryEffects.ProjectionUnknown {
		t.Fatalf("late lease changed acknowledgement safety: lease=%#v effects=%#v", state.Lease, state.HistoryEffects)
	}
	for index, commit := range pending {
		entry := historyCommitEntry(t, state, commit.Token)
		if entry.State != HistoryCommitAcked || entry.AckFrame != 9 || entry.MayHavePartiallyWritten {
			t.Fatalf("delivered entry[%d] rejected after lease barrier: %#v", index, entry)
		}
	}
}

func TestHistoryEffectsReducer_ScrollbackReconciliationRequiresRecoveryAndReplansFreshTokens(t *testing.T) {
	state := historyEffectTestState(t, 2)
	oldEntries := state.HistoryEffects.Entries()
	oldToken := oldEntries[0].Commit.Token
	oldNextToken := state.HistoryEffects.NextToken
	state = reduceUIControllerState(state, BeginHistoryCommit{Token: oldToken, LayoutGeneration: 2}, 3)
	state = reduceUIControllerState(state, HistoryCommitFailed{
		Token: oldToken, LayoutGeneration: 2, Err: errors.New("terminal failed"), MayHavePartiallyWritten: true,
	}, 4)
	if !state.HistoryEffects.ProjectionUnknown {
		t.Fatal("failed handoff did not require recovery")
	}
	if !state.HistoryEffects.ReconciliationRequired {
		t.Fatal("possibly written handoff did not request scrollback reconciliation")
	}

	// The reset claim is not a substitute for a current source-backed frame.
	state = reduceUIControllerState(state, HistoryScrollbackReconciled{LayoutGeneration: 2, TerminalEpoch: 1}, 5)
	if state.HistoryEffects.TerminalEpoch != 0 || len(state.HistoryEffects.Entries()) != len(oldEntries) {
		t.Fatalf("unrecovered reset claim changed delivery ledger: %#v", state.HistoryEffects)
	}
	state = reduceUIControllerState(state, HistoryProjectionRecovered{LayoutGeneration: 2}, 6)
	state = reduceUIControllerState(state, HistoryScrollbackReconciled{LayoutGeneration: 2, TerminalEpoch: 1}, 7)
	entries := state.HistoryEffects.Entries()
	if state.HistoryEffects.TerminalEpoch != 1 || len(entries) == 0 || state.HistoryEffects.NextToken <= oldNextToken {
		t.Fatalf("reconciliation did not mint a fresh epoch: epoch=%d next=%d entries=%#v", state.HistoryEffects.TerminalEpoch, state.HistoryEffects.NextToken, entries)
	}
	if state.HistoryEffects.ReconciliationRequired {
		t.Fatal("fresh terminal epoch retained reconciliation intent")
	}
	for _, entry := range entries {
		if entry.Commit.Token <= oldNextToken || entry.State != HistoryCommitPending {
			t.Fatalf("old delivery leaked into fresh epoch: %#v", entry)
		}
	}

	// The old terminal callback cannot acknowledge a newly replanned range.
	state = reduceUIControllerState(state, HistoryCommitAcknowledged{Token: oldToken, Frame: 99, LayoutGeneration: 2}, 8)
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.Commit.Token == oldToken {
			t.Fatalf("stale acknowledgement resurrected retired token: %#v", entry)
		}
	}
}

func TestHistoryEffectsReducer_AckedSourceIsNotMintedAgainAfterResize(t *testing.T) {
	state := historyEffectTestState(t, 2)
	token := state.HistoryEffects.Entries()[0].Commit.Token
	state = reduceUIControllerState(state, BeginHistoryCommit{Token: token, LayoutGeneration: 2}, 3)
	state = reduceUIControllerState(state, HistoryCommitAcknowledged{Token: token, Frame: 9, LayoutGeneration: 2}, 4)
	if entry := historyCommitEntry(t, state, token); entry.State != HistoryCommitAcked {
		t.Fatalf("head token not acked: %#v", entry)
	}

	beforeToken := state.HistoryEffects.NextToken
	snapshot := state.Transcript.Snapshot()
	state = reduceUIControllerState(state, Resize{Width: 100, Height: 10, Generation: 3}, 5)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, 6)
	if state.HistoryEffects.NextToken != beforeToken {
		t.Fatalf("acked source minted a second token after resize: %d -> %d", beforeToken, state.HistoryEffects.NextToken)
	}
	entry := historyCommitEntry(t, state, token)
	if entry.State != HistoryCommitAcked || entry.Commit.Lines != nil {
		t.Fatalf("acked source was changed or retained payload: %#v", entry)
	}
}

func TestHistoryEffectsReducer_InsertionBeforeAckedHistoryRequiresScrollbackReconciliation(t *testing.T) {
	state := historyEffectTestState(t, 2)
	first := state.HistoryEffects.Entries()[0].Commit
	state = reduceUIControllerState(state, BeginHistoryCommit{
		Token: first.Token, LayoutGeneration: state.LayoutGeneration,
	}, 3)
	state = reduceUIControllerState(state, HistoryCommitAcknowledged{
		Token: first.Token, Frame: 9, LayoutGeneration: state.LayoutGeneration,
	}, 4)

	snapshot := state.Transcript.Snapshot()
	reasoning := &scene.TranscriptCell{
		ID: 99, Revision: 1, Kind: scene.KindReasoning,
		Source: "late reasoning before delivered assistant", Phase: scene.CellCommitted,
	}
	snapshot.Cells = append([]*scene.TranscriptCell{reasoning}, snapshot.Cells...)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, 5)

	if !state.HistoryEffects.ProjectionUnknown || !state.HistoryEffects.ReconciliationRequired {
		t.Fatalf("semantic predecessor reused already-acked physical order: %#v", state.HistoryEffects)
	}
	if len(state.Transcript.Cells) == 0 || state.Transcript.Cells[0].ID != reasoning.ID {
		t.Fatalf("semantic insertion was not retained for source-backed replay: %+v", state.Transcript.Cells)
	}
}

func TestHistoryEffectsReducer_LeaseAndStaleRecoveryCannotClearUnknown(t *testing.T) {
	state := historyEffectTestState(t, 2)
	state.HistoryEffects.ProjectionUnknown = true
	state = reduceUIControllerState(state, LeaseAcquired{LeaseID: 44}, 3)
	state = reduceUIControllerState(state, HistoryProjectionRecovered{LayoutGeneration: 2}, 4)
	if !state.HistoryEffects.ProjectionUnknown {
		t.Fatal("recovery during alternate-screen lease cleared unknown projection")
	}
	state = reduceUIControllerState(state, LeaseReleased{LeaseID: 44}, 5)
	state = reduceUIControllerState(state, HistoryProjectionRecovered{LayoutGeneration: 1}, 6)
	if !state.HistoryEffects.ProjectionUnknown {
		t.Fatal("stale recovery generation cleared unknown projection")
	}
	state = reduceUIControllerState(state, HistoryProjectionRecovered{LayoutGeneration: 2}, 7)
	if state.HistoryEffects.ProjectionUnknown {
		t.Fatal("current recovery generation did not restore known projection")
	}
}

func TestHistoryEffectsReducer_StaleResizeCannotRewindGeneration(t *testing.T) {
	state := historyEffectTestState(t, 7)
	before := state.Clone()
	state = reduceUIControllerState(state, Resize{Width: 20, Height: 10, Generation: 6}, 3)
	if state.Geometry != before.Geometry || state.LayoutGeneration != before.LayoutGeneration {
		t.Fatalf("stale resize rewound geometry/layout: before=%+v/%d after=%+v/%d", before.Geometry, before.LayoutGeneration, state.Geometry, state.LayoutGeneration)
	}
}

func historyEffectTestState(t *testing.T, generation uint64) UIControllerState {
	t.Helper()
	state := UIControllerState{}
	state = reduceUIControllerState(state, Resize{Width: 80, Height: 10, Generation: generation}, 1)
	cells := make([]*scene.TranscriptCell, 0, 20)
	for id := scene.CellID(1); id <= 20; id++ {
		cells = append(cells, &scene.TranscriptCell{ID: id, Revision: 1, Kind: scene.KindAssistant, Source: "final", Phase: scene.CellCommitted})
	}
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{Revision: 1, Cells: cells}}, 2)
	if len(state.HistoryEffects.Entries()) == 0 {
		t.Fatal("expected eligible history effects")
	}
	return state
}

func historyEffectBoundaryChangeState(t *testing.T) (UIControllerState, uint64) {
	t.Helper()
	state := UIControllerState{}
	state = reduceUIControllerState(state, Resize{Width: 80, Height: 6, Generation: 2}, 1)
	cells := []*scene.TranscriptCell{
		{ID: 1, Sequence: 1, Kind: scene.KindAssistant, Source: "first", Revision: 1, Phase: scene.CellCommitted},
		// The candidate has a stable chain identity, while its predecessor
		// initially does not. That leaves one derived boundary row in the
		// pending display payload without changing candidate source identity.
		{ID: 2, Sequence: 2, Kind: scene.KindToolChain, ChainKey: "chain", Source: "candidate", Revision: 1, Phase: scene.CellCommitted},
	}
	for id := scene.CellID(3); id <= 10; id++ {
		cells = append(cells, &scene.TranscriptCell{ID: id, Sequence: uint64(id), Kind: scene.KindAssistant, Source: "tail", Revision: 1, Phase: scene.CellCommitted})
	}
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{Revision: 1, Cells: cells}}, 2)
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.Commit.CellID == 2 {
			return state, entry.Commit.Token
		}
	}
	t.Fatalf("candidate handoff was not eligible: %#v", state.HistoryEffects.Entries())
	return UIControllerState{}, 0
}

func historyEffectBoundaryChangedSnapshot(state UIControllerState) *scene.Snapshot {
	snapshot := state.Transcript.Snapshot()
	if len(snapshot.Cells) > 0 {
		// Keeping the candidate source/revision intact but joining its
		// predecessor's chain removes the boundary display row before it.
		snapshot.Cells[0].ChainKey = "chain"
	}
	return snapshot
}

func historyCommitEntry(t *testing.T, state UIControllerState, token uint64) HistoryCommitEntry {
	t.Helper()
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.Commit.Token == token {
			return entry
		}
	}
	t.Fatalf("history token %d missing from %#v", token, state.HistoryEffects.Entries())
	return HistoryCommitEntry{}
}
