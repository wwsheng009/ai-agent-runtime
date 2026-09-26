package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestGrepRipgrepKeepsPartialOutputOnTimeout pins the §4.5 remaining item: when
// the tool timeout kills rg, the matches rg already printed are still evidence.
func TestGrepRipgrepKeepsPartialOutputOnTimeout(t *testing.T) {
	tool := NewGrepTool()
	tool.lookPath = func(string) (string, error) { return "rg", nil }
	tool.runCommand = func(ctx context.Context, _ string, _ string, _ []string) ([]byte, error) {
		<-ctx.Done()
		return []byte("src/a.go:1: needled-one\nsrc/b.go:7: needled-two\n"), ctx.Err()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	result, err := tool.Execute(ctx, map[string]interface{}{"pattern": "needled"})
	if err != nil {
		t.Fatalf("timeout with partial output must not be an outer error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected partial success, got %+v", result)
	}
	if result.Metadata["partial"] != true || result.Metadata["timed_out"] != true {
		t.Fatalf("expected partial+timed_out metadata, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "src/a.go:1:") ||
		!strings.Contains(result.Content, "src/b.go:7:") ||
		!strings.Contains(result.Content, "needled-one") ||
		!strings.Contains(result.Content, "needled-two") {
		t.Fatalf("partial matches must survive the timeout, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "结果不完整") || !strings.Contains(result.Content, "不要原样重试同一查询") {
		t.Fatalf("expected incompleteness notice, got %q", result.Content)
	}
	if engine, _ := result.Metadata["engine"].(string); engine != "rg" {
		t.Fatalf("expected engine=rg, got %#v", result.Metadata["engine"])
	}
}

// TestGrepRipgrepTimeoutWithoutOutputStaysFailure: no output means no evidence,
// so the ctx error must surface unchanged instead of a fake partial success.
func TestGrepRipgrepTimeoutWithoutOutputStaysFailure(t *testing.T) {
	tool := NewGrepTool()
	tool.lookPath = func(string) (string, error) { return "rg", nil }
	tool.runCommand = func(ctx context.Context, _ string, _ string, _ []string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	result, err := tool.Execute(ctx, map[string]interface{}{"pattern": "needled"})
	// grep surfaces hard failures as an unsuccessful result with Error set
	// (outer err stays nil), so accept either shape and require the ctx cause.
	failure := ""
	switch {
	case err != nil:
		failure = err.Error()
	case result != nil && !result.Success && result.Error != nil:
		failure = result.Error.Error()
	default:
		t.Fatalf("expected the ctx error to surface, got err=%v result=%+v", err, result)
	}
	lower := strings.ToLower(failure)
	if !strings.Contains(lower, "deadline") && !strings.Contains(lower, "context") {
		t.Fatalf("unexpected error: %s", failure)
	}
}

// TestGrepRipgrepKeepsPartialOutputOnCancel: an explicit cancellation keeps the
// already printed matches too, but is not labelled as a timeout.
func TestGrepRipgrepKeepsPartialOutputOnCancel(t *testing.T) {
	tool := NewGrepTool()
	tool.lookPath = func(string) (string, error) { return "rg", nil }
	ctx, cancel := context.WithCancel(context.Background())
	tool.runCommand = func(context.Context, string, string, []string) ([]byte, error) {
		cancel()
		return []byte("src/c.go:3: cancelled-but-found\n"), context.Canceled
	}

	result, err := tool.Execute(ctx, map[string]interface{}{"pattern": "cancelled"})
	if err != nil {
		t.Fatalf("cancel with partial output must not be an outer error: %v", err)
	}
	if result == nil || !result.Success || result.Metadata["partial"] != true {
		t.Fatalf("expected partial success, got %+v", result)
	}
	if _, timedOut := result.Metadata["timed_out"]; timedOut {
		t.Fatalf("cancellation must not be labelled timed_out: %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "搜索被取消") || !strings.Contains(result.Content, "src/c.go:3:") ||
		!strings.Contains(result.Content, "cancelled-but-found") {
		t.Fatalf("unexpected partial content: %q", result.Content)
	}
}

// TestGrepWalkerPartialResultKeepsCollectedMatches exercises the builtin-walker
// branch directly: cancellation with collected matches is partial evidence.
func TestGrepWalkerPartialResultKeepsCollectedMatches(t *testing.T) {
	opts := &grepOptions{pattern: "hit"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	<-ctx.Done()

	partial := grepWalkerPartialResult(opts,
		[]string{"src/a.go:1: hit", "src/a.go:2: hit"}, 2, nil, ctx.Err(), ctx)
	if partial == nil || !partial.Success {
		t.Fatalf("expected partial result, got %+v", partial)
	}
	if partial.Metadata["partial"] != true || partial.Metadata["timed_out"] != true {
		t.Fatalf("expected partial+timed_out metadata, got %#v", partial.Metadata)
	}
	lines := grepPayloadLines(t, partial.Content)
	if len(lines) != 2 {
		t.Fatalf("expected the two collected matches, got %#v (%q)", lines, partial.Content)
	}
	if !strings.Contains(partial.Content, "结果不完整") {
		t.Fatalf("expected incompleteness notice, got %q", partial.Content)
	}
	if next, _ := partial.Metadata["next_action"].(string); !strings.Contains(next, "不要原样重试") {
		t.Fatalf("expected a no-dead-retry next_action, got %#v", partial.Metadata["next_action"])
	}

	// 无产出 / 非中断错误：不制造部分结果，保持硬失败语义。
	if got := grepWalkerPartialResult(opts, nil, 0, nil, ctx.Err(), ctx); got != nil {
		t.Fatalf("empty results must stay a hard failure, got %+v", got)
	}
	if got := grepWalkerPartialResult(opts, []string{"src/a.go:1: hit"}, 1, nil, fmt.Errorf("搜索失败: boom"), context.Background()); got != nil {
		t.Fatalf("non-interruption errors must stay hard failures, got %+v", got)
	}
}

// TestGrepPartialNoticeFitsTheByteBudget proves the partial notice is charged
// against grep's own budget instead of pushing the payload past it.
func TestGrepPartialNoticeFitsTheByteBudget(t *testing.T) {
	results := make([]string, 0, 4000)
	line := "src/very/long/path/for/budget/file.go:12345: " + strings.Repeat("x", 200)
	for index := 0; index < 4000; index++ {
		results = append(results, fmt.Sprintf("%d:%s", index, line))
	}
	notice := grepPartialNotice("搜索超时", len(results))
	result := buildGrepResult(&grepOptions{pattern: "needle"}, results, len(results), true, nil, notice)

	if len(result.Content) > grepOutputBudgetBytes {
		t.Fatalf("partial payload exceeds the grep budget: %d > %d", len(result.Content), grepOutputBudgetBytes)
	}
	tail := result.Content
	if len(tail) > 200 {
		tail = tail[len(tail)-200:]
	}
	if !strings.Contains(result.Content, "结果不完整") {
		t.Fatalf("the partial notice must survive byte-budget trimming, got %q", tail)
	}
}
