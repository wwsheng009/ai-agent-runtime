package knowledge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	baselsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

// adapter_lsp_symbols_test.go 覆盖 LSP 符号解码：两种返回形状（层级
// DocumentSymbol / 扁平 SymbolInformation）、展平、kind 映射与位置边界。

// decodeFixture 构造一个只用于解码测试的适配器（不启动进程）。
func decodeFixture(t *testing.T, root string) *lspSemanticAdapter {
	t.Helper()
	return &lspSemanticAdapter{root: root, version: "lsp/gopls"}
}

func writeDecodeTarget(t *testing.T, root, rel, content string) string {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return abs
}

// readTarget 读取目标文件内容（documentSymbol 路径会把预读内容传给解码器）。
func readTarget(t *testing.T, abs string) []byte {
	t.Helper()
	content, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return content
}

func TestDecodeSymbolsFlattensHierarchy(t *testing.T) {
	root := t.TempDir()
	rel := "pkg/demo.go"
	abs := writeDecodeTarget(t, root, rel, "package pkg\n\ntype T struct{}\n")
	uri := baselsp.PathToURI(abs)
	adapter := decodeFixture(t, root)

	// 层级形状：DocumentSymbol 带 children。展平后子符号的 Container 必须是父名
	// ——只取顶层是常见错误实现，会让结构体/接口成员整片丢失。
	raw := json.RawMessage(`[{
		"name": "T", "kind": 23,
		"selectionRange": {"start": {"line": 2, "character": 5}, "end": {"line": 2, "character": 6}},
		"children": [
			{"name": "Field", "kind": 8,
			 "selectionRange": {"start": {"line": 2, "character": 15}, "end": {"line": 2, "character": 20}}}
		]
	}]`)
	raw = json.RawMessage(strings.ReplaceAll(string(raw), "FILE_URI", uri))

	syms, err := adapter.decodeSymbols(raw, baselsp.EncodingUTF16, readTarget(t, abs), abs)
	if err != nil {
		t.Fatalf("decodeSymbols: %v", err)
	}
	if len(syms) != 2 {
		t.Fatalf("len(syms) = %d, want 2 (层级必须展平): %#v", len(syms), syms)
	}
	if syms[0].Name != "T" || syms[0].Kind != SymbolType {
		t.Fatalf("syms[0] = %+v, want T/type", syms[0])
	}
	if syms[1].Name != "Field" || syms[1].Kind != SymbolField {
		t.Fatalf("syms[1] = %+v, want Field/field", syms[1])
	}
	if syms[1].Container != "T" {
		t.Fatalf("Container = %q, want T", syms[1].Container)
	}
	for _, sym := range syms {
		// Path 必须是 workspace 相对路径——工具侧要拿它去拼 handle.Root。
		if sym.Path != rel {
			t.Fatalf("Path = %q, want %q", sym.Path, rel)
		}
	}
	if syms[0].Line != 2 || syms[0].Col != 5 {
		t.Fatalf("位置 = (%d,%d), want (2,5)", syms[0].Line, syms[0].Col)
	}
}

func TestDecodeSymbolsFlatShape(t *testing.T) {
	root := t.TempDir()
	rel := "pkg/demo.go"
	abs := writeDecodeTarget(t, root, rel, "package pkg\n\nfunc Alpha() {}\n")
	uri := baselsp.PathToURI(abs)
	adapter := decodeFixture(t, root)

	// 扁平形状：SymbolInformation / WorkspaceSymbol 用 location + containerName。
	raw := json.RawMessage(strings.ReplaceAll(`[{
		"name": "Alpha", "kind": 12, "containerName": "pkg", "detail": "func Alpha()",
		"location": {"uri": "FILE_URI",
		 "range": {"start": {"line": 2, "character": 5}, "end": {"line": 2, "character": 10}}}
	}]`, "FILE_URI", uri))

	syms, err := adapter.decodeSymbols(raw, baselsp.EncodingUTF16, nil, "")
	if err != nil {
		t.Fatalf("decodeSymbols: %v", err)
	}
	if len(syms) != 1 {
		t.Fatalf("len = %d, want 1", len(syms))
	}
	got := syms[0]
	if got.Container != "pkg" || got.Kind != SymbolFunction {
		t.Fatalf("got = %+v, want container=pkg kind=function", got)
	}
	if got.Detail != "func Alpha()" {
		t.Fatalf("Detail = %q, want func Alpha()", got.Detail)
	}
	if got.Line != 2 || got.Col != 5 || got.EndLine != 2 || got.EndCol != 10 {
		t.Fatalf("位置 = (%d,%d)-(%d,%d), want (2,5)-(2,10)", got.Line, got.Col, got.EndLine, got.EndCol)
	}
}

