package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// ---------- 批量账目与可见窗口（review m5/m6） ----------

// TestReviewBatchAccountingMatchesDeliveredSections: the summary may not count
// cap-dropped items as read, and the batch must declare the aggregate window it
// really delivers.
func TestReviewBatchAccountingMatchesDeliveredSections(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	names := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		name := fmt.Sprintf("bulk-%d.txt", i)
		var builder strings.Builder
		for line := 0; line < 1400; line++ {
			fmt.Fprintf(&builder, "%05d %s\n", line, strings.Repeat("x", 50))
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(builder.String()), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		names = append(names, name)
	}
	files := make([]interface{}, 0, len(names))
	for _, name := range names {
		files = append(files, map[string]interface{}{"file_path": name, "limit": 2000})
	}
	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{"files": files})

	succeeded := viewMetadataInt(result.Metadata, "succeeded_count", -1)
	items, _ := result.Metadata["items"].([]map[string]interface{})
	if succeeded != len(items) {
		t.Fatalf("succeeded_count must equal delivered items: succeeded=%d items=%d", succeeded, len(items))
	}
	skipped, _ := result.Metadata["batch_skipped_files"].([]map[string]interface{})
	if len(skipped) == 0 {
		t.Fatalf("expected batch_skipped_files metadata when items are dropped, got %#v", result.Metadata)
	}
	if succeeded+len(skipped) != len(names) {
		t.Fatalf("accounting must cover every request: succeeded=%d skipped=%d total=%d", succeeded, len(skipped), len(names))
	}
	if count, _ := result.Metadata["batch_skipped_count"].(int); count != len(skipped) {
		t.Fatalf("batch_skipped_count must match the skipped list: count=%d list=%d", count, len(skipped))
	}
	if result.Metadata[toolresult.MetadataModelVisibleBudgetKey] != viewBatchAggregateBudgetBytes {
		t.Fatalf("batch must declare the aggregate window, got %#v", result.Metadata[toolresult.MetadataModelVisibleBudgetKey])
	}
	if viewBatchAggregateBudgetBytes > 64*1024 {
		t.Fatalf("batch window must not exceed the render-layer ceiling (64 KiB), got %d", viewBatchAggregateBudgetBytes)
	}
}

