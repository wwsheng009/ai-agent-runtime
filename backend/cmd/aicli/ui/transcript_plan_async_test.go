package ui

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// fakeTranscriptPlanSink 捕获 reducer 派发的 screening 请求，模拟控制器侧 plan worker。
type fakeTranscriptPlanSink struct {
	accept   bool
	requests []transcriptPlanWindowRequest
}

func (f *fakeTranscriptPlanSink) RequestTranscriptPlanWindow(req transcriptPlanWindowRequest) bool {
	if !f.accept {
		return false
	}
	f.requests = append(f.requests, req)
	return true
}

// completeFakePlanWindow 模拟 worker：对最后一次请求跑 screening，并把结果作为
// HistoryPlanWindowReady 送回 reducer（等价于真 worker 的 Post）。
func completeFakePlanWindow(t *testing.T, state UIControllerState, sink *fakeTranscriptPlanSink, revision uint64) (UIControllerState, HistoryPlanWindowReady) {
	t.Helper()
	if len(sink.requests) == 0 {
		t.Fatal("没有可消费的 screening 请求")
	}
	req := sink.requests[len(sink.requests)-1]
	rows, screenMs := screenTranscriptPlanWindowRequest(req)
	action := HistoryPlanWindowReady{
		seq:             req.Seq,
		planInputsEpoch: req.PlanInputsEpoch,
		inputs:          req.Inputs,
		rows:            rows,
		screenDuration:  screenMs,
	}
	return reduceUIControllerState(state, action, revision), action
}

func assertHistoryCoversFinalizedCells(t *testing.T, state UIControllerState) {
	t.Helper()
	covered := make(map[scene.CellID]struct{})
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.IsInvalidated() || entry.IsSettled() {
			continue
		}
		covered[entry.Commit.CellID] = struct{}{}
	}
	for _, cell := range state.Transcript.Cells {
		if !cellIsFinalizedForHistory(cell) || cell.Source == "" {
			continue
		}
		if _, ok := covered[cell.ID]; !ok {
			t.Fatalf("cell %d 没有被覆盖（inFlight=%t）",
				cell.ID, state.HistoryEffects.planRequestInFlight)
		}
	}
}

// TestAsyncPlanWindowConvergesLikeSyncPlan 走完整异步生命周期：派发 → worker 单遍
// 结果 → 铸 commit → 收敛。断言与同步路径等价（finalized cell 全覆盖、无 in-flight
// 残影、memo 已落）。
func TestAsyncPlanWindowConvergesLikeSyncPlan(t *testing.T) {
	sink := &fakeTranscriptPlanSink{accept: true}
	state := UIControllerState{}
	state.planSink = sink
	state = reduceUIControllerState(state, Resize{Width: 90, Height: 40, Generation: 1}, 1)
	// 空 transcript 是平凡规划：不派发 worker，锁内直接完整落账（空往返优化）。
	if state.HistoryEffects.planRequestInFlight || len(sink.requests) != 0 {
		t.Fatalf("空装载不得派发 screening 请求：inFlight=%t requests=%d",
			state.HistoryEffects.planRequestInFlight, len(sink.requests))
	}
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: resumeParitySnapshot()}, 2)
	// 首个真实请求：立即派发；在飞期间的后续 reduce 不得重复派发。
	if !state.HistoryEffects.planRequestInFlight || len(sink.requests) != 1 {
		t.Fatalf("替换 transcript 后应派发 screening 请求：inFlight=%t requests=%d",
			state.HistoryEffects.planRequestInFlight, len(sink.requests))
	}
	modified := resumeParitySnapshot()
	modified.Revision++
	before := state.HistoryEffects.planRequestSeq
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: modified}, 3)
	if state.HistoryEffects.planRequestSeq != before {
		t.Fatal("在飞期间不得重复派发请求")
	}

	state, _ = completeFakePlanWindow(t, state, sink, 4)
	if state.HistoryEffects.planRequestInFlight {
		t.Fatal("单遍结果应结算 in-flight")
	}
	if state.HistoryEffects.PlanIncomplete {
		t.Fatal("无预算单遍不存在 incomplete 计划")
	}
	if state.HistoryEffects.NextToken == 0 {
		t.Fatal("screening 结果没有铸出任何提交")
	}
	assertHistoryCoversFinalizedCells(t, state)
}

