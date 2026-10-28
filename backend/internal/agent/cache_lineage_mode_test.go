package agent

// cache_lineage_mode_test.go — §8.4 窄读法（2026-10-28 拍板）端到端接线：
// 会话上下文带 resume 标记 → 请求 metadata 与 llm.request.started 载荷携带
// cache_lineage_mode=resume；未标记不携带（未观测）；未知取值被忽略。

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestRunWithSessionCarriesCacheLineageResume(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{Content: "resume done", Model: "test-model"},
			{Content: "plain done", Model: "test-model"},
			{Content: "bogus done", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{Name: "cache-lineage", Provider: "test-provider", Model: "test-model"}, nil, llmRuntime)

	bus := runtimeevents.NewBus()
	var mu sync.Mutex
	var started []map[string]interface{}
	// 事件类型以点号常量发布（internal/chat 的下划线常量是消费端兼容别名）。
	bus.Subscribe("llm.request.started", func(event runtimeevents.Event) {
		mu.Lock()
		defer mu.Unlock()
		started = append(started, event.Payload)
	})
	agent.SetEventBus(bus)

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 1, EnableThought: true, EnableToolCalls: false})

	newSession := func(id string, marker interface{}) *testContextHistorySession {
		session := &testContextHistorySession{testHistorySession: newTestHistorySession(id)}
		session.ReplaceHistory([]types.Message{{Role: "user", Content: "continue the task"}})
		if marker != nil {
			session.SetContext(CacheLineageModeSessionContextKey, marker)
		}
		return session
	}

	marked := newSession("session-resume", CacheLineageModeResume)
	result, err := loop.RunWithSession(context.Background(), "continue the task", marked)
	require.NoError(t, err)
	require.True(t, result.Success)

	plain := newSession("session-plain", nil)
	result, err = loop.RunWithSession(context.Background(), "continue the task", plain)
	require.NoError(t, err)
	require.True(t, result.Success)

	bogus := newSession("session-bogus", "bogus-value")
	result, err = loop.RunWithSession(context.Background(), "continue the task", bogus)
	require.NoError(t, err)
	require.True(t, result.Success)

	require.Len(t, provider.requests, 3)
	require.Equal(t, CacheLineageModeResume, provider.requests[0].Metadata[cacheLineageModeKey],
		"resume 标记必须进入请求 metadata（provider 侧可观测）")
	require.NotContains(t, provider.requests[1].Metadata, cacheLineageModeKey,
		"未标记会话不得携带 lineage 标签（未观测 ≠ resume）")
	require.NotContains(t, provider.requests[2].Metadata, cacheLineageModeKey,
		"未知取值必须被忽略，不把任意字符串当标签外传")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, started, 3, "每个 run 一条 llm.request.started")
	require.Equal(t, CacheLineageModeResume, started[0]["cache_lineage_mode"],
		"started 载荷（cache analytics 的采集源）必须携带该标签")
	if _, ok := started[1]["cache_lineage_mode"]; ok {
		t.Fatalf("unmarked run must not carry cache_lineage_mode: %+v", started[1])
	}
	if _, ok := started[2]["cache_lineage_mode"]; ok {
		t.Fatalf("unknown marker must be ignored: %+v", started[2])
	}
}
