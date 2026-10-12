package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	runtimeripgrep "github.com/wwsheng009/ai-agent-runtime/internal/ripgrep"
)

// TestCaptureLimitedOutputByLines 覆盖流式捕获的两道闸门：行预算命中即停、
// 字节上限优先、两者同时设置时先到者生效、以及完全不限时读满整条流。
func TestCaptureLimitedOutputByLines(t *testing.T) {
	input := "l1\nl2\nl3\nl4\nl5\nl6\n"

	data, stop, err := captureLimitedOutputByLines(strings.NewReader(input), 0, 3)
	if err != nil {
		t.Fatalf("line budget read error: %v", err)
	}
	if stop != grepCaptureStopLineBudget {
		t.Fatalf("stop=%v, want line budget", stop)
	}
	// 行数按 32 KiB 读块计数，命中预算的那一块可能多带几行（内存仍有界），
	// 因此断言「不少于预算」而不是严格相等。
	if got := strings.Count(string(data), "\n"); got < 3 {
		t.Fatalf("captured %d lines, want >= 3", got)
	}
	if !strings.HasPrefix(input, string(data)) {
		t.Fatalf("captured data is not a prefix: %q", data)
	}

	data, stop, err = captureLimitedOutputByLines(strings.NewReader(strings.Repeat("x", 100)), 10, 0)
	if err != nil {
		t.Fatalf("byte limit read error: %v", err)
	}
	if stop != grepCaptureStopByteLimit || len(data) != 10 {
		t.Fatalf("stop=%v len=%d, want byte limit at 10 bytes", stop, len(data))
	}

	// 字节上限先到：5 字节 < 2 行。
	if _, stop, _ = captureLimitedOutputByLines(strings.NewReader("aaaaaaaaaa\nbbbbbbbbbb\n"), 5, 100); stop != grepCaptureStopByteLimit {
		t.Fatalf("stop=%v, want byte limit", stop)
	}
	// 行预算先到：2 行 ≈ 6 字节 < 1000 字节。
	if _, stop, _ = captureLimitedOutputByLines(strings.NewReader("aa\nbb\ncc\n"), 1000, 2); stop != grepCaptureStopLineBudget {
		t.Fatalf("stop=%v, want line budget", stop)
	}
	// 完全不限：读到 EOF。
	data, stop, err = captureLimitedOutputByLines(strings.NewReader(input), 0, 0)
	if err != nil || stop != grepCaptureStopEOF || string(data) != input {
		t.Fatalf("unlimited read: stop=%v err=%v data=%q", stop, err, data)
	}
}

// TestGrepSearchStreamLineBudgetReachesRunner 验证行预算通过 ctx 抵达 runner：
// 默认页 100 → 100+probe；显式 head_limit=5 → 5+probe；--stats 需要尾部汇总 →
// 不设行预算（0），保持读满（仍受字节上限保护）。
func TestGrepSearchStreamLineBudgetReachesRunner(t *testing.T) {
	tool := NewGrepTool()
	tool.lookPath = func(string) (string, error) { return "rg", nil }
	var budgets []int
	tool.runCommand = func(ctx context.Context, _, _ string, _ []string) ([]byte, error) {
		budgets = append(budgets, grepStreamLineBudget(ctx))
		return []byte("a.go:1:needle\n"), nil
	}
	run := func(params map[string]interface{}) {
		t.Helper()
		if _, err := tool.Execute(context.Background(), params); err != nil {
			t.Fatalf("Execute(%v) error: %v", params, err)
		}
	}
	run(map[string]interface{}{"pattern": "needle"})
	run(map[string]interface{}{"pattern": "needle", "head_limit": 5})
	run(map[string]interface{}{"pattern": "needle", "stats": true})

	want := []int{100 + grepStreamLineProbe, 5 + grepStreamLineProbe, 0}
	if !reflect.DeepEqual(budgets, want) {
		t.Fatalf("runner line budgets = %v, want %v", budgets, want)
	}
}

