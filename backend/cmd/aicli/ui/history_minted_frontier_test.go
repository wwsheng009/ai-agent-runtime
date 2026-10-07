package ui

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// A1-3 步 3a：mintedFrontier 是「已铸造来源」的镜像判定（allocation 序）。
// 在 resume（最新页先同步装载并规划）+ deferred prepend（较早页后插）的
// 生产顺序下，对每个规划候选：
//
//	mintedThrough(key) ≡ 旧记录判定（bySource 非空或压缩墓碑存在）
//
// 定理与前提见 docs/plan/aicli-render-a1-3-row-cursor-plan.md §1.2。
func TestMintedFrontierMirrorsMintedSourcesAcrossResumePrepend(t *testing.T) {
	page2 := prependReplanCorpus(1, 6, 3, 1_000_000)
	page1 := prependReplanCorpus(10_000, 4, 3, 1)
	active := prependReplanMutableCell(20_000, 2_000_000)

	// 第一帧：最新页同步装载并完成首轮规划（生产装载契约）。
	state := prependReplanGeometry(UIControllerState{})
	state = prependReplanInstall(state, append(append([]*scene.TranscriptCell{}, page2...), active), 2, true)
	ledger := state.HistoryEffects.ledger
	if ledger == nil || !ledger.mintedFrontierValid {
		t.Fatalf("first plan did not advance the mint frontier: %#v", ledger)
	}
	frontierAfterPage2 := ledger.mintedFrontier
	assertFrontierCoversMinted(t, ledger)
	assertCandidateEquivalence(t, state, ledger)

	// deferred 较早页 prepend：新 cell ID 更晚分配、也更晚进入规划。
	state = prependReplanInstall(state, append(append(append([]*scene.TranscriptCell{}, page1...), page2...), active), 3, true)
	ledger = state.HistoryEffects.ledger
	if historyCommitSourceKeyLess(ledger.mintedFrontier, frontierAfterPage2) {
		t.Fatalf("mint frontier regressed: before=%#v after=%#v", frontierAfterPage2, ledger.mintedFrontier)
	}
	assertFrontierCoversMinted(t, ledger)
	assertCandidateEquivalence(t, state, ledger)

	// 较早页必须真的被铸造（与 prepend 结构断言同口径）。
	page1IDs := make(map[scene.CellID]struct{}, len(page1))
	for _, cell := range page1 {
		page1IDs[cell.ID] = struct{}{}
	}
	if got := prependReplanCountCommits(ledger.Entries(), page1IDs); got == 0 {
		t.Fatalf("older page was not minted after the deferred prepend: %#v", ledger.Entries())
	}

	// 交付头指针后再次规划：已交付来源仍在游标覆盖内，等价判定不漂移。
	head := ledger.queueHeadToken
	if entry, ok := ledger.Entry(head); ok {
		generation := entry.Commit.LayoutGeneration
		state = reduceUIControllerState(state, BeginHistoryCommit{Token: head, LayoutGeneration: generation}, 4)
		state = reduceUIControllerState(state, HistoryCommitAcknowledged{Token: head, Frame: 7, LayoutGeneration: generation}, 5)
		ledger = state.HistoryEffects.ledger
		if entry, ok := ledger.Entry(head); !ok || entry.State != HistoryCommitDelivered {
			t.Fatalf("head token was not delivered: %#v", entry)
		}
		assertFrontierCoversMinted(t, ledger)
		assertCandidateEquivalence(t, state, ledger)
	}
}

// assertFrontierCoversMinted：每个现存条目的来源必须 ≤ frontier（已铸造）。
func assertFrontierCoversMinted(t *testing.T, ledger *HistoryCommitLedger) {
	t.Helper()
	for token, entry := range ledger.byToken {
		key := historyCommitSourceIdentity(entry.Commit)
		if !ledger.mintedThrough(key) {
			t.Fatalf("live token %d source %#v is beyond the mint frontier %#v", token, key, ledger.mintedFrontier)
		}
	}
}

// assertCandidateEquivalence：规划候选的 mintedThrough 必须与旧记录判定一致
// （3a 的镜像等价契约；3b 切换判定后本断言改为双跑对照）。
func assertCandidateEquivalence(t *testing.T, state UIControllerState, ledger *HistoryCommitLedger) {
	t.Helper()
	candidates, _ := planEligibleHistoryCommitsWithin(state.AppState)
	if len(candidates) == 0 {
		t.Fatal("expected planner candidates")
	}
	for _, candidate := range candidates {
		key := historyCommitSourceIdentity(candidate)
		mintedByRecords := len(ledger.bySource[key]) > 0
		if _, ok := ledger.compactedTerminalSources[key]; ok {
			mintedByRecords = true
		}
		if got := ledger.mintedThrough(key); got != mintedByRecords {
			t.Fatalf("candidate %#v: mintedThrough=%v, mintedByRecords=%v (frontier=%#v)",
				key, got, mintedByRecords, ledger.mintedFrontier)
		}
	}
}

// 游标单调性与 Clone 保真：镜像字段必须随克隆复制、只前进不回退。
func TestMintedFrontierCloneAndMonotonicity(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	first := testHistoryCommit(1, 10, 2)
	second := testHistoryCommit(2, 20, 2)
	if err := ledger.Enqueue(first); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	if err := ledger.Enqueue(second); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}
	if !ledger.mintedThrough(historyCommitSourceIdentity(first)) ||
		!ledger.mintedThrough(historyCommitSourceIdentity(second)) {
		t.Fatalf("frontier did not cover both enqueued sources: %#v", ledger.mintedFrontier)
	}
	clone := ledger.Clone()
	if clone.mintedFrontier != ledger.mintedFrontier || !clone.mintedFrontierValid {
		t.Fatalf("clone lost the mint frontier: %#v", clone.mintedFrontier)
	}
	// 低 ID 来源补铸（重铸例外路径）不得让游标回退。
	if err := ledger.Enqueue(testHistoryCommit(3, 5, 2)); err != nil {
		t.Fatalf("enqueue lower source: %v", err)
	}
	if ledger.mintedFrontier != clone.mintedFrontier {
		t.Fatalf("frontier regressed after a lower-key enqueue: %#v vs %#v", ledger.mintedFrontier, clone.mintedFrontier)
	}
}
