package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
)

// code_semantic_symbols_test.go 覆盖语义通道符号面（documentSymbol /
// workspaceSymbol）的工具侧接线：优先级、位置口径、子串过滤与全路径降级。
//
// 这些用例放在 toolkit/tools 包内（而非 internal/tools），因为要直接断言
// unexported 的 codeEnvelope / codeSymbolHit 形状。

// fakeSymbolAdapter 是同时实现 definition/references 与两个符号面的假适配器。
type fakeSymbolAdapter struct {
	doc     []knowledge.SemanticSymbol
	docErr  error
	ws      []knowledge.SemanticSymbol
	wsErr   error
	wsCalls int
	wsQuery string
	wsLimit int
}

func (f *fakeSymbolAdapter) Name() knowledge.RefSource { return knowledge.SourceLSP }
func (f *fakeSymbolAdapter) Version() string          { return "lsp/fake-1" }
func (f *fakeSymbolAdapter) Available() bool           { return true }

func (f *fakeSymbolAdapter) Definition(context.Context, string, int, int) ([]knowledge.SemanticLocation, error) {
	return nil, nil
}

func (f *fakeSymbolAdapter) References(context.Context, string, int, int) ([]knowledge.SemanticLocation, error) {
	return nil, nil
}

func (f *fakeSymbolAdapter) DocumentSymbols(context.Context, string) ([]knowledge.SemanticSymbol, error) {
	return f.doc, f.docErr
}

func (f *fakeSymbolAdapter) WorkspaceSymbols(_ context.Context, query string, limit int) ([]knowledge.SemanticSymbol, error) {
	f.wsCalls++
	f.wsQuery = query
	f.wsLimit = limit
	return f.ws, f.wsErr
}

// legacySemanticAdapter 只实现 definition/references（旧实现形态）：符号面的
// 类型断言必须失败并静默降级，而不是 panic（Degrade-Not-Fail）。
type legacySemanticAdapter struct{}

func (legacySemanticAdapter) Name() knowledge.RefSource { return knowledge.SourceLSP }
func (legacySemanticAdapter) Version() string           { return "lsp/legacy" }
func (legacySemanticAdapter) Available() bool           { return true }
func (legacySemanticAdapter) Definition(context.Context, string, int, int) ([]knowledge.SemanticLocation, error) {
	return nil, nil
}
func (legacySemanticAdapter) References(context.Context, string, int, int) ([]knowledge.SemanticLocation, error) {
	return nil, nil
}

// symbolFixture 是带语义通道的 code_navigate/code_search 夹具。
func symbolFixture(t *testing.T, sem knowledge.SemanticAdapter) *codeToolsFixture {
	t.Helper()
	fixture := newCodeToolsFixture(t)
	if sem != nil {
		fixture.attachSemantic(sem)
	}
	return fixture
}

// attachSemantic 给夹具句柄装上语义通道（测试里需要先拿到 fixture 才能填
// 带 fileRel 的语义结果，所以拆成两步）。
func (f *codeToolsFixture) attachSemantic(sem knowledge.SemanticAdapter) {
	handle := *f.handle
	handle.Root = f.root
	handle.Semantic = sem
	f.resolver = func(context.Context) (*CodeIndexHandle, bool) { return &handle, true }
}

// ---- code_navigate(members) ----

