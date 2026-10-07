package ui

import (
	"errors"
	"sync"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// A1-2b：claimed token 的失效不再由 WriteCursor 推定物理事实，而由写结果动作
// 携带的 proof 解析。失败路径按 partial 事实决定是否升级为恢复义务。
func TestHistoryEffectQueue_ClaimedInvalidationResolvesFromFailProof(t *testing.T) {
	// partial failure：字节可能已落盘 → 未决隔离 + 恢复义务。
	queue := HistoryEffectQueueState{}
	if err := queue.enqueue(testHistoryCommit(0, 71, 3)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := queue.markInFlight(1, 3); err != nil {
		t.Fatalf("markInFlight: %v", err)
	}
	if err := queue.invalidate(1); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if entry := queue.Entries()[0]; entry.State != HistoryCommitQueued || !entry.InvalidationPending || queue.WriteCursor != 1 {
		t.Fatalf("claimed invalidation must stay pending: entry=%#v cursor=%d", entry, queue.WriteCursor)
	}
	if queue.ProjectionUnknown {
		t.Fatalf("pending invalidation raised recovery before any proof: %#v", queue)
	}
	if err := queue.fail(1, 3, errors.New("short write"), true); err != nil {
		t.Fatalf("fail: %v", err)
	}
	entry := queue.Entries()[0]
	if !entry.IsInvalidated() || !entry.MayHavePartiallyWritten || entry.InvalidationPending {
		t.Fatalf("partial failure must resolve as unresolved invalidation: %#v", entry)
	}
	if queue.WriteCursor != 0 || !queue.ProjectionUnknown || !queue.ReconciliationRequired {
		t.Fatalf("partial failure left claim/obligation inconsistent: %#v", queue)
	}

	// zero-write failure：可证明零写 → 干净失效，投影保持已知。
	queue = HistoryEffectQueueState{}
	if err := queue.enqueue(testHistoryCommit(0, 72, 3)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := queue.markInFlight(1, 3); err != nil {
		t.Fatalf("markInFlight: %v", err)
	}
	if err := queue.invalidate(1); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if err := queue.fail(1, 3, errors.New("write refused"), false); err != nil {
		t.Fatalf("fail: %v", err)
	}
	entry = queue.Entries()[0]
	if !entry.IsInvalidated() || entry.MayHavePartiallyWritten {
		t.Fatalf("zero-write failure must resolve clean: %#v", entry)
	}
	if queue.WriteCursor != 0 || queue.ProjectionUnknown || queue.ReconciliationRequired {
		t.Fatalf("zero-write failure raised a recovery obligation: %#v", queue)
	}
}

// A1-2b/Q4 邻近修复：批次失配隔离会终结被 claim 的成员；若不释放游标，后续
// claim 会被一个已终态 token 永久钉死（out-of-order 拒绝）。
func TestHistoryEffectQueue_BatchUnresolvedReleasesClaimedCursor(t *testing.T) {
	queue := HistoryEffectQueueState{}
	if err := queue.enqueue(testHistoryCommit(0, 81, 3)); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	if err := queue.enqueue(testHistoryCommit(0, 82, 3)); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}
	entries := queue.Entries()
	if len(entries) != 2 {
		t.Fatalf("fixture entries = %d, want 2", len(entries))
	}
	first := entries[0].Commit.Token
	if err := queue.markInFlight(first, 3); err != nil {
		t.Fatalf("markInFlight: %v", err)
	}
	commits := []HistoryCommit{entries[0].Commit, entries[1].Commit}
	// 畸形覆盖集（执行器不变式违反）整批 fail-closed：每个成员保留物理事实。
	// 投影恢复义务由 handler 依据返回的 unresolved 标志置位。
	if !queue.quarantineCoveredBatch(commits, errors.New("batch mismatch")) {
		t.Fatalf("malformed covered batch must report an unresolved delivery: %#v", queue)
	}
	if queue.WriteCursor != 0 {
		t.Fatalf("quarantined batch left the write cursor pinned at %d", queue.WriteCursor)
	}
	for _, entry := range queue.Entries() {
		if entry.State != HistoryCommitQuarantined || !entry.MayHavePartiallyWritten || entry.InvalidationPending {
			t.Fatalf("batch member not quarantined unresolved: %#v", entry)
		}
	}
}

// A1-2b：executor 在写前门控看到 claim 已被失效（pending）时，必须以零写
// Deferred 释放 claim，而不是把失效载荷写出去。
func TestHistoryCommitExecutor_InvalidatedClaimDefersWithoutWrite(t *testing.T) {
	var executor *HistoryCommitExecutor
	controller := NewUIController(UIControllerConfig{}, nil, func(Effect) {})
	go controller.Run()
	t.Cleanup(func() {
		executor.Close()
		controller.Close()
		controller.WaitIdle()
	})

	var mu sync.Mutex
	var calls int
	executor = NewHistoryCommitExecutor(controller, historyCommitSinkFunc(func(commit HistoryCommit) HistoryCommitResult {
		mu.Lock()
		calls++
		mu.Unlock()
		return HistoryCommitResult{Frame: commit.Token + 100}
	}))
	postHistoryEffectFixture(t, controller, 3)
	controller.WaitIdle()

	entries := controller.State().HistoryEffects.Entries()
	if len(entries) < 2 {
		t.Fatalf("fixture entries = %d, want at least 2", len(entries))
	}
	claimed := entries[0]
	if !controller.Post(BeginHistoryCommit{Token: claimed.Commit.Token, LayoutGeneration: claimed.Commit.LayoutGeneration}) {
		t.Fatal("post BeginHistoryCommit")
	}
	controller.WaitIdle()
	if state := controller.State(); state.HistoryEffects.WriteCursor != claimed.Commit.Token {
		t.Fatalf("fixture claim not held: cursor=%d want %d", state.HistoryEffects.WriteCursor, claimed.Commit.Token)
	}

	// Remove the claimed token's source while the claim is outstanding: the
	// reducer records a pending invalidation instead of guessing from the cursor.
	snapshot := controller.State().Transcript.Snapshot()
	remaining := make([]*scene.TranscriptCell, 0, len(snapshot.Cells))
	for _, cell := range snapshot.Cells {
		if cell.ID == claimed.Commit.CellID {
			continue
		}
		remaining = append(remaining, cell)
	}
	if !controller.Post(ReplaceTranscriptAction{Snapshot: &scene.Snapshot{Revision: snapshot.Revision + 1, Cells: remaining}}) {
		t.Fatal("post ReplaceTranscriptAction")
	}
	controller.WaitIdle()
	state := controller.State()
	entry := historyCommitEntry(t, state, claimed.Commit.Token)
	if !entry.InvalidationPending || state.HistoryEffects.WriteCursor != claimed.Commit.Token {
		t.Fatalf("claimed invalidation must stay pending on the claim: entry=%#v cursor=%d", entry, state.HistoryEffects.WriteCursor)
	}

	executor.Request()
	executor.WaitIdle()
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 0 {
		t.Fatalf("invalidated claim reached the sink %d time(s), want zero-write defer", got)
	}
	state = controller.State()
	entry = historyCommitEntry(t, state, claimed.Commit.Token)
	if !entry.IsInvalidated() || entry.MayHavePartiallyWritten || entry.InvalidationPending {
		t.Fatalf("gate refusal must resolve the invalidation clean: %#v", entry)
	}
	if state.HistoryEffects.WriteCursor != 0 || state.HistoryEffects.ProjectionUnknown {
		t.Fatalf("clean gate refusal left claim/recovery inconsistent: %#v", state.HistoryEffects)
	}
}

// A1-2b/Q3：覆盖集内被 claim 的 token 在写后带失效 pending 时，按单 token
// 证明分类解析（invalidated+partial 未决隔离），覆盖集其余成员照常交付。
func TestHistoryEffectQueue_CoveredBatchResolvesPendingInvalidationPerToken(t *testing.T) {
	queue := HistoryEffectQueueState{}
	if err := queue.enqueue(testHistoryCommit(0, 91, 3)); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	if err := queue.enqueue(testHistoryCommit(0, 92, 3)); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}
	entries := queue.Entries()
	if len(entries) != 2 {
		t.Fatalf("fixture entries = %d, want 2", len(entries))
	}
	head, tail := entries[0].Commit, entries[1].Commit
	if err := queue.markInFlight(head.Token, 3); err != nil {
		t.Fatalf("markInFlight: %v", err)
	}
	if err := queue.invalidate(head.Token); err != nil {
		t.Fatalf("invalidate: %v", err)
	}

	unresolved, err := queue.ackBatch([]HistoryCommit{head, tail}, 9, 3)
	if err != nil {
		t.Fatalf("ackBatch: %v", err)
	}
	if !unresolved {
		t.Fatalf("invalidated covered head must stay unresolved: %#v", queue)
	}
	findEntry := func(token uint64) HistoryCommitEntry {
		t.Helper()
		for _, entry := range queue.Entries() {
			if entry.Commit.Token == token {
				return entry
			}
		}
		t.Fatalf("token %d missing from %#v", token, queue.Entries())
		return HistoryCommitEntry{}
	}
	entry := findEntry(head.Token)
	if !entry.IsInvalidated() || !entry.MayHavePartiallyWritten || entry.InvalidationPending {
		t.Fatalf("covered invalidation must resolve as unresolved isolation: %#v", entry)
	}
	tailEntry := findEntry(tail.Token)
	if tailEntry.State != HistoryCommitDelivered || tailEntry.AckFrame != 9 {
		t.Fatalf("unchanged covered tail must stay delivered: %#v", tailEntry)
	}
	if queue.WriteCursor != 0 || !queue.ProjectionUnknown || !queue.ReconciliationRequired {
		t.Fatalf("covered invalidation left claim/obligation inconsistent: %#v", queue)
	}
	if len(queue.Pending()) != 0 {
		t.Fatalf("unresolved covered batch stayed retryable: %#v", queue.Pending())
	}
}
