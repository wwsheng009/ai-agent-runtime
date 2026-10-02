package baseline

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// TestMain 把账本整体重定向到临时目录。包内每个测试都用 t.TempDir() 当日志根，
// 若放任 Analyze 写进真实的 ~/.aicli/cache，就会把用户那套 2.3k 文件的账本
// 换成临时目录的测试数据——既污染用户环境，也让本地手工验证失去参照。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "lsp-baseline-test-index")
	if err != nil {
		panic(err)
	}
	indexPathOverride = filepath.Join(dir, indexFileName)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// withIndexPath 把账本指向某个具体文件，并返回一个恢复函数。它让每个增量索引
// 用例拥有自己的账本，从而可以断言「这一轮到底读了什么」。
func withIndexPath(t *testing.T, path string) {
	t.Helper()
	previous := indexPathOverride
	indexPathOverride = path
	t.Cleanup(func() { indexPathOverride = previous })
}

// eventLine 造一条可被预筛放行的 lsp.request.finished 记录。
func eventLine(t *testing.T, session string, ts time.Time, trigger, outcome string, durationMS int) string {
	t.Helper()
	return `{"type":"lsp.request.finished","session_id":"` + session +
		`","timestamp":"` + ts.UTC().Format(time.RFC3339) +
		`","payload":{"trigger":"` + trigger + `","outcome":"` + outcome +
		`","duration_ms":` + strconv.Itoa(durationMS) + `,"server":"gopls"}}`
}

// TestIndexRoundTripPreservesOutcome 钉住账目的往返不变式：把一个文件的全部事实
// 序列化成账目再还原，必须与原对象逐字段相同。
//
// 这是增量索引最核心的一条不变量——账目还原出来的东西一旦有偏差，索引命中就会
// 悄悄改口径，而且不会有任何报错，只是数字慢慢变得不对。
func TestIndexRoundTripPreservesOutcome(t *testing.T) {
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	original := scanOutcome{
		opened:              true,
		lines:               42,
		malformed:           2,
		skippedOld:          1,
		editCalls:           3,
		editOutput:          999,
		editOutputEvents:    2,
		requests:            []requestFact{{sessionID: "s1", day: "2026-10-01", trigger: "inline", outcome: "injected", durationMS: 11, server: "gopls", ts: base, pathFingerprint: "p1", coldProbeClassified: true, coldFastFail: true, attemptedMembers: 3, appendedBytes: 100, totalDiagCount: 9}},
		timestamps:          []time.Time{base, base.Add(time.Minute)},
		sessionRequests:     map[string]int{"s1": 1, "s2": 2},
		editCallsBySession:  map[string]int{"s1": 3},
		editOutputBySession: map[string]int{"s1": 999},
		coldFirst:           map[string]coldFact{"s1\x00gopls": {ts: base, ms: 1234}},
	}
	restored := newFactRecord(original).toOutcome()
	if restored.lines != original.lines || restored.malformed != original.malformed ||
		restored.skippedOld != original.skippedOld || restored.editCalls != original.editCalls ||
		restored.editOutput != original.editOutput || restored.editOutputEvents != original.editOutputEvents {
		t.Fatalf("scalar round-trip lost data: %+v", restored)
	}
	if len(restored.requests) != 1 || restored.requests[0] != original.requests[0] {
		t.Fatalf("request fact round-trip mismatch:\n got %+v\nwant %+v", restored.requests, original.requests)
	}
	if len(restored.timestamps) != 2 || !restored.timestamps[0].Equal(base) {
		t.Fatalf("timestamps round-trip mismatch: %+v", restored.timestamps)
	}
	if restored.sessionRequests["s2"] != 2 || restored.editCallsBySession["s1"] != 3 ||
		restored.editOutputBySession["s1"] != 999 {
		t.Fatalf("map round-trip mismatch: %+v", restored)
	}
	if got := restored.coldFirst["s1\x00gopls"]; got.ms != 1234 || !got.ts.Equal(base) {
		t.Fatalf("coldFirst round-trip mismatch: %+v", got)
	}
}

