package ui

import (
	"testing"
	"time"
)

// 需求：预算耗尽只能**截断**规划，绝不能把「被截断」伪装成「没有可交付历史」。
//
// 规划器把「行集为空」当作「没有可交付历史」直接返回（planEligibleHistoryCommits
// 的 len(rows)==0 分支）。而 screening 的预算检查在 index=0 就会命中：resume 会话
// 里 LayoutTranscript（4497 cell → 159k 行）与 fold target 这两段前置工作本身就可能
// 超过 historyCommitPlanningBudget，于是 screening 拿到的 deadline 早已过期，循环
// 第一次检查就返回空结果。每一轮 reduce 重试都如此 → 规划永远 0 候选 → next=0 /
// pending=0（live 实测），授权过的销毁式 scrollback 重放清空屏幕后无内容可写。
//
// 因此：即使 deadline 已经过期，规划也必须产出一个非空前缀（并报告 incomplete），
// 让重试严格前进，而不是永远返回「什么都没有」。
func TestExpiredPlanningBudgetStillYieldsAPlanPrefix(t *testing.T) {
	state := reduceUIControllerState(UIControllerState{}, Resize{Width: 72, Height: 12, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot: scrollbackGrantSnapshot(1, "loaded session"),
	}, 2)
	if state.HistoryEffects.NextToken == 0 {
		t.Fatal("fixture did not plan the loaded transcript")
	}

	// 已过期的 deadline 就是 resume 会话上 screening 实际拿到的输入。
	commits, _ := planEligibleHistoryCommitsWithin(state.AppState, time.Now().Add(-time.Second))
	if len(commits) == 0 {
		t.Fatal("an expired planning budget produced an empty plan: the caller cannot " +
			"distinguish a truncated plan from a transcript with no deliverable history, " +
			"so the session plans nothing and stays blank forever")
	}
}

// 需求：截断的前缀必须让**下一次**规划继续前进，而不是每轮重新产出同一个前缀、
// 重复铸造 token。fixture 的 ReplaceTranscriptAction 已经规划并入队过第一批，
// 这里重放同一前缀（过期预算下的真实输入）必须是无操作。
func TestTruncatedPlanPrefixDoesNotRequeueDeliveredSources(t *testing.T) {
	state := reduceUIControllerState(UIControllerState{}, Resize{Width: 72, Height: 12, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot: scrollbackGrantSnapshot(1, "loaded session"),
	}, 2)
	before := state.HistoryEffects.NextToken
	pending := historyPendingCount(state)
	if before == 0 || pending == 0 {
		t.Fatalf("fixture did not queue anything: next=%d pending=%d", before, pending)
	}

	commits, _ := planEligibleHistoryCommitsWithin(state.AppState, time.Now().Add(-time.Second))
	if len(commits) == 0 {
		t.Fatal("fixture produced no plan prefix")
	}
	syncHistoryEffectCandidatesPrefix(&state, commits)
	if state.HistoryEffects.NextToken != before {
		t.Fatalf("a truncated prefix re-minted an already queued source: next=%d before=%d",
			state.HistoryEffects.NextToken, before)
	}
	if got := historyPendingCount(state); got != pending {
		t.Fatalf("pending=%d, want %d: the same prefix was queued twice", got, pending)
	}
}
