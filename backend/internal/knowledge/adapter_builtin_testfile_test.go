package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// golden 诊断的定点用例：builtin 必须能抽取测试文件里的顶层函数。
//
// 背景：golden set 测量显示 function recall 偏低，缺失样本集中在 *_test.go；
// 本用例把"测试文件是否被 builtin 抽取"变成可重复的最小复现。
func TestBuiltinAdapterExtractsTestFileSymbols(t *testing.T) {
	path := filepath.Join(repoRoot(), "backend", "internal", "knowledge", "activation_test.go")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	rec := FileRecord{
		ID:          "f_diag",
		WorkspaceID: "w_diag",
		Path:        "backend/internal/knowledge/activation_test.go",
		Language:    "go",
		IsTest:      true,
	}
	extraction, err := builtinAdapter{}.Extract(context.Background(), rec, content)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	found := map[string]int{}
	for _, sym := range extraction.Symbols {
		if strings.HasPrefix(sym.Name, "TestActivate") {
			found[sym.Name] = sym.Range.Start.Line
		}
	}
	t.Logf("test-file functions extracted: %v (total symbols=%d)", found, len(extraction.Symbols))
	if len(found) == 0 {
		t.Fatal("builtin 未抽取测试文件中的任何 Test* 函数（golden function recall 缺口的直接复现）")
	}
}

// 字符串/注释里的"调用形"文本不得成为引用。
//
// 这是 live references 测量定位到的误报源：`t.Fatalf("Activate(owner): %v", err)`
// 会被行内正则抽成 `Activate` 的调用点，而语义通道在这些位置没有定义（首轮
// 测量把这类样本误判为"语义漏报"，见 golden_lsp_live_test.go 的裁决口径）。
func TestBuiltinAdapterSkipsCallsInsideStringsAndComments(t *testing.T) {
	content := []byte("package demo\n" +
		"\n" +
		"func Target() {}\n" +
		"\n" +
		"func use() {\n" +
		"\tTarget()\n" + // 6：真调用
		"\tt.Fatalf(\"Target(): %v\", err)\n" + // 7：字符串
		"\t// Target() 注释里的调用形\n" + // 8：行注释
		"\tmsg := `Target()`\n" + // 9：原始字符串
		"\tesc := \"escaped \\\" Target()\"\n" + // 10：转义引号后仍在字符串内
		"\tconsume(Target())\n" + // 11：真调用（两个）
		"}\n")
	rec := FileRecord{ID: "f_diag", WorkspaceID: "w_diag", Path: "demo/a.go", Language: "go"}
	extraction, err := builtinAdapter{}.Extract(context.Background(), rec, content)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	byLine := map[int][]string{}
	for _, ref := range extraction.Refs {
		if ref.Kind == RefCall {
			byLine[ref.Line] = append(byLine[ref.Line], ref.Name)
		}
	}
	t.Logf("call refs by line: %v", byLine)
	for _, line := range []int{8, 9, 10} {
		if len(byLine[line]) != 0 {
			t.Fatalf("第 %d 行是字符串/注释文本，不得抽成引用：%v", line, byLine[line])
		}
	}
	// 第 7 行是 `t.Fatalf("Target(): %v", err)`：真调用是 Fatalf，字符串里的
	// `Target(` 不得出现。
	if got := byLine[7]; len(got) != 1 || got[0] != "Fatalf" {
		t.Fatalf("第 7 行抽取 = %v, want [Fatalf]（字符串里的 Target( 必须排除）", got)
	}
	if got := byLine[6]; len(got) != 1 || got[0] != "Target" {
		t.Fatalf("第 6 行真调用抽取 = %v, want [Target]", got)
	}
	if got := byLine[11]; len(got) != 2 {
		t.Fatalf("第 11 行真调用抽取 = %v, want 2 个（consume/Target）", got)
	}
	// 列必须是整行字节偏移（tab 占 1 字节）："\tTarget()" → 1。
	for _, ref := range extraction.Refs {
		if ref.Kind == RefCall && ref.Line == 6 && ref.Col != 1 {
			t.Fatalf("第 6 行列偏移 = %d, want 1（整行字节偏移）", ref.Col)
		}
	}
}
