package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// 纯 tool-call 轮次（无文本/思考增量）也必须记录首字时间：工具调用参数开始
// 流出即"模型已开始产出"。OpenAI Chat Completions 流此前会整轮显示"未采集"。
func TestReActLoop_RunWithSession_EmitsFirstTokenMsForToolCallOnlyStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// 人为让工具增量晚于 attempt 起点 15ms，保证整数毫秒打点必然 > 0。
		time.Sleep(15 * time.Millisecond)
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"shell","arguments":"{\"command\":\"git status\"}"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider, err := llm.NewProvider(&llm.ProviderConfig{
		Type:    "openai",
		BaseURL: server.URL,
	})
	require.NoError(t, err)

	llmRuntime := llm.NewLLMRuntime(nil)
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	agent := &Agent{
		config: &Config{
			Name:     "test-agent",
			Provider: "test-provider",
			Model:    "gpt-4o-mini",
			Options: map[string]interface{}{
				"stream": true,
			},
		},
		state: AgentState{},
	}

	bus := runtimeevents.NewBus()
	var finishedEvents []runtimeevents.Event
	bus.Subscribe("llm.request.finished", func(event runtimeevents.Event) {
		finishedEvents = append(finishedEvents, event)
	})
	agent.SetEventBus(bus)

	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        1,
		EnableThought:   true,
		EnableToolCalls: true,
	})
	// 工具调用执行可能因未注册 shell 而失败，这不影响 llm.request.finished
	// 里的首字事实（首字在工具执行前就已定稿）。
	_, _ = loop.RunWithSession(context.Background(), "run git status", newTestHistorySession("session-ttft-toolcall"))

	require.Len(t, finishedEvents, 1)
	ms, ok := finishedEvents[0].Payload["first_token_ms"].(int64)
	require.True(t, ok, "first_token_ms should be recorded, payload=%v", finishedEvents[0].Payload)
	require.Greater(t, ms, int64(0))
}
