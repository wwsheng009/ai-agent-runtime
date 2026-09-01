package ui

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// TestTranscriptPlanMemoIgnoresActiveCellStreamGrowth 验证 transcript-plan
// memo 的最终化前缀围栏：mutable active cell 的 append-only 增长（场景级
// ContentVersion/Revision 每次 chunk 递增）不得使 memo 失效——否则每次
// streaming delta 都触发 O(entire history) 的全量 replan（生产 pprof：
// syncHistoryEffectsForTranscript 17.5% + layoutTranscriptScreenRows 8.63s +
// vt.blankRow 129GB allocs）。
func TestTranscriptPlanMemoIgnoresActiveCellStreamGrowth(t *testing.T) {
	// 300 个 finalized cell（resume 长会话），加一个 mutable active cell。
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
		t.Fatalf("active cell not mounted: %+v", state.Active)
	}

	// 全量 plan 已记录 memo。直接驱动 syncHistoryEffectsForTranscript（对应
	// reduceUIControllerState 中 SetActiveCellAction/FinalizeActiveCellAction/
	// ReplaceTranscriptAction 非 activeOnly 等入口），模拟每个 streaming chunk：
	// 场景 ContentVersion/Revision 递增、active cell source 增长，但所有
	// finalized cell 不变。
	baseToken := state.HistoryEffects.NextToken
	baseEntries := len(state.HistoryEffects.Entries())
	for chunk := 0; chunk < 50; chunk++ {
		next := *snapshot
		next.Revision = uint64(2 + chunk)
		next.ContentVersion = uint64(2 + chunk)
		next.Cells = cloneBenchSnapshotCells(snapshot.Cells)
		next.Cells[len(next.Cells)-1].Source += " 更多流式内容"
		state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: &next}, seq)
		seq++
		// 决定性断言：memo 命中路径（syncHistoryEffectsForActiveCell）绝不重新
		// enqueue 已 finalized 的 300 个 commit。若全量 replan 发生，NextToken
		// 会随每次 chunk 暴涨，Entries 数量也会因重复 candidate 增长。
		if state.HistoryEffects.NextToken > baseToken+uint64(len(state.Transcript.Cells)) {
			t.Fatalf("chunk %d: full replan re-enqueued history (NextToken %d -> %d): memo missed while only the active cell grew",
				chunk, baseToken, state.HistoryEffects.NextToken)
		}
		if len(state.HistoryEffects.Entries()) > baseEntries+10 {
			t.Fatalf("chunk %d: full replan grew the ledger from %d to %d entries",
				chunk, baseEntries, len(state.HistoryEffects.Entries()))
		}
	}
}

