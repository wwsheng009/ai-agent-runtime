package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// reasoningOnlyProvider 前 N 次 Call 返回「只输出思维链」的退化错误，之后返回正常响应。
// 错误文本带上 finish_reason 取证，与流式聚合层产出的错误同构。
type reasoningOnlyProvider struct {
	name              string
	reasoningOnlyRuns int
	finishReason      string
	callCount         int
	requests          []*llm.LLMRequest
	responses         []*llm.LLMResponse
}

func (p *reasoningOnlyProvider) Name() string { return p.name }

func (p *reasoningOnlyProvider) DefaultModelName() string { return "test-model" }

func (p *reasoningOnlyProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.requests = append(p.requests, cloneLLMRequest(req))
	p.callCount++
	if p.callCount <= p.reasoningOnlyRuns {
		reason := p.finishReason
		if reason == "" {
			reason = "stop"
		}
		return nil, fmt.Errorf("reasoning_only_empty_reply: stream ended with reasoning only and no substantive output: finish_reason=%s", reason)
	}
	if idx := p.callCount - 1 - p.reasoningOnlyRuns; idx < len(p.responses) {
		return p.responses[idx], nil
	}
	return &llm.LLMResponse{Content: "No more responses configured.", Model: "test-model"}, nil
}

func (p *reasoningOnlyProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 1)
	ch <- llm.StreamChunk{Type: llm.EventTypeDone, Done: true}
	close(ch)
	return ch, nil
}

func (p *reasoningOnlyProvider) CountTokens(text string) int { return len(text) / 4 }

func (p *reasoningOnlyProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsJSONMode:  true,
	}
}

func (p *reasoningOnlyProvider) CheckHealth(ctx context.Context) error { return nil }

// countUserMessagesContaining 统计请求里包含 needle 的 user 消息条数，用于断言
// 「反馈回注只注入一次、且随连续退化累积」。
func countUserMessagesContaining(req *llm.LLMRequest, needle string) int {
	if req == nil {
		return 0
	}
	count := 0
	for _, message := range req.Messages {
		if message.Role == "user" && strings.Contains(message.Content, needle) {
			count++
		}
	}
	return count
}

// TestReActLoop_ReasoningOnlyRecoveryRepromptsWithFeedback 验证 reasoning-only 的
// 反馈回注：内层重采样耗尽后不直接终态失败，而是把「只输出了思维链、用户看不到任何
// 内容」写回会话并要求模型给出正文或完整工具调用；下一次 think 成功即恢复。
func TestReActLoop_ReasoningOnlyRecoveryRepromptsWithFeedback(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &reasoningOnlyProvider{
		name:              "test-provider",
		reasoningOnlyRuns: 3,
		finishReason:      "stop",
		responses: []*llm.LLMResponse{
			{Content: "done", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name: "reasoning-only-agent", Provider: "test-provider", Model: "test-model", MaxSteps: 5,
	}, &RecoveringMCPManager{}, llmRuntime)
	bus := runtimeevents.NewBus()
	var recoveredEvents []runtimeevents.Event
	bus.Subscribe("llm.reasoning_only.recovered", func(event runtimeevents.Event) {
		recoveredEvents = append(recoveredEvents, event)
	})
	agent.SetEventBus(bus)

	var persisted []types.Message
	options := loopRunOptions{
		IncludePrompt: true,
		PersistHistory: func(messages []types.Message) error {
			persisted = make([]types.Message, len(messages))
			for i, message := range messages {
				persisted[i] = *message.Clone()
			}
			return nil
		},
	}
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 5, EnableToolCalls: true})

	result, err := loop.run(context.Background(), "answer the question", options)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "done", result.Output)
	// 第 1 次 think 先做满 3 次退化重采样（runtime 层 streak 上限）才冒泡到 loop，
	// 回注反馈后第 2 次 think 首次调用即成功：共 4 次调用。
	require.Equal(t, 4, provider.callCount)
	require.Equal(t, 0, loop.reasoningOnlyRecoveries, "成功产出实质输出后连续退化计数必须清零")

	// 成功那次 think 的请求必须携带反馈回注（而不是重放原始 prompt）。
	require.GreaterOrEqual(t, len(provider.requests), 2)
	lastRequest := provider.requests[len(provider.requests)-1]
	require.Equal(t, 1, countUserMessagesContaining(lastRequest, "只输出了思维链"),
		"重采样耗尽后的请求应包含 reasoning-only 反馈回注")

	require.Len(t, recoveredEvents, 1)
	require.Equal(t, "stop", recoveredEvents[0].Payload["finish_reason"])
	require.Equal(t, 1, recoveredEvents[0].Payload["recoveries"])
	require.Equal(t, maxReasoningOnlyRecoveries, recoveredEvents[0].Payload["max_recoveries"])

	// 反馈回注必须持久化到会话历史，下一次用户 turn 仍能看到模型为何被追问。
	persistedNudge := 0
	for _, message := range persisted {
		if message.Role == "user" && strings.Contains(message.Content, "只输出了思维链") {
			persistedNudge++
		}
	}
	require.Equal(t, 1, persistedNudge, "反馈回注应写入会话历史")
}

// TestReActLoop_ReasoningOnlyRecoveryRespectsGuardRail 验证护栏：连续多轮只输出
// 思维链时，超过阈值后放弃反馈回注、走原有错误路径，避免把 prompt 改写重试变成新的
// 死循环。
func TestReActLoop_ReasoningOnlyRecoveryRespectsGuardRail(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &reasoningOnlyProvider{
		name:              "test-provider",
		reasoningOnlyRuns: 1000,
		finishReason:      "stop",
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name: "reasoning-only-guardrail-agent", Provider: "test-provider", Model: "test-model", MaxSteps: 10,
	}, &RecoveringMCPManager{}, llmRuntime)
	bus := runtimeevents.NewBus()
	var guardrailEvent runtimeevents.Event
	bus.Subscribe("llm.reasoning_only.guardrail_hit", func(event runtimeevents.Event) { guardrailEvent = event })
	agent.SetEventBus(bus)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 10, EnableToolCalls: true})

	result, err := loop.run(context.Background(), "answer the question", loopRunOptions{IncludePrompt: true})

	require.Error(t, err, "超过护栏阈值后应放弃回注并报错")
	require.False(t, result.Success)
	// 每次 think 先做满 3 次退化重采样（runtime 层 streak 上限）才冒泡到 loop：
	// 第 1、2 次 think 回注反馈（continue），第 3 次触发护栏放弃：共 9 次调用。
	require.Equal(t, 9, provider.callCount)
	require.Equal(t, maxReasoningOnlyRecoveries, loop.reasoningOnlyRecoveries)
	require.Equal(t, "llm.reasoning_only.guardrail_hit", guardrailEvent.Type)
	require.Equal(t, "stop", guardrailEvent.Payload["finish_reason"])
	require.Equal(t, maxReasoningOnlyRecoveries, guardrailEvent.Payload["recoveries"])
	require.Equal(t, maxReasoningOnlyRecoveries, guardrailEvent.Payload["max_recoveries"])
	// 触发护栏的那次调用之前，请求里应已带上两次回注（每轮 think 1 条）。
	require.Len(t, provider.requests, 9)
	require.Equal(t, 2, countUserMessagesContaining(provider.requests[8], "只输出了思维链"))
}
