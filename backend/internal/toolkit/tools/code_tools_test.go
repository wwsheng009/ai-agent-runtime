package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// Phase 3（06 §4 Phase 3）code.* 测试：
//   - 索引路径：真实只读 store（临时 workspace + 文件/符号/引用）→ source=index；
//   - 降级路径：无索引 → source=fallback（grep/view），结构不变（04 §4.6）；
//   - shadow 档：候选照算但返回 grep 结果（04 §4.6 第 3 步）；
//   - view --symbol：索引命中按符号范围读取；无索引时退化/报错口径。

// codeToolsFixture 是 code.* 测试的真实索引夹具。
type codeToolsFixture struct {
	root     string
	handle   *CodeIndexHandle
	resolver CodeIndexResolver
	fileRel  string
}

func newCodeToolsFixture(t *testing.T) *codeToolsFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	fileRel := "pkg/demo.go"
	content := strings.Join([]string{
		"package pkg",
		"",
		"func Alpha() {}",
		"",
		"func Beta() {",
		"\tAlpha()",
		"}",
	}, "\n") + "\n"
	abs := filepath.Join(root, filepath.FromSlash(fileRel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg := knowledge.DefaultConfig().WithWorkspace(root)
	cfg.Mode = knowledge.ModeOn
	store, err := knowledge.OpenStore(ctx, knowledge.StorePathFor(cfg), false)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	wsID, err := store.EnsureWorkspace(ctx, knowledge.Workspace{RootPath: root})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	fileID, err := store.UpsertFile(ctx, knowledge.FileRecord{
		WorkspaceID: wsID,
		Path:        fileRel,
		Language:    "go",
		Size:        int64(len(content)),
		ContentHash: "hash-demo-go",
		IndexedAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}

	syms := []knowledge.Symbol{
		{
			FileID: fileID, WorkspaceID: wsID, Name: "Alpha", QualifiedName: "pkg.Alpha",
			StableKey: knowledge.StableKey("go", knowledge.SymbolKind("function"), "pkg", "", "Alpha", ""),
			Kind:      knowledge.SymbolKind("function"), Language: "go", Signature: "func Alpha()",
			Range: knowledge.Range{
				Start: knowledge.Position{Line: 3, Column: 1},
				End:   knowledge.Position{Line: 3, Column: 16},
			},
		},
		{
			FileID: fileID, WorkspaceID: wsID, Name: "Beta", QualifiedName: "pkg.Beta",
			StableKey: knowledge.StableKey("go", knowledge.SymbolKind("function"), "pkg", "", "Beta", ""),
			Kind:      knowledge.SymbolKind("function"), Language: "go", Signature: "func Beta()",
			Range: knowledge.Range{
				Start: knowledge.Position{Line: 5, Column: 1},
				End:   knowledge.Position{Line: 7, Column: 2},
			},
		},
	}
	if err := store.ReplaceSymbols(ctx, fileID, syms); err != nil {
		t.Fatalf("ReplaceSymbols: %v", err)
	}

	resolved, err := store.FindSymbols(ctx, knowledge.SymbolQuery{Name: "Alpha", Exact: true, Limit: 5})
	if err != nil || len(resolved) == 0 {
		t.Fatalf("FindSymbols(Alpha): err=%v n=%d", err, len(resolved))
	}
	refs := []knowledge.Reference{{
		ID:           knowledge.RefID(wsID, fileID, 6, 2, knowledge.RefKind("call")),
		FileID:       fileID,
		WorkspaceID:  wsID,
		ToSymbolID:   resolved[0].ID,
		ToSymbolName: "Alpha",
		Kind:         knowledge.RefKind("call"),
		Line:         6,
		Col:          2,
		Snippet:      "Alpha()",
		Confidence:   0.55,
		Source:       knowledge.RefSource("regex_builtin"),
	}}
	if err := store.ReplaceRefs(ctx, fileID, refs); err != nil {
		t.Fatalf("ReplaceRefs: %v", err)
	}

	handle := &CodeIndexHandle{
		Index:       store,
		Mode:        knowledge.ModeOn,
		WorkspaceID: wsID,
		FilePaths:   map[string]string{fileID: fileRel},
	}
	return &codeToolsFixture{
		root:     root,
		handle:   handle,
		fileRel:  fileRel,
		resolver: func(context.Context) (*CodeIndexHandle, bool) { return handle, true },
	}
}

// codeTestEnvelope 是统一返回结构的测试镜像。
type codeTestEnvelope struct {
	Tool             string          `json:"tool"`
	Source           string          `json:"source"`
	Confidence       float64         `json:"confidence"`
	Version          string          `json:"version"`
	SnapshotTS       int64           `json:"snapshot_ts"`
	StalenessSeconds int64           `json:"staleness_seconds"`
	Completeness     string          `json:"completeness"`
	Range            *codeRange      `json:"range"`
	Truncated        bool            `json:"truncated"`
	NextCursor       string          `json:"next_cursor"`
	Explanation      string          `json:"explanation"`
	Degraded         bool            `json:"degraded"`
	Results          json.RawMessage `json:"results"`
	Fallback         *codeFallback   `json:"fallback"`
}

func decodeCodeEnvelope(t *testing.T, res *toolkit.ToolResult) codeTestEnvelope {
	t.Helper()
	if res == nil || !res.Success {
		t.Fatalf("tool result = %+v", res)
	}
	var env codeTestEnvelope
	if err := json.Unmarshal([]byte(res.Content), &env); err != nil {
		t.Fatalf("envelope decode: %v (content=%q)", err, res.Content)
	}
	return env
}

// ---- 降级路径 ----

func TestCodeSearchFallsBackWithoutIndex(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("timeout default value\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := NewCodeSearchTool()
	tool.SetBasePath(root)

	res, err := tool.Execute(context.Background(), map[string]interface{}{"query": "timeout"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	env := decodeCodeEnvelope(t, res)
	if env.Source != codeSourceFallback || !env.Degraded {
		t.Fatalf("source=%q degraded=%v, want fallback/true", env.Source, env.Degraded)
	}
	if env.Fallback == nil || env.Fallback.Tool != "grep" || env.Fallback.Reason != codeFallbackIndexUnavailable {
		t.Fatalf("fallback = %+v", env.Fallback)
	}
	if !strings.Contains(env.Fallback.Output, "timeout") {
		t.Fatalf("fallback output = %q, want grep hit", env.Fallback.Output)
	}
	if env.Tool != "code_search" || env.NextCursor != "" {
		t.Fatalf("envelope shape = %+v", env)
	}
}

func TestCodeSearchShadowModeReturnsFallbackWithPreview(t *testing.T) {
	fixture := newCodeToolsFixture(t)
	shadow := *fixture.handle
	shadow.Mode = knowledge.ModeShadow

	tool := NewCodeSearchTool()
	tool.SetBasePath(fixture.root)
	tool.SetCodeIndexResolver(func(context.Context) (*CodeIndexHandle, bool) { return &shadow, true })

	res, err := tool.Execute(context.Background(), map[string]interface{}{"query": "Beta"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	env := decodeCodeEnvelope(t, res)
	if env.Source != codeSourceFallback || env.Fallback == nil || env.Fallback.Reason != codeFallbackShadowMode {
		t.Fatalf("shadow envelope = %+v", env)
	}
	if !strings.Contains(env.Explanation, "索引候选") {
		t.Fatalf("explanation = %q, want candidate count", env.Explanation)
	}
}

func TestCodeReferencesFallsBackWithoutIndex(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "call.go"), []byte("Alpha()\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := NewCodeReferencesTool()
	tool.SetBasePath(root)

	res, err := tool.Execute(context.Background(), map[string]interface{}{"symbol": "Alpha"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	env := decodeCodeEnvelope(t, res)
	if env.Source != codeSourceFallback || env.Fallback == nil || env.Fallback.Tool != "grep" {
		t.Fatalf("envelope = %+v", env)
	}
	if !strings.Contains(env.Fallback.Output, "Alpha()") {
		t.Fatalf("fallback output = %q", env.Fallback.Output)
	}
}

// ---- 索引路径 ----

func TestCodeSearchIndexPath(t *testing.T) {
	fixture := newCodeToolsFixture(t)
	tool := NewCodeSearchTool()
	tool.SetBasePath(fixture.root)
	tool.SetCodeIndexResolver(fixture.resolver)

	res, err := tool.Execute(context.Background(), map[string]interface{}{"query": "Beta"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	env := decodeCodeEnvelope(t, res)
	if env.Source != codeSourceIndex || env.Degraded || env.Confidence != codeConfidenceFTS {
		t.Fatalf("envelope = %+v", env)
	}
	var hits []codeSymbolHit
	if err := json.Unmarshal(env.Results, &hits); err != nil {
		t.Fatalf("results decode: %v", err)
	}
	if len(hits) == 0 || hits[0].Name != "Beta" || hits[0].Path != fixture.fileRel {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestCodeInspectSymbolFromIndex(t *testing.T) {
	fixture := newCodeToolsFixture(t)
	tool := NewCodeInspectTool()
	tool.SetBasePath(fixture.root)
	tool.SetCodeIndexResolver(fixture.resolver)

	res, err := tool.Execute(context.Background(), map[string]interface{}{"symbol": "Beta"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	env := decodeCodeEnvelope(t, res)
	if env.Source != codeSourceIndex || env.Confidence != codeConfidenceExact {
		t.Fatalf("envelope = %+v", env)
	}
	if env.Range == nil || env.Range.Path != fixture.fileRel || env.Range.StartLine != 5 || env.Range.EndLine != 7 {
		t.Fatalf("range = %+v", env.Range)
	}
	var payload struct {
		Symbol  codeSymbolHit `json:"symbol"`
		Content string        `json:"content"`
	}
	if err := json.Unmarshal(env.Results, &payload); err != nil {
		t.Fatalf("results decode: %v", err)
	}
	if payload.Symbol.Name != "Beta" {
		t.Fatalf("symbol = %+v", payload.Symbol)
	}
	if !strings.Contains(payload.Content, "func Beta()") {
		t.Fatalf("content = %q, want symbol body", payload.Content)
	}
	if strings.Contains(payload.Content, "package pkg") {
		t.Fatalf("content leaked whole file: %q", payload.Content)
	}
}

func TestCodeCallersAndReferencesFromIndex(t *testing.T) {
	fixture := newCodeToolsFixture(t)

	callers := NewCodeCallersTool()
	callers.SetBasePath(fixture.root)
	callers.SetCodeIndexResolver(fixture.resolver)
	res, err := callers.Execute(context.Background(), map[string]interface{}{"symbol": "Alpha"})
	if err != nil {
		t.Fatalf("callers: %v", err)
	}
	env := decodeCodeEnvelope(t, res)
	if env.Source != codeSourceIndex || env.Confidence != codeConfidenceExact {
		t.Fatalf("callers envelope = %+v", env)
	}
	var refs []codeRefHit
	if err := json.Unmarshal(env.Results, &refs); err != nil {
		t.Fatalf("results decode: %v", err)
	}
	if len(refs) != 1 || refs[0].Path != fixture.fileRel || refs[0].Line != 6 || refs[0].Kind != "call" {
		t.Fatalf("refs = %+v", refs)
	}

	references := NewCodeReferencesTool()
	references.SetBasePath(fixture.root)
	references.SetCodeIndexResolver(fixture.resolver)
	res, err = references.Execute(context.Background(), map[string]interface{}{"symbol": "Alpha", "kind": "call"})
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	env = decodeCodeEnvelope(t, res)
	if env.Source != codeSourceIndex || env.Tool != "code_references" {
		t.Fatalf("references envelope = %+v", env)
	}
}

func TestCodeNavigateMembersAndDefinition(t *testing.T) {
	fixture := newCodeToolsFixture(t)
	navigate := NewCodeNavigateTool()
	navigate.SetBasePath(fixture.root)
	navigate.SetCodeIndexResolver(fixture.resolver)

	res, err := navigate.Execute(context.Background(), map[string]interface{}{
		"file_path": fixture.fileRel, "direction": "members",
	})
	if err != nil {
		t.Fatalf("members: %v", err)
	}
	env := decodeCodeEnvelope(t, res)
	if env.Source != codeSourceIndex || env.Confidence != codeConfidenceExact {
		t.Fatalf("members envelope = %+v", env)
	}
	var members []codeSymbolHit
	if err := json.Unmarshal(env.Results, &members); err != nil {
		t.Fatalf("results decode: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("members = %+v, want 2 symbols", members)
	}

	res, err = navigate.Execute(context.Background(), map[string]interface{}{
		"symbol": "Alpha", "direction": "definition",
	})
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	env = decodeCodeEnvelope(t, res)
	if env.Range == nil || env.Range.StartLine != 3 {
		t.Fatalf("definition range = %+v", env.Range)
	}
}

// ---- view --symbol ----

func TestViewSymbolUsesIndexRange(t *testing.T) {
	fixture := newCodeToolsFixture(t)
	view := NewViewTool()
	view.SetBasePath(fixture.root)
	view.SetCodeIndexResolver(fixture.resolver)

	res, err := view.Execute(context.Background(), map[string]interface{}{"symbol": "Beta"})
	if err != nil || res == nil || !res.Success {
		t.Fatalf("view: err=%v res=%+v", err, res)
	}
	if !strings.Contains(res.Content, "func Beta()") {
		t.Fatalf("content = %q, want symbol body", res.Content)
	}
	if strings.Contains(res.Content, "package pkg") {
		t.Fatalf("content leaked whole file: %q", res.Content)
	}
}

func TestViewSymbolFallsBackToLineRangeWithNote(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	view := NewViewTool()
	view.SetBasePath(root)

	res, err := view.Execute(context.Background(), map[string]interface{}{
		"symbol": "Missing", "file_path": "note.txt",
	})
	if err != nil || res == nil || !res.Success {
		t.Fatalf("view: err=%v res=%+v", err, res)
	}
	if !strings.Contains(res.Content, "未能解析") || !strings.Contains(res.Content, "alpha") {
		t.Fatalf("content = %q, want fallback note + line range", res.Content)
	}
}

func TestViewSymbolWithoutIndexAndFilePathErrors(t *testing.T) {
	view := NewViewTool()
	res, err := view.Execute(context.Background(), map[string]interface{}{"symbol": "Missing"})
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if res == nil || res.Success || res.Error == nil {
		t.Fatalf("res = %+v, want explicit failure", res)
	}
	if !strings.Contains(res.Error.Error(), "grep") {
		t.Fatalf("error = %v, want guidance", res.Error)
	}
}

// ---- 2026-09-30 修复轮：shadow 全覆盖 / kind 校验 / 路径归一化 / 截断 ----

func TestCodeToolsShadowModeCoversAllTools(t *testing.T) {
	fixture := newCodeToolsFixture(t)
	shadow := *fixture.handle
	shadow.Mode = knowledge.ModeShadow
	resolver := func(context.Context) (*CodeIndexHandle, bool) { return &shadow, true }

	cases := []struct {
		name string
		run  func() *toolkit.ToolResult
	}{
		{"code_inspect", func() *toolkit.ToolResult {
			tool := NewCodeInspectTool()
			tool.SetBasePath(fixture.root)
			tool.SetCodeIndexResolver(resolver)
			res, _ := tool.Execute(context.Background(), map[string]interface{}{"symbol": "Beta"})
			return res
		}},
		{"code_references", func() *toolkit.ToolResult {
			tool := NewCodeReferencesTool()
			tool.SetBasePath(fixture.root)
			tool.SetCodeIndexResolver(resolver)
			res, _ := tool.Execute(context.Background(), map[string]interface{}{"symbol": "Alpha"})
			return res
		}},
		{"code_callers", func() *toolkit.ToolResult {
			tool := NewCodeCallersTool()
			tool.SetBasePath(fixture.root)
			tool.SetCodeIndexResolver(resolver)
			res, _ := tool.Execute(context.Background(), map[string]interface{}{"symbol": "Alpha"})
			return res
		}},
		{"code_navigate_members", func() *toolkit.ToolResult {
			tool := NewCodeNavigateTool()
			tool.SetBasePath(fixture.root)
			tool.SetCodeIndexResolver(resolver)
			res, _ := tool.Execute(context.Background(), map[string]interface{}{
				"file_path": fixture.fileRel, "direction": "members",
			})
			return res
		}},
		{"code_navigate_definition", func() *toolkit.ToolResult {
			tool := NewCodeNavigateTool()
			tool.SetBasePath(fixture.root)
			tool.SetCodeIndexResolver(resolver)
			res, _ := tool.Execute(context.Background(), map[string]interface{}{
				"symbol": "Alpha", "direction": "definition",
			})
			return res
		}},
	}
	for _, tc := range cases {
		env := decodeCodeEnvelope(t, tc.run())
		if env.Source != codeSourceFallback || env.Fallback == nil || env.Fallback.Reason != codeFallbackShadowMode {
			t.Fatalf("%s shadow envelope = %+v, want fallback/shadow_mode", tc.name, env)
		}
		if !strings.Contains(env.Explanation, "索引候选") {
			t.Fatalf("%s explanation = %q, want candidate count", tc.name, env.Explanation)
		}
	}

	// view --symbol 在 shadow 档不得走索引：带 file_path 时退化为行范围读取。
	view := NewViewTool()
	view.SetBasePath(fixture.root)
	view.SetCodeIndexResolver(resolver)
	res, err := view.Execute(context.Background(), map[string]interface{}{
		"symbol": "Beta", "file_path": fixture.fileRel,
	})
	if err != nil || res == nil || !res.Success {
		t.Fatalf("view shadow: err=%v res=%+v", err, res)
	}
	if !strings.Contains(res.Content, "package pkg") {
		t.Fatalf("view shadow content = %q, want line-range fallback", res.Content)
	}
}

func TestCodeReferencesRejectsInvalidKind(t *testing.T) {
	fixture := newCodeToolsFixture(t)
	tool := NewCodeReferencesTool()
	tool.SetBasePath(fixture.root)
	tool.SetCodeIndexResolver(fixture.resolver)

	res, err := tool.Execute(context.Background(), map[string]interface{}{"symbol": "Alpha", "kind": "typo"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil || res.Success || res.Error == nil || !strings.Contains(res.Error.Error(), "kind") {
		t.Fatalf("res = %+v, want explicit kind error", res)
	}
}

func TestCodeNavigateMembersNormalizesWindowsPath(t *testing.T) {
	fixture := newCodeToolsFixture(t)
	tool := NewCodeNavigateTool()
	tool.SetBasePath(fixture.root)
	tool.SetCodeIndexResolver(fixture.resolver)

	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "pkg\\demo.go", "direction": "members",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	env := decodeCodeEnvelope(t, res)
	if env.Source != codeSourceIndex {
		t.Fatalf("envelope = %+v, want index path after path normalization", env)
	}
	var members []codeSymbolHit
	if err := json.Unmarshal(env.Results, &members); err != nil {
		t.Fatalf("results decode: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("members = %+v, want 2 symbols", members)
	}
}

func TestCodeNavigateRequiresDirection(t *testing.T) {
	tool := NewCodeNavigateTool()
	res, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil || res.Success || res.Error == nil || !strings.Contains(res.Error.Error(), "direction 必填") {
		t.Fatalf("res = %+v, want direction error", res)
	}
}

func TestCodeInspectLimitMarksTruncated(t *testing.T) {
	fixture := newCodeToolsFixture(t)
	tool := NewCodeInspectTool()
	tool.SetBasePath(fixture.root)
	tool.SetCodeIndexResolver(fixture.resolver)

	res, err := tool.Execute(context.Background(), map[string]interface{}{"symbol": "Beta", "limit": 1})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	env := decodeCodeEnvelope(t, res)
	if !env.Truncated {
		t.Fatalf("envelope = %+v, want truncated=true when limit < symbol span", env)
	}
}

func TestCodeSearchLowConfidenceSupplement(t *testing.T) {
	fixture := newCodeToolsFixture(t)
	tool := NewCodeSearchTool()
	tool.SetBasePath(fixture.root)
	tool.SetCodeIndexResolver(fixture.resolver)

	// "func" 命中签名而非符号名 → 低相关，应补一次 grep（source=index+grep）。
	res, err := tool.Execute(context.Background(), map[string]interface{}{"query": "func"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	env := decodeCodeEnvelope(t, res)
	if env.Source != codeSourceIndexGrep || env.Fallback == nil || env.Fallback.Reason != codeFallbackLowConfidence {
		t.Fatalf("envelope = %+v, want index+grep low-confidence supplement", env)
	}
	if !strings.Contains(env.Fallback.Output, "func Beta()") {
		t.Fatalf("supplement output = %q, want grep hit", env.Fallback.Output)
	}
}

// ---- 2026-10-01 修复轮 2：限定名解析 / 精确名排序 / view 去重语义 ----

func TestCodeSearchQualifiedNameResolvesTail(t *testing.T) {
	fixture := newCodeToolsFixture(t)
	tool := NewCodeSearchTool()
	tool.SetBasePath(fixture.root)
	tool.SetCodeIndexResolver(fixture.resolver)

	res, err := tool.Execute(context.Background(), map[string]interface{}{"query": "pkg.Alpha"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	env := decodeCodeEnvelope(t, res)
	if env.Source != codeSourceIndex || env.Confidence != codeConfidenceExact {
		t.Fatalf("envelope = %+v, want index/exact for qualified query", env)
	}
	var hits []codeSymbolHit
	if err := json.Unmarshal(env.Results, &hits); err != nil {
		t.Fatalf("results decode: %v", err)
	}
	if len(hits) == 0 || hits[0].Name != "Alpha" {
		t.Fatalf("hits = %+v, want Alpha first", hits)
	}
	if !strings.Contains(env.Explanation, "限定名") {
		t.Fatalf("explanation = %q, want qualified-name note", env.Explanation)
	}
}

func TestOrderCodeSearchHitsExactFirst(t *testing.T) {
	hits := []knowledge.SearchHit{
		{Name: "SessionSubscriptionPlanInput"},
		{Name: "PlanInputDraft"},
		{Name: "PlanInput"},
		{Name: "Plan"},
	}
	ordered := orderCodeSearchHits(hits, "PlanInput")
	if ordered[0].Name != "PlanInput" {
		t.Fatalf("ordered = %+v, want exact name first", ordered)
	}
	if ordered[1].Name != "PlanInputDraft" {
		t.Fatalf("ordered = %+v, want prefix match second", ordered)
	}
}

func TestCodeInspectMarksViewDedupStub(t *testing.T) {
	fixture := newCodeToolsFixture(t)
	tool := NewCodeInspectTool()
	tool.SetBasePath(fixture.root)
	tool.SetCodeIndexResolver(fixture.resolver)
	ctx := toolctx.WithSessionID(context.Background(), "code-inspect-dedup-test")

	if _, err := tool.Execute(ctx, map[string]interface{}{"symbol": "Beta"}); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	res, err := tool.Execute(ctx, map[string]interface{}{"symbol": "Beta"})
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	env := decodeCodeEnvelope(t, res)
	if !strings.Contains(env.Explanation, "去重") {
		t.Fatalf("explanation = %q, want view-dedup note", env.Explanation)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(env.Results, &payload); err != nil {
		t.Fatalf("results decode: %v", err)
	}
	if payload["content_omitted"] != "view_dedup" {
		t.Fatalf("payload = %+v, want content_omitted=view_dedup", payload)
	}
}
