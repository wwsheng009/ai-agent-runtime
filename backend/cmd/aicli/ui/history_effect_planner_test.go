package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// TestPlanMutableActiveCellHistoryCommits covers the overflow handoff planner
// for a still-mutable plain cell: blank lines (including the trailing newline
// of a streamed body) must not abort the whole prefix, Markdown stays out of
// scope, and an already-acknowledged source offset resumes the projection.
func TestPlanMutableActiveCellHistoryCommits(t *testing.T) {
	geometry := GeometryState{Width: 80, Height: 12}

	newActive := func(source string, ackedEnd int) ActiveCellState {
		return ActiveCellState{
			CellID:   41,
			Revision: 7,
			Kind:     scene.KindAssistant,
			Phase:    ActiveCellMutable,
			Source:   source,
			Stable:   SourceRange{Start: 0, End: len(source)},
			Acked:    SourceRange{Start: 0, End: ackedEnd},
		}
	}

	t.Run("blank lines and trailing newline do not abort the prefix", func(t *testing.T) {
		lines := make([]string, 0, 31)
		for index := 0; index < 30; index++ {
			if index == 10 {
				lines = append(lines, "")
			}
			lines = append(lines, fmt.Sprintf("mutable-row-%03d", index))
		}
		source := strings.Join(lines, "\n") + "\n"
		active := newActive(source, 0)

		commits := planMutableActiveCellHistoryCommits(active, geometry, 1)
		if len(commits) == 0 {
			t.Fatalf("expected a non-empty overflow prefix, got none")
		}
		if commits[0].Lines[0].Spans[0].Text != "mutable-row-000" {
			t.Fatalf("first handoff row = %q, want mutable-row-000", commits[0].Lines[0].Spans[0].Text)
		}
		wantRows := len(strings.Split(source, "\n")) - ActiveBandRows(geometry.Height)
		if len(commits) != wantRows {
			t.Fatalf("handoff rows = %d, want %d", len(commits), wantRows)
		}
		// Rows are contiguous and ordered by display row.
		for index, commit := range commits {
			if commit.DisplayRange.Start != index || commit.DisplayRange.End != index+1 {
				t.Fatalf("commit %d display range = %+v, want contiguous rows", index, commit.DisplayRange)
			}
			if commit.SourceRange.Start >= commit.SourceRange.End {
				t.Fatalf("commit %d source range %+v is empty", index, commit.SourceRange)
			}
		}
		// The last handoff row is the band's first visible row minus one.
		wantLast := strings.Split(source, "\n")[wantRows-1]
		last := commits[len(commits)-1]
		if last.Lines[0].Spans[0].Text != wantLast {
			t.Fatalf("last handoff row = %q, want %q", last.Lines[0].Spans[0].Text, wantLast)
		}
	})

	t.Run("markdown stays out of scope", func(t *testing.T) {
		source := "# heading\n\nplain body\n" + strings.Repeat("x\n", 30)
		active := newActive(source, 0)
		if commits := planMutableActiveCellHistoryCommits(active, geometry, 1); len(commits) != 0 {
			t.Fatalf("markdown source produced %d handoff commits, want none", len(commits))
		}
	})

	t.Run("body that fits the band budget produces no commits", func(t *testing.T) {
		active := newActive(strings.Join([]string{"one", "two", "three"}, "\n"), 0)
		if commits := planMutableActiveCellHistoryCommits(active, geometry, 1); len(commits) != 0 {
			t.Fatalf("short source produced %d handoff commits, want none", len(commits))
		}
	})

	t.Run("acknowledged offset resumes the projection", func(t *testing.T) {
		source := strings.Join([]string{
			"mutable-row-000", "mutable-row-001", "mutable-row-002",
			"mutable-row-003", "mutable-row-004", "mutable-row-005",
			"mutable-row-006", "mutable-row-007", "mutable-row-008",
			"mutable-row-009", "mutable-row-010", "mutable-row-011",
		}, "\n")
		// ack through row-007 (its trailing newline), leaving rows 008..011 live.
		ackedEnd := strings.Index(source, "mutable-row-008")
		active := newActive(source, ackedEnd)

		commits := planMutableActiveCellHistoryCommits(active, geometry, 1)
		if len(commits) != 0 {
			t.Fatalf("resumed projection produced %d commits, want none (tail fits the band)", len(commits))
		}
	})

	t.Run("partially acknowledged short body hands off the remaining prefix", func(t *testing.T) {
		source := strings.Join([]string{
			"mutable-row-000", "mutable-row-001", "mutable-row-002",
			"mutable-row-003", "mutable-row-004", "mutable-row-005",
			"mutable-row-006", "mutable-row-007", "mutable-row-008",
			"mutable-row-009", "mutable-row-010", "mutable-row-011",
			"mutable-row-012", "mutable-row-013", "mutable-row-014",
			"mutable-row-015", "mutable-row-016", "mutable-row-017",
			"mutable-row-018", "mutable-row-019", "mutable-row-020",
		}, "\n")
		ackedEnd := strings.Index(source, "mutable-row-010")
		active := newActive(source, ackedEnd)

		commits := planMutableActiveCellHistoryCommits(active, geometry, 1)
		want := 21 - ActiveBandRows(geometry.Height) - 10 // rows 010..020, minus the live tail
		if len(commits) != want {
			t.Fatalf("resumed handoff rows = %d, want %d", len(commits), want)
		}
		if commits[0].Lines[0].Spans[0].Text != "mutable-row-010" {
			t.Fatalf("first resumed handoff row = %q, want mutable-row-010", commits[0].Lines[0].Spans[0].Text)
		}
	})

	t.Run("one long logical line hands off complete wrapped rows", func(t *testing.T) {
		source := strings.Repeat("x", 80*12)
		active := newActive(source, 0)
		commits := planMutableActiveCellHistoryCommits(active, geometry, 1)
		if len(commits) == 0 {
			t.Fatal("wrapped single-line overflow created no stable handoff")
		}
		if commits[0].SourceRange != (SourceRange{Start: 0, End: 80}) ||
			len(commits[0].Lines) != 1 || renderLineText(commits[0].Lines[0]) != strings.Repeat("x", 80) {
			t.Fatalf("first wrapped handoff = %#v", commits[0])
		}
		last := commits[len(commits)-1]
		if last.SourceRange.End >= len(source) || last.SourceRange.End <= last.SourceRange.Start {
			t.Fatalf("wrapped handoff consumed the live tail or used an empty range: %#v", last)
		}
	})
}

