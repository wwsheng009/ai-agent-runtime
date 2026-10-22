package ui

import (
	"errors"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// 需求：resize 时 `rebasePendingHistoryEffects` 会按**新宽度**重铸候选，并对
// 尚未写入的 Queued 条目执行 RebasePending。若该重铸产生新的 fragment 身份，
// rebase 会因 ErrCommitSourceChanged 失败；更糟的是新候选会以全新身份入队，
// 让同一内容被写两次。
//
// 判据：resize 前处于 Queued（未写）的历史，在 resize 后仍然只对应**一个**
// 物理交付，且 ledger 中该 cell revision 的 token 数量不增长（只允许 rebase
// 既有 token，不允许新增）。
func TestHistoryEffectQueueResizeRebasesWithoutDuplicatingQueuedCommits(t *testing.T) {
	const (
		cellID   = scene.CellID(611)
		revision = uint64(2)
	)
	// 一条长行的两个片段：resize 后折行数不同，fragment 边界会变化。
	first := HistoryCommit{
		Token: 1, CellID: cellID, Revision: revision,
		SourceRange:      SourceRange{Start: 0, End: 60},
		DisplayRange:     DisplayRange{Start: 0, End: 1},
		LayoutGeneration: 1,
	}
	second := HistoryCommit{
		Token: 2, CellID: cellID, Revision: revision,
		SourceRange:      SourceRange{Start: 60, End: 120},
		DisplayRange:     DisplayRange{Start: 1, End: 2},
		LayoutGeneration: 1,
	}

	ledger := NewHistoryCommitLedger()
	queue := &HistoryEffectQueueState{ledger: ledger}
	if err := queue.enqueue(first); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	if err := queue.enqueue(second); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}
	before := len(ledger.byToken)

	// 布局代次推进：同一 cell 在新宽度下重新切分（区间边界变化）。
	resized := []HistoryCommit{
		{Token: 0, CellID: cellID, Revision: revision, SourceRange: SourceRange{Start: 0, End: 45}, DisplayRange: DisplayRange{Start: 0, End: 1}, LayoutGeneration: 2},
		{Token: 0, CellID: cellID, Revision: revision, SourceRange: SourceRange{Start: 45, End: 90}, DisplayRange: DisplayRange{Start: 1, End: 2}, LayoutGeneration: 2},
		{Token: 0, CellID: cellID, Revision: revision, SourceRange: SourceRange{Start: 90, End: 120}, DisplayRange: DisplayRange{Start: 2, End: 3}, LayoutGeneration: 2},
	}
	for _, candidate := range resized {
		// 与 reducer 的 resize 路径一致：先 rebase 既有条目，再尝试入队候选。
		_ = queue.rebasePending(candidate)
		if err := queue.enqueue(candidate); err != nil &&
			!errors.Is(err, ErrDuplicateCommitRange) {
			t.Fatalf("unexpected enqueue error: %v", err)
		}
	}

	if after := len(ledger.byToken); after > before+1 {
		t.Fatalf("resize minted %d extra tokens (before=%d after=%d); re-wrap must not duplicate delivery",
			after-before, before, after)
	}
	// 同一 cell revision 的已交付内容不得翻倍：统计各 state 条目数。
	queued := 0
	for _, entry := range ledger.byToken {
		if entry.Commit.CellID == cellID && entry.State == HistoryCommitQueued {
			queued++
		}
	}
	if queued > len(resized) {
		t.Fatalf("queued entries=%d exceed the re-wrapped fragment count %d", queued, len(resized))
	}
}
