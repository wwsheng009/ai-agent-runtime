package ui

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// benchmarkMutableTailSnapshot 构造“长会话 + 多个 mutable 尾部 cell”的权威快照，
// 模拟生产 session_20260927073805_QbWBceF5 的真实热路径：
//   - 大量 finalized cell（前缀不应被重新克隆/重排版）；
//   - 一个 streaming 的 active cell（每 chunk 增长）；
//   - 第二个 mutable cell（tool chain）也在变化，使
//     transcriptReplacementActiveOnlySnapshot 的 active-only 快路径失败，
//     从而落入 NewTranscriptState 全量克隆 + transcriptReplacementOnlyUpdatesActive
//     （旧实现是 reflect.DeepEqual 全量深比较）。
func benchmarkMutableTailSnapshot(finalized int) (*scene.Snapshot, *ActiveCellState, func()) {
	snapshot := benchResumedSnapshot(finalized)
	snapshot.Revision = 1
	snapshot.SceneID = 7

	activeID := scene.CellID(900000)
	otherID := scene.CellID(900001)
	snapshot.Cells = append(snapshot.Cells, &scene.TranscriptCell{
		ID:       activeID,
		Sequence: uint64(finalized + 1),
		Kind:     scene.KindAssistant,
		Source:   "stream start\n",
		Revision: 1,
		Phase:    scene.CellMutable,
	})
	snapshot.Cells = append(snapshot.Cells, &scene.TranscriptCell{
		ID:       otherID,
		Sequence: uint64(finalized + 2),
		Kind:     scene.KindToolChain,
		Source:   "tool running\n",
		Revision: 1,
		Phase:    scene.CellMutable,
		ChainKey: "chain-other",
	})

	active := &ActiveCellState{
		CellID:   activeID,
		Revision: 1,
		Kind:     scene.KindAssistant,
		Phase:    ActiveCellMutable,
		Source:   "stream start\n",
		Stable:   SourceRange{Start: 0, End: len("stream start\n")},
	}

	// 每个 chunk：场景 Revision/ContentVersion 前进，active cell 增长，
	// 另一个 mutable cell 的 revision 变化（tool 输出流）。
	advance := func() {
		snapshot.Revision++
		snapshot.ContentVersion++
		lastIndex := len(snapshot.Cells) - 1
		other := snapshot.Cells[lastIndex]
		other.Revision++
		other.Source += "row\n"
	}
	return snapshot, active, advance
}

// BenchmarkReplaceTranscriptMutableTailSnapshot 量化每个权威快照的 reduce 成本：
// 旧实现 = O(全部 cell) 的 reflect.DeepEqual + Presentation 深克隆；目标实现应降为
// O(变化的尾部 cell)。
func BenchmarkReplaceTranscriptMutableTailSnapshot(b *testing.B) {
	snapshot, active, advance := benchmarkMutableTailSnapshot(2000)

	state := UIControllerState{}
	seq := uint64(1)
	state = reduceUIControllerState(state, Resize{Width: 120, Height: 40, Generation: 1}, seq)
	seq++
	state = reduceUIControllerState(state, SetSemanticActiveCellProjectionAction{Enabled: true}, seq)
	seq++
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, seq)
	seq++
	state = reduceUIControllerState(state, SetActiveCellAction{Active: *active}, seq)
	seq++
	if state.Active.CellID != active.CellID || state.Active.Phase != ActiveCellMutable {
		b.Fatalf("active cell not mounted: %+v", state.Active)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		advance()
		state.Active.Source += "chunk "
		state.Active.Revision++
		state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, seq)
		seq++
	}
}

// BenchmarkReplaceTranscriptAppendedMutableSnapshot covers the other live
// pattern: a busy turn keeps appending *mutable* tail cells (new reasoning /
// tool-chain cells), which changes the total cell count while the finalized
// (history-eligible) prefix is untouched. The memo must not treat that as a new
// full plan.
func BenchmarkReplaceTranscriptAppendedMutableSnapshot(b *testing.B) {
	snapshot, active, _ := benchmarkMutableTailSnapshot(2000)

	state := UIControllerState{}
	seq := uint64(1)
	state = reduceUIControllerState(state, Resize{Width: 120, Height: 40, Generation: 1}, seq)
	seq++
	state = reduceUIControllerState(state, SetSemanticActiveCellProjectionAction{Enabled: true}, seq)
	seq++
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, seq)
	seq++
	state = reduceUIControllerState(state, SetActiveCellAction{Active: *active}, seq)
	seq++
	if state.Active.CellID != active.CellID || state.Active.Phase != ActiveCellMutable {
		b.Fatalf("active cell not mounted: %+v", state.Active)
	}

	mutableID := scene.CellID(910000)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		mutableID++
		snapshot.Revision++
		snapshot.ContentVersion++
		snapshot.Cells = append(snapshot.Cells, &scene.TranscriptCell{
			ID:       mutableID,
			Sequence: uint64(len(snapshot.Cells) + 1),
			Kind:     scene.KindReasoning,
			Source:   "late reasoning tail\n",
			Revision: 1,
			Phase:    scene.CellMutable,
		})
		state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, seq)
		seq++
	}
}