func TestDecodeSymbolsUsesRangeWhenSelectionMissing(t *testing.T) {
	root := t.TempDir()
	rel := "pkg/demo.go"
	abs := writeDecodeTarget(t, root, rel, "package pkg\n\nfunc Alpha() {}\n")
	uri := baselsp.PathToURI(abs)
	adapter := decodeFixture(t, root)

	// 只给 range（无 selectionRange）：必须退到 range 而不是拿到零值位置。
	raw := json.RawMessage(strings.ReplaceAll(`[{
		"name": "Alpha", "kind": 12,
		"range": {"start": {"line": 2, "character": 5}, "end": {"line": 2, "character": 10}}
	}]`, "FILE_URI", uri))
	syms, err := adapter.decodeSymbols(raw, baselsp.EncodingUTF16, readTarget(t, abs), abs)
	if err != nil {
		t.Fatalf("decodeSymbols: %v", err)
	}
	if len(syms) != 1 || syms[0].Line != 2 || syms[0].Col != 5 {
		t.Fatalf("syms = %#v, want one hit at (2,5)", syms)
	}
}

func TestDecodeSymbolsEmptyAndSingle(t *testing.T) {
	root := t.TempDir()
	abs := writeDecodeTarget(t, root, "pkg/demo.go", "package pkg\n")
	adapter := decodeFixture(t, root)
	content := readTarget(t, abs)
	for _, raw := range []string{"", "null", "[]"} {
		syms, err := adapter.decodeSymbols(json.RawMessage(raw), baselsp.EncodingUTF16, content, abs)
		if err != nil {
			t.Fatalf("decodeSymbols(%q): %v", raw, err)
		}
		if len(syms) != 0 {
			t.Fatalf("decodeSymbols(%q) = %d 项, want 0", raw, len(syms))
		}
	}
	// 单对象返回（部分 server）也必须接受；层级形状的 URI 来自请求文档。
	syms, err := adapter.decodeSymbols(json.RawMessage(`{"name":"X","kind":12}`), baselsp.EncodingUTF16, content, abs)
	if err != nil {
		t.Fatalf("decodeSymbols single: %v", err)
	}
	if len(syms) != 1 || syms[0].Name != "X" {
		t.Fatalf("single = %#v, want one X", syms)
	}
}

func TestDecodeSymbolsBoundsDepth(t *testing.T) {
	root := t.TempDir()
	abs := writeDecodeTarget(t, root, "pkg/demo.go", "package pkg\n")
	uri := baselsp.PathToURI(abs)
	adapter := decodeFixture(t, root)

	// 构造一条超过深度上限的链：解码必须在上限处停止，而不是无限递归。
	node := `{"name":"leaf","kind":12,"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}}}`
	for i := 0; i < maxSymbolTreeDepth+3; i++ {
		node = `{"name":"n","kind":12,"children":[` + node +
			`],"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}}}`
	}
	syms, err := adapter.decodeSymbols(json.RawMessage("["+strings.ReplaceAll(node, "FILE_URI", uri)+"]"),
		baselsp.EncodingUTF16, readTarget(t, abs), abs)
	if err != nil {
		t.Fatalf("decodeSymbols: %v", err)
	}
	// 展平宽度被深度上限截断：必须有限（不 panic、不爆栈）。
	if len(syms) > maxSymbolTreeDepth+2 {
		t.Fatalf("len(syms) = %d, 深度上限未生效", len(syms))
	}
	if len(syms) == 0 {
		t.Fatal("深度上限内至少应解出一个符号")
	}
}

func TestSemanticSymbolKindMapping(t *testing.T) {
	// 未覆盖的 LSP kind 必须归 unknown——不臆测，宁可让上层看到 unknown。
	for kind, want := range map[int]SymbolKind{
		1:  SymbolPackage,
		6:  SymbolMethod,
		8:  SymbolField,
		9:  SymbolInterface,
		12: SymbolFunction,
		13: SymbolVariable,
		22: SymbolType,
		23: SymbolType,
		26: SymbolType,
		99: SymbolUnknown,
	} {
		if got := semanticSymbolKind(kind); got != want {
			t.Fatalf("semanticSymbolKind(%d) = %q, want %q", kind, got, want)
		}
	}
}

func TestDecodeSymbolsSkipsUnreadableTargets(t *testing.T) {
	root := t.TempDir()
	adapter := decodeFixture(t, root)
	// 目标文件不存在：跳过该条而不是让整次查询失败（与 decodeLocations 同口径）。
	raw := json.RawMessage(`[{"name":"Ghost","kind":12,
		"location":{"uri":"file:///nonexistent/ghost.go",
		"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}}}}]`)
	syms, err := adapter.decodeSymbols(raw, baselsp.EncodingUTF16, nil, "")
	if err != nil {
		t.Fatalf("decodeSymbols 不得因不可读目标失败: %v", err)
	}
	if len(syms) != 0 {
		t.Fatalf("syms = %#v, want 0（不可读目标应跳过）", syms)
	}
}