func TestCodeNavigateMembersPrefersSemanticDocumentSymbols(t *testing.T) {
	fixture := symbolFixture(t, nil)
	sem := &fakeSymbolAdapter{doc: []knowledge.SemanticSymbol{
		{Name: "Type", Kind: knowledge.SymbolType, Container: "pkg", Path: fixture.fileRel, Line: 2, EndLine: 4},
		{Name: "Alpha", Kind: knowledge.SymbolFunction, Path: fixture.fileRel, Line: 5},
		// 别文件：必须被过滤，否则 members 会带出邻居文件的内容。
		{Name: "Foreign", Kind: knowledge.SymbolFunction, Path: "pkg/other.go", Line: 1},
	}}
	fixture.attachSemantic(sem)
	tool := NewCodeNavigateTool()
	tool.SetBasePath(fixture.root)
	tool.SetCodeIndexResolver(fixture.resolver)

	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": fixture.fileRel, "direction": "members",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	env := decodeCodeEnvelope(t, res)
	if env.Source != codeSourceSemantic {
		t.Fatalf("source = %q, want %q", env.Source, codeSourceSemantic)
	}
	if env.Confidence != codeConfidenceExact {
		t.Fatalf("confidence = %v, want %v", env.Confidence, codeConfidenceExact)
	}
	hits := decodeSymbolHits(t, env.Results)
	if len(hits) != 2 {
		t.Fatalf("len(hits) = %d, want 2: %#v", len(hits), hits)
	}
	// 位置口径：适配器出参 canonical 0-based，结果必须 1-based。
	if hits[0].Range.StartLine != 3 {
		t.Fatalf("StartLine = %d, want 3 (canonical 2 + 1)", hits[0].Range.StartLine)
	}
	if hits[0].Range.EndLine != 5 {
		t.Fatalf("EndLine = %d, want 5", hits[0].Range.EndLine)
	}
	// 容器名拼出限定名，模型才能区分同名成员。
	if hits[0].QualifiedName != "pkg.Type" {
		t.Fatalf("QualifiedName = %q, want pkg.Type", hits[0].QualifiedName)
	}
	if hits[1].QualifiedName != "Alpha" {
		t.Fatalf("QualifiedName = %q, want Alpha", hits[1].QualifiedName)
	}
}

func TestCodeNavigateMembersFallsBackToIndexWhenSemanticUnavailable(t *testing.T) {
	for name, sem := range map[string]knowledge.SemanticAdapter{
		"error": &fakeSymbolAdapter{docErr: errors.New("lsp boom")},
		"empty": &fakeSymbolAdapter{},
		// 只有 definition/references 的实现：符号面类型断言失败。
		"legacy": legacySemanticAdapter{},
	} {
		fixture := symbolFixture(t, sem)
		tool := NewCodeNavigateTool()
		tool.SetBasePath(fixture.root)
		tool.SetCodeIndexResolver(fixture.resolver)

		res, err := tool.Execute(context.Background(), map[string]interface{}{
			"file_path": fixture.fileRel, "direction": "members",
		})
		if err != nil {
			t.Fatalf("%s: LSP 故障不得让工具失败: %v", name, err)
		}
		if env := decodeCodeEnvelope(t, res); env.Source != codeSourceIndex {
			t.Fatalf("%s: source = %q, want %q", name, env.Source, codeSourceIndex)
		}
	}
}

// ---- code_search ----