// TestReviewDedupEntrySurvivesUntilCommitted pins the m13 contract directly: a
// peeked hit keeps its entry until the consume closure runs.
func TestReviewDedupEntrySurvivesUntilCommitted(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	path := filepath.Join(root, "peek.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ctx := toolctx.WithSessionID(context.Background(), "review-peek-"+t.Name())
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	recordFileReadFromDisk(ctx, path, true, "view", fileReadWindow{})
	recordViewWindowRead(ctx, path, info, 0, 500, 2, 2, true)

	if _, hit, _ := viewDedupPeek(ctx, path, info, 0, 500); !hit {
		t.Fatalf("expected a dedup hit after recording the window")
	}
	// 未提交：再次 peek 仍然是命中。
	stub, hit, commit := viewDedupPeek(ctx, path, info, 0, 500)
	if !hit || stub == nil {
		t.Fatalf("a peeked-but-uncommitted entry must stay available")
	}
	commit()
	if _, hit, _ := viewDedupPeek(ctx, path, info, 0, 500); hit {
		t.Fatalf("commit must consume the entry")
	}
}

// ---------- 账本：全读单调性与采样哈希（review m7/m2） ----------

func TestReviewFullReadSurvivesPartialWindow(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "mono.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ctx := toolctx.WithSessionID(context.Background(), "review-fullread-"+t.Name())
	tool := newViewToolAt(t, root)
	executeViewParams(t, tool, ctx, map[string]interface{}{"file_path": "mono.txt"})
	if !sessionHasFullRead(ctx, path) {
		t.Fatalf("full read must be recorded")
	}
	executeViewParams(t, tool, ctx, map[string]interface{}{
		"file_path": "mono.txt", "offset": float64(1), "limit": float64(1),
	})
	if !sessionHasFullRead(ctx, path) {
		t.Fatalf("a later partial window must not demote the full-read record")
	}
	record, ok := ledgerForSession(toolctx.SessionID(ctx)).lookup(path)
	if !ok || !record.FullRead {
		t.Fatalf("expected a monotonic FullRead record, got %#v ok=%v", record, ok)
	}
}

func TestReviewLedgerHashScopeSelection(t *testing.T) {
	cases := []struct {
		size int64
		want string
	}{
		{size: 0, want: readLedgerHashFull},
		{size: readLedgerHashMaxBytes, want: readLedgerHashFull},
		{size: readLedgerHashMaxBytes + 1, want: readLedgerHashSampled},
		{size: readLedgerSampleHashMaxBytes, want: readLedgerHashSampled},
		{size: readLedgerSampleHashMaxBytes + 1, want: readLedgerHashUnverifd},
	}
	for _, tc := range cases {
		if got := ledgerHashScope(tc.size); got != tc.want {
			t.Fatalf("ledgerHashScope(%d) = %q, want %q", tc.size, got, tc.want)
		}
	}
}

// TestReviewSampledHashBytesMatchesStreamingDigest: the write side compares the
// in-memory digest, so both implementations must agree byte for byte.
func TestReviewSampledHashBytesMatchesStreamingDigest(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.bin")
	payload := bytes.Repeat([]byte("0123456789abcdef"), int(readLedgerSampleWindowBytes*2)/16)
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	streamed, err := hashFileSample(path, int64(len(payload)))
	if err != nil {
		t.Fatalf("hashFileSample: %v", err)
	}
	if inMemory := hashSampleBytes(payload); streamed != inMemory {
		t.Fatalf("sampled digests differ: file=%s bytes=%s", streamed, inMemory)
	}
}

// TestReviewLargeFileStaleWriteRecoversAfterReView: files above the full-hash
// cap must still refresh their record on re-view, otherwise edit is refused
// forever (review M2).
func TestReviewLargeFileStaleWriteRecoversAfterReView(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	path := filepath.Join(root, "large.txt")
	payload := append(
		[]byte("UNIQUE-MARKER-LINE\n"),
		bytes.Repeat([]byte("0123456789abcdef\n"), int(readLedgerHashMaxBytes+4096)/17+1)...,
	)
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if ledgerHashScope(int64(len(payload))) != readLedgerHashSampled {
		t.Fatalf("fixture must land in the sampled tier, size=%d", len(payload))
	}
	ctx := toolctx.WithSessionID(context.Background(), "review-large-"+t.Name())
	viewTool := newViewToolAt(t, root)
	executeViewParams(t, viewTool, ctx, map[string]interface{}{"file_path": "large.txt"})

	// 外部改动：追加一行（大小变化必然被采样哈希发现）。
	appended, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := appended.WriteString("external change\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	_ = appended.Close()

	editTool := NewEditTool()
	editTool.SetBasePath(root)
	refused, err := editTool.Execute(ctx, map[string]interface{}{
		"file_path":  "large.txt",
		"old_string": "UNIQUE-MARKER-LINE",
		"new_string": "RENAMED-MARKER-LINE",
	})
	if err != nil || refused == nil || refused.Success {
		t.Fatalf("expected the sampled stale check to refuse, got err=%v result=%+v", err, refused)
	}

	// 重新 view 刷新采样哈希后，同样的 edit 必须能成功。
	executeViewParams(t, viewTool, ctx, map[string]interface{}{"file_path": "large.txt"})
	accepted, err := editTool.Execute(ctx, map[string]interface{}{
		"file_path":  "large.txt",
		"old_string": "UNIQUE-MARKER-LINE",
		"new_string": "RENAMED-MARKER-LINE",
	})
	if err != nil || accepted == nil || !accepted.Success {
		t.Fatalf("re-view must clear the sampled stale state, got err=%v result=%+v", err, accepted)
	}
}

// TestReviewUnverifiedRecordNeverHardRefuses: a file too large to hash records
// size/source only and must surface a warning instead of a refusal.
func TestReviewUnverifiedRecordHardRefusesNothing(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "huge.txt")
	if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ctx := toolctx.WithSessionID(context.Background(), "review-unverified-"+t.Name())
	ledgerForSession(toolctx.SessionID(ctx)).record(path, fileReadRecord{
		SHA256:    "",
		Size:      1 << 40,
		FullRead:  false,
		ReadAt:    time.Now(),
		Source:    "view",
		HashScope: readLedgerHashUnverifd,
	})
	verdict := evaluateStaleWrite(ctx, path, []byte("content"))
	if verdict.State != staleWriteStateUnverified {
		t.Fatalf("expected unverified state, got %q", verdict.State)
	}
	if shouldRefuseStaleWrite(verdict) {
		t.Fatalf("unverified records must not hard-refuse a write")
	}
	meta := staleWriteMetadata(verdict)
	if meta["read_before_write"] != "unverified" {
		t.Fatalf("expected an unverified warning, got %#v", meta)
	}
}

// ---------- 会话状态清理（review m8） ----------

func TestReviewSessionReadStateForgetAndSweep(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "gc.txt")
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ctx := toolctx.WithSessionID(context.Background(), "review-gc-"+t.Name())
	recordFileReadFromDisk(ctx, path, true, "view", fileReadWindow{})
	if _, ok := ledgerForSession(toolctx.SessionID(ctx)).lookup(path); !ok {
		t.Fatalf("expected a ledger record")
	}
	ForgetSessionReadState(toolctx.SessionID(ctx))
	if _, ok := ledgerForSession(toolctx.SessionID(ctx)).lookup(path); ok {
		t.Fatalf("ForgetSessionReadState must drop the ledger record")
	}

	idleSession := "review-gc-idle-" + t.Name()
	ledger := ledgerForSession(idleSession)
	ledger.mu.Lock()
	ledger.touched = time.Now().Add(-2 * sessionReadStateIdleTTL)
	ledger.mu.Unlock()
	if removed := sweepIdleSessionReadState(time.Now(), sessionReadStateIdleTTL); removed == 0 {
		t.Fatalf("expected the idle sweep to drop at least one bucket")
	}
	if _, ok := sessionReadLedgers.Load(idleSession); ok {
		t.Fatalf("idle bucket must be swept")
	}
}

