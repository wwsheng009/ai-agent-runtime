package knowledge

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestScopeFilterSpec_AndSemantics 钉住双作用域语义：path 限制根、glob 限制
// 文件名，二者是 AND（rg 语义）。
func TestScopeFilterSpec_AndSemantics(t *testing.T) {
	filter := newScopeFilterSpec("backend", "*.go", "")
	require.True(t, filter.matches("backend/a.go"))
	require.True(t, filter.matches("backend/deep/nested/b.go"))
	require.False(t, filter.matches("backend/a.ts"), "glob 不匹配必须被拒绝")
	require.False(t, filter.matches("frontend/b.go"), "path 作用域外必须被拒绝")

	// 只给其一时与 newScopeFilter 等价。
	require.True(t, newScopeFilterSpec("", "*.go", "").matches("frontend/b.go"))
	require.False(t, newScopeFilterSpec("", "*.go", "").matches("frontend/b.ts"))
	require.True(t, newScopeFilterSpec("backend", "", "").matches("backend/a.ts"))
	require.False(t, newScopeFilterSpec("backend", "", "").matches("frontend/a.go"))
}

// TestShadowObserver_GrepDualScope 钉住观察器把 path+glob 同时用于候选过滤：
// 两种约束各自都会剔除候选，overlap 只保留同时满足的条目。
func TestShadowObserver_GrepDualScope(t *testing.T) {
	index := &fakeShadowIndex{hits: []SearchHit{
		{Path: "backend/a.go", Line: 10, Name: "A"},
		{Path: "backend/a.ts", Line: 5, Name: "A"},  // glob 外
		{Path: "frontend/b.go", Line: 3, Name: "A"}, // path 外
	}}
	obs := NewShadowObserver(ShadowConfig{Mode: ModeShadow, Index: index})

	rec, err := obs.Observe(context.Background(), ObservedCall{
		Tool: "grep",
		Args: map[string]any{"pattern": "A", "path": "backend", "glob": "*.go"},
		Output: "backend/a.go:10:func A()\n" +
			"backend/a.ts:5:const A = 1",
	})
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.Equal(t, 2, rec.BaselineN)
	require.Equal(t, 1, rec.CandidateN, "仅同时满足 path 与 glob 的候选可进入候选集")
	require.Equal(t, 1, rec.OverlapN)
	require.NotNil(t, rec.Coverage)
	require.InDelta(t, 0.5, *rec.Coverage, 1e-9)
}
