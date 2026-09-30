package runtimeapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type wiringShadowIndex struct{ hits []knowledge.SearchHit }

func (w *wiringShadowIndex) Search(ctx context.Context, q knowledge.SearchQuery) ([]knowledge.SearchHit, error) {
	return w.hits, nil
}

func (w *wiringShadowIndex) FindSymbols(ctx context.Context, q knowledge.SymbolQuery) ([]knowledge.Symbol, error) {
	return nil, nil
}

type wiringShadowSink struct {
	records []*entity.ExplorationAttribution
}

func (s *wiringShadowSink) AppendExplorationAttribution(ctx context.Context, rec *entity.ExplorationAttribution) error {
	s.records = append(s.records, rec)
	return nil
}

// 未注入观察器（知识层 off / 账本不可用 / 未接线）时 hook 必须保持 nil：
// agent loop 行为与无知识层逐字节一致。
func TestApplyAPISessionToolObservation_NilObserverKeepsHookNil(t *testing.T) {
	config := &agent.LoopReActConfig{}
	applyAPISessionToolObservation(config, &Handler{}, "sess-1", knowledge.ObservationSourceMainSession, "")
	require.Nil(t, config.OnToolObserved)

	applyAPISessionToolObservation(config, nil, "sess-1", knowledge.ObservationSourceMainSession, "")
	require.Nil(t, config.OnToolObserved)

	applyAPISessionToolObservation(nil, &Handler{}, "sess-1", knowledge.ObservationSourceMainSession, "") // 不得 panic
}

// 注入后 hook 把 grep 结果旁路给观察器；sessionID 为空时回落到宿主会话。
func TestApplyAPISessionToolObservation_ForwardsObservedCall(t *testing.T) {
	sink := &wiringShadowSink{}
	observer := knowledge.NewShadowObserver(knowledge.ShadowConfig{
		Mode:  knowledge.ModeShadow,
		Index: &wiringShadowIndex{hits: []knowledge.SearchHit{{Path: "backend/a.go", Line: 3, Name: "Foo"}}},
		Sink:  sink,
	})
	config := &agent.LoopReActConfig{}
	applyAPISessionToolObservation(config, &Handler{knowledgeShadow: observer}, "sess-host",
		knowledge.ObservationSourceMainSession, "")
	require.NotNil(t, config.OnToolObserved)

	config.OnToolObserved(context.Background(), "", runtimetypes.ToolCall{
		Name: "grep",
		Args: map[string]interface{}{"pattern": "Foo"},
	}, "backend/a.go:3:func Foo\n", "")
	require.Len(t, sink.records, 1)
	require.Equal(t, "sess-host", sink.records[0].SessionID, "空 sessionID 回落到宿主会话")
	require.Equal(t, "grep", sink.records[0].Tool)
	// ADR-0008 §8.1：API 入口 live 写入必须带 file-level 两列。
	require.Equal(t, 1, sink.records[0].BaselineFilesN, "baseline 文件集合落库")
	require.Equal(t, 1, sink.records[0].OverlapFilesN, "文件交集落库")
	require.True(t, sink.records[0].Usable, "file-level 全覆盖时 grep usable = true")

	// 非目标工具（glob）不落行。
	config.OnToolObserved(context.Background(), "sess-1", runtimetypes.ToolCall{Name: "glob"}, "x", "")
	require.Len(t, sink.records, 1)
}