func TestCanonicalHistoryFrontierNoLongerBlockedAfterOrphanToolFinalization(t *testing.T) {
	tool := scene.TranscriptCell{
		ID: 1, Revision: 1, Kind: scene.KindToolChain, Source: "shell command", Phase: scene.CellMutable,
	}
	answer := scene.TranscriptCell{
		ID: 2, Revision: 1, Kind: scene.KindAssistant, Source: "final answer", Phase: scene.CellCommitted,
	}
	state := AppState{
		Geometry:         GeometryState{Width: 80, Height: 12, Generation: 1},
		LayoutGeneration: 1,
		Transcript:       TranscriptState{Revision: 1, Cells: []scene.TranscriptCell{tool, answer}},
	}
	if commits := planEligibleHistoryCommits(state); len(commits) != 0 {
		t.Fatalf("mutable orphan tool did not hold the ordering frontier: %#v", commits)
	}

	state.Transcript.Revision++
	state.Transcript.Cells[0].Revision++
	state.Transcript.Cells[0].Phase = scene.CellCommitted
	commits := planEligibleHistoryCommits(state)
	if len(commits) == 0 {
		t.Fatal("terminalized orphan tool left a permanent history frontier")
	}
	foundAnswer := false
	for _, commit := range commits {
		if commit.CellID == answer.ID {
			foundAnswer = true
		}
	}
	if !foundAnswer {
		t.Fatalf("history after orphan tool omitted later finalized answer: %#v", commits)
	}
}

func TestMutableHistoryIdentitySurvivesAppendOnlyActiveRevision(t *testing.T) {
	lines := make([]string, 18)
	for index := range lines {
		lines[index] = fmt.Sprintf("epoch-row-%03d", index)
	}
	source := strings.Join(lines, "\n")
	state := UIControllerState{}
	state = reduceUIControllerState(state, Resize{Width: 80, Height: 12, Generation: 1}, 1)
	state = reduceUIControllerState(state, SetSemanticActiveCellProjectionAction{Enabled: true}, 2)
	state = reduceUIControllerState(state, SetActiveCellAction{Active: ActiveCellState{
		CellID: 91, Revision: 1, Kind: scene.KindAssistant,
		Phase: ActiveCellMutable, Source: source,
	}}, 3)

	entries := state.HistoryEffects.Entries()
	if len(entries) == 0 || state.Active.Enqueued.End == 0 || state.Active.Acked.End != 0 {
		t.Fatalf("initial active handoff was not queued without ack: active=%+v effects=%+v", state.Active, entries)
	}
	if !historyCommitWakeNeeded(SetActiveCellAction{}, state) ||
		!historyCommitWakeNeeded(UpdateActiveCellAction{}, state) {
		t.Fatal("active Set/Update did not wake the terminal history consumer")
	}
	first := entries[0].Commit
	if first.Origin != HistoryCommitActive {
		t.Fatalf("initial effect origin = %v, want active", first.Origin)
	}

	nextSource := source + "\nepoch-row-018\nepoch-row-019"
	state = reduceUIControllerState(state, UpdateActiveCellAction{
		ExpectedCellID: 91, ExpectedRevision: 1,
		Active: ActiveCellState{
			CellID: 91, Revision: 2, Kind: scene.KindAssistant,
			Phase: ActiveCellMutable, Source: nextSource,
			Stable: state.Active.Stable, Enqueued: state.Active.Enqueued, Acked: state.Active.Acked,
		},
	}, 4)
	entry := historyCommitEntry(t, state, first.Token)
	if entry.State != HistoryCommitPending || entry.Commit.Token != first.Token || state.HistoryEffects.ProjectionUnknown {
		t.Fatalf("append-only revision invalidated stable effect identity: entry=%+v state=%+v", entry, state.HistoryEffects)
	}

	state = reduceUIControllerState(state, BeginHistoryCommit{
		Token: first.Token, LayoutGeneration: state.LayoutGeneration,
	}, 5)
	thirdSource := nextSource + "\nepoch-row-020"
	state = reduceUIControllerState(state, UpdateActiveCellAction{
		ExpectedCellID: 91, ExpectedRevision: 2,
		Active: ActiveCellState{
			CellID: 91, Revision: 3, Kind: scene.KindAssistant,
			Phase: ActiveCellMutable, Source: thirdSource,
			Stable: state.Active.Stable, Enqueued: state.Active.Enqueued, Acked: state.Active.Acked,
		},
	}, 6)
	entry = historyCommitEntry(t, state, first.Token)
	if entry.State != HistoryCommitInFlight || state.HistoryEffects.ProjectionUnknown {
		t.Fatalf("append-only revision invalidated in-flight stable effect: entry=%+v", entry)
	}
	state = reduceUIControllerState(state, HistoryCommitAcknowledged{
		Token: first.Token, Frame: 1, LayoutGeneration: state.LayoutGeneration,
	}, 7)
	if state.Active.Acked.End != first.SourceRange.End {
		t.Fatalf("ack frontier=%d, want first stable range end %d", state.Active.Acked.End, first.SourceRange.End)
	}
}

