package ui

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// fakeTranscriptPlanSink 捕获 reducer 派发的窗口请求，模拟控制器侧 plan worker。
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
		t.Fatal("没有可消费的窗口请求")
	}
	req := sink.requests[len(sink.requests)-1]
	rows, complete, nextRow, screenMs := screenTranscriptPlanWindowRequest(req)
	action := HistoryPlanWindowReady{
		seq:              req.Seq,
		planInputsEpoch:  req.PlanInputsEpoch,
		inputs:           req.Inputs,
		resume:           req.Resume,
		startRow:         req.StartRow,
		screenRowsBefore: req.ScreenRowsBefore,
		rows:             rows,
		complete:         complete,
		nextRow:          nextRow,
		screenDuration:   screenMs,
	}
	return reduceUIControllerState(state, action, revision), action
}

// drainFakePlanPending 交付当前全部前缀 token（等价于 executor 的 claim→ack），
// 否则 continueTruncatedHistoryPlan 的 drain gate 不允许继续规划。
func drainFakePlanPending(t *testing.T, state *UIControllerState) {
	t.Helper()
	for _, commit := range state.HistoryEffects.Pending() {
		if err := state.HistoryEffects.markInFlight(commit.Token, commit.LayoutGeneration); err != nil {
			t.Fatalf("claim token %d: %v", commit.Token, err)
		}
		if err := state.HistoryEffects.ack(commit.Token, 1, commit.LayoutGeneration); err != nil {
			t.Fatalf("ack token %d: %v", commit.Token, err)
		}
	}
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
			t.Fatalf("cell %d 没有被覆盖（incomplete=%t inFlight=%t）",
				cell.ID, state.HistoryEffects.PlanIncomplete, state.HistoryEffects.planRequestInFlight)
		}
	}
}

// TestAsyncPlanWindowConvergesLikeSyncPlan 走完整异步生命周期：派发 → worker 结果
// → 铸 commit → 交付前缀 → 续跑派发 → … → 收敛。断言与同步路径等价（游标严格由
// resume 窗口推进、最终 PlanIncomplete 清除、finalized cell 全覆盖）。
func TestAsyncPlanWindowConvergesLikeSyncPlan(t *testing.T) {
	restoreBudget := historyCommitPlanningBudget
	defer func() { historyCommitPlanningBudget = restoreBudget }()
	historyCommitPlanningBudget = 0

	sink := &fakeTranscriptPlanSink{accept: true}
	state := UIControllerState{}
	state.planSink = sink
	state = reduceUIControllerState(state, Resize{Width: 90, Height: 40, Generation: 1}, 1)
	// 空 transcript 是平凡规划：不派发 worker，锁内直接完整落账（空往返优化）。
	if state.HistoryEffects.planRequestInFlight || len(sink.requests) != 0 {
		t.Fatalf("空装载不得派发窗口请求：inFlight=%t requests=%d",
			state.HistoryEffects.planRequestInFlight, len(sink.requests))
	}
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: resumeParitySnapshot()}, 2)
	// 首个真实窗口：立即派发；在飞期间的后续 reduce 不得重复派发。
	if !state.HistoryEffects.planRequestInFlight || len(sink.requests) != 1 {
		t.Fatalf("替换 transcript 后应派发窗口请求：inFlight=%t requests=%d",
			state.HistoryEffects.planRequestInFlight, len(sink.requests))
	}
	modified := resumeParitySnapshot()
	modified.Revision++
	before := state.HistoryEffects.planRequestSeq
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: modified}, 3)
	if state.HistoryEffects.planRequestSeq != before {
		t.Fatal("在飞期间不得重复派发请求")
	}

	rounds := 0
	seenResume := false
	for rounds < 128 {
		rounds++
		if state.HistoryEffects.planRequestInFlight {
			var a HistoryPlanWindowReady
			state, a = completeFakePlanWindow(t, state, sink, 100+uint64(rounds))
			if a.resume {
				seenResume = true
			}
			continue
		}
		if state.HistoryEffects.PlanIncomplete {
			drainFakePlanPending(t, &state)
			if !continueTruncatedHistoryPlan(&state) {
				t.Fatalf("第 %d 轮：空闲且未完成时续跑没有派发请求", rounds)
			}
			continue
		}
		break
	}
	if state.HistoryEffects.PlanIncomplete || state.HistoryEffects.planRequestInFlight {
		t.Fatalf("异步路径在 %d 轮内没有收敛：incomplete=%t inFlight=%t",
			rounds, state.HistoryEffects.PlanIncomplete, state.HistoryEffects.planRequestInFlight)
	}
	if !seenResume {
		t.Fatal("没有观察到游标续跑窗口（resume=true）")
	}
	assertHistoryCoversFinalizedCells(t, state)
}