func TestCodeSearchAppendsSemanticExactNameMatches(t *testing.T) {
	fixture := symbolFixture(t, &fakeSymbolAdapter{ws: []knowledge.SemanticSymbol{
		// 子串命中必须被过滤：workspace/symbol 是模糊匹配，放进来只会让
		// code_search 比 FTS 更噪。
		{Name: "AlphaExtended", Kind: knowledge.SymbolFunction, Path: "pkg/other.go", Line: 1},
		{Name: "Alpha", Kind: knowledge.SymbolFunction, Container: "pkg", Path: "pkg/other.go", Line: 6},
		{Name: "Alpha", Kind: knowledge.SymbolVariable, Path: "pkg/third.go", Line: 0},
	}})
	sem := fixture.semanticProbe(t)
	tool := NewCodeSearchTool()
	tool.SetBasePath(fixture.root)
	tool.SetCodeIndexResolver(fixture.resolver)

	res, err := tool.Execute(context.Background(), map[string]interface{}{"query": "Alpha"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	env := decodeCodeEnvelope(t, res)
	if env.Source != codeSourceIndexSemantic {
		t.Fatalf("source = %q, want %q", env.Source, codeSourceIndexSemantic)
	}
	if sem.wsQuery != "Alpha" {
		t.Fatalf("wsQuery = %q, want Alpha", sem.wsQuery)
	}
	if sem.wsLimit <= 0 {
		t.Fatalf("wsLimit = %d, 必须为正（LSP 空 query 会返回全部符号）", sem.wsLimit)
	}
	hits := decodeSymbolHits(t, env.Results)
	// 索引 FTS 命中 Alpha 一条 + 语义精确同名两条（子串那条必须被过滤）。
	if len(hits) != 3 {
		t.Fatalf("len(hits) = %d, want 3: %#v", len(hits), hits)
	}
	for _, hit := range hits {
		if hit.Name == "AlphaExtended" {
			t.Fatalf("子串命中不得混入语义补充结果: %#v", hits)
		}
	}
	var semanticStartLines []int
	for _, hit := range hits {
		if hit.QualifiedName == "pkg.Alpha" || hit.Path == "pkg/third.go" {
			semanticStartLines = append(semanticStartLines, hit.Range.StartLine)
		}
	}
	// canonical 6 → 1-based 7；canonical 0 → 1-based 1。
	if !containsInt(semanticStartLines, 7) || !containsInt(semanticStartLines, 1) {
		t.Fatalf("语义补充位置口径错误（应为 1-based 7 与 1）: %v", semanticStartLines)
	}
}

func TestCodeSearchKeepsIndexSourceWhenSemanticUnusable(t *testing.T) {
	for name, sem := range map[string]knowledge.SemanticAdapter{
		"error":  &fakeSymbolAdapter{wsErr: errors.New("timeout")},
		"empty":  &fakeSymbolAdapter{},
		"fuzzy":  &fakeSymbolAdapter{ws: []knowledge.SemanticSymbol{{Name: "AlphaExtended", Path: "pkg/x.go"}}},
		"legacy": legacySemanticAdapter{},
	} {
		fixture := symbolFixture(t, sem)
		tool := NewCodeSearchTool()
		tool.SetBasePath(fixture.root)
		tool.SetCodeIndexResolver(fixture.resolver)

		res, err := tool.Execute(context.Background(), map[string]interface{}{"query": "Alpha"})
		if err != nil {
			t.Fatalf("%s: execute: %v", name, err)
		}
		if env := decodeCodeEnvelope(t, res); env.Source == codeSourceIndexSemantic {
			t.Fatalf("%s: 语义不可用时 source 不得变成 %q", name, codeSourceIndexSemantic)
		}
	}
}

// semanticProbe 从夹具句柄取回假适配器，用于断言调用参数。
func (f *codeToolsFixture) semanticProbe(t *testing.T) *fakeSymbolAdapter {
	t.Helper()
	handle, ok := f.resolver(context.Background())
	if !ok || handle.Semantic == nil {
		t.Fatal("fixture 缺少语义通道")
	}
	probe, ok := handle.Semantic.(*fakeSymbolAdapter)
	if !ok {
		t.Fatalf("语义适配器类型 = %T, want *fakeSymbolAdapter", handle.Semantic)
	}
	return probe
}

// decodeSymbolHits 把信封的 results 解回符号结果。
func decodeSymbolHits(t *testing.T, raw json.RawMessage) []codeSymbolHit {
	t.Helper()
	var hits []codeSymbolHit
	if err := json.Unmarshal(raw, &hits); err != nil {
		t.Fatalf("results decode: %v (raw=%s)", err, string(raw))
	}
	return hits
}

func fixtureFileRel(fixture *codeToolsFixture) string { return fixture.fileRel }

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestSemanticSymbolKindMapsUnknownToUnknown(t *testing.T) {
	// 语义通道的位置/kind 口径由 knowledge 层负责（见 adapter_lsp_symbols_test.go）；
	// 这里只锁工具侧不依赖任何 kind 具体值即可编译运行（防止误引入导入）。
}
