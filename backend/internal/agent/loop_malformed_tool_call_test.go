package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	llmadapter "github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// malformedToolCallProvider 前几次 Call 返回 MalformedToolCallError，之后返回正常响应。
type malformedToolCallProvider struct {
	name          string
	malformedRuns int
	callCount     int
	requests      []*llm.LLMRequest
	responses     []*llm.LLMResponse
}

func (p *malformedToolCallProvider) Name() string { return p.name }

func (p *malformedToolCallProvider) DefaultModelName() string { return "test-model" }

func (p *malformedToolCallProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.requests = append(p.requests, cloneLLMRequest(req))
	p.callCount++
	if p.callCount <= p.malformedRuns {
		return nil, &llmadapter.MalformedToolCallError{
			Kind:    "openai_stream_protocol_error",
			Code:    "invalid_tool_arguments",
			Message: "openai_stream_protocol_error: code=invalid_tool_arguments: tool call 0 (write_file) has incomplete or non-object JSON arguments",
			ToolCalls: []llmadapter.MalformedToolCall{{
				Index:     0,
				ID:        "call-bad",
				Name:      "write_file",
				Arguments: `{"path": "out.txt", "timeout": 60s}`,
			}},
		}
	}
	if idx := p.callCount - 1 - p.malformedRuns; idx < len(p.responses) {
		return p.responses[idx], nil
	}
	return &llm.LLMResponse{Content: "No more responses configured.", Model: "test-model"}, nil
}

func (p *malformedToolCallProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 1)
	ch <- llm.StreamChunk{Type: llm.EventTypeDone, Done: true}
	close(ch)
	return ch, nil
}

func (p *malformedToolCallProvider) CountTokens(text string) int { return len(text) / 4 }

func (p *malformedToolCallProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsJSONMode:  true,
	}
}

func (p *malformedToolCallProvider) CheckHealth(ctx context.Context) error { return nil }

// findToolResultMessages 提取请求中的 tool_role 消息。
func findToolResultMessages(req *llm.LLMRequest) []string {
	var contents []string
	for _, message := range req.Messages {
		if strings.EqualFold(message.Role, "tool") {
			contents = append(contents, message.Content)
		}
	}
	return contents
}

// TestReActLoop_RecoversMalformedToolCallArguments 验证 invalid_tool_arguments
// 错误被降级为工具反馈回注（附 schema），turn 不终止、循环继续。
func TestReActLoop_RecoversMalformedToolCallArguments(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	// invalid_tool_arguments 不可重试（retry_policy）：第 1 次调用即失败并冒泡到 loop 层降级。
	provider := &malformedToolCallProvider{
		name:          "test-provider",
		malformedRuns: 1,
		responses: []*llm.LLMResponse{
			{Content: "The file was written successfully.", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name: "malformed-recovery-agent", Provider: "test-provider", Model: "test-model", MaxSteps: 5,
	}, &RecoveringMCPManager{}, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 5, EnableToolCalls: true})

	result, err := loop.Run(context.Background(), "write the file")

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "The file was written successfully.", result.Output)
	// 第 1 次 think 失败（被降级恢复），第 2 次 think 成功。
	require.Equal(t, 2, provider.callCount)

	// 第 2 次 think 的请求必须携带注入的 tool_result 反馈（含 schema 指引）。
	require.Len(t, provider.requests, 2)
	contents := findToolResultMessages(provider.requests[1])
	require.NotEmpty(t, contents, "第二次请求应包含降级注入的 tool_result")
	require.Contains(t, contents[0], "was NOT executed")
	require.Contains(t, contents[0], "not valid JSON")
	require.Contains(t, contents[0], "Re-emit the call exactly per this schema")
}

// TestReActLoop_MalformedToolCallRecoveryRespectsGuardRail 验证护栏：
// 同一工具连续多次参数非法时，超过阈值后放弃降级、走原有错误路径。
func TestReActLoop_MalformedToolCallRecoveryRespectsGuardRail(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	// 永远返回 malformed：护栏前 2 次降级注入，第 3 次触发护栏放弃。
	provider := &malformedToolCallProvider{
		name:          "test-provider",
		malformedRuns: 1000,
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name: "malformed-guardrail-agent", Provider: "test-provider", Model: "test-model", MaxSteps: 10,
	}, &RecoveringMCPManager{}, llmRuntime)
	bus := runtimeevents.NewBus()
	var guardrailEvent runtimeevents.Event
	bus.Subscribe("tool.malformed_arguments.guardrail_hit", func(event runtimeevents.Event) { guardrailEvent = event })
	agent.SetEventBus(bus)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 10, EnableToolCalls: true})

	result, err := loop.Run(context.Background(), "write the file")

	require.Error(t, err, "超过护栏阈值后应放弃降级并报错")
	require.False(t, result.Success)
	// invalid_tool_arguments 不可重试：每次 think 只 1 次调用。
	// 第 1、2 次 think 降级注入（continue），第 3 次触发护栏放弃：共 3 次调用。
	require.Equal(t, 3, provider.callCount)
	require.Equal(t, 2, loop.malformedToolCallRecoveries["write_file"])
	require.Len(t, provider.requests, 3)
	// 第 3 次请求中只有前两次注入的 tool_result（各 1 条），不再新增。
	contents := findToolResultMessages(provider.requests[2])
	require.Len(t, contents, 2)
	// 护栏命中事件：载荷与 recovered 事件同构（tool_names 排序 + 全量 recoveries map）。
	require.Equal(t, "tool.malformed_arguments.guardrail_hit", guardrailEvent.Type)
	require.Equal(t, "write_file", guardrailEvent.ToolName)
	require.Equal(t, []string{"write_file"}, guardrailEvent.Payload["tool_names"])
	recoveries, ok := guardrailEvent.Payload["recoveries"].(map[string]int)
	require.True(t, ok)
	require.Equal(t, 2, recoveries["write_file"])
	require.Equal(t, maxMalformedToolCallRecoveries, guardrailEvent.Payload["max_recoveries"])
}

// TestReActLoop_MalformedToolCallRecoveryPersistsFeedback 验证注入的
// assistant tool_calls + tool_result 被持久化到会话历史。
func TestReActLoop_MalformedToolCallRecoveryPersistsFeedback(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &malformedToolCallProvider{
		name:          "test-provider",
		malformedRuns: 1,
		responses: []*llm.LLMResponse{
			{Content: "done", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name: "malformed-persist-agent", Provider: "test-provider", Model: "test-model", MaxSteps: 5,
	}, &RecoveringMCPManager{}, llmRuntime)

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

	result, err := loop.run(context.Background(), "write the file", options)
	require.NoError(t, err)
	require.True(t, result.Success)

	var sawToolResult bool
	for _, message := range persisted {
		if strings.EqualFold(message.Role, "tool") && strings.Contains(message.Content, "not valid JSON") {
			sawToolResult = true
			break
		}
	}
	require.True(t, sawToolResult, "降级注入的 tool_result 应持久化到会话历史")
	assert.True(t, len(persisted) > 0)
}
