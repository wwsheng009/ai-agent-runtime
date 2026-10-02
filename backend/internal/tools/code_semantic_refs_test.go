package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	toolkit "github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
)

// Phase 4 接线的工具面行为：语义通道优先 + 位置口径转换 + 全路径降级。
//
// 位置口径：索引 1-based（adapter_builtin.go:276）↔ canonical 0-based
// （ADR-0006 §4.4）——转换只在 tools 侧发生一次，本文件锁定该边界。

type fakeCodeIndex struct {
	symbols []knowledge.Symbol
	// allSymbols 用于非精确查询（如按路径列符号 / 所在符号反查）。
	allSymbols []knowledge.Symbol
	refs       []knowledge.Reference
	refCalls   int
}

func (f *fakeCodeIndex) FindWorkspace(context.Context, string) (string, bool, error) {
	return "ws-1", true, nil
}

func (f *fakeCodeIndex) FindSymbols(_ context.Context, q knowledge.SymbolQuery) ([]knowledge.Symbol, error) {
	if q.Exact {
		return f.symbols, nil
	}
	if len(f.allSymbols) > 0 {
		return f.allSymbols, nil
	}
	return nil, nil
}

func (f *fakeCodeIndex) FindRefs(context.Context, knowledge.RefQuery) ([]knowledge.Reference, error) {
	f.refCalls++
	return f.refs, nil
}

func (f *fakeCodeIndex) Search(context.Context, knowledge.SearchQuery) ([]knowledge.SearchHit, error) {
	return nil, nil
}

func (f *fakeCodeIndex) ListActiveFiles(context.Context, string) ([]knowledge.FileRecord, error) {
	return nil, nil
}

type fakeSemanticAdapter struct {
	locs              []knowledge.SemanticLocation
	err               error
	defs              []knowledge.SemanticLocation
	defErr            error
	defCalls          []fakeSemanticCall
	calls             int
	lastFile          string
	lastLine, lastCol int
}

type fakeSemanticCall struct {
	file      string
	line, col int
}

func (f *fakeSemanticAdapter) Name() knowledge.RefSource { return knowledge.SourceLSP }
func (f *fakeSemanticAdapter) Version() string           { return "lsp/fake-1" }
func (f *fakeSemanticAdapter) Available() bool           { return true }
func (f *fakeSemanticAdapter) Capabilities() knowledge.AdapterCapabilities {
	return knowledge.AdapterCapabilities{Definition: true, References: true}
}
func (f *fakeSemanticAdapter) Definition(_ context.Context, file string, line, col int) ([]knowledge.SemanticLocation, error) {
	if f == nil { // 防御：测试里可能把 typed-nil 塞进接口
		return nil, nil
	}
	f.defCalls = append(f.defCalls, fakeSemanticCall{file: file, line: line, col: col})
	if f.defErr != nil {
		return nil, f.defErr
	}
	return f.defs, nil
}
func (f *fakeSemanticAdapter) References(_ context.Context, file string, line, col int) ([]knowledge.SemanticLocation, error) {
	if f == nil {
		return nil, nil
	}
	f.calls++
	f.lastFile, f.lastLine, f.lastCol = file, line, col
	if f.err != nil {
		return nil, f.err
	}
	return f.locs, nil
}

type refsEnvelope struct {
	Source      string  `json:"source"`
	Confidence  float64 `json:"confidence"`
	Degraded    bool    `json:"degraded"`
	Explanation string  `json:"explanation"`
	Results     []struct {
		Path   string `json:"path"`
		Line   int    `json:"line"`
		Col    int    `json:"col"`
		Kind   string `json:"kind"`
		Source string `json:"source"`
	} `json:"results"`
	Fallback *struct {
		Tool   string `json:"tool"`
		Reason string `json:"reason"`
	} `json:"fallback"`
}

func decodeRefsEnvelope(t *testing.T, content string) refsEnvelope {
	t.Helper()
	var env refsEnvelope
	if err := json.Unmarshal([]byte(content), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, content)
	}
	return env
}

// buildRefsTool 组装一个注入了假索引与假语义通道的 code_references 工具。
func buildRefsTool(t *testing.T, root string, index *fakeCodeIndex, sem *fakeSemanticAdapter) *toolkit.CodeReferencesTool {
	t.Helper()
	tool := toolkit.NewCodeReferencesTool()
	tool.SetBasePath(root)
	handle := &toolkit.CodeIndexHandle{
		Index:       index,
		Mode:        knowledge.ModeOn,
		WorkspaceID: "ws-1",
		Root:        root,
		Semantic:    sem,
		FilePaths:   map[string]string{"f1": "internal/demo/demo.go"},
	}
	tool.SetCodeIndexResolver(func(context.Context) (*toolkit.CodeIndexHandle, bool) {
		return handle, true
	})
	return tool
}

// 符号定义在第 10 行（1-based）、第 6 列（UTF-8 字节）。
func demoIndex() *fakeCodeIndex {
	return &fakeCodeIndex{symbols: []knowledge.Symbol{{
		ID:     "s1",
		Name:   "Target",
		FileID: "f1",
		Kind:   knowledge.SymbolFunction,
		Range:  knowledge.Range{Start: knowledge.Position{Line: 10, Column: 6}},
	}}}
}

