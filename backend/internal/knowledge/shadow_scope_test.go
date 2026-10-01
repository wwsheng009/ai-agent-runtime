package knowledge

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// capturingIndex 在 fakeShadowIndex 基础上记录最后一次查询，用于断言
// 观察器把绝对路径折叠成了相对作用域。
type capturingIndex struct {
	fakeShadowIndex
	lastSymbols SymbolQuery
	lastSearch  SearchQuery
	searches    []SearchQuery
}

func (c *capturingIndex) Search(ctx context.Context, q SearchQuery) ([]SearchHit, error) {
	c.lastSearch = q
	c.searches = append(c.searches, q)
	return c.fakeShadowIndex.Search(ctx, q)
}

func (c *capturingIndex) FindSymbols(ctx context.Context, q SymbolQuery) ([]Symbol, error) {
	c.lastSymbols = q
	return c.fakeShadowIndex.FindSymbols(ctx, q)
}

// TestShadowObserver_AbsoluteWorkspaceScopeIsRelativized 钉住实测发现的缺陷：
// 调用传绝对 workspace 路径时，候选必须与索引的相对 path 对齐，否则恒为空。
func TestShadowObserver_AbsoluteWorkspaceScopeIsRelativized(t *testing.T) {
	index := &capturingIndex{fakeShadowIndex: fakeShadowIndex{hits: []SearchHit{
		{Path: "backend/a.go", Line: 10, Name: "A"},
	}}}
	workspace := `E:\projects\ai\ai-agent-runtime`
	obs := NewShadowObserver(ShadowConfig{Mode: ModeShadow, Workspace: workspace, Index: index})

	rec, err := obs.Observe(context.Background(), ObservedCall{
		Tool:   "grep",
		Args:   map[string]any{"pattern": "A", "path": workspace},
		Output: "backend/a.go:10:func A",
	})
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.Equal(t, 1, rec.CandidateN, "绝对 path 折叠为相对路径后候选必须命中")
	require.Equal(t, 1, rec.OverlapN)
	require.NotNil(t, rec.Coverage)
	require.InDelta(t, 1.0, *rec.Coverage, 1e-9)
}

// TestShadowObserver_GlobScopeMatchesBasenameAndSegments 钉住 glob 作用域语义：
// `*.go` 按 basename 匹配，`backend/**/*.go` 按段匹配，无作用域时全放行。
func TestShadowObserver_GlobScopeMatchesBasenameAndSegments(t *testing.T) {
	index := &fakeShadowIndex{hits: []SearchHit{
		{Path: "backend/a.go", Line: 10, Name: "A"},
		{Path: "frontend/b.ts", Line: 5, Name: "B"},
	}}
	obs := NewShadowObserver(ShadowConfig{Mode: ModeShadow, Index: index})

	rec, err := obs.Observe(context.Background(), ObservedCall{
		Tool: "grep", Args: map[string]any{"pattern": "A", "glob": "*.go"},
		Output: "backend/a.go:10:func A",
	})
	require.NoError(t, err)
	require.Equal(t, 1, rec.CandidateN, "*.go 必须按 basename 匹配（只放行 a.go）")

	rec, err = obs.Observe(context.Background(), ObservedCall{
		Tool: "grep", Args: map[string]any{"pattern": "A", "glob": "backend/**/*.go"},
		Output: "backend/a.go:10:func A",
	})
	require.NoError(t, err)
	require.Equal(t, 1, rec.CandidateN, "backend/**/*.go 必须按段匹配")

	rec, err = obs.Observe(context.Background(), ObservedCall{
		Tool: "grep", Args: map[string]any{"pattern": "A"},
		Output: "backend/a.go:10:func A",
	})
	require.NoError(t, err)
	require.Equal(t, 2, rec.CandidateN, "无作用域必须放行全部候选")
}

// TestShadowObserver_ViewRelativizesAbsoluteFilePath 钉住 view 的 file_path
// 同样需要相对化，否则 PathPrefix 查询永远落空。
func TestShadowObserver_ViewRelativizesAbsoluteFilePath(t *testing.T) {
	index := &capturingIndex{fakeShadowIndex: fakeShadowIndex{syms: []Symbol{
		{Name: "A", QualifiedName: "A", Range: Range{Start: Position{Line: 2}, End: Position{Line: 3}}},
	}}}
	workspace := `E:\projects\ai\ai-agent-runtime`
	obs := NewShadowObserver(ShadowConfig{Mode: ModeShadow, Workspace: workspace, Index: index})

	rec, err := obs.Observe(context.Background(), ObservedCall{
		Tool: "view",
		Args: map[string]any{
			"file_path": workspace + `\backend\a.go`,
			"offset":    1,
			"limit":     5,
		},
		Output: "1: package a\n2: func A() {}\n3: }\n",
	})
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.Equal(t, "backend/a.go", index.lastSymbols.PathPrefix)
	require.Equal(t, 1, rec.CandidateN)
	require.Equal(t, 2, rec.OverlapN)
}

// TestParseGrepBaselineAppliesScopePrefix 钉住 grep 输出路径补全：rg 以 path
// 参数为根输出相对路径，必须补成 workspace 相对路径才能与候选相交。
func TestParseGrepBaselineAppliesScopePrefix(t *testing.T) {
	keys, _ := parseGrepBaseline(
		"internal/agent/a.go:5315:x\nbackend/internal/b.go:10:y\nE:/abs/c.go:3:z",
		"backend",
	)
	require.Equal(t, []string{
		"backend/internal/agent/a.go:5315",
		"backend/internal/b.go:10",
		"E:/abs/c.go:3",
	}, keys)
	// glob 作用域不补前缀；无作用域原样返回。
	keys, _ = parseGrepBaseline("internal/a.go:1:x", "*.go")
	require.Equal(t, []string{"internal/a.go:1"}, keys)
	keys, _ = parseGrepBaseline("internal/a.go:1:x", "")
	require.Equal(t, []string{"internal/a.go:1"}, keys)
}