// ---------- 写侧二进制判定（review m10） ----------

func TestReviewBinaryGateHandlesBOMEncodings(t *testing.T) {
	utf16Text := encodeFileText("hello\nworld\n", fileEncodingUTF16LE)
	if looksBinaryFileBytes(utf16Text) {
		t.Fatalf("a UTF-16 text file must stay editable")
	}
	utf16HeadedBinary := append([]byte{0xFF, 0xFE}, []byte{0x00, 0x00, 0x01, 0x00, 0x02, 0x00}...)
	if !looksBinaryFileBytes(utf16HeadedBinary) {
		t.Fatalf("a UTF-16-headed binary blob must be refused")
	}
	if !looksBinaryFileBytes([]byte("abc\x00def\x00\x00")) {
		t.Fatalf("the plain NUL heuristic must keep working")
	}
	if looksBinaryFileBytes([]byte("plain text\n")) {
		t.Fatalf("ordinary UTF-8 text must pass the gate")
	}
}

// ---------- 原子写与链接语义（review M4） ----------

func TestReviewAtomicWritePreservesSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable in this environment: %v", err)
	}
	if err := writeFileAtomicLocal(link, []byte("new\n"), writeFileModeDefault); err != nil {
		t.Fatalf("write through symlink: %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink must survive the write, got %v err=%v", info.Mode(), err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "new\n" {
		t.Fatalf("the link target must receive the bytes, got %q err=%v", data, err)
	}
}

func TestReviewAtomicWritePreservesHardLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("link count is not exposed through os.FileInfo on Windows")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	hard := filepath.Join(root, "hard.txt")
	if err := os.Link(target, hard); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := writeFileAtomicLocal(target, []byte("shared\n"), writeFileModeDefault); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, path := range []string{target, hard} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "shared\n" {
			t.Fatalf("hard link %s must see the new bytes, got %q err=%v", path, data, err)
		}
	}
	info, err := os.Stat(target)
	if err != nil || !hasMultipleHardLinks(info) {
		t.Fatalf("the hard-link set must not be split: %+v err=%v", info, err)
	}
}

// ---------- tail 窗口语义（review m11） ----------