func TestFinalizeActiveCellPlansOnlyUnacknowledgedResidentTail(t *testing.T) {
	const width, height = 100, 24
	markers := make([]string, 40)
	for index := range markers {
		markers[index] = fmt.Sprintf("FINALIZED-SUFFIX-%02d terminal history validation", index+1)
	}
	source := "Terminal scrollback keeps completed rows in the host buffer.\n\n" +
		"FINALIZED-SUFFIX-REASONING\n\n" + strings.Join(markers, "\n")

	state := UIControllerState{}
	state = reduceUIControllerState(state, Resize{Width: width, Height: height, Generation: 1}, 1)
	state = reduceUIControllerState(state, SetSemanticActiveCellProjectionAction{Enabled: true}, 2)
	state = reduceUIControllerState(state, SetActiveCellAction{Active: ActiveCellState{
		CellID: 71, Revision: 40, Kind: scene.KindAssistant,
		Phase: ActiveCellMutable, Source: source,
	}}, 3)

	activeEntries := state.HistoryEffects.Entries()
	wantPrefixRows := 4 + 32
	if len(activeEntries) != wantPrefixRows {
		t.Fatalf("active overflow entries = %d, want %d", len(activeEntries), wantPrefixRows)
	}
	for index, entry := range activeEntries {
		state = reduceUIControllerState(state, BeginHistoryCommit{
			Token: entry.Commit.Token, LayoutGeneration: state.LayoutGeneration,
		}, uint64(4+index*2))
		state = reduceUIControllerState(state, HistoryCommitAcknowledged{
			Token: entry.Commit.Token, Frame: uint64(index + 1), LayoutGeneration: state.LayoutGeneration,
		}, uint64(5+index*2))
	}
	if state.Active.Acked.End == 0 || state.Active.Acked.End >= len(source) {
		t.Fatalf("active ack frontier = %+v, want delivered prefix and resident suffix", state.Active.Acked)
	}

	state = reduceUIControllerState(state, FinalizeActiveCellAction{
		Snapshot: &scene.Snapshot{Revision: 41, Cells: []*scene.TranscriptCell{{
			ID: 71, Revision: 41, Kind: scene.KindAssistant,
			Source: source, Phase: scene.CellCommitted,
		}}},
		ExpectedActiveCellID: 71, ExpectedActiveRevision: 40,
		ExpectedSceneRevision: 41,
		ExpectedActiveKind:    scene.KindAssistant, ExpectedActiveKindKnown: true,
	}, 100)

	var transcriptRows []string
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.Commit.Origin != HistoryCommitTranscript || entry.State != HistoryCommitPending {
			continue
		}
		for _, line := range entry.Commit.Lines {
			transcriptRows = append(transcriptRows, renderLineText(line))
		}
	}
	wantTail := markers[32:]
	if got, want := strings.Join(transcriptRows, "\n"), strings.Join(wantTail, "\n"); got != want {
		t.Fatalf("finalized transcript tail = %q, want %q\neffects=%#v", got, want, state.HistoryEffects.Entries())
	}
}

