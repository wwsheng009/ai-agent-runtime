package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// 这些基准复现生产 pprof 标记的 resume 会话热循环：
// 1. ResumeReplay：一次 ReplaceTranscriptAction 安装数百个 finalized cell，
//    触发 O(全历史) 的 planEligibleHistoryCommits + 逐 cell enqueue。
// 2. NoopActiveRemount：transcript 与 active 完全不变时重复触发
//    syncHistoryEffectsForTranscript（如 SetActiveCellAction 重挂载），
//    验证 transcript 规划记忆化把全量重排版降为 O(1) 跳过。

func benchReplyLine(index int) string {
	return fmt.Sprintf("reply-row-%04d 这是一行典型的中文回复内容，用于撑起真实的排版宽度。", index)
}

func benchResumedSnapshot(cells int) *scene.Snapshot {
	snapshotCells := make([]*scene.TranscriptCell, 0, cells)
	for id := 1; id <= cells; id++ {
		var builder strings.Builder
		for line := 0; line < 6; line++ {
			builder.WriteString(benchReplyLine(id*100 + line))
			builder.WriteByte('\n')
		}
		snapshotCells = append(snapshotCells, &scene.TranscriptCell{
			ID:       scene.CellID(id),
			Sequence: uint64(id),
			Kind:     scene.KindAssistant,
			Source:   builder.String(),
			Revision: 1,
			Phase:    scene.CellCommitted,
		})
	}
	return &scene.Snapshot{SceneID: 7, Revision: 1, Cells: snapshotCells}
}

func benchMutableActive(source string) ActiveCellState {
	return ActiveCellState{
		CellID:   scene.CellID(100000),
		Revision: 1,
		Kind:     scene.KindAssistant,
		Phase:    ActiveCellMutable,
		Source:   source,
		Stable:   SourceRange{Start: 0, End: len(source)},
	}
}

// BenchmarkResumeReplay300Cells measures one full snapshot install of a
// resumed session: planEligibleHistoryCommits lays out and wraps every
// finalized cell, then each candidate walks enqueue (which previously paid an
// O(ledger) hasOlderPendingOrInFlight scan per token).
func BenchmarkResumeReplay300Cells(b *testing.B) {
	snapshot := benchResumedSnapshot(300)
	for b.Loop() {
		state := UIControllerState{}
		seq := uint64(1)
		state = reduceUIControllerState(state, Resize{Width: 100, Height: 24, Generation: 1}, seq)
		seq++
		state = reduceUIControllerState(state, SetSemanticActiveCellProjectionAction{Enabled: true}, seq)
		seq++
		state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, seq)
		if len(state.HistoryEffects.Entries()) == 0 {
			b.Fatal("resume replay produced no history candidates")
		}
	}
}

// BenchmarkNoopActiveRemount measures repeated syncHistoryEffectsForTranscript
// calls whose every planner input is unchanged. Before the transcript-plan
// memo each call re-laid-out the entire finalized transcript; after it the
// fingerprint skips the rebuild.
func BenchmarkNoopActiveRemount(b *testing.B) {
	state := UIControllerState{}
	seq := uint64(1)
	state = reduceUIControllerState(state, Resize{Width: 100, Height: 24, Generation: 1}, seq)
	seq++
	state = reduceUIControllerState(state, SetSemanticActiveCellProjectionAction{Enabled: true}, seq)
	seq++
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: benchResumedSnapshot(300)}, seq)
	seq++
	active := benchMutableActive(strings.Repeat("active stream chunk\n", 8))
	state = reduceUIControllerState(state, SetActiveCellAction{Active: active}, seq)
	seq++
	b.ResetTimer()
	for b.Loop() {
		state = reduceUIControllerState(state, SetActiveCellAction{Active: active}, seq)
		seq++
	}
}

// BenchmarkReplyStreamChunks measures the per-chunk cost of a long mutable
// reply on the semantic fast path (syncHistoryEffectsForActiveCell), with a
// large finalized transcript behind it.
func BenchmarkReplyStreamChunks(b *testing.B) {
	state := UIControllerState{}
	seq := uint64(1)
	state = reduceUIControllerState(state, Resize{Width: 100, Height: 24, Generation: 1}, seq)
	seq++
	state = reduceUIControllerState(state, SetSemanticActiveCellProjectionAction{Enabled: true}, seq)
	seq++
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: benchResumedSnapshot(300)}, seq)
	seq++
	chunk := strings.Repeat("流式输出的一行内容，包含中英文混合 token 流。\n", 3)
	source := ""
	revision := uint64(1)
	state = reduceUIControllerState(state, SetActiveCellAction{Active: benchMutableActive("")}, seq)
	seq++
	b.ResetTimer()
	for b.Loop() {
		revision++
		source += chunk
		next := benchMutableActive(source)
		next.Revision = revision
		state = reduceUIControllerState(state, UpdateActiveCellAction{
			ExpectedCellID:   next.CellID,
			ExpectedRevision: revision - 1,
			Active:           next,
		}, seq)
		seq++
	}
}