// TestAsyncPlanWindowStaleResultReDispatches 锁定栅栏②：结果落地前输入变化（几何）
// → 旧结果必须被丢弃且**立即**按当前输入重派发；丢弃不得留下任何半应用痕迹。
func TestAsyncPlanWindowStaleResultReDispatches(t *testing.T) {
	restoreBudget := historyCommitPlanningBudget
	defer func() { historyCommitPlanningBudget = restoreBudget }()
	historyCommitPlanningBudget = 0

	sink := &fakeTranscriptPlanSink{accept: true}
	state := UIControllerState{}
	state.planSink = sink
	state = reduceUIControllerState(state, Resize{Width: 90, Height: 40, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: resumeParitySnapshot()}, 2)
	// 首个窗口（from-0，0 预算）截断并留下游标。
	state, _ = completeFakePlanWindow(t, state, sink, 3)
	if state.HistoryEffects.planRequestInFlight || !state.HistoryEffects.PlanIncomplete || !state.HistoryEffects.planResumeValid {
		t.Fatalf("截断窗口没有留下游标/未完成状态：inFlight=%t incomplete=%t resume=%t",
			state.HistoryEffects.planRequestInFlight, state.HistoryEffects.PlanIncomplete, state.HistoryEffects.planResumeValid)
	}

	drainFakePlanPending(t, &state)
	if !continueTruncatedHistoryPlan(&state) {
		t.Fatal("续跑没有派发窗口请求")
	}
	staleSeq := state.HistoryEffects.planRequestSeq
	nextBefore := state.HistoryEffects.NextToken
	rowBefore := state.HistoryEffects.planResumeRow
	// 在飞期间改几何：reduce 不得重复派发，但指纹已变。
	state = reduceUIControllerState(state, Resize{Width: 100, Height: 30, Generation: 2}, 5)
	if state.HistoryEffects.planRequestSeq != staleSeq {
		t.Fatal("在飞期间改几何不得重复派发")
	}
	state, stale := completeFakePlanWindow(t, state, sink, 6)
	if stale.inputs == currentTranscriptPlanInputs(&state) {
		t.Fatal("测试自检：陈旧结果的指纹不应与当前一致")
	}
	if state.HistoryEffects.NextToken != nextBefore || state.HistoryEffects.planResumeRow != rowBefore {
		t.Fatal("陈旧结果被半应用：token/游标发生了变化")
	}
	if !state.HistoryEffects.planRequestInFlight {
		t.Fatal("陈旧结果必须立即按当前输入重派发")
	}
	// 新请求的输入必须是当前输入；喂回后正常推进。
	state, _ = completeFakePlanWindow(t, state, sink, 7)
	if state.HistoryEffects.planRequestInFlight {
		t.Fatal("新窗口没有结算")
	}
	// 该 fixture 的游标已在末尾：新的续跑窗口可以合法地一个 token 都不铸，直接走
	// 末尾全量 pass 收敛；有效判定是"被应用且要么收敛、要么仍留有可续游标"。
	if state.HistoryEffects.PlanIncomplete && !state.HistoryEffects.planResumeValid {
		t.Fatal("新窗口既没有收敛也没有留下可续跑的游标")
	}
}

// TestAsyncPlanWindowEpochInvalidationReDispatches 锁定栅栏①的显式失效面：armed
// replay / no-op 安装走的 invalidateTranscriptPlanMemo 必须让在飞结果作废，且旧结果
// 不得复活刚被清掉的游标（P1.2 审查漏项 A 的空屏路径）。
func TestAsyncPlanWindowEpochInvalidationReDispatches(t *testing.T) {
	restoreBudget := historyCommitPlanningBudget
	defer func() { historyCommitPlanningBudget = restoreBudget }()
	historyCommitPlanningBudget = 0

	sink := &fakeTranscriptPlanSink{accept: true}
	state := UIControllerState{}
	state.planSink = sink
	state = reduceUIControllerState(state, Resize{Width: 90, Height: 40, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: resumeParitySnapshot()}, 2)
	state, _ = completeFakePlanWindow(t, state, sink, 3)
	if !state.HistoryEffects.planResumeValid {
		t.Fatal("前置条件：应有截断游标")
	}
	drainFakePlanPending(t, &state)
	if !continueTruncatedHistoryPlan(&state) {
		t.Fatal("续跑没有派发窗口请求")
	}
	// 显式失效（等价 armed replay / no-op 安装路径）：清 memo + 游标 + 推进 epoch。
	state.HistoryEffects.invalidateTranscriptPlanMemo()
	if state.HistoryEffects.planInputsEpoch == 0 {
		t.Fatal("显式失效必须推进 planInputsEpoch")
	}
	if state.HistoryEffects.planResumeValid {
		t.Fatal("显式失效必须清掉游标")
	}
	state, _ = completeFakePlanWindow(t, state, sink, 5)
	if state.HistoryEffects.planResumeValid {
		t.Fatal("失效前的在飞结果复活了游标（A.4 空屏路径）")
	}
	if !state.HistoryEffects.planRequestInFlight {
		t.Fatal("失效后的旧结果必须丢弃并重派发")
	}
}

// TestAsyncPlanWindowSinkRefusalFallsBackToSync 锁定回退：sink 拒绝受理（worker 不在）
// 时，reducer 必须走 Stage A 的锁内同步规划，且不得留下 in-flight 残影。
func TestAsyncPlanWindowSinkRefusalFallsBackToSync(t *testing.T) {
	restoreBudget := historyCommitPlanningBudget
	defer func() { historyCommitPlanningBudget = restoreBudget }()
	historyCommitPlanningBudget = 0

	sink := &fakeTranscriptPlanSink{accept: false}
	state := UIControllerState{}
	state.planSink = sink
	state = reduceUIControllerState(state, Resize{Width: 90, Height: 40, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: resumeParitySnapshot()}, 2)
	if state.HistoryEffects.planRequestInFlight {
		t.Fatal("受理被拒绝不得置 in-flight")
	}
	if !state.HistoryEffects.PlanIncomplete || !state.HistoryEffects.planResumeValid {
		t.Fatalf("sink 拒绝后必须回退同步截断规划：incomplete=%t resume=%t",
			state.HistoryEffects.PlanIncomplete, state.HistoryEffects.planResumeValid)
	}
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