// TestTranscriptPlanMemoHitSkipsRebuildWhenOnlyActiveCellGrew 直接驱动
// syncHistoryEffectsForTranscript（绕过 activeOnly 快速路径），验证 memo 在
// 仅 active cell 增长时命中并走便宜路径。
func TestTranscriptPlanMemoHitSkipsRebuildWhenOnlyActiveCellGrew(t *testing.T) {
	snapshot := benchResumedSnapshot(100)
	snapshot.ContentVersion = 1
	snapshot.Cells = append(snapshot.Cells, &scene.TranscriptCell{
		ID:       100001,
		Sequence: 101,
		Kind:     scene.KindAssistant,
		Source:   "start",
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
	baseToken := state.HistoryEffects.NextToken

	// 模拟场景升级（ContentVersion/Revision 递增）+ active source 增长，然后
	// 直接调用 syncHistoryEffectsForTranscript（这是 reduceUIControllerState
	// 各入口最终汇聚的同步函数）。修复前 memo 用 ContentVersion 围栏必 miss →
	// 全量 replan → NextToken 暴涨；修复后用 finalized-prefix 围栏命中 → 跳过。
	for chunk := 0; chunk < 30; chunk++ {
		state.Transcript.Revision = uint64(2 + chunk)
		state.Transcript.ContentVersion = uint64(2 + chunk)
		state.Active.Source += " more stream"
		state.Active.Revision = uint64(2 + chunk)
		syncHistoryEffectsForTranscript(&state)
		if state.HistoryEffects.NextToken > baseToken+uint64(len(state.Transcript.Cells)) {
			t.Fatalf("chunk %d: memo missed; full replan advanced NextToken %d -> %d",
				chunk, baseToken, state.HistoryEffects.NextToken)
		}
	}
}

// TestTranscriptPlanMemoInvalidatesOnFinalizedCellChange 验证围栏的另一半：
// 任何 finalized cell 的内容/身份变化（例如用户新消息 append 为 finalized
// cell）必须使 memo 失效，触发全量 replan。
func TestTranscriptPlanMemoInvalidatesOnFinalizedCellChange(t *testing.T) {
	snapshot := benchResumedSnapshot(50)
	snapshot.ContentVersion = 1
	state := UIControllerState{}
	seq := uint64(1)
	state = reduceUIControllerState(state, Resize{Width: 100, Height: 24, Generation: 1}, seq)
	seq++
	state = reduceUIControllerState(state, SetSemanticActiveCellProjectionAction{Enabled: true}, seq)
	seq++
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, seq)
	seq++

	// 直接验证围栏：把新 snapshot 安装后的状态构造出来（reducer 全量 replan
	// 后会重新记录 memo，所以要在 replan 之前验证 memo 必须 miss）。
	next := *snapshot
	next.Revision = 2
	next.ContentVersion = 2
	next.Cells = append(cloneBenchSnapshotCells(snapshot.Cells), &scene.TranscriptCell{
		ID:       999001,
		Sequence: 51,
		Kind:     scene.KindUser,
		Source:   "new user message",
		Revision: 1,
		Phase:    scene.CellCommitted,
	})
	nextState := state
	nextState.Transcript = NewTranscriptState(&next)
	nextState.Active = reconcileTranscriptActiveCell(nextState.Active, nextState.Transcript)
	if transcriptPlanMemoHit(&nextState) {
		t.Fatal("finalized-prefix memo hit despite a new finalized cell")
	}
}

// TestTranscriptPlanMemoHitsOnUnversionedSnapshot 验证未版本化快照
// （SceneID == 0，resume 从 sqlite 重建的常见形态）下 memo 依然生效。
// 修复前 transcriptPlanMemoHit 的第一道门槛 SceneID == 0 直接返回 false，
// resume 会话每次 streaming delta / SetActiveCellAction 都触发 O(entire
// history) 的全量 planEligibleHistoryCommits —— 生产 pprof 实测 192% CPU
// （syncHistoryEffectsForTranscript 18.35% + GC 风暴）。修复后围栏补齐了
// Sequence/ChainKey/BoundaryGroupKey，未版本化快照也能安全 memoize。
func TestTranscriptPlanMemoHitsOnUnversionedSnapshot(t *testing.T) {
	snapshot := benchResumedSnapshot(100)
	snapshot.SceneID = 0 // 未版本化：resume 重建快照可能不带 scene 来源
	snapshot.ContentVersion = 1
	snapshot.Cells = append(snapshot.Cells, &scene.TranscriptCell{
		ID:       100001,
		Sequence: 101,
		Kind:     scene.KindAssistant,
		Source:   "start",
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
	if state.Transcript.SceneID != 0 {
		t.Fatalf("fixture must stay unversioned: SceneID=%d", state.Transcript.SceneID)
	}
	if !transcriptPlanMemoHit(&state) {
		t.Fatal("unversioned snapshot must be memoizable after the first full plan")
	}

	baseToken := state.HistoryEffects.NextToken
	baseEntries := len(state.HistoryEffects.Entries())
	for chunk := 0; chunk < 30; chunk++ {
		state.Transcript.Revision = uint64(2 + chunk)
		state.Transcript.ContentVersion = uint64(2 + chunk)
		state.Active.Source += " more stream"
		state.Active.Revision = uint64(2 + chunk)
		syncHistoryEffectsForTranscript(&state)
		if state.HistoryEffects.NextToken > baseToken+uint64(len(state.Transcript.Cells)) {
			t.Fatalf("chunk %d: memo missed on unversioned snapshot; full replan advanced NextToken %d -> %d",
				chunk, baseToken, state.HistoryEffects.NextToken)
		}
		if len(state.HistoryEffects.Entries()) > baseEntries+10 {
			t.Fatalf("chunk %d: memo missed on unversioned snapshot; ledger grew %d -> %d entries",
				chunk, baseEntries, len(state.HistoryEffects.Entries()))
		}
	}
}

// TestTranscriptPlanMemoInvalidatesOnChainKeyChange 验证围栏覆盖 tool-chain
// 归组键：即使 cell ID/Revision/Phase/Boundary 都不变，仅 ChainKey 重排也
// 必须使 memo 失效（layout gap 决策读取 ChainKey）。
func TestTranscriptPlanMemoInvalidatesOnChainKeyChange(t *testing.T) {
	snapshot := benchResumedSnapshot(10)
	snapshot.ContentVersion = 1
	state := UIControllerState{}
	seq := uint64(1)
	state = reduceUIControllerState(state, Resize{Width: 100, Height: 24, Generation: 1}, seq)
	seq++
	state = reduceUIControllerState(state, SetSemanticActiveCellProjectionAction{Enabled: true}, seq)
	seq++
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, seq)
	seq++
	if !transcriptPlanMemoHit(&state) {
		t.Fatal("baseline memo must hit")
	}

	nextState := state
	nextState.Transcript.Cells[0].ChainKey = "rewired-chain"
	if transcriptPlanMemoHit(&nextState) {
		t.Fatal("finalized-prefix memo hit despite a chain-key rewire")
	}
}

func cloneBenchSnapshotCells(cells []*scene.TranscriptCell) []*scene.TranscriptCell {
	cloned := make([]*scene.TranscriptCell, len(cells))
	for index, cell := range cells {
		cp := *cell
		cloned[index] = &cp
	}
	return cloned
}