// TestCaptureLimitedOutput 覆盖读取侧字节闸门的三种形态：未达上限原样返回、
// 恰好等于上限不算溢出、超过上限只保留 limit 字节并标记 exceeded、
// limit<=0 表示不限。
func TestCaptureLimitedOutput(t *testing.T) {
	cases := []struct {
		name         string
		limit        int
		input        string
		wantLen      int
		wantExceeded bool
	}{
		{name: "under", limit: 16, input: "0123456789", wantLen: 10, wantExceeded: false},
		{name: "exact", limit: 10, input: "0123456789", wantLen: 10, wantExceeded: false},
		{name: "over", limit: 10, input: "0123456789ABC", wantLen: 10, wantExceeded: true},
		{name: "unlimited", limit: 0, input: strings.Repeat("x", 4096), wantLen: 4096, wantExceeded: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, exceeded, err := captureLimitedOutput(strings.NewReader(tc.input), tc.limit)
			if err != nil {
				t.Fatalf("captureLimitedOutput error: %v", err)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("len(output)=%d, want %d", len(got), tc.wantLen)
			}
			if exceeded != tc.wantExceeded {
				t.Fatalf("exceeded=%v, want %v", exceeded, tc.wantExceeded)
			}
			if tc.limit > 0 && exceeded && !strings.HasPrefix(tc.input, string(got)) {
				t.Fatalf("truncated output %q is not a prefix of input %q", got, tc.input)
			}
		})
	}
}