func TestPlanPlainCellHistoryCommitsMapsInternalAndTrailingBlankRows(t *testing.T) {
	const source = "first\n\nlast\n"
	state := AppState{
		Geometry:         GeometryState{Width: 80, Height: 24, Generation: 1},
		LayoutGeneration: 1,
		Transcript: NewTranscriptState(&scene.Snapshot{Revision: 1, Cells: []*scene.TranscriptCell{{
			ID: 72, Revision: 1, Kind: scene.KindAssistant,
			Source: source, Phase: scene.CellCommitted,
		}}}),
	}

	commits := planEligibleHistoryCommits(state)
	if len(commits) != 4 {
		t.Fatalf("blank-line commits = %d, want 4: %#v", len(commits), commits)
	}
	gotRows := make([]string, 0, len(commits))
	for _, commit := range commits {
		if !commit.SourceRange.Valid() || commit.SourceRange.End <= commit.SourceRange.Start {
			t.Fatalf("blank-line commit has empty source identity: %#v", commit)
		}
		gotRows = append(gotRows, renderLineText(commit.Lines[0]))
	}
	if got, want := strings.Join(gotRows, "|"), "first||last|"; got != want {
		t.Fatalf("plain blank projection = %q, want %q", got, want)
	}
	last := commits[len(commits)-1]
	if last.SourceRange != (SourceRange{Start: len(source) - 1, End: len(source)}) || last.FragmentID == 0 {
		t.Fatalf("trailing blank identity = range %+v fragment %d", last.SourceRange, last.FragmentID)
	}
}

func TestPlanEligibleHistoryCommitsRespectsCanonicalMutableFrontier(t *testing.T) {
	geometry := GeometryState{Width: 80, Height: 12, Generation: 1}
	assistantLines := make([]string, 24)
	for index := range assistantLines {
		assistantLines[index] = fmt.Sprintf("assistant-after-reasoning-%02d", index+1)
	}
	assistantSource := strings.Join(assistantLines, "\n")
	state := AppState{
		Geometry:                     geometry,
		LayoutGeneration:             1,
		SemanticActiveCellProjection: true,
		Transcript: NewTranscriptState(&scene.Snapshot{Revision: 1, Cells: []*scene.TranscriptCell{
			{ID: 1, Sequence: 1, Revision: 3, Kind: scene.KindReasoning, Source: "reasoning still mutable", Phase: scene.CellMutable},
			{ID: 2, Sequence: 2, Revision: 8, Kind: scene.KindAssistant, Source: assistantSource, Phase: scene.CellMutable},
		}}),
		Active: ActiveCellState{
			CellID: 2, Revision: 8, Kind: scene.KindAssistant,
			Phase: ActiveCellMutable, Source: assistantSource,
			Stable: SourceRange{Start: 0, End: len(assistantSource)},
		},
	}

	if commits := planEligibleHistoryCommits(state); len(commits) != 0 {
		t.Fatalf("assistant crossed preceding mutable reasoning: %#v", commits)
	}

	state.Transcript = NewTranscriptState(&scene.Snapshot{Revision: 2, Cells: []*scene.TranscriptCell{
		{ID: 1, Sequence: 1, Revision: 4, Kind: scene.KindReasoning, Source: "reasoning still mutable", Phase: scene.CellCommitted},
		{ID: 2, Sequence: 2, Revision: 8, Kind: scene.KindAssistant, Source: assistantSource, Phase: scene.CellMutable},
	}})
	commits := planEligibleHistoryCommits(state)
	if len(commits) == 0 || commits[0].CellID != 1 {
		t.Fatalf("canonical frontier did not begin with reasoning: %#v", commits)
	}
	seenAssistant := false
	for _, commit := range commits {
		if commit.CellID == 2 {
			seenAssistant = true
		}
		if seenAssistant && commit.CellID == 1 {
			t.Fatalf("reasoning appeared after assistant commit: %#v", commits)
		}
	}
}

func TestPlanEligibleHistoryCommitsStopsAtFirstMutableCell(t *testing.T) {
	state := AppState{
		Geometry:         GeometryState{Width: 80, Height: 12, Generation: 1},
		LayoutGeneration: 1,
		Transcript: NewTranscriptState(&scene.Snapshot{Revision: 1, Cells: []*scene.TranscriptCell{
			{ID: 1, Sequence: 1, Revision: 1, Kind: scene.KindUser, Source: "committed prefix", Phase: scene.CellCommitted},
			{ID: 2, Sequence: 2, Revision: 1, Kind: scene.KindReasoning, Source: "mutable barrier", Phase: scene.CellMutable},
			{ID: 3, Sequence: 3, Revision: 1, Kind: scene.KindAssistant, Source: "finalized but blocked", Phase: scene.CellCommitted},
		}}),
	}
	commits := planEligibleHistoryCommits(state)
	if len(commits) == 0 {
		t.Fatal("committed canonical prefix produced no history commit")
	}
	for _, commit := range commits {
		if commit.CellID != 1 {
			t.Fatalf("commit crossed mutable barrier: %#v", commits)
		}
	}
}

func TestPlanEligibleHistoryCommitsTreatsEmptyMutableCellAsBarrier(t *testing.T) {
	state := AppState{
		Geometry:         GeometryState{Width: 80, Height: 12, Generation: 1},
		LayoutGeneration: 1,
		Transcript: NewTranscriptState(&scene.Snapshot{Revision: 1, Cells: []*scene.TranscriptCell{
			{ID: 1, Sequence: 1, Revision: 1, Kind: scene.KindUser, Source: "committed prefix", Phase: scene.CellCommitted},
			{ID: 2, Sequence: 2, Revision: 1, Kind: scene.KindReasoning, Source: "", Phase: scene.CellMutable},
			{ID: 3, Sequence: 3, Revision: 1, Kind: scene.KindAssistant, Source: "must remain blocked", Phase: scene.CellCommitted},
		}}),
	}

	commits := planEligibleHistoryCommits(state)
	if len(commits) == 0 {
		t.Fatal("committed prefix produced no history commit")
	}
	for _, commit := range commits {
		if commit.CellID != 1 {
			t.Fatalf("commit crossed empty mutable barrier: %#v", commits)
		}
	}
}

