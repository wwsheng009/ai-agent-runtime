package ui

import (
	"errors"
	"testing"

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
	if len(commits) != 1 || commits[0].CellID != 1 {
		t.Fatalf("physical wrapped eligibility = %#v, want only cell 1", commits)
	}
	if commits[0].DisplayRange != (DisplayRange{Start: 0, End: 2}) || len(commits[0].Lines) != 2 {
		t.Fatalf("physical display payload = %#v", commits[0])
	}
	for index, line := range commits[0].Lines {
		if len(line.Spans) != 1 || line.Spans[0].Style.Role != string(style.RoleAssistant) {
			t.Fatalf("history display line %d lost assistant render role: %#v", index+1, line)
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
