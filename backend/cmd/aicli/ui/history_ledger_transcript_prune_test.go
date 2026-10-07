package ui

import (
	"errors"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// deliverAndCompact 把一个来源交付并压缩为墓碑（装载剪枝用例的最小前置）。
func deliverAndCompact(t *testing.T, ledger *HistoryCommitLedger, commit HistoryCommit) historyCommitSourceKey {
	t.Helper()
	if err := ledger.Enqueue(commit); err != nil {
		t.Fatalf("enqueue token=%d: %v", commit.Token, err)
	}
	if err := ledger.Ack(commit.Token, 3, commit.LayoutGeneration); err != nil {
		t.Fatalf("ack token=%d: %v", commit.Token, err)
	}
	if !ledger.pruneResolvedToken(commit.Token) {
		t.Fatal("delivered entry must be prunable")
	}
	key := historyCommitSourceIdentity(commit)
	if !ledger.hasTerminalRecordForSource(key) {
		t.Fatal("fixture tombstone missing after compaction")
	}
	return key
}

// TestLedgerPrunesCompactedSourcesForDroppedCells 锁定 R5 的修复点：装载（整体替换）
// 后，已被移出 transcript 的 cell 的压缩来源墓碑必须释放，否则 key 低于旧 frontier
// 的新会话来源会被误判为「已压缩阻断」而永不交付。
func TestLedgerPrunesCompactedSourcesForDroppedCells(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	dropped := testHistoryCommit(1, 7, 2)
	droppedKey := deliverAndCompact(t, ledger, dropped)

	// 装载不同会话：新 transcript 只含 cell 1（key 低于旧 cell 7）。
	ledger.pruneCompactedSourcesNotInTranscript([]scene.TranscriptCell{
		{ID: 1, Kind: scene.KindAssistant},
	})
	if len(ledger.compactedTerminalSources) != 0 {
		t.Fatalf("dropped cell kept %d tombstones", len(ledger.compactedTerminalSources))
	}
	if ledger.hasTerminalRecordForSource(droppedKey) {
		t.Fatal("dropped cell source still reports a terminal record")
	}

	// 新来源现在必须可以铸造：这正是 3c 无墓碑判定下失败的生产流。
	loaded := testHistoryCommit(2, 1, 2)
	if err := ledger.Enqueue(loaded); err != nil {
		t.Fatalf("loaded source blocked after prune: %v", err)
	}
}

// TestLedgerKeepsCompactedSourcesForRetainedCells 锁定反向：重装同一会话时来源仍在
// transcript，阻断身份必须保留——native scrollback 是 append-only，重复追加即丢行。
func TestLedgerKeepsCompactedSourcesForRetainedCells(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	commit := testHistoryCommit(1, 7, 2)
	key := deliverAndCompact(t, ledger, commit)

	ledger.pruneCompactedSourcesNotInTranscript([]scene.TranscriptCell{
		{ID: 7, Kind: scene.KindAssistant},
	})
	if len(ledger.compactedTerminalSources) != 1 || !ledger.hasTerminalRecordForSource(key) {
		t.Fatalf("retained cell lost its blocking tombstone: %d", len(ledger.compactedTerminalSources))
	}
	remint := commit
	remint.Token = 2
	if err := ledger.Enqueue(remint); !errors.Is(err, ErrDuplicateCommitRange) {
		t.Fatalf("retained source re-mint = %v, want ErrDuplicateCommitRange", err)
	}
}

// TestReplaceTranscriptPrunesCompactedBlockingByCellSet 钉住 reducer 装载路径：
// 整体替换按新 cell 集合剪枝——被移除 cell 的墓碑释放，仍在 transcript 的保留。
func TestReplaceTranscriptPrunesCompactedBlockingByCellSet(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	kept := testHistoryCommit(1, 7, 2)
	keptKey := deliverAndCompact(t, ledger, kept)
	dropped := testHistoryCommit(2, 9, 2)
	droppedKey := deliverAndCompact(t, ledger, dropped)

	state := UIControllerState{}
	state.HistoryEffects.ledger = ledger
	state.Transcript = TranscriptState{Revision: 1, Cells: []scene.TranscriptCell{
		{ID: 7, Kind: scene.KindAssistant, Source: "kept"},
		{ID: 9, Kind: scene.KindAssistant, Source: "dropped"},
	}}
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
		Revision: 2,
		Cells: []*scene.TranscriptCell{
			{ID: 7, Revision: 1, Kind: scene.KindAssistant, Source: "kept", Phase: scene.CellCommitted},
		},
	}}, 1)

	effects := state.HistoryEffects
	if effects.ledger == nil {
		t.Fatal("replacement retired the delivery ledger")
	}
	if _, ok := effects.ledger.compactedTerminalSources[droppedKey]; ok {
		t.Fatal("replacement kept a tombstone for a cell no longer in the transcript")
	}
	if !effects.ledger.hasTerminalRecordForSource(keptKey) {
		t.Fatal("replacement dropped the blocking tombstone of a retained cell")
	}
}