func TestSyncHistoryEffectsForActiveCellLeavesTranscriptEntriesUntouched(t *testing.T) {
	state := UIControllerState{AppState: AppState{
		Geometry:                     GeometryState{Width: 80, Height: 24, Generation: 1},
		LayoutGeneration:             1,
		SemanticActiveCellProjection: true,
		Active: ActiveCellState{
			CellID: 91, Revision: 2, Kind: scene.KindAssistant,
			Phase: ActiveCellMutable, Source: "short active source",
		},
	}}
	commit := testHistoryCommit(1, 41, 1)
	state.HistoryEffects.ledger = NewHistoryCommitLedger()
	if err := state.HistoryEffects.ledger.Enqueue(commit); err != nil {
		t.Fatalf("enqueue transcript effect: %v", err)
	}
	state.HistoryEffects.NextToken = commit.Token

	syncHistoryEffectsForActiveCell(&state)

	entry, ok := state.HistoryEffects.ledger.Entry(commit.Token)
	if !ok || entry.State != HistoryCommitPending {
		t.Fatalf("active-only sync changed unrelated transcript effect: entry=%+v found=%t", entry, ok)
	}
}

func TestTranscriptReplacementOnlyUpdatesActive(t *testing.T) {
	previous := NewTranscriptState(&scene.Snapshot{Revision: 1, Cells: []*scene.TranscriptCell{
		{ID: 1, Sequence: 1, Revision: 1, Kind: scene.KindUser, Source: "prompt", Phase: scene.CellCommitted},
		{ID: 2, Sequence: 2, Revision: 3, Kind: scene.KindAssistant, Source: "partial", Phase: scene.CellMutable},
	}})
	active := ActiveCellState{CellID: 2, Revision: 3, Kind: scene.KindAssistant, Phase: ActiveCellMutable, Source: "partial"}
	next := previous.Clone()
	next.Revision++
	next.Cells[1].Revision++
	next.Cells[1].Source += " response"
	if !transcriptReplacementOnlyUpdatesActive(previous, next, active) {
		t.Fatal("append-only mutable snapshot was not recognized as active-only")
	}

	changedFinalized := next.Clone()
	changedFinalized.Cells[0].Source = "corrected prompt"
	if transcriptReplacementOnlyUpdatesActive(previous, changedFinalized, active) {
		t.Fatal("finalized-cell correction was incorrectly classified as active-only")
	}
	finalized := next.Clone()
	finalized.Cells[1].Phase = scene.CellCommitted
	if transcriptReplacementOnlyUpdatesActive(previous, finalized, active) {
		t.Fatal("active finalization was incorrectly classified as active-only")
	}
	regrouped := next.Clone()
	regrouped.Cells[1].BoundaryGroupKey = "different-request"
	if transcriptReplacementOnlyUpdatesActive(previous, regrouped, active) {
		t.Fatal("active boundary-group change was incorrectly classified as content-only")
	}
	if transcriptCellStaticMetadataEqual(previous.Cells[1], regrouped.Cells[1]) {
		t.Fatal("BoundaryGroupKey change was ignored by static metadata equality")
	}
}

func BenchmarkReplaceTranscriptActiveOnlyLargeLedger(b *testing.B) {
	const finalizedCells = 256
	cells := make([]*scene.TranscriptCell, 0, finalizedCells+1)
	for id := scene.CellID(1); id <= finalizedCells; id++ {
		cells = append(cells, &scene.TranscriptCell{
			ID: id, Sequence: uint64(id), Revision: 1, Kind: scene.KindAssistant,
			Source: "The quick brown fox jumps over the lazy dog.", Phase: scene.CellCommitted,
		})
	}
	activeID := scene.CellID(finalizedCells + 1)
	cells = append(cells, &scene.TranscriptCell{
		ID: activeID, Sequence: uint64(activeID), Revision: 2, Kind: scene.KindAssistant,
		Source: "short active source", Phase: scene.CellMutable,
	})
	snapshot := &scene.Snapshot{SceneID: 1, Revision: 2, ContentVersion: 1, Cells: cells}
	state := UIControllerState{AppState: AppState{
		Geometry:                     GeometryState{Width: 100, Height: 24, Generation: 1},
		LayoutGeneration:             1,
		SemanticActiveCellProjection: true,
		Transcript:                   NewTranscriptState(snapshot),
		Active: ActiveCellState{
			CellID: activeID, Revision: 2, Kind: scene.KindAssistant,
			Phase: ActiveCellMutable, Source: "short active source",
		},
	}}
	state.HistoryEffects.ledger = NewHistoryCommitLedger()
	for token := uint64(1); token <= 8192; token++ {
		commit := testHistoryCommit(token, scene.CellID(token%finalizedCells+1), 1)
		state.HistoryEffects.ledger.byToken[token] = HistoryCommitEntry{Commit: commit, State: HistoryCommitPending}
	}
	state.HistoryEffects.NextToken = 8192

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		snapshot.Revision++
		snapshot.ContentVersion++
		cells[len(cells)-1].Revision++
		state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, uint64(index+1))
	}
}