// TestScopeFilterSpecMultiPaths 钉住多路径作用域：前缀 OR、glob AND、绝对路径折叠。
func TestScopeFilterSpecMultiPaths(t *testing.T) {
	filter := newScopeFilterSpecMulti([]string{"backend", "docs"}, "*.go", "E:/ws")
	require.True(t, filter.matches("backend/internal/a.go"))
	require.True(t, filter.matches("docs/x/b.go"))
	require.False(t, filter.matches("frontend/app.js"))
	require.False(t, filter.matches("backend/readme.md"))

	filter = newScopeFilterSpecMulti([]string{"E:/ws/backend"}, "", "E:/ws")
	require.True(t, filter.matches("backend/x.go"))
	require.False(t, filter.matches("docs/x.go"))
}

// TestParseGrepBaselineMultiPaths 钉住多作用域基准的逐行归属与首项回退。
func TestParseGrepBaselineMultiPaths(t *testing.T) {
	keys, _ := parseGrepBaselineMulti(
		"backend/a.go:3:x\ndocs/b.md:7:y\ninternal/c.go:1:z",
		[]string{"backend", "docs"},
	)
	require.Equal(t, []string{
		"backend/a.go:3",
		"docs/b.md:7",
		"backend/internal/c.go:1",
	}, keys)
}

// TestGrepPathScopes 钉住 path + paths 的收集、去重与字符串单值语义。
func TestGrepPathScopes(t *testing.T) {
	args := map[string]any{"path": "backend", "paths": []any{"docs", "backend", " e2e "}}
	require.Equal(t, []string{"backend", "docs", "e2e"}, grepPathScopes(args))
	require.Equal(t, []string{"src"}, grepPathScopes(map[string]any{"paths": "src"}))
	require.Nil(t, grepPathScopes(nil))
}

// TestShadowObserver_GrepMultiPathsUnion 钉住多 paths 观察：候选按 OR 域过滤、
// baseline 前缀逐行归属，overlap 覆盖多个作用域。
func TestShadowObserver_GrepMultiPathsUnion(t *testing.T) {
	index := &fakeShadowIndex{hits: []SearchHit{
		{Path: "backend/internal/agent/a.go", Line: 3, Name: "X"},
		{Path: "docs/guide/b.md", Line: 7, Name: "X"},
		{Path: "frontend/src/c.js", Line: 9, Name: "X"},
	}}
	obs := NewShadowObserver(ShadowConfig{Mode: ModeShadow, Index: index})
	rec, err := obs.Observe(context.Background(), ObservedCall{
		Tool:   "grep",
		Args:   map[string]any{"pattern": "X", "paths": []any{"backend", "docs"}},
		Output: "internal/agent/a.go:3:x\ndocs/guide/b.md:7:y",
	})
	require.NoError(t, err)
	require.Equal(t, 2, rec.BaselineN)
	require.Equal(t, 2, rec.CandidateN)
	require.Equal(t, 2, rec.OverlapN)
	require.NotNil(t, rec.Coverage)
	require.InDelta(t, 1.0, *rec.Coverage, 1e-9)
}

// TestShadowObserver_GrepScopeRelativeOutputAligns 钉住端到端对齐：
// path=backend + 输出相对 backend 时，候选（workspace 相对）必须计入 overlap。
func TestShadowObserver_GrepScopeRelativeOutputAligns(t *testing.T) {
	index := &fakeShadowIndex{hits: []SearchHit{
		{Path: "backend/internal/agent/a.go", Line: 5315, Name: "X"},
	}}
	obs := NewShadowObserver(ShadowConfig{Mode: ModeShadow, Index: index})
	rec, err := obs.Observe(context.Background(), ObservedCall{
		Tool:   "grep",
		Args:   map[string]any{"pattern": "X", "path": "backend"},
		Output: "internal/agent/a.go:5315:func X()",
	})
	require.NoError(t, err)
	require.Equal(t, 1, rec.BaselineN)
	require.Equal(t, 1, rec.CandidateN)
	require.Equal(t, 1, rec.OverlapN)
	require.NotNil(t, rec.Coverage)
	require.InDelta(t, 1.0, *rec.Coverage, 1e-9)
}

// 单文件作用域：rg 以文件为根输出 basename，补全不得拼成 file/file（收口轮六）。
func TestParseGrepBaselineFileScopeDoesNotDoublePrefix(t *testing.T) {
	keys, _ := parseGrepBaseline("planner.go:210: x\nplanner.go:244: y", "internal/knowledge/planner.go")
	require.Equal(t, []string{
		"internal/knowledge/planner.go:210",
		"internal/knowledge/planner.go:244",
	}, keys)

	// 输出已是完整相对路径（与作用域同文件）时不重复拼接。
	keys, _ = parseGrepBaseline("internal/knowledge/planner.go:7: z", "internal/knowledge/planner.go")
	require.Equal(t, []string{"internal/knowledge/planner.go:7"}, keys)

	// 目录作用域的既有补全语义不变。
	keys, _ = parseGrepBaseline("planner.go:1: x", "internal/knowledge")
	require.Equal(t, []string{"internal/knowledge/planner.go:1"}, keys)
}
