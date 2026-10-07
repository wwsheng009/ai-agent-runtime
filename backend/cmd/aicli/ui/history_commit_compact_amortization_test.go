package ui

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// TestCompactResolvedIfLargeAmortizesScansOnQueuedBacklog 钉住 2026-10-08 的
// 交付期性能修复：ledger 一次性持有大量不可剪除的 Queued 条目时，「超过高水位」
// 恒真，但压缩不得每个 ack 都全量扫描——首次越界立即压缩，此后按 ack 窗口摊还。
//
// 现场（86k 行恢复装载）：compactResolvedIfLarge 占交付期 CPU 58.8% cum，
// 其中 mapaccess2 35.7s，即每个 ack 对全量 token 的 map 查询。
func TestCompactResolvedIfLargeAmortizesScansOnQueuedBacklog(t *testing.T) {
	high, target := historyLedgerCompactHighWater, historyLedgerCompactTarget
	defer func() { historyLedgerCompactHighWater, historyLedgerCompactTarget = high, target }()
	historyLedgerCompactHighWater, historyLedgerCompactTarget = 8, 4
	step := historyLedgerCompactHighWater - historyLedgerCompactTarget

	ledger := NewHistoryCommitLedger()
	commits := make([]HistoryCommit, 0, 40)
	for token := uint64(1); token <= 40; token++ {
		commit := testHistoryCommit(token, scene.CellID(token), 2)
		if err := ledger.Enqueue(commit); err != nil {
			t.Fatalf("Enqueue(%d): %v", token, err)
		}
		commits = append(commits, commit)
	}

	// 40 条全部 Queued（不可剪除）：反复压缩只允许首次全量扫描一次。
	for i := 0; i < 5; i++ {
		if pruned := ledger.compactResolvedIfLarge(); pruned != 0 {
			t.Fatalf("queued-only compaction pruned %d entries, want 0", pruned)
		}
	}
	if ledger.compactScans != 1 {
		t.Fatalf("compactScans = %d, want 1 (first crossing scans once; queued backlog must not rescan per ack)", ledger.compactScans)
	}

	// 累计一个窗口宽度（step）的 ack 之后才允许再次扫描。
	ack := func(from, to int) {
		for i := from; i < to; i++ {
			if err := ledger.Ack(commits[i].Token, 7, commits[i].LayoutGeneration); err != nil {
				t.Fatalf("Ack(%d): %v", commits[i].Token, err)
			}
		}
	}
	ack(0, step)
	if pruned := ledger.compactResolvedIfLarge(); pruned != step {
		t.Fatalf("window compaction pruned %d entries, want %d", pruned, step)
	}
	if ledger.compactScans != 2 {
		t.Fatalf("compactScans = %d, want 2 after a full window of acks", ledger.compactScans)
	}
	if got := len(ledger.byToken); got != 40-step {
		t.Fatalf("byToken = %d, want %d after pruning the delivered window", got, 40-step)
	}

	// 不足一个窗口：不得重扫；补满窗口：重扫并回收该窗口。
	ack(step, 2*step-1)
	if pruned := ledger.compactResolvedIfLarge(); pruned != 0 {
		t.Fatalf("sub-window compaction pruned %d entries, want 0", pruned)
	}
	if ledger.compactScans != 2 {
		t.Fatalf("compactScans = %d, want 2 while the ack window is not full", ledger.compactScans)
	}
	ack(2*step-1, 2*step)
	if pruned := ledger.compactResolvedIfLarge(); pruned != step {
		t.Fatalf("second window compaction pruned %d entries, want %d", pruned, step)
	}
	if ledger.compactScans != 3 || len(ledger.byToken) != 40-2*step {
		t.Fatalf("compactScans=%d byToken=%d, want 3 and %d", ledger.compactScans, len(ledger.byToken), 40-2*step)
	}
}