// A mutable reasoning cell whose markdown-looking body overflows the ActiveBand
// must hand off the same source-faithful rows that finalization projects. Its
// derived divider crosses the handoff with the first body-backed source range
// and is never stored in Source.
func TestPlanMutableReasoningHistoryCommitMatchesSourceFaithfulFinalize(t *testing.T) {
	bodyLines := make([]string, 0, 30)
	bodyLines = append(bodyLines, "Good. Key modules all exist:")
	for i := 0; i < 14; i++ {
		bodyLines = append(bodyLines, fmt.Sprintf("- `backend/internal/module%02d` ✅", i))
	}
	bodyLines = append(bodyLines, "", "And `.agents/agents/explore.md`, `general.md`, `plan.md` exist.", "")
	source := strings.Join(bodyLines, "\n")

	geometry := GeometryState{Width: 80, Height: 12, Generation: 1}
	active := ActiveCellState{
		CellID: 1, Revision: 3, Kind: scene.KindReasoning,
		Phase:  ActiveCellMutable,
		Source: source,
		Stable: SourceRange{Start: 0, End: len(source)},
		Acked:  SourceRange{Start: 0, End: 0},
	}

	commits := planMutableActiveCellHistoryCommits(active, geometry, 1)
	if len(commits) == 0 {
		t.Fatalf("long reasoning should hand off overflow rows, got no commits")
	}
	rawReasoningSeen := false
	for _, c := range commits {
		for _, line := range c.Lines {
			if strings.Contains(renderLineText(line), "- `backend/internal/module") {
				rawReasoningSeen = true
			}
		}
	}
	if !rawReasoningSeen {
		t.Fatalf("reasoning handoff normalized markdown-looking provider text: %#v", commits)
	}
}

// TestPlanReasoningAckedPrefixMatchesFinalize proves the source-faithful handoff
// rows are recognized by the acked-prefix matcher, so the finalized cell only
// commits the remaining tail instead of the whole cell (duplicate render).
func TestPlanReasoningAckedPrefixMatchesFinalize(t *testing.T) {
	geometry := GeometryState{Width: 80, Height: 12, Generation: 1}
	bodyLines := make([]string, 0, 30)
	bodyLines = append(bodyLines, "Good. Key modules all exist:")
	for i := 0; i < 20; i++ {
		bodyLines = append(bodyLines, fmt.Sprintf("- `backend/internal/module%02d` ✅", i))
	}
	bodyLines = append(bodyLines, "", "And `.agents/agents/explore.md`, `general.md`, `plan.md` exist.", "")
	source := strings.Join(bodyLines, "\n")

	active := ActiveCellState{
		CellID: 1, Revision: 3, Kind: scene.KindReasoning,
		Phase:  ActiveCellMutable,
		Source: source,
		Stable: SourceRange{Start: 0, End: len(source)},
		Acked:  SourceRange{Start: 0, End: 0},
	}
	commits := planMutableActiveCellHistoryCommits(active, geometry, 1)
	if len(commits) == 0 {
		t.Fatal("long reasoning should hand off overflow rows")
	}

	ledger := NewHistoryCommitLedger()
	for i := range commits {
		commits[i].Token = uint64(i + 1)
		if err := ledger.Enqueue(commits[i]); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if err := ledger.MarkInFlight(commits[i].Token); err != nil {
			t.Fatalf("mark in-flight: %v", err)
		}
		if err := ledger.Ack(commits[i].Token, 1, 1); err != nil {
			t.Fatalf("ack: %v", err)
		}
	}

	state := AppState{
		Geometry:         geometry,
		LayoutGeneration: 1,
		Transcript: NewTranscriptState(&scene.Snapshot{Revision: 2, Cells: []*scene.TranscriptCell{
			{ID: 1, Sequence: 1, Revision: 4, Kind: scene.KindReasoning, Source: source, Phase: scene.CellCommitted},
		}}),
		HistoryEffects: HistoryEffectQueueState{ledger: ledger},
	}
	byID := transcriptCellsByID(state.Transcript)
	rows := layoutTranscriptScreenRows(state.Transcript.LayoutRows(1), byID, nil, 80, style.ThemeContext{})
	acked := indexAckedActiveHistoryCommits(state.HistoryEffects)
	if matched := activeAckedRenderedPrefixRows(acked, 1, rows, byID); matched == 0 {
		t.Fatalf("BUG: acked handoff rows do not match finalize rows — whole cell re-committed (duplicate reasoning)")
	}
}

