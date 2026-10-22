package ui

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// 需求（本次用户报告的回归）：历史 cell 已交付后，终端宽度变化会让同一份
// 源文本按新宽度重新切分成**不同的 fragment 身份**（SourceRange / FragmentID
// 都由布局宽度决定）。旧去重只看 fragment 身份，于是把同一内容当成全新来源
// 再次入队并再次写入原生 scrollback —— 用户看到“resize 后大量重复信息”。
//
// 判据：同一 cell revision 在布局代次变化后不得再次铸造；其文本在物理流中
// 恰好出现一次。
func TestHistoryCommitResizeDoesNotRemintDeliveredCell(t *testing.T) {
	ledger := NewHistoryCommitLedger()

	const (
		cellID   = scene.CellID(41)
		revision = uint64(3)
		source   = "RESIZE-DUP-GUARD-ONE\nRESIZE-DUP-GUARD-TWO\n"
	)
	// 代次 1（宽度 40）：源被切成三片。
	firstFragments := []HistoryCommit{
		{Token: 1, CellID: cellID, Revision: revision, SourceRange: SourceRange{Start: 0, End: 20}, DisplayRange: DisplayRange{Start: 0, End: 1}, LayoutGeneration: 1},
		{Token: 2, CellID: cellID, Revision: revision, SourceRange: SourceRange{Start: 20, End: 40}, DisplayRange: DisplayRange{Start: 1, End: 2}, LayoutGeneration: 1},
		{Token: 3, CellID: cellID, Revision: revision, SourceRange: SourceRange{Start: 40, End: 41}, DisplayRange: DisplayRange{Start: 2, End: 3}, LayoutGeneration: 1},
	}
	for _, commit := range firstFragments {
		if err := ledger.Enqueue(commit); err != nil {
			t.Fatalf("enqueue gen1 token %d: %v", commit.Token, err)
		}
	}
	for _, commit := range firstFragments {
		if err := ledger.Ack(commit.Token, 1, 1); err != nil {
			t.Fatalf("ack gen1 token %d: %v", commit.Token, err)
		}
	}
	if len(ledger.completedCells) != 1 {
		t.Fatalf("fully delivered revision not recorded: %#v", ledger.completedCells)
	}

	// 代次 2（宽度 20）：同一源被重新切成四片，SourceRange/FragmentID 全部不同。
	resized := []HistoryCommit{
		{Token: 11, CellID: cellID, Revision: revision, SourceRange: SourceRange{Start: 0, End: 20}, DisplayRange: DisplayRange{Start: 0, End: 1}, LayoutGeneration: 2},
		{Token: 12, CellID: cellID, Revision: revision, SourceRange: SourceRange{Start: 20, End: 24}, DisplayRange: DisplayRange{Start: 1, End: 2}, LayoutGeneration: 2},
		{Token: 13, CellID: cellID, Revision: revision, SourceRange: SourceRange{Start: 24, End: 40}, DisplayRange: DisplayRange{Start: 2, End: 3}, LayoutGeneration: 2},
		{Token: 14, CellID: cellID, Revision: revision, SourceRange: SourceRange{Start: 40, End: 41}, DisplayRange: DisplayRange{Start: 3, End: 4}, LayoutGeneration: 2},
	}
	accepted := 0
	for _, commit := range resized {
		if err := ledger.Enqueue(commit); err == nil {
			accepted++
		}
	}
	if accepted != 0 {
		t.Fatalf("resize re-minted %d/%d fragments of an already delivered cell", accepted, len(resized))
	}
	if len(ledger.byToken) != len(firstFragments) {
		t.Fatalf("ledger grew to %d entries after resize, want %d", len(ledger.byToken), len(firstFragments))
	}
}

// 需求：整份交付的判定不能只看“有任一 entry 已 Delivered”——流式单元还有
// 未交付分片时，剩余分片必须继续流动，否则内容永久丢失。
func TestHistoryCommitPartialCellDeliveryKeepsRemainingFragments(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	const (
		cellID   = scene.CellID(52)
		revision = uint64(1)
	)
	first := HistoryCommit{Token: 1, CellID: cellID, Revision: revision, SourceRange: SourceRange{Start: 0, End: 10}, DisplayRange: DisplayRange{Start: 0, End: 1}, LayoutGeneration: 1}
	second := HistoryCommit{Token: 2, CellID: cellID, Revision: revision, SourceRange: SourceRange{Start: 10, End: 20}, DisplayRange: DisplayRange{Start: 1, End: 2}, LayoutGeneration: 1}
	if err := ledger.Enqueue(first); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	if err := ledger.Enqueue(second); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}
	if err := ledger.Ack(first.Token, 1, 1); err != nil {
		t.Fatalf("ack first: %v", err)
	}
	if len(ledger.completedCells) != 0 {
		t.Fatalf("a partially delivered revision was marked complete: %#v", ledger.completedCells)
	}

	// 同代次后续分片（以及 resize 后同代次的重排）都必须继续可用。
	third := HistoryCommit{Token: 3, CellID: cellID, Revision: revision, SourceRange: SourceRange{Start: 20, End: 30}, DisplayRange: DisplayRange{Start: 2, End: 3}, LayoutGeneration: 1}
	if err := ledger.Enqueue(third); err != nil {
		t.Fatalf("remaining fragment was suppressed: %v", err)
	}
}