// TestClampGrepContentLine 验证单行截断：保留行首前缀、附确定性标记、
// UTF-8 边界不被切断、max<=0 或未超限时原样返回。
func TestClampGrepContentLine(t *testing.T) {
	ascii := strings.Repeat("a", 100)
	got := clampGrepContentLine(ascii, 10)
	if !strings.HasPrefix(got, strings.Repeat("a", 10)) {
		t.Fatalf("prefix lost: %q", got)
	}
	if !strings.Contains(got, "+90 bytes truncated") {
		t.Fatalf("truncation marker missing: %q", got)
	}

	// 10 个 3 字节汉字：cut=8 落在字符中间，必须回退到 6（UTF-8 边界）。
	han := strings.Repeat("中", 10)
	got = clampGrepContentLine(han, 8)
	if !utf8.ValidString(got) {
		t.Fatalf("clamped line is not valid UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, strings.Repeat("中", 2)) {
		t.Fatalf("unexpected clamped prefix: %q", got)
	}

	if got := clampGrepContentLine("short", 10); got != "short" {
		t.Fatalf("line under budget changed: %q", got)
	}
	if got := clampGrepContentLine(ascii, 0); got != ascii {
		t.Fatalf("limit<=0 must disable clamping, got len=%d", len(got))
	}
}

// TestResolveGrepByteBudgetEnv 验证 env 解析：未设置/非法值回退默认，
// 正值生效，关闭词返回 0（关闭上限）。
func TestResolveGrepByteBudgetEnv(t *testing.T) {
	t.Setenv(grepRawOutputBudgetEnv, "")
	if got := resolveGrepRawOutputBudget(); got != defaultGrepRawOutputBudgetBytes {
		t.Fatalf("unset: got %d, want default %d", got, defaultGrepRawOutputBudgetBytes)
	}
	t.Setenv(grepRawOutputBudgetEnv, "1048576")
	if got := resolveGrepRawOutputBudget(); got != 1024*1024 {
		t.Fatalf("explicit: got %d, want %d", got, 1024*1024)
	}
	t.Setenv(grepRawOutputBudgetEnv, "off")
	if got := resolveGrepRawOutputBudget(); got != 0 {
		t.Fatalf("off: got %d, want 0", got)
	}
	t.Setenv(grepRawOutputBudgetEnv, "not-a-number")
	if got := resolveGrepRawOutputBudget(); got != defaultGrepRawOutputBudgetBytes {
		t.Fatalf("invalid: got %d, want default %d", got, defaultGrepRawOutputBudgetBytes)
	}
	t.Setenv(grepContentLineBudgetEnv, "2048")
	if got := resolveGrepContentLineBudget(); got != 2048 {
		t.Fatalf("line budget: got %d, want 2048", got)
	}
}

// TestNormalizeRipgrepContentLinesClampsLongLines 验证归一化入口先截断再走
// regex/Sprintf：超长行的前缀（path:line:col:）保留，尾部带截断标记。
func TestNormalizeRipgrepContentLinesClampsLongLines(t *testing.T) {
	t.Setenv(grepContentLineBudgetEnv, "32")
	longLine := "backend/a.go:12:7: " + strings.Repeat("z", 400)
	lines := normalizeRipgrepContentLines([]string{longLine})
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	got := lines[0]
	if !strings.HasPrefix(got, "backend/a.go:12:7: ") {
		t.Fatalf("match prefix lost: %q", got)
	}
	if !strings.Contains(got, "bytes truncated") {
		t.Fatalf("truncation marker missing: %q", got)
	}
	// 归一化会按 `path:line:col: text` 重新拼接（text 组含前导空格，重排后
	// 长度可能有 ±1~2 字节的出入），因此用「预算 + 标记 + 少量余量」作上界。
	clamped := clampGrepContentLine(longLine, 32)
	if len(got) > len(clamped)+4 {
		t.Fatalf("line not clamped: len=%d, want <= %d", len(got), len(clamped)+4)
	}
	if len(got) >= len(longLine) {
		t.Fatalf("clamped line was not shortened: %d >= %d", len(got), len(longLine))
	}
}

// TestRunGrepCommandStopsAtRawOutputBudget 用测试二进制自身作为子进程，
// 验证 runGrepCommand 在 stdout 超过上限时提前终止（不把超量输出读进内存），
// 并返回 errGrepOutputLimit 供 searchWithRipgrep 走 partial 分支。
func TestRunGrepCommandStopsAtRawOutputBudget(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv(grepRawOutputBudgetEnv, "65536")
	output, err := runGrepCommand(
		context.Background(),
		exe,
		t.TempDir(),
		[]string{"-test.run=^TestRunGrepCommandOutputFloodHelper$"},
	)
	if !errors.Is(err, errGrepOutputLimit) {
		t.Fatalf("expected errGrepOutputLimit, got %v (output=%d bytes)", err, len(output))
	}
	if len(output) > 65536 {
		t.Fatalf("output exceeded budget: %d bytes", len(output))
	}

	// 上限放宽后同一子进程应完整跑完。
	t.Setenv(grepRawOutputBudgetEnv, "1048576")
	output, err = runGrepCommand(
		context.Background(),
		exe,
		t.TempDir(),
		[]string{"-test.run=^TestRunGrepCommandOutputFloodHelper$"},
	)
	if err != nil {
		t.Fatalf("helper failed under wide budget: %v", err)
	}
	if len(output) < 256*1024 {
		t.Fatalf("expected full flood output, got %d bytes", len(output))
	}
}

// TestRunGrepCommandOutputFloodHelper 是上面测试的子进程助手：向 stdout
// 写入 256 KiB 后退出（远大于测试设置的 64 KiB 上限）。
func TestRunGrepCommandOutputFloodHelper(t *testing.T) {
	if !containsString(os.Args, "-test.run=^TestRunGrepCommandOutputFloodHelper$") {
		t.Skip("subprocess helper")
	}
	chunk := strings.Repeat("x", 4096)
	for i := 0; i < 64; i++ {
		fmt.Print(chunk)
	}
}

// TestGrepToolRealRipgrepStopsAtRawOutputBudget 是 2026-10-12 事故形态的端到端
// 回归：单文件被 --max-count=100 收敛，只有多文件累计才会超过读取上限。
// 要求真实 rg；未安装时跳过（读取侧闸门的确定性覆盖在 TestRunGrepCommandStopsAtRawOutputBudget）。
func TestGrepToolRealRipgrepStopsAtRawOutputBudget(t *testing.T) {
	if _, err := runtimeripgrep.LookPath("rg"); err != nil {
		t.Skip("rg not available")
	}

	dir := t.TempDir()
	line := "gateway 2026-10-12 payload=" + strings.Repeat("x", 80) + "\n"
	for i := 0; i < 400; i++ {
		name := filepath.Join(dir, fmt.Sprintf("part-%03d.log", i))
		if err := os.WriteFile(name, []byte(strings.Repeat(line, 100)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// 把字节上限压到 ~85 行（小于默认页 100+probe），让这条用例覆盖「读取侧
	// 字节上限」路径；「收满页即停」由 TestGrepToolRealRipgrepStopsAfterPage 覆盖。
	t.Setenv(grepRawOutputBudgetEnv, "8192")
	tool := NewGrepTool()
	start := time.Now()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "gateway",
		"path":    dir,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("result not successful: %+v", result.Error)
	}
	if partial, _ := result.Metadata["partial"].(bool); !partial {
		t.Fatalf("expected partial=true, metadata=%v", result.Metadata)
	}
	if scope, _ := result.Metadata["match_count_scope"].(string); scope != "observed_before_stop" {
		t.Fatalf("byte-cap result must annotate observed match_count, metadata=%v", result.Metadata)
	}
	if !strings.Contains(result.Content, "超过上限") {
		t.Fatalf("cap notice missing, output head: %.300s", result.Content)
	}
	if len(result.Content) > 64*1024 {
		t.Fatalf("model-visible payload too large: %d bytes", len(result.Content))
	}
	if elapsed > 30*time.Second {
		t.Fatalf("cap did not stop the search quickly enough: %s", elapsed)
	}
}

// TestGrepToolRealRipgrepStopsAfterPage 验证「流式逐行收集、收满页即停」：
// 默认页 100，语料有 400 个文件 × 100 行匹配；rg 应在读满 100+probe 行后即被
// 终止（分页语义，不是 partial），输出只覆盖前面的文件，并给出 next_offset。
func TestGrepToolRealRipgrepStopsAfterPage(t *testing.T) {
	if _, err := runtimeripgrep.LookPath("rg"); err != nil {
		t.Skip("rg not available")
	}

	dir := t.TempDir()
	line := "page-stop 2026-10-12 payload=" + strings.Repeat("x", 80) + "\n"
	for i := 0; i < 400; i++ {
		name := filepath.Join(dir, fmt.Sprintf("part-%03d.log", i))
		if err := os.WriteFile(name, []byte(strings.Repeat(line, 100)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tool := NewGrepTool()
	start := time.Now()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "page-stop",
		"path":    dir,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("result not successful: %+v", result.Error)
	}
	if partial, _ := result.Metadata["partial"].(bool); partial {
		t.Fatalf("early page stop must not be partial, metadata=%v", result.Metadata)
	}
	if truncated, _ := result.Metadata["truncated"].(bool); !truncated {
		t.Fatalf("expected truncated page, metadata=%v", result.Metadata)
	}
	if hasMore, _ := result.Metadata["has_more"].(bool); !hasMore {
		t.Fatalf("expected has_more=true, metadata=%v", result.Metadata)
	}
	if next, _ := result.Metadata["next_offset"].(int); next != 100 {
		t.Fatalf("next_offset=%v, want 100 (metadata=%v)", result.Metadata["next_offset"], result.Metadata)
	}
	if reason, _ := result.Metadata["truncation_reason"].(string); reason != "page_stop" {
		t.Fatalf("truncation_reason=%v, want page_stop (metadata=%v)", result.Metadata["truncation_reason"], result.Metadata)
	}
	if scope, _ := result.Metadata["match_count_scope"].(string); scope != "observed_before_stop" {
		t.Fatalf("match_count_scope=%v, want observed_before_stop (metadata=%v)", result.Metadata["match_count_scope"], result.Metadata)
	}
	// rg 的遍历起点由并发调度决定（part-000/001 都可能先出现），因此不断言具体
	// 文件名，而是断言「页内文件序号必须很小」——证明 rg 在第一页收满后即被终止，
	// 而不是扫完 400 个文件。
	highest := -1
	for _, match := range regexp.MustCompile(`part-(\d+)`).FindAllStringSubmatch(result.Content, -1) {
		if index, err := strconv.Atoi(match[1]); err == nil && index > highest {
			highest = index
		}
	}
	if highest < 0 {
		t.Fatalf("expected matches from leading files, output head: %.200s", result.Content)
	}
	if highest > 50 {
		t.Fatalf("expected rg to stop early, but page reached part-%03d; output head: %.200s", highest, result.Content)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("page stop did not terminate rg quickly: %s", elapsed)
	}
}

// TestGrepCompleteResultHasNoObservedAnnotation 验证完整结果不携带观测口径标注：
// match_count_scope / truncation_reason 只在结果不完整时出现，避免消费者把
// 完整结果误判为截断。
func TestGrepCompleteResultHasNoObservedAnnotation(t *testing.T) {
	tool := NewGrepTool()
	tool.lookPath = func(string) (string, error) { return "rg", nil }
	tool.runCommand = func(context.Context, string, string, []string) ([]byte, error) {
		return []byte("a.go:1:needle\nb.go:2:needle\n"), nil
	}
	result, err := tool.Execute(context.Background(), map[string]interface{}{"pattern": "needle"})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if truncated, _ := result.Metadata["truncated"].(bool); truncated {
		t.Fatalf("expected complete result, metadata=%v", result.Metadata)
	}
	if _, exists := result.Metadata["match_count_scope"]; exists {
		t.Fatalf("complete result must not carry match_count_scope, metadata=%v", result.Metadata)
	}
	if _, exists := result.Metadata["truncation_reason"]; exists {
		t.Fatalf("complete result must not carry truncation_reason, metadata=%v", result.Metadata)
	}
	if count, _ := result.Metadata["match_count"].(int); count != 2 {
		t.Fatalf("match_count=%v, want 2 (metadata=%v)", result.Metadata["match_count"], result.Metadata)
	}
}
