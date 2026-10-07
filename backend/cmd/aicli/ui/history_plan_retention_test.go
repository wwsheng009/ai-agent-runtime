package ui

import (
	"fmt"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// TestPrependPlanRetainsQueuedCellsAndKeepsLedgerStable 钉死 P1-1 Stage 1 的
// 1c/1d 契约：完整规划 pass 对内容未变且已排队的复用段只做分拣（并回台账提交），
// integration 侧跳过逐条 invalidate/rebase 与重复入队 —— 台账零抖动、零新铸。
func TestPrependPlanRetainsQueuedCellsAndKeepsLedgerStable(t *testing.T) {
	const (
		page2Cells = 24
		page1Cells = 16
		lines      = 6
	)
	page2 := prependReplanCorpus(1, page2Cells, lines, 1_000_000)
	page1 := prependReplanCorpus(10_000, page1Cells, lines, 1)
	active := prependReplanMutableCell(20_000, 2_000_000)

	state := prependReplanGeometry(UIControllerState{})
	state = prependReplanInstall(state, append(append([]*scene.TranscriptCell{}, page2...), active), 2, true)
	prepended := append(append([]*scene.TranscriptCell{}, page1...), page2...)
	prepended = append(prepended, active)
	state = prependReplanInstall(state, prepended, 3, true)

	commits, complete, _, _, retention := planEligibleHistoryCommitsWithinFrom(state.AppState, time.Time{}, 0, 0)
	if !complete {
		t.Fatal("zero-deadline full pass must be complete")
	}
	wantCells := transcriptFinalizedCellCount(state.Transcript)
	if len(retention.cells) != wantCells {
		t.Fatalf("retention cells=%d, want %d (all queued finalized cells must be classified as reusable)",
			len(retention.cells), wantCells)
	}

	page1IDs := map[scene.CellID]struct{}{}
	for _, cell := range page1 {
		page1IDs[cell.ID] = struct{}{}
	}
	page2IDs := map[scene.CellID]struct{}{}
	for _, cell := range page2 {
		page2IDs[cell.ID] = struct{}{}
	}

	firstOlder, firstNewer := -1, -1
	for index, commit := range commits {
		if _, ok := page1IDs[commit.CellID]; ok && firstOlder < 0 {
			firstOlder = index
		}
		if _, ok := page2IDs[commit.CellID]; ok && firstNewer < 0 {
			firstNewer = index
		}
	}
	if firstOlder < 0 || firstNewer < 0 || firstOlder > firstNewer {
		t.Fatalf("retained union must keep plan order (older before newer): firstOlder=%d firstNewer=%d",
			firstOlder, firstNewer)
	}
	// 1b 断言：并集（含保留段按当前坐标重写的 DisplayRange）必须沿计划顺序
	// 单调推进 —— 若保留段沿用入队时旧坐标，prepend 后会出现坐标倒退。
	lastStart := -1
	for index, commit := range commits {
		if !commit.Valid() {
			t.Fatalf("commit %d is invalid after retention merge: %+v", index, commit)
		}
		if commit.DisplayRange.Start <= lastStart {
			t.Fatalf("union display ranges must advance monotonically: commit %d start=%d last=%d",
				index, commit.DisplayRange.Start, lastStart)
		}
		lastStart = commit.DisplayRange.Start
	}

	beforeTokens := state.HistoryEffects.NextToken
	before := retentionEntryFingerprint(state)
	projected := state.Clone()
	syncHistoryEffectCandidatesRetained(&projected, commits, retention)
	if got := projected.HistoryEffects.NextToken; got != beforeTokens {
		t.Fatalf("retained pass minted new tokens: next %d -> %d", beforeTokens, got)
	}
	after := retentionEntryFingerprint(projected)
	if len(after) != len(before) {
		t.Fatalf("retained pass changed ledger size: %d -> %d", len(before), len(after))
	}
	for token, fingerprint := range before {
		if after[token] != fingerprint {
			t.Fatalf("retained pass mutated ledger entry %d: %q -> %q", token, fingerprint, after[token])
		}
	}
}

func retentionEntryFingerprint(state UIControllerState) map[uint64]string {
	out := make(map[uint64]string)
	for _, entry := range state.HistoryEffects.Entries() {
		out[entry.Commit.Token] = fmt.Sprintf("%v|%d|%d", entry.State, entry.Commit.CellID, entry.Commit.Revision)
	}
	return out
}

// TestRetentionRejectsGenerationDrift 钉死安全条件：呈现代（LayoutGeneration）
// 漂移时不得整行保留 —— 必须回退重铸以触发 rebase，否则执行器的 generation
// 闸门会永久 Defer 旧条目。
func TestRetentionRejectsGenerationDrift(t *testing.T) {
	page := prependReplanCorpus(1, 4, 3, 1_000_000)
	active := prependReplanMutableCell(9_000, 2_000_000)
	state := prependReplanGeometry(UIControllerState{})
	state = prependReplanInstall(state, append(append([]*scene.TranscriptCell{}, page...), active), 2, true)

	var queued HistoryCommitEntry
	found := false
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.State == HistoryCommitQueued {
			queued = entry
			found = true
			break
		}
	}
	if !found {
		t.Fatal("fixture has no queued entry")
	}
	key := historyCommitSourceIdentity(queued.Commit)
	generation := queued.Commit.LayoutGeneration
	if _, hasQueued, terminal, safe := state.HistoryEffects.retainedQueuedCommitForSource(key, generation); !hasQueued || !terminal || !safe {
		t.Fatalf("same-generation queued source must be retention-safe: hasQueued=%t terminal=%t safe=%t",
			hasQueued, terminal, safe)
	}
	if _, _, terminal, safe := state.HistoryEffects.retainedQueuedCommitForSource(key, generation+1); !terminal || safe {
		t.Fatalf("generation drift must force re-mint/rebase: terminal=%t safe=%t", terminal, safe)
	}
}
