package knowledge

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// refFakeIndex 在最小 fake 上补出 refs 通道（可选接口）。
type refFakeIndex struct {
	fakeShadowIndex
	refs []Reference
}

func (f *refFakeIndex) FindRefs(ctx context.Context, q RefQuery) ([]Reference, error) {
	return f.refs, nil
}

// TestShadowObserver_GrepUsesRefsCandidates 钉住候选并集的 refs 通道：
// 使用点行进入候选集、作用域外被过滤、(path,line) 与 baseline 相交。
func TestShadowObserver_GrepUsesRefsCandidates(t *testing.T) {
	index := &refFakeIndex{refs: []Reference{
		{Path: "backend/a.go", Line: 20, ToSymbolName: "RunIndex"},
		{Path: "frontend/b.ts", Line: 3, ToSymbolName: "RunIndex"},
	}}
	obs := NewShadowObserver(ShadowConfig{Mode: ModeShadow, Index: index})

	rec, err := obs.Observe(context.Background(), ObservedCall{
		Tool:   "grep",
		Args:   map[string]any{"pattern": "RunIndex", "path": "backend"},
		Output: "backend/a.go:20:\tindex.RunIndex(ctx)",
	})
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.Equal(t, 1, rec.BaselineN)
	require.Equal(t, 1, rec.CandidateN, "refs 通道提供使用点候选，作用域外必须被过滤")
	require.Equal(t, 1, rec.OverlapN)
	require.NotNil(t, rec.Coverage)
	require.InDelta(t, 1.0, *rec.Coverage, 1e-9)
}

// TestShadowObserver_GrepAcceptsPatternsList 钉住批量 pattern 两种形态
// （[]string 与预览字符串 "a | b"）都展开成多次检索。
func TestShadowObserver_GrepAcceptsPatternsList(t *testing.T) {
	for _, patterns := range []any{
		[]string{"Alpha", "Beta"},
		"Alpha | Beta",
	} {
		index := &capturingIndex{fakeShadowIndex: fakeShadowIndex{hits: []SearchHit{
			{Path: "backend/a.go", Line: 10, Name: "A"},
		}}}
		obs := NewShadowObserver(ShadowConfig{Mode: ModeShadow, Index: index})
		rec, err := obs.Observe(context.Background(), ObservedCall{
			Tool:   "grep",
			Args:   map[string]any{"patterns": patterns},
			Output: "backend/a.go:10:func A()",
		})
		require.NoError(t, err)
		require.NotNil(t, rec)
		require.Len(t, index.searches, 2, "patterns=%v", patterns)
		require.Equal(t, "Alpha", index.searches[0].Text)
		require.Equal(t, "Beta", index.searches[1].Text)
		require.Equal(t, 1, rec.CandidateN)
		require.Equal(t, 1, rec.OverlapN)
	}
}

// TestGrepPatternList 钉住 pattern/patterns 的汇总顺序与去噪。
func TestGrepPatternList(t *testing.T) {
	require.Equal(t, []string{"x"}, grepPatternList(map[string]any{"pattern": " x "}))
	require.Equal(t, []string{"a", "b", "c"}, grepPatternList(map[string]any{"patterns": []any{"a", "", "b", "c"}}))
	require.Equal(t, []string{"a", "b"}, grepPatternList(map[string]any{"patterns": "a | b"}))
	require.Empty(t, grepPatternList(map[string]any{"patterns": 42}))
	require.Empty(t, grepPatternList(nil))
}