// TestIncrementalScanMatchesFullRescan 是本次改动的**总证明**：在同一份日志上，
// 「冷索引全量扫」与「跑过索引、复用/增量后再扫」的 Stats 必须逐字节相同。
func TestIncrementalScanMatchesFullRescan(t *testing.T) {
	withIndexPath(t, filepath.Join(t.TempDir(), indexFileName))
	root := t.TempDir()
	eventPath := filepath.Join(root, "2026", "10", "01", "s1", "events", "runtime-events.jsonl")
	if err := os.MkdirAll(filepath.Dir(eventPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

	// 第一次：建账本。
	first := []string{
		eventLine(t, "s1", base, "inline", "injected", 10),
		eventLine(t, "s1", base.Add(time.Minute), "tool", "clean", 7),
		`{"type":"tool.completed","session_id":"s1","timestamp":"` + base.Format(time.RFC3339) + `","payload":{"logical_tool":"apply_patch","output_model_visible_bytes":1000}}`,
		"", // 空行
	}
	writeLines(t, eventPath, first)
	cold, err := Analyze(Options{Roots: []string{root}})
	if err != nil {
		t.Fatalf("cold analyze: %v", err)
	}
	if cold.Scan.ReusedFiles != 0 || cold.Scan.DeltaFiles != 0 {
		t.Fatalf("cold scan reported reuse: %+v", cold.Scan)
	}

	// 第二次：文件没动 → 应全部复用，且数字与冷扫完全一致。
	warm, err := Analyze(Options{Roots: []string{root}})
	if err != nil {
		t.Fatalf("warm analyze: %v", err)
	}
	if warm.Scan.ReusedFiles != 1 || warm.Scan.DeltaFiles != 0 {
		t.Fatalf("warm scan = %+v, want 1 reused / 0 delta", warm.Scan)
	}
	assertStatsEqual(t, cold, warm)

	// 第三次：追加新事件 → 应走增量，且结果与「清掉账本重扫」完全一致。
	appendLines(t, eventPath, []string{
		eventLine(t, "s1", base.Add(2*time.Minute), "inline", "degraded_no_fresh", 30),
		`{"type":"tool.completed","session_id":"s1","timestamp":"` + base.Format(time.RFC3339) + `","payload":{"logical_tool":"write","output_model_visible_bytes":100}}`,
	})
	incremental, err := Analyze(Options{Roots: []string{root}})
	if err != nil {
		t.Fatalf("incremental analyze: %v", err)
	}
	if incremental.Scan.ReusedFiles != 0 || incremental.Scan.DeltaFiles != 1 {
		t.Fatalf("incremental scan = %+v, want 0 reused / 1 delta", incremental.Scan)
	}
	withIndexPath(t, filepath.Join(t.TempDir(), indexFileName)) // 丢弃账本 → 强制全量
	fromScratch, err := Analyze(Options{Roots: []string{root}})
	if err != nil {
		t.Fatalf("from-scratch analyze: %v", err)
	}
	assertStatsEqual(t, fromScratch, incremental)
	if fromScratch.Requests != 3 {
		t.Fatalf("requests = %d, want 3 after append", fromScratch.Requests)
	}
}

// TestIncrementalTornTailIsNotDoubleCounted 覆盖「文件正被追加、尾部半行」：
// 半行既不能被计数，也不能被写进账目的 offset（否则下一轮会把它连同补全后的
// 内容重复计一次）。
func TestIncrementalTornTailIsNotDoubleCounted(t *testing.T) {
	withIndexPath(t, filepath.Join(t.TempDir(), indexFileName))
	root := t.TempDir()
	eventPath := filepath.Join(root, "s1", "events", "runtime-events.jsonl")
	if err := os.MkdirAll(filepath.Dir(eventPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	complete := eventLine(t, "s1", base, "inline", "injected", 10)
	torn := eventLine(t, "s1", base.Add(time.Minute), "tool", "clean", 7)

	// 只写完整行 + 半行（无换行结尾）。
	raw := complete + "\n" + torn
	if err := os.WriteFile(eventPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	first, err := Analyze(Options{Roots: []string{root}})
	if err != nil {
		t.Fatalf("analyze torn: %v", err)
	}
	if first.Requests != 1 {
		t.Fatalf("torn scan requests = %d, want 1 (half-line must not be counted)", first.Requests)
	}

	// 补上换行，半行变成完整行：这一轮必须把它计进去，且只能计一次。
	if err := os.WriteFile(eventPath, []byte(raw+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	second, err := Analyze(Options{Roots: []string{root}})
	if err != nil {
		t.Fatalf("analyze completed: %v", err)
	}
	if second.Requests != 2 {
		t.Fatalf("completed scan requests = %d, want 2 (half-line must count exactly once)", second.Requests)
	}
	if second.Scan.DeltaFiles != 1 {
		t.Fatalf("completed scan = %+v, want a delta read", second.Scan)
	}
}

// TestIncrementalTruncationForcesFullRescan 覆盖轮转/截断：文件变小后不能拿旧
// offset 去续读（seek 会落在文件外或读出错误的切片），必须整文件重解析。
func TestIncrementalTruncationForcesFullRescan(t *testing.T) {
	withIndexPath(t, filepath.Join(t.TempDir(), indexFileName))
	root := t.TempDir()
	eventPath := filepath.Join(root, "s1", "events", "runtime-events.jsonl")
	if err := os.MkdirAll(filepath.Dir(eventPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	writeLines(t, eventPath, []string{
		eventLine(t, "s1", base, "inline", "injected", 10),
		eventLine(t, "s1", base.Add(time.Minute), "tool", "clean", 7),
		eventLine(t, "s1", base.Add(2*time.Minute), "inline", "no_server", 5),
	})
	if _, err := Analyze(Options{Roots: []string{root}}); err != nil {
		t.Fatalf("initial analyze: %v", err)
	}

	// 截断重写：内容与长度都与账目不符。
	writeLines(t, eventPath, []string{eventLine(t, "s1", base, "inline", "injected", 10)})
	truncated, err := Analyze(Options{Roots: []string{root}})
	if err != nil {
		t.Fatalf("truncated analyze: %v", err)
	}
	if truncated.Scan.ReusedFiles != 0 || truncated.Scan.DeltaFiles != 0 {
		t.Fatalf("truncated scan = %+v, want a full rescan", truncated.Scan)
	}
	if truncated.Requests != 1 {
		t.Fatalf("truncated requests = %d, want 1 (old tail must not survive)", truncated.Requests)
	}
}

// TestIndexIsNeverWrittenForWindowedScans 钉住「窗口查询不入账」：窗口扫描的
// 事实是按 since 裁剪过的，写进账本会让下一次全窗口扫描复用到被裁剪的数字。
func TestIndexIsNeverWrittenForWindowedScans(t *testing.T) {
	indexFile := filepath.Join(t.TempDir(), indexFileName)
	withIndexPath(t, indexFile)
	root := writeFixture(t)

	if _, err := Analyze(Options{Roots: []string{root}, Since: time.Now()}); err != nil {
		t.Fatalf("windowed analyze: %v", err)
	}
	if _, err := os.Stat(indexFile); err == nil {
		t.Fatal("windowed scan wrote an index; its facts are window-filtered and must not be reused")
	}
}

// TestExactWindowSkipUsesIndexedLastEvent 覆盖新增的精确跳过：账目显示该文件最后
// 一条相关事件早于窗口时，即使它的 mtime 很新（刚被 append 刷新过）也该整文件跳过。
func TestExactWindowSkipUsesIndexedLastEvent(t *testing.T) {
	seedIndex := filepath.Join(t.TempDir(), indexFileName)
	withIndexPath(t, seedIndex)
	root := writeFixture(t) // fixture 的事件都在 2026-09-29 10:00–10:02
	if _, err := Analyze(Options{Roots: []string{root}}); err != nil {
		t.Fatalf("seed analyze: %v", err)
	}

	// 把所有文件的 mtime 刷成"刚刚"——mtime 快速路径此时会说"要读"。
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		now := time.Now()
		return os.Chtimes(path, now, now)
	}); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// 窗口取 fixture 之后：mtime 说要读，账目说整文件都早于窗口 → 应跳过。
	stats, err := Analyze(Options{Roots: []string{root}, Since: time.Date(2026, 9, 29, 10, 3, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("windowed analyze: %v", err)
	}
	if stats.Scan.SkippedFiles != 1 || stats.Scan.Files != 0 {
		t.Fatalf("scan = %+v, want the indexed file skipped despite fresh mtime", stats.Scan)
	}
	if stats.Requests != 0 {
		t.Fatalf("requests = %d, want 0 outside the window", stats.Requests)
	}
}

// TestCorruptIndexFallsBackToFullScan 覆盖账本被写坏：静默退回全量扫描，
// 绝不能因此报错，更不能产出错误数字。
func TestCorruptIndexFallsBackToFullScan(t *testing.T) {
	indexFile := filepath.Join(t.TempDir(), indexFileName)
	withIndexPath(t, indexFile)
	if err := os.WriteFile(indexFile, []byte("{not json at all"), 0o644); err != nil {
		t.Fatalf("write corrupt index: %v", err)
	}
	root := writeFixture(t)
	stats, err := Analyze(Options{Roots: []string{root}})
	if err != nil {
		t.Fatalf("analyze with corrupt index: %v", err)
	}
	if stats.Requests != 4 || stats.Sessions != 2 {
		t.Fatalf("stats = %+v, want the correct fixture numbers despite a corrupt index", stats)
	}
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	payload := ""
	for _, line := range lines {
		payload += line + "\n"
	}
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	// mtime 会随追加变化，但显式 touch 让"账目命中"条件不依赖文件系统精度。
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

// appendLines 真正往文件尾部追加（writeLines 是整文件覆盖）。增量场景必须用
// 它：用覆盖写会把文件变小，那走的是「截断 → 全量重读」分支，测不到增量。
func appendLines(t *testing.T, path string, lines []string) {
	t.Helper()
	handle, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := handle.WriteString(joinLines(lines)); err != nil {
		handle.Close()
		t.Fatalf("append: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close after append: %v", err)
	}
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

func joinLines(lines []string) string {
	payload := ""
	for _, line := range lines {
		payload += line + "\n"
	}
	return payload
}

// assertStatsEqual 用 reflect.DeepEqual 逐字段比较两份 Stats——包括未导出的
// durations 切片，所以 P50/P95 的推导输入也被覆盖（只比 JSON 会漏掉它，因为
// durations 标了 json:"-"）。
//
// 唯一被排除的是 ScanStats 里「这轮怎么读的」三个字段：复用/增量/入账数按定义
// 就该在冷扫与增量扫之间不同，比较它们会把本测试变成自相矛盾。
func assertStatsEqual(t *testing.T, want, got Stats) {
	t.Helper()
	want.Scan.ReusedFiles, want.Scan.DeltaFiles, want.Scan.IndexedFiles = 0, 0, 0
	got.Scan.ReusedFiles, got.Scan.DeltaFiles, got.Scan.IndexedFiles = 0, 0, 0
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("Stats differ between full rescan and incremental scan:\n want %+v\n  got %+v", want, got)
	}
}
