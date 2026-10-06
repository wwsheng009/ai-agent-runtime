package ui

import (
	"errors"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

func testHistoryCommit(token uint64, cellID scene.CellID, generation uint64) HistoryCommit {
	return HistoryCommit{
		Token:            token,
		CellID:           cellID,
		Revision:         7,
		SourceRange:      SourceRange{Start: 4, End: 12},
		DisplayRange:     DisplayRange{Start: 2, End: 5},
		LayoutGeneration: generation,
		Lines:            []render.Line{{Spans: []render.Span{{Text: "same visible text"}}}},
	}
}

func TestHistoryCommitLedger_AckExactlyOnceByTokenAndRange(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	commit := testHistoryCommit(11, 42, 3)
	if err := ledger.Enqueue(commit); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := ledger.Ack(commit.Token, 99, 3); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if err := ledger.Ack(commit.Token, 100, 3); !errors.Is(err, ErrDuplicateCommitAck) {
		t.Fatalf("second Ack = %v, want ErrDuplicateCommitAck", err)
	}
	entry, ok := ledger.Entry(commit.Token)
	if !ok || entry.State != HistoryCommitAcked || entry.AckFrame != 99 {
		t.Fatalf("entry = %+v, found=%t", entry, ok)
	}
	if entry.Commit.Lines != nil {
		t.Fatalf("ack retained delivered payload: %#v", entry.Commit.Lines)
	}
}

func TestHistoryCommitLedger_SameTextDifferentIdentityIsAllowed(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	first := testHistoryCommit(1, 41, 8)
	second := testHistoryCommit(2, 42, 8)
	if err := ledger.Enqueue(first); err != nil {
		t.Fatalf("first Enqueue: %v", err)
	}
	if err := ledger.Enqueue(second); err != nil {
		t.Fatalf("same-text different-cell Enqueue: %v", err)
	}
	duplicate := first
	duplicate.Token = 3
	if err := ledger.Enqueue(duplicate); !errors.Is(err, ErrDuplicateCommitRange) {
		t.Fatalf("same identity/range Enqueue = %v, want ErrDuplicateCommitRange", err)
	}
}

func TestHistoryCommitLedger_RichFragmentsShareSourceWithoutColliding(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	first := testHistoryCommit(1, 41, 8)
	first.SourceRange = SourceRange{Start: 0, End: 40}
	first.FragmentID = 1
	first.DisplayRange = DisplayRange{Start: 0, End: 1}
	second := first
	second.Token = 2
	second.FragmentID = 2
	second.DisplayRange = DisplayRange{Start: 1, End: 2}
	if err := ledger.Enqueue(first); err != nil {
		t.Fatalf("first rich fragment Enqueue: %v", err)
	}
	if err := ledger.Enqueue(second); err != nil {
		t.Fatalf("second rich fragment Enqueue: %v", err)
	}
	if got := len(ledger.Entries()); got != 2 {
		t.Fatalf("rich fragment entries=%d want 2", got)
	}
}

func TestHistoryCommitLedger_RejectsIncompleteIdentity(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	missingGeneration := testHistoryCommit(1, 41, 0)
	if err := ledger.Enqueue(missingGeneration); !errors.Is(err, ErrInvalidHistoryCommit) {
		t.Fatalf("missing layout generation = %v, want ErrInvalidHistoryCommit", err)
	}
	emptyDisplay := testHistoryCommit(2, 42, 1)
	emptyDisplay.DisplayRange = DisplayRange{Start: 3, End: 3}
	if err := ledger.Enqueue(emptyDisplay); !errors.Is(err, ErrInvalidHistoryCommit) {
		t.Fatalf("empty display range = %v, want ErrInvalidHistoryCommit", err)
	}
}

func TestHistoryCommitLedger_StaleGenerationDoesNotAdvanceAck(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	commit := testHistoryCommit(9, 71, 4)
	if err := ledger.Enqueue(commit); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := ledger.Ack(commit.Token, 3, 5); !errors.Is(err, ErrStaleLayoutGeneration) {
		t.Fatalf("stale Ack = %v, want ErrStaleLayoutGeneration", err)
	}
	entry, ok := ledger.Entry(commit.Token)
	if !ok || entry.State != HistoryCommitPending || entry.AckFrame != 0 {
		t.Fatalf("stale Ack advanced entry: %+v, found=%t", entry, ok)
	}
}

func TestHistoryCommitLedger_FailurePreservesPartialWriteSignal(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	commit := testHistoryCommit(13, 88, 2)
	if err := ledger.Enqueue(commit); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	cause := errors.New("short write")
	if err := ledger.Fail(commit.Token, cause, true); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	entry, ok := ledger.Entry(commit.Token)
	if !ok || entry.State != HistoryCommitStateFailed || !entry.MayHavePartiallyWritten || !errors.Is(entry.Failure, cause) {
		t.Fatalf("entry = %+v, found=%t", entry, ok)
	}
}

func TestHistoryCommitLedger_SourceLookupKeepsLifecycleWithoutDetachedCopies(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	for token := uint64(1); token <= 64; token++ {
		commit := testHistoryCommit(token, scene.CellID(token), 2)
		commit.Lines = make([]render.Line, 16)
		if err := ledger.Enqueue(commit); err != nil {
			t.Fatalf("Enqueue(%d): %v", token, err)
		}
	}
	target := testHistoryCommit(1, 1, 2)
	key := historyCommitSourceIdentity(target)
	if allocs := testing.AllocsPerRun(100, func() {
		if !ledger.hasTerminalRecordForSource(key) {
			t.Fatal("source index lost pending lifecycle")
		}
	}); allocs != 0 {
		t.Fatalf("source lookup allocated %v objects; detached payload copies must stay off the hot path", allocs)
	}
	if err := ledger.Ack(target.Token, 10, target.LayoutGeneration); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if !ledger.hasTerminalRecordForSource(key) {
		t.Fatal("acked source lifecycle was lost")
	}
}

// TestHistoryCommitLedger_TerminalCompactionRetainsSourceIdentity 锁定 P2-1：已
// 确认的 transcript 交付不再被任何读取方消费（行载荷已在 Ack 时置 nil），条目可
// 立即回收；但 "该来源已交付" 必须永远阻断重复铸造，身份以最小 tombstone 留下。
func TestHistoryCommitLedger_TerminalCompactionRetainsSourceIdentity(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	commit := testHistoryCommit(1, 1, 2)
	if err := ledger.Enqueue(commit); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := ledger.Ack(commit.Token, 3, 2); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if !ledger.pruneResolvedToken(commit.Token) {
		t.Fatal("acked transcript entry must be prunable")
	}
	if _, ok := ledger.Entry(commit.Token); ok {
		t.Fatal("pruned entry stayed addressable")
	}
	if len(ledger.byToken) != 0 || len(ledger.byRange) != 0 || len(ledger.bySource) != 0 || len(ledger.tokens) != 0 {
		t.Fatalf("compaction left index residue: byToken=%d byRange=%d bySource=%d tokens=%d",
			len(ledger.byToken), len(ledger.byRange), len(ledger.bySource), len(ledger.tokens))
	}
	if ledger.compactedEntries != 1 {
		t.Fatalf("compacted counter = %d, want 1", ledger.compactedEntries)
	}
	key := historyCommitSourceIdentity(commit)
	if !ledger.hasTerminalRecordForSource(key) {
		t.Fatal("compaction dropped the terminal source identity")
	}
	if !ledger.holdsPlan() {
		t.Fatal("compaction must keep the plan-memoizable lifecycle fact")
	}
	remint := commit
	remint.Token = 2
	if err := ledger.Enqueue(remint); !errors.Is(err, ErrDuplicateCommitRange) {
		t.Fatalf("re-mint after compaction = %v, want ErrDuplicateCommitRange", err)
	}
	// Clone 必须携带 tombstone 与计数：AppState 快照也要保持同一阻断语义。
	clone := ledger.Clone()
	if !clone.hasTerminalRecordForSource(key) || clone.compactedEntries != 1 {
		t.Fatalf("clone lost compacted terminal state: entries=%d tombstones=%d",
			clone.compactedEntries, len(clone.compactedTerminalSources))
	}
	if err := clone.Enqueue(remint); !errors.Is(err, ErrDuplicateCommitRange) {
		t.Fatalf("clone re-mint = %v, want ErrDuplicateCommitRange", err)
	}
}

// TestHistoryCommitLedger_TerminalCompactionKeepsRetainedPayloads 锁定压缩的
// 保留集合：Acked+Active 的渲染行仍被 finalized 计划的前导证明读取；未决的
// Failed/部分写入交付在 settle 前必须保持可寻址；非部分写入的 Invalidated 不
// 阻断再铸造，因此剪除时不留 tombstone。
func TestHistoryCommitLedger_TerminalCompactionKeepsRetainedPayloads(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	active := testHistoryCommit(1, 7, 2)
	active.Origin = HistoryCommitActive
	if err := ledger.Enqueue(active); err != nil {
		t.Fatalf("Enqueue(active): %v", err)
	}
	if err := ledger.Ack(active.Token, 5, 2); err != nil {
		t.Fatalf("Ack(active): %v", err)
	}
	if ledger.pruneResolvedToken(active.Token) {
		t.Fatal("acked Active-origin entry still backs the rendered-prefix proof")
	}
	if entry, ok := ledger.Entry(active.Token); !ok || len(entry.Commit.Lines) == 0 {
		t.Fatal("active payload must be retained until finalization consumes it")
	}

	failed := testHistoryCommit(2, 8, 2)
	failed.Origin = HistoryCommitActive
	if err := ledger.Enqueue(failed); err != nil {
		t.Fatalf("Enqueue(failed): %v", err)
	}
	if err := ledger.Fail(failed.Token, errors.New("short write"), true); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if ledger.pruneResolvedToken(failed.Token) {
		t.Fatal("unresolved failed delivery must stay addressable until settle")
	}
	if !ledger.SettleUnresolvedWithoutReplay() {
		t.Fatal("settle must retire unresolved entries")
	}
	if entry, ok := ledger.Entry(failed.Token); !ok || entry.State != HistoryCommitAbandoned {
		t.Fatalf("settled entry must stay addressable as Abandoned until the window compaction retires it: %+v found=%t", entry, ok)
	}
	if !ledger.pruneResolvedToken(failed.Token) {
		t.Fatal("abandoned entry must be prunable once retired")
	}
	if !ledger.hasTerminalRecordForSource(historyCommitSourceIdentity(failed)) {
		t.Fatal("abandoned source identity was lost")
	}
	if len(ledger.activeTokensByCell[8]) != 0 {
		t.Fatalf("active token index kept compacted tokens: %v", ledger.activeTokensByCell[8])
	}

	invalidated := testHistoryCommit(3, 9, 2)
	if err := ledger.Enqueue(invalidated); err != nil {
		t.Fatalf("Enqueue(invalidated): %v", err)
	}
	if err := ledger.Invalidate(invalidated.Token, false); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if !ledger.pruneResolvedToken(invalidated.Token) {
		t.Fatal("non-partial invalidation is not consumed by any reader and must be prunable")
	}
	if ledger.hasTerminalRecordForSource(historyCommitSourceIdentity(invalidated)) {
		t.Fatal("non-partial invalidation must not block a future mint")
	}
	// 剪除后镜像与有序遍历保持精确：剩余 live 条目不受影响。
	if tokens := ledger.orderedTokens(); len(tokens) != 1 || tokens[0] != active.Token {
		t.Fatalf("ordered mirror after compaction = %v, want [%d]", tokens, active.Token)
	}
}

// TestHistoryEffectQueueAckCompactsResolvedEntries 走 queue 层（生产 ack 入口）：
// 库存超过高水位后，最老的已确认 transcript 条目被回收（近期条目保留），诊断读数
// 同步反映压缩，来源身份仍阻断重复铸造。
func TestHistoryEffectQueueAckCompactsResolvedEntries(t *testing.T) {
	restoreHigh, restoreTarget := historyLedgerCompactHighWater, historyLedgerCompactTarget
	t.Cleanup(func() {
		historyLedgerCompactHighWater, historyLedgerCompactTarget = restoreHigh, restoreTarget
	})
	historyLedgerCompactHighWater, historyLedgerCompactTarget = 8, 4

	state := HistoryEffectQueueState{ledger: NewHistoryCommitLedger()}
	for token := uint64(1); token <= 20; token++ {
		commit := testHistoryCommit(token, scene.CellID(token), 2)
		if err := state.ledger.Enqueue(commit); err != nil {
			t.Fatalf("Enqueue(%d): %v", token, err)
		}
		if err := state.markInFlight(token, 2); err != nil {
			t.Fatalf("markInFlight(%d): %v", token, err)
		}
		if err := state.ack(token, token, 2); err != nil {
			t.Fatalf("ack(%d): %v", token, err)
		}
	}
	summary := state.Summary()
	if summary.LedgerEntries > historyLedgerCompactHighWater {
		t.Fatalf("inventory stayed above the high water: %#v", summary)
	}
	if summary.LedgerCompacted == 0 || summary.LedgerTerminalSources == 0 {
		t.Fatalf("window compaction did not retire resolved entries: %#v", summary)
	}
	if summary.LedgerEntries+int(summary.LedgerCompacted) != 20 {
		t.Fatalf("compaction accounting = %d live + %d compacted, want 20",
			summary.LedgerEntries, summary.LedgerCompacted)
	}
	if _, ok := state.ledger.Entry(20); !ok {
		t.Fatal("windowed compaction must retain recent acknowledgements")
	}
	if !state.hasTerminalRecordForSource(testHistoryCommit(1, 1, 2)) {
		t.Fatal("oldest compacted source identity was lost")
	}
}

func TestTerminalEffectResultsKeepAckAndPartialFailureDistinct(t *testing.T) {
	ack := TerminalEffectAck{Token: 17, Frame: 4}.AsAction()
	if ack.Token != 17 || ack.Err != nil || ack.MayHavePartiallyWritten {
		t.Fatalf("ack action = %+v", ack)
	}
	failed := (TerminalEffectFailed{Token: 18, MayHavePartiallyWritten: true}).AsAction()
	if failed.Token != 18 || !errors.Is(failed.Err, ErrUIActionEffectFailed) || !failed.MayHavePartiallyWritten {
		t.Fatalf("failed action = %+v", failed)
	}
}

// TestHistoryCommitLedger_UnresolvedCounterMatchesScan cross-checks the O(1)
// unresolvedCount cache against a brute-force scan of the same ledger across
// every state transition. The executor and reducer rely on
// hasUnresolvedTerminalDelivery to decide recovery; a counter/scan divergence
// would either skip a required recovery or freeze the queue forever.
func TestHistoryCommitLedger_UnresolvedCounterMatchesScan(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	check := func(when string) {
		t.Helper()
		want := 0
		for _, entry := range ledger.byToken {
			switch entry.State {
			case HistoryCommitStateFailed:
				want++
			case HistoryCommitInvalidated:
				if entry.MayHavePartiallyWritten {
					want++
				}
			}
		}
		if got := ledger.hasUnresolvedTerminalDelivery(); got != (want > 0) {
			t.Fatalf("%s: hasUnresolvedTerminalDelivery=%t, scan=%d", when, got, want)
		}
		if ledger.unresolvedCount != want {
			t.Fatalf("%s: unresolvedCount=%d, scan=%d", when, ledger.unresolvedCount, want)
		}
	}

	// Pending and in-flight entries are never unresolved.
	first := testHistoryCommit(1, 41, 8)
	if err := ledger.Enqueue(first); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	check("pending")

	// Ack resolves cleanly and never counts.
	if err := ledger.Ack(first.Token, 1, 8); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	check("acked")

	// Fail always counts, even without a partial-write signal.
	second := testHistoryCommit(2, 42, 8)
	if err := ledger.Enqueue(second); err != nil {
		t.Fatalf("Enqueue second: %v", err)
	}
	if err := ledger.Fail(second.Token, errors.New("boom"), false); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	check("failed without partial write")

	// Pending invalidation without a partial write is NOT unresolved.
	third := testHistoryCommit(3, 43, 8)
	if err := ledger.Enqueue(third); err != nil {
		t.Fatalf("Enqueue third: %v", err)
	}
	if err := ledger.Invalidate(third.Token, false); err != nil {
		t.Fatalf("Invalidate pending: %v", err)
	}
	check("pending invalidated")

	// Partial-write invalidation (the token held the write cursor) IS unresolved.
	fourth := testHistoryCommit(4, 44, 8)
	if err := ledger.Enqueue(fourth); err != nil {
		t.Fatalf("Enqueue fourth: %v", err)
	}
	if err := ledger.Invalidate(fourth.Token, true); err != nil {
		t.Fatalf("Invalidate partial: %v", err)
	}
	check("partial invalidated")

	// Clone must carry the counter forward.
	clone := ledger.Clone()
	if got := clone.hasUnresolvedTerminalDelivery(); got != ledger.hasUnresolvedTerminalDelivery() {
		t.Fatalf("clone counter diverged: clone=%t ledger=%t", got, ledger.hasUnresolvedTerminalDelivery())
	}
	if clone.unresolvedCount != ledger.unresolvedCount {
		t.Fatalf("clone unresolvedCount=%d, want %d", clone.unresolvedCount, ledger.unresolvedCount)
	}
}

// TestHistoryCommitLedger_OrderedTokensStaysAscending verifies the cached
// token slice mirrors byToken keys in ascending order even for out-of-order
// Enqueue calls (the defensive fallback path must never be observable). The
// fast path returns the cache as a read-only view; the aliasing contract is
// asserted so a silent copy-on-return regression cannot reintroduce the
// per-call allocation hotspot on resumed sessions.
func TestHistoryCommitLedger_OrderedTokensStaysAscending(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	for _, token := range []uint64{5, 1, 9, 3, 7} {
		if err := ledger.Enqueue(testHistoryCommit(token, scene.CellID(token), 8)); err != nil {
			t.Fatalf("Enqueue(%d): %v", token, err)
		}
	}
	got := ledger.orderedTokens()
	want := []uint64{1, 3, 5, 7, 9}
	if len(got) != len(want) {
		t.Fatalf("orderedTokens = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("orderedTokens = %v, want %v", got, want)
		}
	}
	// The fast path returns the ledger's cached ascending slice as a
	// read-only view: zero-copy by contract, because production pprof showed
	// the previous per-call copy as a top allocation source on resumed
	// sessions. Callers must only iterate it within the current actor turn
	// and never mutate or retain it (Enqueue may shift elements in place).
	// The aliasing is asserted deliberately so a silent copy-on-return
	// regression cannot reintroduce the allocation hotspot.
	got[0] = 99
	if after := ledger.orderedTokens(); after[0] != 99 {
		t.Fatalf("orderedTokens returned a detached copy; want the cached read-only view: %v", after)
	}
	// External byToken write (test-only path) triggers the defensive rebuild.
	ledger.byToken[11] = HistoryCommitEntry{Commit: testHistoryCommit(11, 42, 8), State: HistoryCommitPending}
	rebuild := ledger.orderedTokens()
	if len(rebuild) != len(want)+1 || rebuild[len(rebuild)-1] != 11 {
		t.Fatalf("defensive rebuild = %v, want %v + 11", rebuild, want)
	}
}
