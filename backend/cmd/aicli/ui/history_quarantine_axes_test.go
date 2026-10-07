package ui

import (
	"errors"
	"testing"
)

// A1-3 步 3d：Quarantine 子类折叠为 (UnresolvedDelivery, MayRemint) 两轴后的
// 逐行为等价矩阵（设计冻结要求的前置等价测试）。每个场景钉住三个行为差异——
// 未决计数 / 可压缩性 / 可重铸性——以及 settle 与 covered 强化的边界语义
// （settled 的物理事实强化不得转成恢复义务）。
func TestQuarantineAxesBehaviorMatrix(t *testing.T) {
	assertAxes := func(t *testing.T, entry HistoryCommitEntry, unresolved, mayRemint bool) {
		t.Helper()
		if got := entry.Unresolved(); got != unresolved {
			t.Fatalf("Unresolved()=%t want %t (%#v)", got, unresolved, entry)
		}
		if entry.MayRemint != mayRemint {
			t.Fatalf("MayRemint=%t want %t (%#v)", entry.MayRemint, mayRemint, entry)
		}
		if got := entry.BlocksRemint(); got != !mayRemint {
			t.Fatalf("BlocksRemint()=%t want %t (%#v)", got, !mayRemint, entry)
		}
		if got := prunableResolvedEntry(entry); got != !unresolved {
			t.Fatalf("prunableResolvedEntry()=%t want %t (%#v)", got, !unresolved, entry)
		}
	}

	t.Run("fail_then_settle", func(t *testing.T) {
		ledger := NewHistoryCommitLedger()
		commit := testHistoryCommit(1, 7, 2)
		if err := ledger.Enqueue(commit); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if err := ledger.Fail(commit.Token, errors.New("sink refused"), false); err != nil {
			t.Fatalf("fail: %v", err)
		}
		entry, _ := ledger.Entry(commit.Token)
		assertAxes(t, entry, true, false)
		if entry.IsSettled() {
			t.Fatal("failed entry must not read as settled")
		}
		if ledger.unresolvedCount != 1 {
			t.Fatalf("unresolvedCount=%d want 1", ledger.unresolvedCount)
		}
		if ledger.pruneResolvedToken(commit.Token) {
			t.Fatal("unresolved entry must not be prunable before settle")
		}

		if !ledger.SettleUnresolvedWithoutReplay() {
			t.Fatal("settle must retire unresolved entries")
		}
		entry, _ = ledger.Entry(commit.Token)
		assertAxes(t, entry, false, false)
		if !entry.IsSettled() {
			t.Fatalf("settled entry axes wrong: %#v", entry)
		}
		if ledger.unresolvedCount != 0 {
			t.Fatalf("settle must clear the unresolved counter: %d", ledger.unresolvedCount)
		}
		if !ledger.pruneResolvedToken(commit.Token) {
			t.Fatal("settled entry must be prunable")
		}
		key := historyCommitSourceIdentity(commit)
		if !ledger.hasTerminalRecordForSource(key) {
			t.Fatal("settled source must keep blocking a second mint")
		}
		remint := commit
		remint.Token = 2
		if err := ledger.Enqueue(remint); !errors.Is(err, ErrDuplicateCommitRange) {
			t.Fatalf("settled source re-mint = %v, want ErrDuplicateCommitRange", err)
		}
	})

	t.Run("invalidate_clean_allows_remint", func(t *testing.T) {
		ledger := NewHistoryCommitLedger()
		commit := testHistoryCommit(1, 7, 2)
		if err := ledger.Enqueue(commit); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if err := ledger.Invalidate(commit.Token, false); err != nil {
			t.Fatalf("invalidate: %v", err)
		}
		entry, _ := ledger.Entry(commit.Token)
		assertAxes(t, entry, false, true)
		if entry.IsSettled() {
			t.Fatal("invalidated-clean must not read as settled")
		}
		if ledger.unresolvedCount != 0 {
			t.Fatalf("clean invalidation must not count as unresolved: %d", ledger.unresolvedCount)
		}
		if !ledger.pruneResolvedToken(commit.Token) {
			t.Fatal("clean invalidation must be prunable")
		}
		if ledger.hasTerminalRecordForSource(historyCommitSourceIdentity(commit)) {
			t.Fatal("clean invalidation must not leave a blocking tombstone")
		}
		remint := commit
		remint.Token = 2
		if err := ledger.Enqueue(remint); err != nil {
			t.Fatalf("clean invalidation must allow a re-mint: %v", err)
		}
	})

	t.Run("invalidate_partial_is_unresolved", func(t *testing.T) {
		ledger := NewHistoryCommitLedger()
		commit := testHistoryCommit(1, 7, 2)
		if err := ledger.Enqueue(commit); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if err := ledger.Invalidate(commit.Token, true); err != nil {
			t.Fatalf("invalidate: %v", err)
		}
		entry, _ := ledger.Entry(commit.Token)
		assertAxes(t, entry, true, false)
		if ledger.unresolvedCount != 1 {
			t.Fatalf("partial invalidation must count as unresolved: %d", ledger.unresolvedCount)
		}
		if ledger.pruneResolvedToken(commit.Token) {
			t.Fatal("partial invalidation must not be prunable before settle")
		}
	})

	t.Run("covered_strengthening_preserves_axes", func(t *testing.T) {
		// queued -> failed：转未决并撤回重铸许可。
		ledger := NewHistoryCommitLedger()
		commit := testHistoryCommit(1, 7, 2)
		if err := ledger.Enqueue(commit); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		queue := &HistoryEffectQueueState{ledger: ledger}
		if !queue.quarantineCoveredToken(commit.Token, ErrCommitSourceChanged) {
			t.Fatal("covered queued token must be quarantined unresolved")
		}
		entry, _ := ledger.Entry(commit.Token)
		assertAxes(t, entry, true, false)
		if !entry.MayHavePartiallyWritten || !errors.Is(entry.Failure, ErrCommitSourceChanged) {
			t.Fatalf("covered strengthening must keep the physical fact: %#v", entry)
		}

		// settled 强化：物理事实被加强，但恢复义务必须保持 false（折叠陷阱回归）。
		settledLedger := NewHistoryCommitLedger()
		settledCommit := testHistoryCommit(1, 7, 2)
		if err := settledLedger.Enqueue(settledCommit); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if err := settledLedger.Fail(settledCommit.Token, errors.New("sink refused"), true); err != nil {
			t.Fatalf("fail: %v", err)
		}
		if !settledLedger.SettleUnresolvedWithoutReplay() {
			t.Fatal("fixture must settle")
		}
		settledQueue := &HistoryEffectQueueState{ledger: settledLedger}
		settledQueue.quarantineCoveredToken(settledCommit.Token, ErrCommitSourceChanged)
		after, _ := settledLedger.Entry(settledCommit.Token)
		assertAxes(t, after, false, false)
		if !after.MayHavePartiallyWritten {
			t.Fatal("settled strengthening must still record the physical fact")
		}
		if settledLedger.unresolvedCount != 0 {
			t.Fatalf("settled strengthening created a recovery obligation: %d", settledLedger.unresolvedCount)
		}

		// invalidated-clean 强化：转为未决（可能已部分落盘），不再允许重铸。
		cleanLedger := NewHistoryCommitLedger()
		cleanCommit := testHistoryCommit(1, 7, 2)
		if err := cleanLedger.Enqueue(cleanCommit); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if err := cleanLedger.Invalidate(cleanCommit.Token, false); err != nil {
			t.Fatalf("invalidate: %v", err)
		}
		cleanQueue := &HistoryEffectQueueState{ledger: cleanLedger}
		cleanQueue.quarantineCoveredToken(cleanCommit.Token, ErrCommitSourceChanged)
		cleanEntry, _ := cleanLedger.Entry(cleanCommit.Token)
		assertAxes(t, cleanEntry, true, false)
		if cleanLedger.unresolvedCount != 1 {
			t.Fatalf("strengthened clean invalidation must count as unresolved: %d", cleanLedger.unresolvedCount)
		}
	})
}
