package knowledge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
)

type fakeShadowIndex struct {
	hits []SearchHit
	syms []Symbol
}

func (f *fakeShadowIndex) Search(ctx context.Context, q SearchQuery) ([]SearchHit, error) {
	return f.hits, nil
}

func (f *fakeShadowIndex) FindSymbols(ctx context.Context, q SymbolQuery) ([]Symbol, error) {
	return f.syms, nil
}

type fakeAttributionSink struct {
	records []*entity.ExplorationAttribution
	err     error
}

func (f *fakeAttributionSink) AppendExplorationAttribution(ctx context.Context, rec *entity.ExplorationAttribution) error {
	if f.err != nil {
		return f.err
	}
	f.records = append(f.records, rec)
	return nil
}

// grep 路径：作用域外的候选被过滤，coverage = overlap / baseline，且行落库。
func TestShadowObserver_GrepComputesCoverageAndWrites(t *testing.T) {
	index := &fakeShadowIndex{hits: []SearchHit{
		{Path: "backend/a.go", Line: 10, Name: "A"},
		{Path: "frontend/b.ts", Line: 5, Name: "B"},
	}}
	sink := &fakeAttributionSink{}
	obs := NewShadowObserver(ShadowConfig{Mode: ModeShadow, ProjectID: "proj-1", Index: index, Sink: sink})

	rec, err := obs.Observe(context.Background(), ObservedCall{
		SessionID: "sess-1",
		Tool:      "grep",
		Args:      map[string]any{"pattern": "A", "path": "backend"},
		Output:    "backend/a.go:10:func A\nbackend/a.go:22:var a\nbackend/c.go:7:x",
	})
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.Equal(t, 3, rec.BaselineN)
	require.Equal(t, 1, rec.CandidateN, "作用域 frontend 的候选必须被过滤")
	require.Equal(t, 1, rec.OverlapN)
	require.NotNil(t, rec.Coverage)
	require.InDelta(t, 1.0/3.0, *rec.Coverage, 1e-9)
	require.False(t, rec.Usable, "coverage < α 时 usable=false")
	require.Len(t, rec.QueryHash, 64, "query_hash 是 sha256 十六进制")
	require.Equal(t, "grep", rec.Tool)
	require.Equal(t, "shadow", rec.KnowledgeMode)
	require.Equal(t, "proj-1", rec.ProjectID)
	require.Equal(t, "heuristic", rec.Source)
	require.Len(t, sink.records, 1)
	require.Equal(t, rec.ID, sink.records[0].ID)
}

// 零结果调用：baseline_n = 0，coverage 为 NULL，但仍落库（ADR-0003 §4.5）。
func TestShadowObserver_GrepZeroResultStillRecords(t *testing.T) {
	index := &fakeShadowIndex{hits: []SearchHit{{Path: "backend/a.go", Line: 10, Name: "A"}}}
	sink := &fakeAttributionSink{}
	obs := NewShadowObserver(ShadowConfig{Mode: ModeShadow, Index: index, Sink: sink})

	rec, err := obs.Observe(context.Background(), ObservedCall{
		Tool:   "grep",
		Args:   map[string]any{"pattern": "nomatch"},
		Output: "No matches found\n",
	})
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.Zero(t, rec.BaselineN)
	require.Nil(t, rec.Coverage, "baseline_n = 0 时 coverage 必须为 NULL")
	require.False(t, rec.Usable)
	require.Len(t, sink.records, 1, "零结果调用也要落库（单独过滤统计）")
}

// view 路径：baseline 是 offset/limit 覆盖的行，candidate 是与区间相交的符号 span。
func TestShadowObserver_ViewRangeCoverage(t *testing.T) {
	index := &fakeShadowIndex{syms: []Symbol{
		{Name: "A", Range: Range{Start: Position{Line: 12}, End: Position{Line: 14}}},
		{Name: "B", Range: Range{Start: Position{Line: 100}, End: Position{Line: 110}}},
	}}
	sink := &fakeAttributionSink{}
	obs := NewShadowObserver(ShadowConfig{Mode: ModeShadow, Index: index, Sink: sink})

	rec, err := obs.Observe(context.Background(), ObservedCall{
		Tool:   "view",
		Args:   map[string]any{"file_path": "backend/a.go", "offset": 10, "limit": 20},
		Output: strings.Repeat("line of code\n", 20),
	})
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.Equal(t, 20, rec.BaselineN)
	require.Equal(t, 1, rec.CandidateN, "区间外符号（100-110）不计入候选")
	require.Equal(t, 3, rec.OverlapN, "12-14 与区间 10-29 相交 3 行")
	require.NotNil(t, rec.Coverage)
	require.InDelta(t, 3.0/20.0, *rec.Coverage, 1e-9)
	require.False(t, rec.Usable)
}

