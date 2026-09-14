package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	llmadapter "github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
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
// 错误在重采样耗尽后被降级为工具反馈回注（附 schema），turn 不终止、循环继续。
func TestReActLoop_RecoversMalformedToolCallArguments(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	// invalid_tool_arguments 现在是可重试的退化采样：runtime 层先用短退避重采样，
	// 连续 degenerateOutputReplyMaxStreak 次仍是非法参数才收敛（retryExhaustedError
	// 包装冒泡，Unwrap 链上仍是 *MalformedToolCallError，errors.As 可达），
	// 由 loop 降级为工具反馈回注。因此第 1 次 think 恰好消耗 3 次 provider 调用。
	provider := &malformedToolCallProvider{
		name:          "test-provider",
		malformedRuns: 3,
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
	// 第 1 次 think：3 次退化采样后被降级恢复；第 2 次 think 成功（第 4 次调用）。
	require.Equal(t, 4, provider.callCount)

	// 第 2 次 think 的请求（第 4 次 provider 调用）必须携带注入的 tool_result
	// 反馈（含 schema 指引）。
	require.Len(t, provider.requests, 4)
	contents := findToolResultMessages(provider.requests[3])
	require.NotEmpty(t, contents, "重采样耗尽后的请求应包含降级注入的 tool_result")
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
	// 每次 think 先做满 3 次退化重采样（runtime 层 streak 上限）才冒泡到 loop：
	// 第 1、2 次 think 降级注入（continue），第 3 次触发护栏放弃：共 9 次调用。
	require.Equal(t, 9, provider.callCount)
	require.Equal(t, 2, loop.malformedToolCallRecoveries["write_file"])
	require.Len(t, provider.requests, 9)
	// 第 3 次 think 的首次请求中只有前两次注入的 tool_result（各 1 条），不再新增。
	contents := findToolResultMessages(provider.requests[6])
	require.Len(t, contents, 2)
	// 触发护栏的那次调用之前，重采样请求不应携带新的降级反馈。
	require.Len(t, findToolResultMessages(provider.requests[8]), 2)
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
		malformedRuns: 3,
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
	require.Equal(t, 4, provider.callCount)

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

// scriptedMalformedToolCallStep 描述一次 provider 调用：malformed / toolCall / content。
type scriptedMalformedToolCallStep struct {
	malformed bool
	toolCall  string
	content   string
}

// scriptedMalformedToolCallProvider 按脚本返回「非法参数错误 / 合法工具调用 / 最终答复」，
// 用于验证降级计数在成功执行后的重置语义。
type scriptedMalformedToolCallProvider struct {
	name       string
	steps      []scriptedMalformedToolCallStep
	callCount  int
	toolCallNo int
}

func (p *scriptedMalformedToolCallProvider) Name() string { return p.name }

func (p *scriptedMalformedToolCallProvider) DefaultModelName() string { return "test-model" }

func (p *scriptedMalformedToolCallProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.callCount++
	if len(p.steps) == 0 {
		return &llm.LLMResponse{Content: "script exhausted", Model: "test-model"}, nil
	}
	step := p.steps[0]
	p.steps = p.steps[1:]
	if step.malformed {
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
	if step.toolCall != "" {
		p.toolCallNo++
		return &llm.LLMResponse{
			Content: "writing",
			Model:   "test-model",
			ToolCalls: []types.ToolCall{{
				ID:   fmt.Sprintf("call-ok-%d", p.toolCallNo),
				Name: step.toolCall,
				// 参数每次不同：避免语义重复调用触发 doom-loop 追踪，同时保证合法 JSON。
				Args: map[string]interface{}{"path": fmt.Sprintf("out-%d.txt", p.toolCallNo)},
			}},
		}, nil
	}
	return &llm.LLMResponse{Content: step.content, Model: "test-model"}, nil
}

func (p *scriptedMalformedToolCallProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 1)
	ch <- llm.StreamChunk{Type: llm.EventTypeDone, Done: true}
	close(ch)
	return ch, nil
}

func (p *scriptedMalformedToolCallProvider) CountTokens(text string) int { return len(text) / 4 }

func (p *scriptedMalformedToolCallProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsJSONMode:  true,
	}
}

func (p *scriptedMalformedToolCallProvider) CheckHealth(ctx context.Context) error { return nil }

// resetRecoveryMCPManager 提供一个每次都成功的 write_file 执行端点。
type resetRecoveryMCPManager struct{ calls int }

func (m *resetRecoveryMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{Name: toolName, Enabled: true, MCPName: "reset-mcp"}, nil
}

func (m *resetRecoveryMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	m.calls++
	return "written", nil
}

func (m *resetRecoveryMCPManager) ListTools() []skill.ToolInfo {
	return []skill.ToolInfo{{Name: "write_file", Description: "Write a file", Enabled: true, MCPName: "reset-mcp"}}
}

// TestReActLoop_MalformedToolCallRecoveryResetsAfterSuccessfulExecution 固化
// 「连续」语义：一次成功的工具执行证明模型已能按 schema 产出合法参数，此前的
// 参数非法降级计数清零。若沿用历史累计语义，第 3 段 malformed（累计 3）会提前
// 触发护栏并终止 turn；重置后应继续降级恢复并最终成功。
func TestReActLoop_MalformedToolCallRecoveryResetsAfterSuccessfulExecution(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &scriptedMalformedToolCallProvider{
		name: "test-provider",
		steps: []scriptedMalformedToolCallStep{
			{malformed: true}, {malformed: true}, {malformed: true}, // think1：降级注入（连续 1）
			{toolCall: "write_file"},                                // think2：成功执行 → 计数清零
			{malformed: true}, {malformed: true}, {malformed: true}, // think3：降级注入（连续 1）
			{toolCall: "write_file"},                                // think4：成功执行 → 计数清零
			{malformed: true}, {malformed: true}, {malformed: true}, // think5：累计语义会在此触发护栏
			{content: "done after resets"}, // think6：完成
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	mcp := &resetRecoveryMCPManager{}
	agent := NewAgentWithLLM(&Config{
		Name: "malformed-reset-agent", Provider: "test-provider", Model: "test-model", MaxSteps: 10,
	}, mcp, llmRuntime)
	bus := runtimeevents.NewBus()
	guardrailHit := false
	bus.Subscribe("tool.malformed_arguments.guardrail_hit", func(event runtimeevents.Event) { guardrailHit = true })
	// 每次恢复事件携带的 recoveries 计数固化了「连续」语义：若沿用历史累计，
	// 第 2 次恢复事件会看到 2（并导致第 3 段 malformed 提前触发护栏）。
	var recoveredCounts []int
	bus.Subscribe("tool.malformed_arguments.recovered", func(event runtimeevents.Event) {
		if recoveries, ok := event.Payload["recoveries"].(map[string]int); ok {
			recoveredCounts = append(recoveredCounts, recoveries["write_file"])
		}
	})
	agent.SetEventBus(bus)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 10, EnableToolCalls: true})

	result, err := loop.Run(context.Background(), "write the file")

	require.NoError(t, err, "成功执行后的降级计数应清零，不应触发护栏终止 turn")
	require.True(t, result.Success)
	require.False(t, guardrailHit, "guardrail must not fire while recoveries are not consecutive")
	require.Equal(t, 12, provider.callCount)
	require.Equal(t, 2, mcp.calls, "两段合法工具调用都应真正执行")
	require.Equal(t, []int{1, 1, 1}, recoveredCounts,
		"每次成功执行都把连续计数清零，因此每段 malformed 都只是连续第 1 次")
	// 最后一次降级后没有新的成功执行，计数保留为 1（而不是历史累计的 3）。
	require.Equal(t, 1, loop.malformedToolCallRecoveries["write_file"])
}

// TestResetMalformedToolCallRecoveriesOnSuccessSemantics 单元级固化重置规则：
// 只有真正执行成功（Call.Name 已写入且无错误）的调用才清零，失败/被拒绝的调用
// 保留计数，零值槽位不算成功。
func TestResetMalformedToolCallRecoveriesOnSuccessSemantics(t *testing.T) {
	loop := &ReActLoop{malformedToolCallRecoveries: map[string]int{"write_file": 2, "shell": 1}}
	loop.resetMalformedToolCallRecoveriesOnSuccess(
		[]types.ToolCall{{Name: "write_file"}, {Name: "shell"}, {Name: "edit"}},
		[]toolExecutionResult{
			{Call: types.ToolCall{Name: "write_file"}},
			{Call: types.ToolCall{Name: "shell"}, Error: "exit status 1"},
			{}, // 零值槽位：未写入结果，不能当作成功
		},
	)

	require.NotContains(t, loop.malformedToolCallRecoveries, "write_file")
	require.Equal(t, 1, loop.malformedToolCallRecoveries["shell"], "执行失败的调用保留连续计数")
}
