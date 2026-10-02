package baseline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFixture 写入与 scripts/analyze-lsp-baseline.py --selftest 同构的样例：
// 两侧实现必须给出同一组数字（4 请求 / P50 7ms / 覆盖率 1.0 / 追加比 100:1100 /
// closure 1.0）。
func writeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "2026", "09", "29", "sess_a", "events")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	lines := []string{
		`{"type":"lsp.request.finished","session_id":"s1","timestamp":"2026-09-29T10:00:00Z","payload":{"trigger":"inline","outcome":"injected","duration_ms":10,"diag_count":2,"total_diag_count":3,"new_diag_count":2,"appended_bytes":100,"appended_diag_bytes":80,"appended_note_bytes":10,"appended_empty_bytes":10,"attempted_members":2,"server":"gopls","path_fingerprint":"p1","diag_fingerprint":"d1"}}`,
		`{"type":"lsp.request.finished","session_id":"s1","timestamp":"2026-09-29T10:02:00Z","payload":{"trigger":"tool","outcome":"clean","duration_ms":7,"path_fingerprint":"p1"}}`,
		`{"type":"lsp.request.finished","session_id":"s1","timestamp":"2026-09-29T10:01:00Z","payload":{"trigger":"inline","outcome":"degraded_no_fresh","duration_ms":30,"reason_category":"wait_timeout","cold_fast_fail":false}}`,
		`{"type":"lsp.request.finished","session_id":"s2","timestamp":"2026-09-29T10:02:00Z","payload":{"trigger":"tool","outcome":"no_server","duration_ms":5}}`,
		`{"type":"lsp.server.state","session_id":"s1","timestamp":"2026-09-29T10:00:00Z","payload":{"server":"gopls","state":"ready","pid":42,"first_publish_ms":1234}}`,
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
	if stats.Requests != 4 || stats.Sessions != 2 {
		t.Fatalf("requests/sessions = %d/%d, want 4/2", stats.Requests, stats.Sessions)
	}
	if stats.Triggers["inline"] != 2 || stats.Triggers["tool"] != 2 {
		t.Fatalf("triggers = %#v", stats.Triggers)
	}
	if stats.Injected != 1 || stats.DiagHit != 1 || stats.Degraded != 1 || stats.NoServer != 1 {
		t.Fatalf("outcome counters = injected %d hit %d degraded %d no_server %d",
			stats.Injected, stats.DiagHit, stats.Degraded, stats.NoServer)
	}
	if stats.LatencyP50MS == nil || *stats.LatencyP50MS != 7 {
		t.Fatalf("p50 = %v, want 7", stats.LatencyP50MS)
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
	// fallback 分母是 attempted（requests - no_server）：1/3，而不是 1/4。
	if got := rows["lsp_fallback_ratio"].Value; got != "0.3333" {
		t.Fatalf("fallback = %q, want 0.3333（attempted 分母）", got)
	}
	if stats.ClosureEligible != 1 || stats.ClosureClosed != 1 {
		t.Fatalf("closure counters = %d/%d, want 1/1", stats.ClosureEligible, stats.ClosureClosed)
	}
	if got := rows["lsp_closure_ratio"].Value; got != "1.0000" {
		t.Fatalf("closure = %q, want 1.0000", got)
	}
	if got := stats.DegradeReasons["wait_timeout"]; got != 1 {
		t.Fatalf("degrade_reasons[wait_timeout] = %d, want 1", got)
	}
	if stats.AppendedDiagBytes != 80 || stats.AppendedNoteBytes != 10 || stats.AppendedEmptyBytes != 10 {
		t.Fatalf("append breakdown = %d/%d/%d, want 80/10/10",
			stats.AppendedDiagBytes, stats.AppendedNoteBytes, stats.AppendedEmptyBytes)
	}
	if stats.ColdFirstProbe != 1 || stats.ColdRepeat != 0 {
		t.Fatalf("cold probe split = %d/%d, want 1/0", stats.ColdFirstProbe, stats.ColdRepeat)
	}
	if stats.MultiMemberRequests != 1 || stats.AttemptedMembersMax != 2 {
		t.Fatalf("multi-member = %d/max %d, want 1/2", stats.MultiMemberRequests, stats.AttemptedMembersMax)
	}
	if stats.TotalDiagCount != 3 || stats.NewDiagCount != 2 {
		t.Fatalf("diag new/total = %d/%d, want 2/3", stats.NewDiagCount, stats.TotalDiagCount)
	}
	if got := rows["lsp_diag_new_ratio"].Value; got != "0.6667" {
		t.Fatalf("diag new ratio = %q, want 0.6667", got)
	}
	if got := rows["lsp_cold_first_probe_ratio"].Value; got != "1.0000" {
		t.Fatalf("cold first probe ratio = %q, want 1.0000", got)
	}
	if got := rows["lsp_append_bytes_ratio"].Samples; !strings.Contains(got, "诊断 80") {
		t.Fatalf("append samples missing breakdown: %q", got)
	}
	if stats.ColdFirstPublishP50MS == nil || *stats.ColdFirstPublishP50MS != 1234 {
		t.Fatalf("cold first publish p50 = %v, want 1234", stats.ColdFirstPublishP50MS)
	}
	if got := rows["lsp_cold_first_publish_p95"].Value; got != "1234 ms" {
		t.Fatalf("cold first publish p95 row = %q, want 1234 ms", got)
	}
	if report := RenderMarkdown(stats); !strings.Contains(report, "§4.3 基线登记表") ||
		!strings.Contains(report, "冷路径探针（O4）") || !strings.Contains(report, "多成员请求（O5）") ||
		!strings.Contains(report, "诊断新旧构成（O9/A6）") {
		t.Fatalf("report missing table header or O4/O5 detail lines")
	}
}

// TestColdProbeRowStates pins the n/a vs ratio states of the O4 split row.
func TestColdProbeRowStates(t *testing.T) {
	byMetric := func(stats Stats) map[string]Row {
		rows := map[string]Row{}
		for _, row := range Rows(stats) {
			rows[row.Metric] = row
		}
		return rows
	}
	if got := byMetric(Stats{})["lsp_cold_first_probe_ratio"].Value; !strings.HasPrefix(got, "n/a") {
		t.Fatalf("empty cold probe row = %q, want n/a", got)
	}
	row := byMetric(Stats{ColdFirstProbe: 3, ColdRepeat: 1})["lsp_cold_first_probe_ratio"]
	if row.Value != "0.7500" || !strings.Contains(row.Samples, "first 3 / repeat 1") {
		t.Fatalf("mixed cold probe row = %#v", row)
	}
}

// TestColdProbeIgnoresUnclassifiedNoFresh pins that legacy no_fresh events
// (without the cold_fast_fail field) do not inflate the first-probe counter.
func TestColdProbeIgnoresUnclassifiedNoFresh(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "2026", "10", "01", "sess", "events")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	line := `{"type":"lsp.request.finished","session_id":"s1","timestamp":"2026-10-01T00:00:00Z","payload":{"trigger":"inline","outcome":"degraded_no_fresh","duration_ms":1000}}`
	if err := os.WriteFile(filepath.Join(dir, "runtime-events.jsonl"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	stats, err := Analyze(Options{Roots: []string{root}})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if stats.ColdFirstProbe != 0 || stats.ColdRepeat != 0 {
		t.Fatalf("unclassified no_fresh must not count: first=%d repeat=%d",
			stats.ColdFirstProbe, stats.ColdRepeat)
	}
	rows := map[string]Row{}
	for _, row := range Rows(stats) {
		rows[row.Metric] = row
	}
	if got := rows["lsp_cold_first_probe_ratio"].Value; !strings.HasPrefix(got, "n/a") {
		t.Fatalf("row = %q, want n/a for an unclassified window", got)
	}
}

func TestAnalyzeWindowAndEmptyState(t *testing.T) {
	root := writeFixture(t)
	// 窗口只保留 10:02 之后的事件：剩 2 条（clean + tool/no_server）；被窗口
	// 挡掉的事件（2 条 request + 3 条工具回执 + 1 条冷启动状态事件）都计入
	// skipped_old。
	stats, err := Analyze(Options{Roots: []string{root}, Since: time.Date(2026, 9, 29, 10, 2, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if stats.Requests != 2 || stats.Scan.SkippedOld != 6 {
		t.Fatalf("window requests/skipped = %d/%d, want 2/6", stats.Requests, stats.Scan.SkippedOld)
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

// writeMultiFileFixture 造一个跨多文件、且**故意制造跨文件并列时间戳**的库：
//
//   - 40 个文件分布在多层目录，文件名排序与"事件时间"刻意相反，逼出遍历顺序；
//   - s1 的同一 path_fingerprint 在多个文件里出现**完全相同**的时间戳——
//     这是 closure 判定里 sort.SliceStable(ts) 唯一能暴露顺序差异的场景；
//   - 同一个 (session, server) 的 first_publish_ms 在不同文件给出不同值，
//     用来验证"取最早"在并行归并后仍然成立；
//   - 混入空行与损坏行，确认 malformed/lines 口径不变。
func writeMultiFileFixture(t *testing.T, files int) string {
	t.Helper()
	root := t.TempDir()
	for index := 0; index < files; index++ {
		// 目录名逆序铺开：WalkDir 的字典序 index 递增，与事件时间递减相反。
		dir := filepath.Join(root, "d", fmt.Sprintf("%02d", files-index), "sess", "events")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		minute := 40 - index%20
		lines := []string{
			// 与文件 0 完全相同的时间戳（跨文件并列）：稳定排序下谁的顺序先到谁算前一条。
			fmt.Sprintf(`{"type":"lsp.request.finished","session_id":"s1","timestamp":"2026-09-29T10:00:00Z","payload":{"trigger":"inline","outcome":"injected","duration_ms":%d,"diag_count":1,"diag_fingerprint":"d%[1]d","path_fingerprint":"p1","appended_bytes":%[1]d}}`, 100+index),
			// 同 session 同文件指纹、较晚时间戳的 clean：只有紧跟在 injected 之后才闭合。
			fmt.Sprintf(`{"type":"lsp.request.finished","session_id":"s1","timestamp":"2026-09-29T10:%02d:00Z","payload":{"trigger":"tool","outcome":"clean","duration_ms":%d,"path_fingerprint":"p1"}}`, minute, 5+index),
			fmt.Sprintf(`{"type":"lsp.request.finished","session_id":"s2","timestamp":"2026-09-29T10:%02d:00Z","payload":{"trigger":"tool","outcome":"degraded_slow","duration_ms":%d,"reason_category":"wait_timeout","attempted_members":%d}}`, minute, 900+index, index%3+1),
			fmt.Sprintf(`{"type":"lsp.server.state","session_id":"s1","timestamp":"2026-09-29T09:%02d:00Z","payload":{"server":"gopls","state":"ready","first_publish_ms":%d}}`, minute, 1000+index),
			`{"type":"tool.completed","session_id":"s1","timestamp":"2026-09-29T09:00:00Z","payload":{"logical_tool":"apply_patch","output_model_visible_bytes":1000}}`,
			"",
			`{"type":"tool.completed","payload":{`,
		}
		path := filepath.Join(dir, "runtime-events.jsonl")
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatalf("write fixture %d: %v", index, err)
		}
	}
	return root
}

// TestAnalyzeParallelMatchesSerial 是并行改造的核心护栏：同一份库，worker=1
// （退化串行）与默认 pool（4..16 并发）的 Stats 必须**完全一致**。
//
// 比对走 JSON 序列化：Stats 的 map 键由 encoding/json 排序输出，序列化结果是
// 稳定的，所以字节相等就等价于"每一个数字、每一个 map、每一条 ByDay 明细都相等"。
// 夹具里的跨文件并列时间戳专门盯 closure 的稳定排序口径。
func TestAnalyzeParallelMatchesSerial(t *testing.T) {
	// 关掉增量索引：本测试要比较的变量是「worker 数」，而账本命中会让第二次
	// 扫描改走复用路径，把「串行 vs 并发」和「冷 vs 热」两件事搅在一起。索引的
	// 等价性由 incremental_test.go 单独证明。
	t.Setenv("AICLI_LSP_BASELINE_INDEX", "off")
	root := writeMultiFileFixture(t, 40)
	opts := Options{Roots: []string{root}, Now: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)}

	scanWorkerOverride = 1
	serial, err := Analyze(opts)
	scanWorkerOverride = 0
	if err != nil {
		t.Fatalf("serial analyze: %v", err)
	}
	parallel, err := Analyze(opts)
	if err != nil {
		t.Fatalf("parallel analyze: %v", err)
	}

	serialJSON, err := json.Marshal(serial)
	if err != nil {
		t.Fatalf("marshal serial: %v", err)
	}
	parallelJSON, err := json.Marshal(parallel)
	if err != nil {
		t.Fatalf("marshal parallel: %v", err)
	}
	if string(serialJSON) != string(parallelJSON) {
		t.Fatalf("parallel output differs from serial:\n serial=%s\nparallel=%s", serialJSON, parallelJSON)
	}
	// 夹具必须真的产生非平凡聚合，否则"相等"没有说服力。
	if serial.Scan.Files != 40 || serial.Scan.Malformed != 40 || serial.Scan.Lines != 240 {
		t.Fatalf("fixture scan = %#v, want files=40 malformed=40 lines=240", serial.Scan)
	}
	if serial.Requests != 120 || serial.Sessions != 2 {
		t.Fatalf("fixture requests/sessions = %d/%d, want 120/2", serial.Requests, serial.Sessions)
	}
	if len(serial.ByDay) != 1 {
		t.Fatalf("fixture by_day = %#v, want a single day", serial.ByDay)
	}
}

// TestAnalyzeParallelDeterministicAcrossRuns 反复跑并行路径，要求结果稳定
// （不为 true 就不可能：一旦有 goroutine 调度依赖的累加，这里会随机翻车）。
func TestAnalyzeParallelDeterministicAcrossRuns(t *testing.T) {
	// 同上：本测试盯的是调度无关性，账本命中会让后续几轮走复用路径，测的不再
	// 是同一条代码路径。
	t.Setenv("AICLI_LSP_BASELINE_INDEX", "off")
	root := writeMultiFileFixture(t, 24)
	opts := Options{Roots: []string{root}, Now: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)}
	first, err := Analyze(opts)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	want, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for round := 0; round < 5; round++ {
		again, err := Analyze(opts)
		if err != nil {
			t.Fatalf("analyze round %d: %v", round, err)
		}
		got, err := json.Marshal(again)
		if err != nil {
			t.Fatalf("marshal round %d: %v", round, err)
		}
		if string(got) != string(want) {
			t.Fatalf("round %d differs:\n want=%s\n got=%s", round, want, got)
		}
	}
}

// TestScanOneFileUnreadableIsNonFatal 钉住"单文件读取失败不中断整次统计"：
// 打不开的路径不得被计入 Scan.Files。
func TestScanOneFileUnreadableIsNonFatal(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing", "runtime-events.jsonl")
	outcome := scanFile(missing, 0, scanOutcome{}, time.Time{}, 0)
	if outcome.opened || outcome.lines != 0 || len(outcome.requests) != 0 {
		t.Fatalf("unreadable file outcome = %+v, want zero-value non-fatal result", outcome)
	}
}

// TestScanWorkerCountBounds 覆盖 worker 档位：夹在 [4,16] 且不超过文件数。
func TestScanWorkerCountBounds(t *testing.T) {
	scanWorkerOverride = 0
	if got := scanWorkerCount(0); got < 4 || got > 16 {
		t.Fatalf("scanWorkerCount(0) = %d, want within [4,16]", got)
	}
	if got := scanWorkerCount(2); got != 2 {
		t.Fatalf("scanWorkerCount(2) = %d, want capped at file count 2", got)
	}
	if got := scanWorkerCount(1000); got < 4 || got > 16 {
		t.Fatalf("scanWorkerCount(1000) = %d, want within [4,16]", got)
	}
}