// 全覆盖 + 更便宜 → usable = true（coverage >= α 且 economy <= 1）。
func TestShadowObserver_ViewFullCoverageUsable(t *testing.T) {
	index := &fakeShadowIndex{syms: []Symbol{
		{Name: "A", Range: Range{Start: Position{Line: 10}, End: Position{Line: 29}}},
	}}
	obs := NewShadowObserver(ShadowConfig{Mode: ModeShadow, Index: index})

	rec, err := obs.Observe(context.Background(), ObservedCall{
		Tool:   "view",
		Args:   map[string]any{"file_path": "backend/a.go", "offset": 10, "limit": 20},
		Output: strings.Repeat("this is a fairly long line of source code\n", 20),
	})
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.NotNil(t, rec.Coverage)
	require.InDelta(t, 1.0, *rec.Coverage, 1e-9)
	require.NotNil(t, rec.Economy)
	require.LessOrEqual(t, *rec.Economy, 1.0)
	require.True(t, rec.Usable)
}

// 非目标工具 / mode=off / 调用失败都不产生行。
func TestShadowObserver_SkipsNonTargetOffAndErrors(t *testing.T) {
	index := &fakeShadowIndex{}
	sink := &fakeAttributionSink{}
	obs := NewShadowObserver(ShadowConfig{Mode: ModeShadow, Index: index, Sink: sink})

	rec, err := obs.Observe(context.Background(), ObservedCall{Tool: "glob", Output: "x"})
	require.NoError(t, err)
	require.Nil(t, rec)

	off := NewShadowObserver(ShadowConfig{Mode: ModeOff, Index: index, Sink: sink})
	rec, err = off.Observe(context.Background(), ObservedCall{Tool: "grep", Output: "a.go:1:x"})
	require.NoError(t, err)
	require.Nil(t, rec, "mode=off 时观察器整体 no-op")

	rec, err = obs.Observe(context.Background(), ObservedCall{Tool: "grep", Output: "a.go:1:x", Err: "boom"})
	require.NoError(t, err)
	require.Nil(t, rec, "失败调用没有可信 baseline，不落行")

	require.Empty(t, sink.records)
}

// sink 报错时仍返回计算出的行，错误交由调用方（尽力而为契约）。
func TestShadowObserver_SinkErrorSurfacesRecord(t *testing.T) {
	index := &fakeShadowIndex{hits: []SearchHit{{Path: "backend/a.go", Line: 10, Name: "A"}}}
	obs := NewShadowObserver(ShadowConfig{
		Mode:  ModeShadow,
		Index: index,
		Sink:  &fakeAttributionSink{err: errors.New("boom")},
	})

	rec, err := obs.Observe(context.Background(), ObservedCall{
		Tool:   "grep",
		Args:   map[string]any{"pattern": "A"},
		Output: "backend/a.go:10:func A\n",
	})
	require.Error(t, err)
	require.NotNil(t, rec)
	require.Equal(t, 1, rec.BaselineN)
}

// ShadowObserverFor 只在 shadow 模式 + 索引/落库口可用时返回观察器。
func TestShadowObserverFor_Gating(t *testing.T) {
	require.Nil(t, ShadowObserverFor(nil, &fakeAttributionSink{}), "未启用知识层时整体 no-op")
	require.Nil(t, ShadowObserverFor(&Activation{}, &fakeAttributionSink{}), "零值 Activation 不是 shadow 模式")
	require.Nil(t, ShadowObserverFor(&Activation{}, nil), "无落库口时不接线")
}

// ProjectIDForWorkspace：稳定、大小写/分隔符不敏感、不含绝对路径（ADR-0003 §4.6）。
func TestProjectIDForWorkspace_StableAndPrivate(t *testing.T) {
	a := ProjectIDForWorkspace("E:\\Projects\\Ai\\Repo")
	b := ProjectIDForWorkspace("e:/projects/ai/repo")
	require.Equal(t, a, b)
	require.NotEmpty(t, a)
	require.NotContains(t, a, "projects")
	require.NotContains(t, a, "E:")
	require.Empty(t, ProjectIDForWorkspace("  "))
}