// TestAsyncPlanWindowStaleResultReDispatches 锁定栅栏②：结果落地前输入变化（几何）
// → 旧结果必须被丢弃且**立即**按当前输入重派发；丢弃不得留下任何半应用痕迹。
func TestAsyncPlanWindowStaleResultReDispatches(t *testing.T) {
	sink := &fakeTranscriptPlanSink{accept: true}
	state := UIControllerState{}
	state.planSink = sink
	state = reduceUIControllerState(state, Resize{Width: 90, Height: 40, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: resumeParitySnapshot()}, 2)
	nextBefore := state.HistoryEffects.NextToken
	// 在飞期间改几何：reduce 不得重复派发，但指纹已变。
	state = reduceUIControllerState(state, Resize{Width: 100, Height: 30, Generation: 2}, 3)
	if state.HistoryEffects.planRequestInFlight == false {
		t.Fatal("在飞期间改几何不得结算 in-flight")
	}
	state, stale := completeFakePlanWindow(t, state, sink, 4)
	if stale.inputs == currentTranscriptPlanInputs(&state) {
		t.Fatal("测试自检：陈旧结果的指纹不应与当前一致")
	}
	if state.HistoryEffects.NextToken != nextBefore {
		t.Fatal("陈旧结果被半应用：铸出了 token")
	}
	if !state.HistoryEffects.planRequestInFlight {
		t.Fatal("陈旧结果必须立即按当前输入重派发")
	}
	// 新请求的输入必须是当前输入；喂回后正常推进并收敛。
	state, _ = completeFakePlanWindow(t, state, sink, 5)
	if state.HistoryEffects.planRequestInFlight {
		t.Fatal("新结果没有结算")
	}
	assertHistoryCoversFinalizedCells(t, state)
}

// TestAsyncPlanWindowEpochInvalidationReDispatches 锁定栅栏①的显式失效面：armed
// replay / no-op 安装走的 invalidateTranscriptPlanMemo 必须让在飞结果作废并重派发，
// 旧结果不得复活任何已作废计划。
func TestAsyncPlanWindowEpochInvalidationReDispatches(t *testing.T) {
	sink := &fakeTranscriptPlanSink{accept: true}
	state := UIControllerState{}
	state.planSink = sink
	state = reduceUIControllerState(state, Resize{Width: 90, Height: 40, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: resumeParitySnapshot()}, 2)
	nextBefore := state.HistoryEffects.NextToken
	// 显式失效（等价 armed replay / no-op 安装路径）：清 memo + 推进 epoch。
	state.HistoryEffects.invalidateTranscriptPlanMemo()
	if state.HistoryEffects.planInputsEpoch == 0 {
		t.Fatal("显式失效必须推进 planInputsEpoch")
	}
	state, _ = completeFakePlanWindow(t, state, sink, 3)
	if state.HistoryEffects.NextToken != nextBefore {
		t.Fatal("失效前的在飞结果被应用（应丢弃）")
	}
	if !state.HistoryEffects.planRequestInFlight {
		t.Fatal("失效后的旧结果必须丢弃并重派发")
	}
}

// TestAsyncPlanWindowSinkRefusalFallsBackToSync 锁定回退：sink 拒绝受理（worker 不在）
// 时，reducer 必须走锁内同步单遍规划，且不得留下 in-flight 残影。
func TestAsyncPlanWindowSinkRefusalFallsBackToSync(t *testing.T) {
	sink := &fakeTranscriptPlanSink{accept: false}
	state := UIControllerState{}
	state.planSink = sink
	state = reduceUIControllerState(state, Resize{Width: 90, Height: 40, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: resumeParitySnapshot()}, 2)
	if state.HistoryEffects.planRequestInFlight {
		t.Fatal("受理被拒绝不得置 in-flight")
	}
	if state.HistoryEffects.PlanIncomplete {
		t.Fatal("无预算单遍不存在 incomplete 计划")
	}
	if !transcriptPlanMemoHit(&state) {
		t.Fatal("回退同步规划必须完整落账并记录 memo")
	}
	assertHistoryCoversFinalizedCells(t, state)
}

// TestPlanContinuationPendingSuppressedWhileInFlight 锁定防热旋转门：在飞请求期间
// planContinuationPending 必须为 false，否则 executor 会无 sleep 地反复 Post kick。
func TestPlanContinuationPendingSuppressedWhileInFlight(t *testing.T) {
	var effects HistoryEffectQueueState
	effects.PlanIncomplete = true
	if !effects.planContinuationPending() {
		t.Fatal("未在飞、未停摆时应为 pending")
	}
	effects.planRequestInFlight = true
	if effects.planContinuationPending() {
		t.Fatal("在飞请求期间不得报告 pending（否则 executor 热旋转）")
	}
	effects.planRequestInFlight = false
	effects.PlanStalled = true
	if effects.planContinuationPending() {
		t.Fatal("停摆后不得报告 pending")
	}
}