func TestCodeReferencesPrefersSemanticChannel(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sem := &fakeSemanticAdapter{locs: []knowledge.SemanticLocation{
		{Path: "internal/demo/demo.go", Line: 20, Col: 3},
		{Path: "internal/demo/demo.go", Line: 9, Col: 6}, // 声明自身（canonical 0-based）：必须剔除
	}}
	tool := buildRefsTool(t, root, demoIndex(), sem)

	result, err := tool.Execute(ctx, map[string]interface{}{"symbol": "Target"})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("Execute: err=%v result=%+v", err, result)
	}
	env := decodeRefsEnvelope(t, result.Content)
	if env.Source != "lsp" {
		t.Fatalf("source=%q, want lsp", env.Source)
	}
	if len(env.Results) != 1 {
		t.Fatalf("results=%d, want 1（声明自身被剔除）", len(env.Results))
	}
	if env.Results[0].Line != 21 {
		t.Fatalf("line=%d, want 21（canonical 0-based 20 → 结果 1-based）", env.Results[0].Line)
	}
	if env.Results[0].Source != "lsp" {
		t.Fatalf("hit.source=%q, want lsp", env.Results[0].Source)
	}
	if sem.lastLine != 9 || sem.lastCol != 6 || sem.lastFile != "internal/demo/demo.go" {
		t.Fatalf("语义查询位置=%s:%d:%d, want internal/demo/demo.go:9:6（1-based→0-based）",
			sem.lastFile, sem.lastLine, sem.lastCol)
	}
}

func TestCodeReferencesSemanticFallsBackToIndex(t *testing.T) {
	ctx := context.Background()
	index := demoIndex()
	index.refs = []knowledge.Reference{{
		FileID: "f1", Line: 30, Col: 2, Kind: knowledge.RefCall, Source: knowledge.SourceBuiltin,
	}}
	sem := &fakeSemanticAdapter{err: errors.New("lsp boom")}
	tool := buildRefsTool(t, t.TempDir(), index, sem)

	result, err := tool.Execute(ctx, map[string]interface{}{"symbol": "Target"})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("Execute: err=%v result=%+v", err, result)
	}
	env := decodeRefsEnvelope(t, result.Content)
	if env.Source != "index" {
		t.Fatalf("source=%q, want index（语义失败必须回落索引）", env.Source)
	}
	if len(env.Results) != 1 || env.Results[0].Line != 30 {
		t.Fatalf("results=%+v, want 索引路径 1 条 line=30", env.Results)
	}
	if sem.calls != 1 {
		t.Fatalf("semantic calls=%d, want 1", sem.calls)
	}
}

func TestCodeReferencesKindFilterBypassesSemantic(t *testing.T) {
	ctx := context.Background()
	index := demoIndex()
	index.refs = []knowledge.Reference{{FileID: "f1", Line: 40, Kind: knowledge.RefImport}}
	sem := &fakeSemanticAdapter{locs: []knowledge.SemanticLocation{{Path: "internal/demo/demo.go", Line: 41}}}
	tool := buildRefsTool(t, t.TempDir(), index, sem)

	result, err := tool.Execute(ctx, map[string]interface{}{"symbol": "Target", "kind": "import"})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("Execute: err=%v result=%+v", err, result)
	}
	env := decodeRefsEnvelope(t, result.Content)
	if env.Source != "index" {
		t.Fatalf("source=%q, want index（kind=import 由索引通道负责）", env.Source)
	}
	if sem.calls != 0 {
		t.Fatalf("semantic calls=%d, want 0（kind=import 不查语义通道）", sem.calls)
	}
}

func TestCodeReferencesSemanticCallHeuristic(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	// 第 21 行（1-based）= canonical 20：调用点；第 23 行 = 非调用引用。
	lines := make([]string, 0, 25)
	for i := 1; i <= 25; i++ {
		switch i {
		case 21:
			lines = append(lines, "\tif Target(x) {")
		case 23:
			lines = append(lines, "\t_ = Target")
		default:
			lines = append(lines, "\t// filler")
		}
	}
	target := filepath.Join(root, "internal", "demo", "demo.go")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	sem := &fakeSemanticAdapter{locs: []knowledge.SemanticLocation{
		{Path: "internal/demo/demo.go", Line: 20},
		{Path: "internal/demo/demo.go", Line: 22},
	}}
	tool := buildRefsTool(t, root, demoIndex(), sem)

	result, err := tool.Execute(ctx, map[string]interface{}{"symbol": "Target", "kind": "call"})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("Execute: err=%v result=%+v", err, result)
	}
	env := decodeRefsEnvelope(t, result.Content)
	if env.Source != "lsp" {
		t.Fatalf("source=%q, want lsp", env.Source)
	}
	if len(env.Results) != 1 || env.Results[0].Kind != "call" || env.Results[0].Line != 21 {
		t.Fatalf("results=%+v, want 唯一调用点 line=21 kind=call", env.Results)
	}
}

func TestSemanticAdapterForRespectsGating(t *testing.T) {
	t.Cleanup(closeSemanticAdapters)
	root := t.TempDir()

	// 未启用 → nil（工具面照旧走索引）。
	cfg := knowledge.DefaultConfig().WithWorkspace(root)
	if adapter := semanticAdapterFor(cfg, root, nil); adapter != nil {
		t.Fatalf("disabled: adapter=%v, want nil", adapter)
	}

	// 启用但 workspace 不是 Go 模块 → nil（v1 仅 Go）。
	cfg.LSP.Enabled = true
	cfg.LSP.Mode = "self"
	if adapter := semanticAdapterFor(cfg, root, nil); adapter != nil {
		t.Fatalf("non-go workspace: adapter=%v, want nil", adapter)
	}

	// 启用 + Go 模块 → 构造成功（不启动进程）。
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module demo\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	adapter := semanticAdapterFor(cfg, root, nil)
	if adapter == nil {
		t.Fatal("go workspace: adapter=nil, want constructed")
	}
	if adapter.Available() {
		t.Fatal("构造阶段不得启动进程")
	}
	// 缓存命中：同一 key 返回同一实例。
	if again := semanticAdapterFor(cfg, root, nil); again != adapter {
		t.Fatal("缓存未命中：同一 key 应返回同一适配器实例")
	}
}