func TestSyncHistoryEffectsForActiveCellSkipsUnchangedInput(t *testing.T) {
	lines := make([]string, 0, 31)
	for index := 0; index < 30; index++ {
		lines = append(lines, fmt.Sprintf("mutable-row-%03d", index))
	}
	plainSource := strings.Join(lines, "\n") + "\n"

	newState := func(source string) *UIControllerState {
		state := &UIControllerState{AppState: AppState{
			Geometry:                     GeometryState{Width: 80, Height: 12, Generation: 1},
			LayoutGeneration:             1,
			SemanticActiveCellProjection: true,
			Active: ActiveCellState{
				CellID:   91,
				Revision: 2,
				Kind:     scene.KindAssistant,
				Phase:    ActiveCellMutable,
				Source:   source,
				Stable:   SourceRange{Start: 0, End: len(source)},
				Acked:    SourceRange{Start: 0, End: 0},
			},
		}}
		state.HistoryEffects.ledger = NewHistoryCommitLedger()
		return state
	}

	// corruptPending rewrites the display payload of the first pending entry so
	// a later planning pass must notice the mismatch (rebase or invalidate).
	// If the fast path skips planning, the corruption must survive untouched.
	corruptPending := func(state *UIControllerState) (token uint64, broken string) {
		for token, entry := range state.HistoryEffects.ledger.byToken {
			if entry.State != HistoryCommitPending {
				continue
			}
			broken = "BROKEN-SENTINEL"
			entry.Commit.Lines[0].Spans[0].Text = broken
			state.HistoryEffects.ledger.byToken[token] = entry
			return token, broken
		}
		t.Fatalf("no pending entry to corrupt")
		return 0, ""
	}

	state := newState(plainSource)
	syncHistoryEffectsForActiveCell(state)
	first := len(state.HistoryEffects.ledger.Entries())
	if first == 0 {
		t.Fatalf("expected handoff candidates on first sync, got none")
	}
	if state.Active.Enqueued.End == 0 {
		t.Fatalf("expected Enqueued advanced past the candidate frontier, got End=0")
	}

	t.Run("unchanged input hits the fast path", func(t *testing.T) {
		// Identical input on a later stream chunk must be a no-op. Corrupt a
		// pending payload first: only the fast-path skip leaves it untouched,
		// because a real planning pass would rebase or invalidate it.
		token, broken := corruptPending(state)
		enqueuedBefore := state.Active.Enqueued
		syncHistoryEffectsForActiveCell(state)
		if got := len(state.HistoryEffects.ledger.Entries()); got != first {
			t.Fatalf("unchanged input re-planned: ledger entries %d -> %d", first, got)
		}
		entry, ok := state.HistoryEffects.ledger.byToken[token]
		if !ok || entry.State != HistoryCommitPending {
			t.Fatalf("fast path did not run: pending entry %d was reconciled", token)
		}
		if entry.Commit.Lines[0].Spans[0].Text != broken {
			t.Fatalf("fast path did not run: corrupted payload was rebased to %q", entry.Commit.Lines[0].Spans[0].Text)
		}
		if state.Active.Enqueued != enqueuedBefore {
			t.Fatalf("unchanged input moved Enqueued %+v -> %+v", enqueuedBefore, state.Active.Enqueued)
		}
	})

	t.Run("stable advance re-plans", func(t *testing.T) {
		// New stream content past the stable boundary must re-plan and extend
		// the candidate frontier even though no ack moved.
		state.Active.Source += "mutable-row-030\nmutable-row-031\n"
		state.Active.Stable.End = len(state.Active.Source)
		syncHistoryEffectsForActiveCell(state)
		if got := len(state.HistoryEffects.ledger.Entries()); got <= first {
			t.Fatalf("stable advance did not re-plan: ledger entries %d -> %d", first, got)
		}
	})

	t.Run("markdown shape flip re-plans", func(t *testing.T) {
		// A source that flips from plain to markdown must re-plan even when no
		// boundary moved: the planner branches on LooksLikeMarkdown, and the
		// markdown collector can leave Stable pinned before an unclosed
		// construct while the source keeps growing.
		state2 := newState("# heading\n\n" + strings.Repeat("body-line-here\n", 30))
		syncHistoryEffectsForActiveCell(state2)
		if len(state2.HistoryEffects.ledger.Entries()) == 0 {
			t.Fatalf("markdown source produced no candidates")
		}
		token, broken := corruptPending(state2)
		state2.Active.Source = strings.Repeat("plain-body-line\n", 30) // flips to plain
		syncHistoryEffectsForActiveCell(state2)
		entry, ok := state2.HistoryEffects.ledger.byToken[token]
		if !ok {
			return // invalidated and pruned by the re-plan: acceptable
		}
		if entry.State == HistoryCommitInvalidated {
			return // invalidated by the re-plan: acceptable
		}
		if entry.Commit.Lines[0].Spans[0].Text == broken {
			t.Fatalf("markdown flip skipped the fast path: corrupted payload survived")
		}
	})

	t.Run("commit barrier flip re-plans", func(t *testing.T) {
		// HistoryCommitBlocked gates the planner (returns nil candidates), so
		// the barrier flipping while boundaries stay put must invalidate all
		// pending effects instead of being skipped.
		state3 := newState(plainSource)
		syncHistoryEffectsForActiveCell(state3)
		if !state3.HistoryEffects.HasPending() {
			t.Fatalf("expected pending effects before barrier")
		}
		state3.Active.HistoryCommitBlocked = true
		syncHistoryEffectsForActiveCell(state3)
		if state3.HistoryEffects.HasPending() {
			t.Fatalf("barrier flip did not re-plan: pending effects survived")
		}
		if state3.HistoryEffects.ledger.pendingCount != 0 {
			t.Fatalf("barrier flip left %d pending effects", state3.HistoryEffects.ledger.pendingCount)
		}
	})

	t.Run("equal-length replacement with empty frontier still re-plans", func(t *testing.T) {
		// The dangerous case for the fast-path memo: the frontier has been
		// consumed back to zero (no candidate ≤ Stable.End, so Enqueued stays
		// {0,0}) and a producer replaces the source with equal-length content.
		// Every memo key (Stable/Enqueued/Acked/Len/markdown shape/blocked)
		// coincides with the pre-replacement snapshot, so only the explicit
		// memo reset inside reduceActiveCellUpdate can force the re-plan.
		state5 := newState(plainSource)
		syncHistoryEffectsForActiveCell(state5)
		if len(state5.HistoryEffects.ledger.Entries()) == 0 {
			t.Fatalf("expected candidates before replacement")
		}
		// Fold the frontier and the memo back to the empty state that the
		// planner legitimately records when no candidate is enqueue-able.
		state5.Active.Enqueued = SourceRange{}
		state5.Active.Acked = SourceRange{}
		state5.HistoryEffects.lastPlannedActiveEnqueued = SourceRange{}
		state5.HistoryEffects.lastPlannedActiveAcked = SourceRange{}

		replLines := make([]string, 0, 30)
		for index := 0; index < 30; index++ {
			replLines = append(replLines, fmt.Sprintf("replacement-%03d", index))
		}
		replacement := strings.Join(replLines, "\n") + "\n"
		if len(replacement) != len(plainSource) {
			t.Fatalf("test setup: replacement length %d != %d", len(replacement), len(plainSource))
		}
		next := state5.Active.Clone()
		next.Revision++
		next.Source = replacement
		next.Acked = SourceRange{}
		next.Enqueued = SourceRange{}
		next.Stable = SourceRange{}
		action := UpdateActiveCellAction{
			ExpectedCellID:   state5.Active.CellID,
			ExpectedRevision: state5.Active.Revision,
			Active:           next,
		}
		if err := reduceActiveCellUpdate(state5, action); err != nil {
			t.Fatalf("reduceActiveCellUpdate: %v", err)
		}
		syncHistoryEffectsForActiveCell(state5)
		found := false
		for _, entry := range state5.HistoryEffects.ledger.Entries() {
			for _, line := range entry.Commit.Lines {
				for _, span := range line.Spans {
					if span.Text == "replacement-000" {
						found = true
					}
				}
			}
		}
		if !found {
			t.Fatalf("empty-frontier replacement was skipped by the fast path: no replacement payload planned")
		}
	})

	t.Run("equal-length replacement invalidates the memo", func(t *testing.T) {
		// A producer correction that replaces content (not appends) clears the
		// streaming ranges. With an equal-length source the fast-path boundary
		// keys would otherwise coincide and the new content would be planned
		// one chunk late; reduceActiveCellUpdate must reset the memo instead.
		state4 := newState(plainSource)
		syncHistoryEffectsForActiveCell(state4)
		if len(state4.HistoryEffects.ledger.Entries()) == 0 {
			t.Fatalf("expected candidates before replacement")
		}
		replLines := make([]string, 0, 30)
		for index := 0; index < 30; index++ {
			replLines = append(replLines, fmt.Sprintf("replacement-%03d", index))
		}
		replacement := strings.Join(replLines, "\n") + "\n"
		if len(replacement) != len(plainSource) {
			t.Fatalf("test setup: replacement length %d != %d", len(replacement), len(plainSource))
		}
		next := state4.Active.Clone()
		next.Revision++
		next.Source = replacement
		next.Acked = SourceRange{}
		next.Enqueued = SourceRange{}
		next.Stable = SourceRange{}
		action := UpdateActiveCellAction{
			ExpectedCellID:   state4.Active.CellID,
			ExpectedRevision: state4.Active.Revision,
			Active:           next,
		}
		if err := reduceActiveCellUpdate(state4, action); err != nil {
			t.Fatalf("reduceActiveCellUpdate: %v", err)
		}
		syncHistoryEffectsForActiveCell(state4)
		if len(state4.HistoryEffects.ledger.Entries()) == 0 {
			t.Fatalf("replacement did not re-plan: no candidates after sync")
		}
		found := false
		for _, entry := range state4.HistoryEffects.ledger.Entries() {
			for _, line := range entry.Commit.Lines {
				for _, span := range line.Spans {
					if span.Text == "replacement-000" {
						found = true
					}
				}
			}
		}
		if !found {
			t.Fatalf("replacement re-planned stale content: no replacement payload in candidates")
		}
	})
}
