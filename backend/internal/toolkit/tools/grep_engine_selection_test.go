package tools

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestGrepTool_PCREPatternRoutesToRipgrepBeforeGoCompile 锁定 2026-09-27 修复：
// 显式要求 rg（rg_args:["-P"]）的 lookaround 模式在 ripgrep 上合法，但 Go
// regexp 不支持；该请求必须先把模式原样交给 rg，而不是先被 Go 编译拒绝。
func TestGrepTool_PCREPatternRoutesToRipgrepBeforeGoCompile(t *testing.T) {
	tool := NewGrepTool()
	tool.lookPath = func(string) (string, error) { return "rg", nil }
	var gotArgs []string
	tool.runCommand = func(_ context.Context, _ string, _ string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("src/a.go:2:needle\n"), nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": `foo(?!bar)`,
		"rg_args": []interface{}{"-P"},
	})
	if err != nil {
		t.Fatalf("outer error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected rg-backed success, got %+v", result)
	}
	if engine, _ := result.Metadata["engine"].(string); engine != "rg" {
		t.Fatalf("expected engine=rg, got %#v", result.Metadata["engine"])
	}
	if !containsGrepArg(gotArgs, "--pcre2") {
		t.Fatalf("expected --pcre2 in rg args, got %#v", gotArgs)
	}
}

func containsGrepArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

// TestGrepTool_PCREPatternWithoutRipgrepReportsEngineError: rg 不可用时必须
// 如实报告引擎缺失，而不是把合法的 PCRE 模式判成“正则表达式无效”。
func TestGrepTool_PCREPatternWithoutRipgrepReportsEngineError(t *testing.T) {
	tool := NewGrepTool()
	tool.lookPath = func(string) (string, error) { return "", os.ErrNotExist }

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": `foo(?!bar)`,
		"rg_args": []interface{}{"-P"},
	})
	if err != nil {
		t.Fatalf("outer error: %v", err)
	}
	if result == nil || result.Success || result.Error == nil {
		t.Fatalf("expected rg-availability failure, got %+v", result)
	}
	message := strings.ToLower(result.Error.Error())
	if strings.Contains(message, "正则表达式无效") {
		t.Fatalf("PCRE pattern must not be rejected by Go regexp when rg is required: %s", result.Error)
	}
	if !strings.Contains(message, "ripgrep") && !strings.Contains(message, "rg") {
		t.Fatalf("expected ripgrep availability guidance, got %s", result.Error)
	}
}

// TestGrepTool_InvalidPatternHintUsesModelVisibleRgArgs: 恢复提示必须指向精简
// 模型面里真实存在的 rg_args（pcre2 字段会被 compactGrepParametersForModel
// 裁掉），否则模型会照着一个不存在的参数排查。
func TestGrepTool_InvalidPatternHintUsesModelVisibleRgArgs(t *testing.T) {
	tool := NewGrepTool()
	tool.lookPath = func(string) (string, error) { return "", os.ErrNotExist }

	result, err := tool.Execute(context.Background(), map[string]interface{}{"pattern": `foo(?!bar)`})
	if err != nil {
		t.Fatalf("outer error: %v", err)
	}
	if result == nil || result.Success || result.Error == nil {
		t.Fatalf("expected compile failure, got %+v", result)
	}
	message := result.Error.Error()
	if !strings.Contains(message, "rg_args") {
		t.Fatalf("expected rg_args guidance, got %s", message)
	}
	if strings.Contains(message, "pcre2=true") {
		t.Fatalf("recovery hint must not point at a field missing from the compact model surface: %s", message)
	}
}
