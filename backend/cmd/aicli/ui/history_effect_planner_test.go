package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
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
		Geometry:   GeometryState{Width: 80, Height: 12, Generation: 1},
		Transcript: TranscriptState{Revision: 1, Cells: []scene.TranscriptCell{tool, answer}},
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

// TestFinalizeDefersTranscriptPlanWhileActiveBatchInFlight 固化 resident-tail
// 双写的 reducer 侧护栏：当 Active 批次已交给执行器（head InFlight、成员仍
// Pending，等待同一次物理写证明）时，finalize 不得立刻做全会话 reconcile ——
// 那会以 Transcript 身份逐出批次成员，使随后成功的写 ackBatch 因
// ErrCommitNotInFlight 被拒并触发重放。批次结算（ack）后才以 Transcript 身份
// 补铸未确认后缀，已交付前缀不重发。

func TestPlanPlainCellHistoryCommitsMapsInternalAndTrailingBlankRows(t *testing.T) {
	const source = "first\n\nlast\n"
	state := AppState{
		Geometry: GeometryState{Width: 80, Height: 24, Generation: 1},
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
		Geometry: GeometryState{Width: 80, Height: 12, Generation: 1},
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
		Geometry: GeometryState{Width: 80, Height: 12, Generation: 1},
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
		state.HistoryEffects.ledger.byToken[token] = HistoryCommitEntry{Commit: commit, State: HistoryCommitQueued}
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

// TestSyncHistoryEffectCandidates_ActiveInFlightDifferentDisplayRange is a
// regression test for the high-CPU loop (185% sustained).  The loop was driven
// by every streaming delta invalidating an in-flight Active-origin commit
// whose DisplayRange differed from the new candidate's DisplayRange because
// the Acked frontier advanced between the full-transcript replan (after
// reconciliation reset Acked=0) and the active-only replan (from the new
// Acked.End).  Source range + lines were identical; only the derived
// display-row range changed.  historyCommitPresentationEqual now skips
// DisplayRange for active commits, so this scenario must NOT trigger
// invalidate → ReconciliationRequired=true.
func TestSyncHistoryEffectCandidates_ActiveInFlightDifferentDisplayRange(t *testing.T) {
	// Setup: state with a ledger containing one Active-origin commit in-flight.
	state := &UIControllerState{AppState: AppState{Geometry: GeometryState{Generation: 1}}}
	state.HistoryEffects.ledger = NewHistoryCommitLedger()

	// Build a valid Active-origin commit (as produced by a full-transcript
	// replan after reconciliation reset Acked=0 → DisplayRange {1,2}).
	inFlight := HistoryCommit{
		Origin:           HistoryCommitActive,
		CellID:           91,
		Revision:         2,
		SourceRange:      SourceRange{Start: 0, End: 10},
		FragmentID:       0,
		DisplayRange:     DisplayRange{Start: 1, End: 2},
		LayoutGeneration: 1,
		Lines: []render.Line{{
			Spans: []render.Span{{Text: "test-text"}},
		}},
	}
	if err := state.HistoryEffects.enqueue(inFlight); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// Claim it (simulating executor write claim).
	if err := state.HistoryEffects.markInFlight(1, 1); err != nil {
		t.Fatalf("markInFlight: %v", err)
	}
	entry, ok := state.HistoryEffects.ledger.Entry(1)
	if !ok || entry.State != HistoryCommitQueued || state.HistoryEffects.WriteCursor != 1 {
		t.Fatalf("expected claimed pending entry, got state=%s cursor=%d", entry.State, state.HistoryEffects.WriteCursor)
	}

	// Build a candidate with the same source identity (origin, cellID, source
	// range, fragmentID) but a different DisplayRange — this is what happens
	// when the active-only replan starts from the new Acked frontier and
	// assigns displayRow=0 again.
	candidate := inFlight
	candidate.Token = 0                                     // auto-assigned on enqueue
	candidate.DisplayRange = DisplayRange{Start: 0, End: 1} // relative row

	// Call syncHistoryEffectCandidates — the exact trigger path.
	syncHistoryEffectCandidates(state, []HistoryCommit{candidate}, 91)

	// Assert: the claimed entry was NOT invalidated.
	entry, ok = state.HistoryEffects.ledger.Entry(1)
	if !ok {
		t.Fatal("claimed entry was removed from ledger")
	}
	if entry.State != HistoryCommitQueued || state.HistoryEffects.WriteCursor != 1 {
		t.Fatalf("claimed entry was invalidated (state=%s cursor=%d); fix broke: "+
			"DisplayRange-only difference for active commit must not trigger invalidate",
			entry.State, state.HistoryEffects.WriteCursor)
	}
	if state.HistoryEffects.ProjectionUnknown {
		t.Fatal("ProjectionUnknown was set — claimed commit was invalidated")
	}
	if state.HistoryEffects.ReconciliationRequired {
		t.Fatal("ReconciliationRequired was set — in-flight commit was invalidated")
	}

	// Also verify that the candidate was NOT enqueued as a duplicate
	// (hasTerminalRecordForSource returns true for the in-flight source).
	entries := state.HistoryEffects.ledger.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 ledger entry, got %d: %#v", len(entries), entries)
	}
}

// TestSyncHistoryEffectCandidatesPrefixKeepsPendingTail 锁定截断规划的语义：被
// historyCommitPlanningBudget 截断的规划结果只是完整规划的前缀，它缺少的尾部
// cell 并不代表那些候选失效。若把前缀交给 syncHistoryEffectCandidates（它把输入
// 当作**完整**有效集合），尾部已经排队、甚至已经交给 terminal 的提交会被
// invalidate —— 内容不会错（下一次完整规划会重新入队），但 token 抖动会回退
// Enqueued 前沿，代价是 scrollback 的重复写或漏写。
func TestSyncHistoryEffectCandidatesPrefixKeepsPendingTail(t *testing.T) {
	state := &UIControllerState{AppState: AppState{Geometry: GeometryState{Generation: 1}}}
	state.HistoryEffects.ledger = NewHistoryCommitLedger()

	tail := HistoryCommit{
		Origin:           HistoryCommitTranscript,
		CellID:           42,
		Revision:         1,
		SourceRange:      SourceRange{Start: 0, End: 10},
		DisplayRange:     DisplayRange{Start: 1, End: 2},
		LayoutGeneration: 1,
		Lines:            []render.Line{{Spans: []render.Span{{Text: "tail-cell"}}}},
	}
	if err := state.HistoryEffects.enqueue(tail); err != nil {
		t.Fatalf("enqueue tail: %v", err)
	}
	tailEntry, ok := state.HistoryEffects.ledger.Entry(1)
	if !ok || tailEntry.State != HistoryCommitQueued {
		t.Fatalf("尾部提交没有以 pending 进入 ledger: ok=%t state=%s", ok, tailEntry.State)
	}

	// 截断前缀：只包含更早的一个 cell，尾部 cell 不在集合里。
	prefix := HistoryCommit{
		Origin:           HistoryCommitTranscript,
		CellID:           7,
		Revision:         1,
		SourceRange:      SourceRange{Start: 0, End: 5},
		DisplayRange:     DisplayRange{Start: 0, End: 1},
		LayoutGeneration: 1,
		Lines:            []render.Line{{Spans: []render.Span{{Text: "head-cell"}}}},
	}
	syncHistoryEffectCandidatesPrefix(state, []HistoryCommit{prefix})

	// Invalidate 把条目置为 quarantined/invalidated 而不是从 ledger 删除，所以
	// 契约要断言 state，不能只断言存在性。
	tailEntry, ok = state.HistoryEffects.ledger.Entry(1)
	if !ok {
		t.Fatal("尾部提交从 ledger 消失了")
	}
	if tailEntry.State != HistoryCommitQueued {
		t.Fatalf("截断前缀把尾部已排队的提交置为 %s：这会让 Enqueued 前沿回退", tailEntry.State)
	}
	if entries := state.HistoryEffects.Entries(); len(entries) != 2 {
		t.Fatalf("ledger entries = %d, want 2（前缀必须入队，尾部必须保留）", len(entries))
	}

	// 对照：完整集合语义下，不在有效集合里的 pending 条目确实会被逐出 —— 这
	// 正是两条路径必须分开的原因，也说明上面的断言不是恒真。
	syncHistoryEffectCandidates(state, []HistoryCommit{prefix}, 0)
	tailEntry, ok = state.HistoryEffects.ledger.Entry(1)
	if !ok || !tailEntry.IsInvalidated() {
		t.Fatalf("完整规划语义下，不在有效集合里的 pending 条目应当被置为 invalidated: ok=%t state=%s", ok, tailEntry.State)
	}
}