// BenchmarkAckLoopResync reproduces the production hot loop measured on the
// resumed session: every active-handoff ack advances Active.Acked.End, and a
// transcript-path sync runs afterwards. Before the finalized-prefix memo this
// re-laid-out the entire transcript history per ack (production pprof: ~190%
// CPU pinned by layoutTranscriptScreenRows + nextNonTerminalToken); after it
// the finalized plan is skipped and only the O(viewport) active handoff is
// reconciled.
func BenchmarkAckLoopResync(b *testing.B) {
	state := UIControllerState{}
	seq := uint64(1)
	state = reduceUIControllerState(state, Resize{Width: 100, Height: 24, Generation: 1}, seq)
	seq++
	state = reduceUIControllerState(state, SetSemanticActiveCellProjectionAction{Enabled: true}, seq)
	seq++
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: benchResumedSnapshot(300)}, seq)
	seq++
	source := strings.Repeat("ack loop overflow row\n", 40)
	active := benchMutableActive(source)
	state = reduceUIControllerState(state, SetActiveCellAction{Active: active}, seq)
	seq++
	b.ResetTimer()
	for b.Loop() {
		// Simulate the executor ack of one overflow row: Acked.End advances
		// (advanceActiveCellLedgerOnAck) and the next sync re-plans.
		next := active
		next.Acked = SourceRange{Start: 0, End: next.Acked.End + 22}
		if next.Acked.End > len(source) {
			next.Acked.End = len(source)
		}
		active = next
		state.Active = next
		syncHistoryEffectsForTranscript(&state)
	}
}

// BenchmarkResumeStreamChunkWithLargeHistory measures the per-chunk cost of a
// resumed long session while the mutable active cell streams. Each iteration
// calls syncHistoryEffectsForTranscript directly (bypassing the activeOnly fast
// path) after advancing the scene-wide Revision/ContentVersion and the active
// cell's source — exactly what happens post-memo in the reducer when every new
// scene snapshot arrives.
//
// Before the finalized-prefix memo (ContentVersion fence), each chunk incurred
// a full planEligibleHistoryCommits: re-layout and re-wrap of every finalized
// cell (production pprof: syncHistoryEffectsForTranscript 17.5%,
// planEligibleHistoryCommits 9.67s, vt.blankRow 129GB allocs). After the fix
// (finalized-prefix fence), the memo hits and only the O(viewport) active
// handoff is reconciled.
func BenchmarkResumeStreamChunkWithLargeHistory(b *testing.B) {
	snapshot := benchResumedSnapshot(300)
	snapshot.ContentVersion = 1
	snapshot.Cells = append(snapshot.Cells, &scene.TranscriptCell{
		ID:       300001,
		Sequence: 301,
		Kind:     scene.KindAssistant,
		Source:   "stream start",
		Revision: 1,
		Phase:    scene.CellMutable,
	})

	state := UIControllerState{}
	seq := uint64(1)
	state = reduceUIControllerState(state, Resize{Width: 100, Height: 24, Generation: 1}, seq)
	seq++
	state = reduceUIControllerState(state, SetSemanticActiveCellProjectionAction{Enabled: true}, seq)
	seq++
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, seq)
	seq++
	if state.Active.CellID != 300001 || state.Active.Phase != ActiveCellMutable {
		b.Fatalf("active cell not mounted: %+v", state.Active)
	}

	b.ResetTimer()
	for b.Loop() {
		// Bump scene counters and active source to simulate a new chunk.
		state.Transcript.Revision++
		state.Transcript.ContentVersion++
		state.Active.Source += " more streaming content"
		state.Active.Revision++
		// Drive syncHistoryEffectsForTranscript directly — this is what the
		// non-activeOnly reducer paths (SetActiveCellAction, FinalizeActiveCellAction,
		// ReplaceTranscriptAction when activeOnly fails) call per chunk in production.
		syncHistoryEffectsForTranscript(&state)
	}
}
