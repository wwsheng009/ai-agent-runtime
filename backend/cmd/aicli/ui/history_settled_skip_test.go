package ui

import (
	"testing"
	"time"
)

// 需求：已结算（Acked/Failed/Abandoned/Invalidated）分片不得在后续规划中被
// 重新物化 payload；仍在 Pending/InFlight 的分片必须继续发射候选，因为
// syncHistoryEffectCandidates 依赖候选 payload 做比对与 rebase。
//
// 背景（生产 pprof）：resume 长会话的每一次 transcript 迁移都会重扫整段历史，
// planMarkdownCellHistoryCommits 因此累计分配 211GB，而其中绝大多数分片早已
// Acked 且 payload 在 Ack 时就被丢弃（见 HistoryCommitLedger.Ack 对
// HistoryCommitTranscript 的处理）。规划器提前跳过这些来源即可省掉整轮重建。
func TestReplanSkipsSettledFragments(t *testing.T) {
	state := reduceUIControllerState(UIControllerState{}, Resize{Width: 72, Height: 12, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot: scrollbackGrantSnapshot(1, "loaded session"),
	}, 2)

	first, complete := planEligibleHistoryCommitsWithin(state.AppState, time.Time{})
	if !complete || len(first) == 0 {
		t.Fatalf("fixture produced no plan: complete=%t commits=%d", complete, len(first))
	}
	if len(state.HistoryEffects.ledger.byToken) == 0 {
		t.Fatal("fixture planned candidates but queued no ledger entries")
	}

	// 未结算分片必须继续出现在候选集里：reconcile 需要它们做
	// historyCommitPresentationEqual / RebasePending。
	again, _ := planEligibleHistoryCommitsWithin(state.AppState, time.Time{})
	if countFinalizedCandidates(again) == 0 {
		t.Fatal("pending finalized fragments must stay in the candidate set for reconciliation")
	}

	// 全部标记为已交付（Acked）后，重规划不得再为任何已结算分片建 payload。
	ledger := state.HistoryEffects.ledger
	acked := 0
	for _, token := range ledger.orderedTokens() {
		entry, ok := ledger.byToken[token]
		if !ok || entry.State != HistoryCommitPending {
			continue
		}
		if err := ledger.Ack(token, 1, entry.Commit.LayoutGeneration); err != nil {
			t.Fatalf("ack %d: %v", token, err)
		}
		acked++
	}
	if acked == 0 {
		t.Fatal("fixture queued nothing to acknowledge")
	}

	settled, _ := planEligibleHistoryCommitsWithin(state.AppState, time.Time{})
	if got := countFinalizedCandidates(settled); got != 0 {
		t.Fatalf("re-plan rebuilt %d settled finalized fragments; want 0", got)
	}
}

func countFinalizedCandidates(commits []HistoryCommit) int {
	count := 0
	for _, commit := range commits {
		if commit.Origin != HistoryCommitActive {
			count++
		}
	}
	return count
}
