package ui

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// A1-3 步 3b：三个来源判定（hasSettled / retainedQueued / hasTerminal）切换到
// mint 游标（mintedThrough）+ live 检查后，必须与 3a 之前的实现语义等价。
//
// 唯一允许的分歧是**已压缩终态来源**的 settled 判定：
//   - 旧：bySource 为空 → false（白物化一次 payload，随后在入队处以墓碑跳过）；
//   - 新：minted && bySource 为空 && 墓碑存在 → true（直接跳过物化）。
//
// 该分歧不影响任何投递语义：分歧分支要求 bySource 为空（没有 live 条目可被
// reconcile），且候选本就会被 hasTerminalRecordForSource 在入队处跳过。
// 其余判定（含 remintable 的 invalidated-clean）必须逐字等价。

// legacyHasSettledRecordForSource 是 3b 之前的实现（逐字复制），仅作测试对照。
func legacyHasSettledRecordForSource(ledger *HistoryCommitLedger, key historyCommitSourceKey) bool {
	if ledger == nil {
		return false
	}
	tokens := ledger.bySource[key]
	if len(tokens) == 0 {
		return false
	}
	for token := range tokens {
		entry, ok := ledger.byToken[token]
		if !ok {
			continue
		}
		if entry.State == HistoryCommitQueued {
			return false
		}
	}
	return true
}

// legacyHasTerminalRecordForSource 是 3b 之前的实现（逐字复制）。
func legacyHasTerminalRecordForSource(ledger *HistoryCommitLedger, key historyCommitSourceKey) bool {
	if ledger == nil {
		return false
	}
	if _, blocked := ledger.compactedTerminalSources[key]; blocked {
		return true
	}
	for token := range ledger.bySource[key] {
		entry, ok := ledger.byToken[token]
		if !ok {
			continue
		}
		if entry.BlocksRemint() {
			return true
		}
	}
	return false
}

// legacyRetainedQueuedCommitForSource 是 3b 之前的实现（逐字复制）。
func legacyRetainedQueuedCommitForSource(ledger *HistoryCommitLedger, key historyCommitSourceKey, generation uint64) (commit HistoryCommit, hasQueued bool, terminal bool, safe bool) {
	if ledger == nil {
		return HistoryCommit{}, false, false, false
	}
	if _, blocked := ledger.compactedTerminalSources[key]; blocked {
		return HistoryCommit{}, false, true, true
	}
	tokens := ledger.bySource[key]
	if len(tokens) == 0 {
		return HistoryCommit{}, false, false, false
	}
	terminal = false
	for token := range tokens {
		entry, ok := ledger.byToken[token]
		if !ok {
			continue
		}
		if !entry.BlocksRemint() {
			continue
		}
		terminal = true
		if entry.State == HistoryCommitQueued {
			if entry.Commit.LayoutGeneration != generation {
				return HistoryCommit{}, false, true, false
			}
			commit = entry.Commit
			hasQueued = true
		}
	}
	if !terminal {
		return HistoryCommit{}, false, false, false
	}
	return commit, hasQueued, true, true
}

// assertSourceJudgmentsMatchLegacy 对单个 key 双跑三判定并断言等价（允许
// settled 的已压缩终态分歧）。
func assertSourceJudgmentsMatchLegacy(t *testing.T, queue HistoryEffectQueueState, key historyCommitSourceKey) {
	t.Helper()
	ledger := queue.ledger

	newSettled := queue.hasSettledRecordForSource(key)
	legacySettled := legacyHasSettledRecordForSource(ledger, key)
	if newSettled != legacySettled {
		_, blocked := ledger.compactedTerminalSources[key]
		if !(newSettled && !legacySettled && len(ledger.bySource[key]) == 0 && blocked) {
			t.Fatalf("settled judgment diverged for %#v: new=%v legacy=%v (tombstone=%v live=%d)",
				key, newSettled, legacySettled, blocked, len(ledger.bySource[key]))
		}
	}

	if got, want := ledger.hasTerminalRecordForSource(key), legacyHasTerminalRecordForSource(ledger, key); got != want {
		t.Fatalf("terminal judgment diverged for %#v: new=%v legacy=%v", key, got, want)
	}

	for _, generation := range []uint64{1, 2, 3} {
		newCommit, newQueued, newTerminal, newSafe := queue.retainedQueuedCommitForSource(key, generation)
		legacyCommit, legacyQueued, legacyTerminal, legacySafe := legacyRetainedQueuedCommitForSource(ledger, key, generation)
		if newQueued != legacyQueued || newTerminal != legacyTerminal || newSafe != legacySafe || newCommit.Token != legacyCommit.Token {
			t.Fatalf("retainedQueued judgment diverged for %#v gen=%d: new=(%d,%v,%v,%v) legacy=(%d,%v,%v,%v)",
				key, generation, newCommit.Token, newQueued, newTerminal, newSafe,
				legacyCommit.Token, legacyQueued, legacyTerminal, legacySafe)
		}
	}
}

