package baseline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFixture 写入与 scripts/analyze-lsp-baseline.py --selftest 同构的样例：
// 两侧实现必须给出同一组数字（3 请求 / P50 10ms / 覆盖率 1.0 / 追加比 100:1100）。
func writeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "2026", "09", "29", "sess_a", "events")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	lines := []string{
		`{"type":"lsp.request.finished","session_id":"s1","timestamp":"2026-09-29T10:00:00Z","payload":{"trigger":"inline","outcome":"injected","duration_ms":10,"diag_count":2,"appended_bytes":100,"server":"gopls"}}`,
		`{"type":"lsp.request.finished","session_id":"s1","timestamp":"2026-09-29T10:01:00Z","payload":{"trigger":"inline","outcome":"degraded_no_fresh","duration_ms":30}}`,
		`{"type":"lsp.request.finished","session_id":"s2","timestamp":"2026-09-29T10:02:00Z","payload":{"trigger":"tool","outcome":"no_server","duration_ms":5}}`,
		`{"type":"tool.completed","session_id":"s1","timestamp":"2026-09-29T10:00:00Z","payload":{"logical_tool":"apply_patch","output_model_visible_bytes":1000}}`,
		`{"type":"tool.completed","session_id":"s1","timestamp":"2026-09-29T10:01:00Z","payload":{"logical_tool":"write","output_model_visible_bytes":100}}`,
		`{"type":"tool.completed","session_id":"s1","timestamp":"2026-09-29T10:01:30Z","payload":{"logical_tool":"grep"}}`,
		``,                                     // 空行：跳过且不计损坏
		`{"type":"tool.completed","payload":{`, // 命中预筛但 JSON 损坏：计 malformed
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime-events.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return root
}

func TestAnalyzeFixtureMatchesScriptNumbers(t *testing.T) {
	root := writeFixture(t)
	stats, err := Analyze(Options{Roots: []string{root}})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if stats.Requests != 3 || stats.Sessions != 2 {
		t.Fatalf("requests/sessions = %d/%d, want 3/2", stats.Requests, stats.Sessions)
	}
	if stats.Triggers["inline"] != 2 || stats.Triggers["tool"] != 1 {
		t.Fatalf("triggers = %#v", stats.Triggers)
	}
	if stats.Injected != 1 || stats.DiagHit != 1 || stats.Degraded != 1 || stats.NoServer != 1 {
		t.Fatalf("outcome counters = injected %d hit %d degraded %d no_server %d",
			stats.Injected, stats.DiagHit, stats.Degraded, stats.NoServer)
	}
	if stats.LatencyP50MS == nil || *stats.LatencyP50MS != 10 {
		t.Fatalf("p50 = %v, want 10", stats.LatencyP50MS)
	}
	if stats.LatencyP95MS == nil || *stats.LatencyP95MS != 30 {
		t.Fatalf("p95 = %v, want 30", stats.LatencyP95MS)
	}
	if stats.EditCalls != 2 || stats.ActiveEdit != 2 {
		t.Fatalf("edit calls = %d/%d, want 2/2", stats.EditCalls, stats.ActiveEdit)
	}
	if stats.ActiveOutput != 1100 {
		t.Fatalf("active output = %d, want 1100", stats.ActiveOutput)
	}
	if stats.Scan.Files != 1 || stats.Scan.Malformed != 1 {
		t.Fatalf("scan = %#v, want files=1 malformed=1", stats.Scan)
	}

	rows := map[string]Row{}
	for _, row := range Rows(stats) {
		rows[row.Metric] = row
	}
	if got := rows["lsp_edit_coverage_ratio"].Value; got != "1.0000" {
		t.Fatalf("coverage = %q, want 1.0000", got)
	}
	if got := rows["lsp_append_bytes_ratio"].Value; got != "0.0909" {
		t.Fatalf("append ratio = %q, want 0.0909", got)
	}
	if !strings.HasPrefix(rows["lsp_closure_ratio"].Value, "n/a") {
		t.Fatalf("closure must be honest n/a, got %q", rows["lsp_closure_ratio"].Value)
	}
	if report := RenderMarkdown(stats); !strings.Contains(report, "§4.3 基线登记表") {
		t.Fatalf("report missing table header")
	}
}

