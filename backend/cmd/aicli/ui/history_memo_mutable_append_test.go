package ui

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// TestTranscriptPlanMemoIgnoresAppendedMutableTailCell 锁定 memo 的 cell 数围栏
// 语义：忙碌 turn 不断追加 *mutable* 尾部 cell（新的 reasoning / tool-chain
// 边界，总量不进入 finalized-prefix 计划）。若把总 cell 数当计划输入，每追加
// 一个 cell 就全量重规划整段历史（基准 723ms/op @2000 cells；线上 plan-last-ms
// 4.7-6.9s、~9 plans/min），UI actor 邮箱随之中毒。
func TestTranscriptPlanMemoIgnoresAppendedMutableTailCell(t *testing.T) {
	snapshot := benchResumedSnapshot(50)
	snapshot.ContentVersion = 1
	snapshot.Cells = append(snapshot.Cells, &scene.TranscriptCell{
		ID:       50001,
		Sequence: 51,
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
	if !transcriptPlanMemoHit(&state) {
		t.Fatal("precondition: the initial plan must be memoized")
	}
	baseToken := state.HistoryEffects.NextToken

	// Append a new mutable tail cell: total count changes, finalized prefix does not.
	next := *snapshot
	next.Revision = 2
	next.ContentVersion = 2
	next.Cells = cloneBenchSnapshotCells(snapshot.Cells)
	next.Cells = append(next.Cells, &scene.TranscriptCell{
		ID:       50002,
		Sequence: 52,
		Kind:     scene.KindReasoning,
		Source:   "late reasoning tail",
		Revision: 1,
		Phase:    scene.CellMutable,
	})
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: &next}, seq)
	seq++
	if !transcriptPlanMemoHit(&state) {
		t.Fatal("appending a mutable tail cell invalidated the finalized-prefix plan memo")
	}
	if state.HistoryEffects.NextToken != baseToken {
		t.Fatalf("appending a mutable tail cell re-planned history: NextToken %d -> %d",
			baseToken, state.HistoryEffects.NextToken)
	}

	// Negative control: appending a *finalized* cell must still invalidate the memo
	// (probe the installed fence without running another plan, which would record
	// a fresh memo for the new inputs).
	finalized := *snapshot
	finalized.Revision = 3
	finalized.ContentVersion = 3
	finalized.Cells = cloneBenchSnapshotCells(snapshot.Cells)
	finalized.Cells = append(finalized.Cells, &scene.TranscriptCell{
		ID:       50003,
		Sequence: 53,
		Kind:     scene.KindAssistant,
		Source:   "final answer",
		Revision: 1,
		Phase:    scene.CellCommitted,
	})
	probe := state
	probe.Transcript = newTranscriptStateFromSnapshot(state.Transcript, &finalized)
	if transcriptPlanMemoHit(&probe) {
		t.Fatal("appending a finalized cell must invalidate the plan memo")
	}
}