func TestReviewTailWindowHonorsLimitAndPublishesResolvedOffset(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	var builder strings.Builder
	for line := 1; line <= 30; line++ {
		fmt.Fprintf(&builder, "line-%02d\n", line)
	}
	if err := os.WriteFile(filepath.Join(root, "tail.txt"), []byte(builder.String()), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{
		"file_path": "tail.txt",
		"offset":    float64(-25),
		"limit":     float64(10),
	})
	if result.Metadata["offset_resolved"] != 20 {
		t.Fatalf("expected offset_resolved=20, got %#v", result.Metadata["offset_resolved"])
	}
	if result.Metadata["tail_lines_dropped_by_limit"] != 15 {
		t.Fatalf("expected 15 lines dropped by the limit, got %#v", result.Metadata["tail_lines_dropped_by_limit"])
	}
	if result.Metadata["suggested_earlier_offset"] != 0 {
		t.Fatalf("expected an earlier-offset hint, got %#v", result.Metadata["suggested_earlier_offset"])
	}
	if !strings.Contains(result.Content, "line-30") || strings.Contains(result.Content, "line-19") {
		t.Fatalf("the window must be the newest 10 lines, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "for earlier content") {
		t.Fatalf("expected the continuation advisory, got %q", result.Content)
	}

	clamped := executeViewParams(t, tool, context.Background(), map[string]interface{}{
		"file_path": "tail.txt",
		"offset":    float64(-999999),
	})
	if clamped.Metadata["tail_clamped"] != true {
		t.Fatalf("an over-limit tail request must be marked clamped, got %#v", clamped.Metadata)
	}
}

// ---------- 派生渲染的读侧记账（review F11） ----------

func TestReviewNotebookReadFeedsLedgerAndDedup(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ledger.ipynb")
	writeNotebook(t, path, []map[string]interface{}{
		{"cell_type": "code", "source": "print(1)\n"},
	})
	ctx := toolctx.WithSessionID(context.Background(), "review-nb-"+t.Name())
	tool := newViewToolAt(t, root)
	executeViewParams(t, tool, ctx, map[string]interface{}{"file_path": "ledger.ipynb"})
	if !sessionHasFullRead(ctx, path) {
		t.Fatalf("a full notebook render must record a ledger entry")
	}
	second := executeViewParams(t, tool, ctx, map[string]interface{}{"file_path": "ledger.ipynb"})
	if second.Metadata["dedup_hit"] != true {
		t.Fatalf("an identical notebook re-read must be stubbed, got %#v", second.Metadata)
	}
}

func TestReviewNotebookDegradedWhenOutputsOmitted(t *testing.T) {
	root := t.TempDir()
	writeNotebook(t, filepath.Join(root, "big.ipynb"), []map[string]interface{}{
		{"cell_type": "code", "source": "run()", "outputs": []map[string]interface{}{
			{"output_type": "stream", "text": strings.Repeat("x", 10001)},
		}},
	})
	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "big.ipynb"})
	if viewMetadataInt(result.Metadata, "notebook_outputs_omitted", 0) < 1 {
		t.Fatalf("expected the oversized output to be counted, got %#v", result.Metadata)
	}
	if result.Metadata["doc_degraded"] != true {
		t.Fatalf("omitted outputs must mark the render degraded, got %#v", result.Metadata)
	}
}

// TestReviewDerivedRenderPublishesByteBudgetFlag: the notebook/document window
// metadata must expose the byte-budget stop just like the text path (review F7).
func TestReviewDerivedRenderPublishesByteBudgetFlag(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	var builder strings.Builder
	for line := 0; line < 1400; line++ {
		fmt.Fprintf(&builder, "%05d %s\n", line, strings.Repeat("x", 50))
	}
	writeNotebook(t, filepath.Join(root, "bulk.ipynb"), []map[string]interface{}{
		{"cell_type": "code", "source": builder.String()},
	})
	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{
		"file_path": "bulk.ipynb",
		"limit":     float64(2000),
	})
	if result.Metadata["byte_budget_applied"] != true {
		t.Fatalf("expected byte_budget_applied for an oversized derived window, got %#v", result.Metadata)
	}
	if result.Metadata["is_truncated"] != true {
		t.Fatalf("expected is_truncated alongside the budget stop, got %#v", result.Metadata)
	}
	// 字节预算停止时行数未知（与文本路径一致），但续读路径必须可用。
	if next, ok := result.Metadata["suggested_next_offset"].(int); !ok || next <= 0 {
		t.Fatalf("expected a continuation offset for the truncated derived window, got %#v", result.Metadata)
	}
}