func TestAnalyzeWindowAndEmptyState(t *testing.T) {
	root := writeFixture(t)
	// 窗口只保留 10:02 之后的事件：只剩 1 条（tool/no_server）；被窗口挡掉的
	// 两类事件（3 条 request + 2 条编辑回执）都计入 skipped_old。
	stats, err := Analyze(Options{Roots: []string{root}, Since: time.Date(2026, 9, 29, 10, 2, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if stats.Requests != 1 || stats.Scan.SkippedOld != 5 {
		t.Fatalf("window requests/skipped = %d/%d, want 1/5", stats.Requests, stats.Scan.SkippedOld)
	}
	// 无 LSP 活跃会话时覆盖率不得渲染成 0（§4.3 反模式）。
	empty, err := Analyze(Options{Roots: []string{filepath.Join(root, "missing")}})
	if err != nil {
		t.Fatalf("analyze empty: %v", err)
	}
	rows := map[string]Row{}
	for _, row := range Rows(empty) {
		rows[row.Metric] = row
	}
	if got := rows["lsp_edit_coverage_ratio"].Value; !strings.HasPrefix(got, "n/a") {
		t.Fatalf("empty coverage = %q, want n/a", got)
	}
	if got := rows["lsp_fallback_ratio"].Value; !strings.HasPrefix(got, "n/a") {
		t.Fatalf("empty fallback = %q, want n/a", got)
	}
	if empty.Sessions != 0 || empty.Requests != 0 {
		t.Fatalf("empty stats = %#v", empty)
	}
}

// TestAnalyzeSkipsFilesByModTime 覆盖窗口快速路径：最后写入早于窗口的文件
// 整文件跳过，不打开、不计行。
func TestAnalyzeSkipsFilesByModTime(t *testing.T) {
	root := writeFixture(t)
	old := time.Now().AddDate(0, 0, -30)
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		return os.Chtimes(path, old, old)
	}); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	stats, err := Analyze(Options{Roots: []string{root}, Since: time.Now().AddDate(0, 0, -14)})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if stats.Scan.SkippedFiles != 1 || stats.Scan.Files != 0 || stats.Scan.Lines != 0 {
		t.Fatalf("scan = %#v, want one file skipped before reading", stats.Scan)
	}
	if stats.Requests != 0 || stats.Sessions != 0 {
		t.Fatalf("stats = %#v, want empty window", stats)
	}
}

// TestAnalyzeRealRootBenchmark 只在显式给出真实 chat-logs 根时运行：
//
//	AICLI_LSP_BASELINE_ROOT=%USERPROFILE%\.aicli\chat-logs go test ./internal/lsp/baseline/ -run RealRoot -v
//
// 用于确认默认 14 天窗口在真实库上的耗时（按 mtime 整文件跳过是主要收益）。
func TestAnalyzeRealRootBenchmark(t *testing.T) {
	root := strings.TrimSpace(os.Getenv("AICLI_LSP_BASELINE_ROOT"))
	if root == "" {
		t.Skip("set AICLI_LSP_BASELINE_ROOT to scan a real chat-logs root")
	}
	start := time.Now()
	stats, err := Analyze(Options{Roots: []string{root}, Since: time.Now().AddDate(0, 0, -14)})
	if err != nil {
		t.Fatalf("analyze real root: %v", err)
	}
	t.Logf("files=%d skipped_files=%d lines=%d requests=%d elapsed=%s",
		stats.Scan.Files, stats.Scan.SkippedFiles, stats.Scan.Lines, stats.Requests, time.Since(start))
}
