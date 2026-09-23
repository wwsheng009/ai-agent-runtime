package ui

import "testing"

// 需求：一次已经被终端证明的 scrollback 替换（armed replay 的销毁式 reset）必须
// 进入新 epoch。HistoryScrollbackReconciled 是唯一会推进 epoch、丢弃旧交付记录并
// 从语义源重规划的分支；如果它被静默丢弃，而 reset 已经物理发生，就同时造成：
//
//  1. 一次性授权（ScrollbackReplayArmed）存活 —— 执行器会再次选中销毁式计划，
//     反复清空 scrollback（live: scrollback_reset_count=4）；
//  2. ledger 保留着"已交付"的记录 —— 重规划被 hasTerminalRecordForSource 挡住，
//     什么都补不回来（live: pending=0 / acked=0）；
//  3. 投影被标记为 known 而常驻历史区为空 —— 没有任何东西会再修它
//     （live: history_rows=0 / history_known=true，永久空屏）。
//
// 因此：只要终端 owner 证明了替换（新 epoch + 事务成功），reducer 就必须完成
// reconcile，而不是因为并发发生的投影失效或布局代数漂移把它丢掉。
func TestProvenScrollbackReplacementReconcilesDespiteConcurrentInvalidation(t *testing.T) {
	state := reduceUIControllerState(UIControllerState{}, Resize{Width: 72, Height: 12, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:            scrollbackGrantSnapshot(1, "loaded session"),
		ArmScrollbackReplay: true,
	}, 2)
	if !state.HistoryEffects.ScrollbackReplayArmed {
		t.Fatal("load did not arm the one-shot replay")
	}
	loadedNextToken := state.HistoryEffects.NextToken
	if loadedNextToken == 0 {
		t.Fatal("load did not plan the loaded transcript")
	}

	// The armed replay physically replaced scrollback, but a concurrent writer
	// failure invalidated the projection before the reconcile barrier landed.
	state = reduceUIControllerState(state, HistoryProjectionInvalidated{
		LayoutGeneration: state.LayoutGeneration,
	}, 3)
	state = reduceUIControllerState(state, HistoryScrollbackReconciled{
		LayoutGeneration: state.LayoutGeneration,
		TerminalEpoch:    1,
	}, 4)

	// An unrecovered claim is still not a substitute for a source-backed frame:
	// the epoch must not advance and the ledger must not be rewritten yet.
	if state.HistoryEffects.TerminalEpoch != 0 {
		t.Fatalf("unrecovered replacement advanced the terminal epoch: %#v", state.HistoryEffects)
	}
	// But the physical act is durable. Dropping it entirely is what strands the
	// session: the authorization survives (the executor replaces scrollback
	// again) and the ledger keeps claiming rows that the reset removed.
	if state.HistoryEffects.ProvenScrollbackEpoch != 1 {
		t.Fatalf("proven scrollback replacement was not recorded: %#v", state.HistoryEffects)
	}
	if !state.HistoryEffects.ScrollbackReplayArmed {
		t.Fatal("fixture consumed the authorization before the replacement was reconciled")
	}

	// The frame proof arrives: the recorded replacement must now reconcile, which
	// is what makes the transcript re-mintable and repopulates the region.
	state = reduceUIControllerState(state, HistoryProjectionRecovered{
		LayoutGeneration: state.LayoutGeneration,
	}, 5)

	if state.HistoryEffects.TerminalEpoch != 1 {
		t.Fatalf("recovered frame did not reconcile the recorded replacement: %#v", state.HistoryEffects)
	}
	if state.HistoryEffects.ScrollbackReplayArmed {
		t.Fatal("reconciled replacement left the one-shot authorization armed; the executor would reset scrollback again")
	}
	if state.HistoryEffects.ReconciliationRequired {
		t.Fatalf("epoch replacement retained reconciliation intent: %#v", state.HistoryEffects)
	}
	// The projection stays fail-closed (ProjectionUnknown) until the terminal
	// proves a frame again, so HasPending() is deliberately false here. What must
	// exist is the replanned work itself: without it the ledger keeps claiming
	// those ranges are delivered and nothing can ever repopulate the region.
	if pending := historyPendingCount(state); pending == 0 {
		t.Fatalf("epoch replacement did not replan the loaded transcript from source: %#v", state.HistoryEffects)
	}
	if state.HistoryEffects.NextToken <= loadedNextToken {
		t.Fatalf("epoch replacement did not mint fresh tokens: next=%d loaded=%d", state.HistoryEffects.NextToken, loadedNextToken)
	}
}

// 需求：布局代数在事务与屏障之间漂移（并发 resize/theme）同样不能让已经发生的
// 销毁式替换失去 reconcile —— 否则 ledger 会继续声称那些行"已交付"，而它们已经
// 被物理清除了。
func TestProvenScrollbackReplacementReconcilesDespiteLayoutDrift(t *testing.T) {
	state := reduceUIControllerState(UIControllerState{}, Resize{Width: 72, Height: 12, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:            scrollbackGrantSnapshot(1, "loaded session"),
		ArmScrollbackReplay: true,
	}, 2)

	// A resize lands between the physical replacement and its barrier.
	state = reduceUIControllerState(state, Resize{Width: 60, Height: 10, Generation: 2}, 3)
	staleGeneration := state.LayoutGeneration - 1

	state = reduceUIControllerState(state, HistoryScrollbackReconciled{
		LayoutGeneration: staleGeneration,
		TerminalEpoch:    1,
	}, 4)

	if state.HistoryEffects.TerminalEpoch != 1 {
		t.Fatalf("layout drift discarded a proven scrollback replacement: %#v", state.HistoryEffects)
	}
	if state.HistoryEffects.ScrollbackReplayArmed {
		t.Fatal("layout drift left the one-shot authorization armed")
	}
	if pending := historyPendingCount(state); pending == 0 {
		t.Fatalf("epoch replacement did not replan the transcript after layout drift: %#v", state.HistoryEffects)
	}
}

// historyPendingCount counts ledger entries still awaiting delivery, without the
// ProjectionUnknown gate that HasPending applies to the scheduler predicate.
func historyPendingCount(state UIControllerState) int {
	pending := 0
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.State == HistoryCommitPending {
			pending++
		}
	}
	return pending
}