// TestSourceJudgmentsMatchLegacyAcrossResumePrepend 在生产顺序（最新页先装 +
// deferred prepend + 交付 + 压缩）上对全部相关 key 双跑三判定。
func TestSourceJudgmentsMatchLegacyAcrossResumePrepend(t *testing.T) {
	page2 := prependReplanCorpus(1, 6, 3, 1_000_000)
	page1 := prependReplanCorpus(10_000, 4, 3, 1)
	active := prependReplanMutableCell(20_000, 2_000_000)

	state := prependReplanGeometry(UIControllerState{})
	state = prependReplanInstall(state, append(append([]*scene.TranscriptCell{}, page2...), active), 2, true)
	state = prependReplanInstall(state, append(append(append([]*scene.TranscriptCell{}, page1...), page2...), active), 3, true)

	ledger := state.HistoryEffects.ledger
	// 交付全部在队条目并触发压缩（调小水位，保持测试有界）。
	high, target := historyLedgerCompactHighWater, historyLedgerCompactTarget
	historyLedgerCompactHighWater, historyLedgerCompactTarget = 4, 2
	defer func() { historyLedgerCompactHighWater, historyLedgerCompactTarget = high, target }()
	for _, token := range ledger.orderedTokens() {
		entry, ok := ledger.Entry(token)
		if !ok || entry.State != HistoryCommitQueued {
			continue
		}
		if err := ledger.Ack(token, 3, entry.Commit.LayoutGeneration); err != nil {
			t.Fatalf("ack token %d: %v", token, err)
		}
	}
	ledger.compactResolvedIfLarge()

	keys := make(map[historyCommitSourceKey]struct{})
	for key := range ledger.bySource {
		keys[key] = struct{}{}
	}
	for key := range ledger.compactedTerminalSources {
		keys[key] = struct{}{}
	}
	for token, entry := range ledger.byToken {
		_ = token
		keys[historyCommitSourceIdentity(entry.Commit)] = struct{}{}
	}
	candidates, _ := planEligibleHistoryCommitsWithin(state.AppState)
	for _, candidate := range candidates {
		keys[historyCommitSourceIdentity(candidate)] = struct{}{}
	}
	if len(keys) == 0 {
		t.Fatal("fixture produced no source keys")
	}
	queue := state.HistoryEffects
	compacted := 0
	for key := range keys {
		assertSourceJudgmentsMatchLegacy(t, queue, key)
		if _, ok := ledger.compactedTerminalSources[key]; ok {
			compacted++
		}
	}
	if compacted == 0 {
		t.Fatal("fixture did not compact any terminal source; the settled divergence branch was not exercised")
	}
}

// TestCompactedTerminalSourceSkipsMaterializationAndMinting：已压缩终态来源
// 在新判定下 settled=true（跳过物化），且候选即使被构造也不会再入队铸造。
func TestCompactedTerminalSourceSkipsMaterializationAndMinting(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	commit := testHistoryCommit(1, 7, 2)
	if err := ledger.Enqueue(commit); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := ledger.Ack(commit.Token, 3, commit.LayoutGeneration); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if !ledger.pruneResolvedToken(commit.Token) {
		t.Fatal("delivered entry must be prunable")
	}
	key := historyCommitSourceIdentity(commit)
	if _, blocked := ledger.compactedTerminalSources[key]; !blocked {
		t.Fatal("fixture expected a tombstone")
	}
	queue := HistoryEffectQueueState{ledger: ledger}
	if !queue.hasSettledRecordForSource(key) {
		t.Fatal("compacted terminal source must be settled (no payload materialization)")
	}
	if legacyHasSettledRecordForSource(ledger, key) {
		t.Fatal("fixture assumption broken: legacy settled judgment must be false before 3b")
	}

	state := &UIControllerState{}
	state.HistoryEffects.ledger = ledger
	candidate := commit
	candidate.Token = 0
	enqueueHistoryCandidatesRetained(state, []HistoryCommit{candidate}, transcriptPlanRetention{})
	if len(ledger.byToken) != 0 {
		t.Fatalf("compacted source was re-minted: %#v", ledger.byToken)
	}
}

// TestPrunedRemintableSourceStillRequiresMaterialization：invalidated-clean
// 剪除后无墓碑（可再铸），新判定必须返回 settled=false / terminal=false，
// 保证「零写作废 → 重规划再铸」不丢行。
func TestPrunedRemintableSourceStillRequiresMaterialization(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	commit := testHistoryCommit(1, 9, 2)
	if err := ledger.Enqueue(commit); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := ledger.Invalidate(commit.Token, false); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if !ledger.pruneResolvedToken(commit.Token) {
		t.Fatal("non-partial invalidation must be prunable")
	}
	key := historyCommitSourceIdentity(commit)
	if _, blocked := ledger.compactedTerminalSources[key]; blocked {
		t.Fatal("non-partial invalidation must not leave a tombstone")
	}
	queue := HistoryEffectQueueState{ledger: ledger}
	if queue.hasSettledRecordForSource(key) {
		t.Fatal("remintable source must stay unsettled so the planner re-materializes it")
	}
	if queue.hasTerminalRecordForSource(commit) {
		t.Fatal("remintable source must not block a future mint")
	}
	if _, _, terminal, _ := queue.retainedQueuedCommitForSource(key, 2); terminal {
		t.Fatal("remintable source must not be retention-safe")
	}
	assertSourceJudgmentsMatchLegacy(t, queue, key)
}
