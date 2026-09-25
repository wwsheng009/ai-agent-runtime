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
	// truncated 为 true 时把非法参数标记为「被输出预算切断」
	// （finish_reason=length），用于区分语法退化与预算截断两条处置路径。
	truncated bool
	callCount int
	requests  []*llm.LLMRequest
	responses []*llm.LLMResponse
}

func (p *malformedToolCallProvider) Name() string { return p.name }

func (p *malformedToolCallProvider) DefaultModelName() string { return "test-model" }

func (p *malformedToolCallProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.requests = append(p.requests, cloneLLMRequest(req))
	p.callCount++
	if p.callCount <= p.malformedRuns {
		finishReason := "tool_calls"
		if p.truncated {
			finishReason = "length"
		}
		return nil, &llmadapter.MalformedToolCallError{
			Kind:         "openai_stream_protocol_error",
			Code:         "invalid_tool_arguments",
			Message:      "openai_stream_protocol_error: code=invalid_tool_arguments: tool call 0 (write_file) has incomplete or non-object JSON arguments",
			FinishReason: finishReason,
			Truncated:    p.truncated,
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

// aggregateTruncatedToolCallProvider 前几次 Call 返回聚合校验层的
// truncated_tool_call（响应带工具调用且 finish_reason=length，整条响应被丢弃），
// 之后返回正常响应。
type aggregateTruncatedToolCallProvider struct {
	name          string
	truncatedRuns int
	callCount     int
	requests      []*llm.LLMRequest
	responses     []*llm.LLMResponse
}

func (p *aggregateTruncatedToolCallProvider) Name() string { return p.name }

func (p *aggregateTruncatedToolCallProvider) DefaultModelName() string { return "test-model" }

func (p *aggregateTruncatedToolCallProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.requests = append(p.requests, cloneLLMRequest(req))
	p.callCount++
	if p.callCount <= p.truncatedRuns {
		return nil, fmt.Errorf("truncated_tool_call: incomplete tool call markup in aggregated assistant response")
	}
	if idx := p.callCount - 1 - p.truncatedRuns; idx < len(p.responses) {
		return p.responses[idx], nil
	}
	return &llm.LLMResponse{Content: "No more responses configured.", Model: "test-model"}, nil
}

func (p *aggregateTruncatedToolCallProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 1)
	ch <- llm.StreamChunk{Type: llm.EventTypeDone, Done: true}
	close(ch)
	return ch, nil
}

func (p *aggregateTruncatedToolCallProvider) CountTokens(text string) int { return len(text) / 4 }

func (p *aggregateTruncatedToolCallProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsJSONMode:  true,
	}
}

func (p *aggregateTruncatedToolCallProvider) CheckHealth(ctx context.Context) error { return nil }

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

// TestReActLoop_TruncatedToolCallEscalatesBudgetThenReprompts 固化截断型
// invalid_tool_arguments 的完整处置链：同预算重采样必然再截断，所以 loop 先做
// 一次性 8k→64k 预算升级并重试；升级后仍截断才降级为 schema re-prompt，且反馈
// 文案点明「被输出预算切断」而不是「非法 JSON 字面量」。
func TestReActLoop_TruncatedToolCallEscalatesBudgetThenReprompts(t *testing.T) {
	t.Setenv(llm.EnvDisableMaxTokensCap, "")
	t.Setenv(llm.EnvMaxOutputTokens, "")
	t.Setenv(llm.EnvAICLIMaxOutputTokens, "")

	llmRuntime := llm.NewLLMRuntime(nil)
	// 前 6 次非法：首次 think 的 3 次退化重采样 + 升级后重试的 3 次重采样；
	// 第 7 次调用是降级 re-prompt 之后的成功回复。
	provider := &malformedToolCallProvider{
		name:          "test-provider",
		malformedRuns: 6,
		truncated:     true,
		responses: []*llm.LLMResponse{
			{Content: "The file was written successfully.", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name: "truncated-escalation-agent", Provider: "test-provider", Model: "test-model",
		MaxSteps: 5, DefaultMaxTokens: llm.CappedDefaultMaxTokens,
	}, &RecoveringMCPManager{}, llmRuntime)
	bus := runtimeevents.NewBus()
	var escalated runtimeevents.Event
	bus.Subscribe("llm.max_output_tokens.escalated", func(event runtimeevents.Event) { escalated = event })
	agent.SetEventBus(bus)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 5, EnableToolCalls: true})

	result, err := loop.Run(context.Background(), "write the file")

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 7, provider.callCount)
	require.Len(t, provider.requests, 7)

	// 首次调用保持 8k 槽位预留；升级只发生一次（第 4~6 次调用），只作用于本次
	// 请求对象——降级 re-prompt 之后的下一轮 think 重新按默认 8k 构造请求。
	require.Equal(t, llm.CappedDefaultMaxTokens, provider.requests[0].MaxTokens)
	for _, index := range []int{3, 4, 5} {
		require.Equal(t, llm.EscalatedMaxTokens, provider.requests[index].MaxTokens)
	}
	require.Equal(t, true, provider.requests[3].Metadata["max_output_tokens_escalated"])
	require.Equal(t, llm.CappedDefaultMaxTokens, provider.requests[6].MaxTokens)

	// 事件载荷把「截断」与「语法退化」区分开，便于日志侧归因。
	require.Equal(t, "llm.max_output_tokens.escalated", escalated.Type)
	require.Equal(t, "truncated_tool_call", escalated.Payload["reason"])
	require.Equal(t, "length", escalated.Payload["finish_reason"])
	require.Equal(t, llm.CappedDefaultMaxTokens, escalated.Payload["from_max_tokens"])
	require.Equal(t, llm.EscalatedMaxTokens, escalated.Payload["to_max_tokens"])

	// 降级反馈点明预算截断，并给出「拆小 payload」的可执行建议。
	contents := findToolResultMessages(provider.requests[6])
	require.NotEmpty(t, contents, "升级后仍截断才应回到 schema re-prompt")
	require.Contains(t, contents[0], "was NOT executed")
	require.Contains(t, contents[0], "cut off by the output budget")
	require.Contains(t, contents[0], "finish_reason=length")
	require.NotContains(t, contents[0], "not valid JSON")
}

// TestReActLoop_AggregatedTruncatedToolCallEscalatesBudget 固化聚合校验层
// truncated_tool_call 的升级出口：响应在到达 caller 之前就被丢弃，finish_reason
// 只存在于错误对象里，而该形态既不满足 MalformedToolCallError 判据（无法升级、
// 无法 re-prompt），也没有 turn 自动重跑通道——此前只能等内层退化 streak 耗尽后
// 终止整轮。现在它复用同一套一次性 8k→64k escalate：前 3 次调用跑在 8k，
// 升级后的 3 次跑在 64k；升级后仍截断则整轮终态（聚合形态没有可回注的工具调用）。
func TestReActLoop_AggregatedTruncatedToolCallEscalatesBudget(t *testing.T) {
	t.Setenv(llm.EnvDisableMaxTokensCap, "")
	t.Setenv(llm.EnvMaxOutputTokens, "")
	t.Setenv(llm.EnvAICLIMaxOutputTokens, "")

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &aggregateTruncatedToolCallProvider{name: "test-provider", truncatedRuns: 6}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name: "aggregate-truncated-agent", Provider: "test-provider", Model: "test-model",
		MaxSteps: 5, DefaultMaxTokens: llm.CappedDefaultMaxTokens,
	}, &RecoveringMCPManager{}, llmRuntime)
	bus := runtimeevents.NewBus()
	var escalated runtimeevents.Event
	bus.Subscribe("llm.max_output_tokens.escalated", func(event runtimeevents.Event) { escalated = event })
	agent.SetEventBus(bus)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 5, EnableToolCalls: true})

	_, err := loop.Run(context.Background(), "write the file")

	require.Error(t, err, "升级后仍截断：聚合形态没有 re-prompt 通道，整轮终态")
	require.Equal(t, 6, provider.callCount)
	require.Len(t, provider.requests, 6)

	// 首次 think 保持 8k 槽位预留；升级只发生一次（第 4~6 次调用）。
	require.Equal(t, llm.CappedDefaultMaxTokens, provider.requests[0].MaxTokens)
	for _, index := range []int{3, 4, 5} {
		require.Equal(t, llm.EscalatedMaxTokens, provider.requests[index].MaxTokens)
	}
	require.Equal(t, true, provider.requests[3].Metadata["max_output_tokens_escalated"])

	require.Equal(t, "llm.max_output_tokens.escalated", escalated.Type)
	require.Equal(t, "truncated_tool_call", escalated.Payload["reason"])
	require.Equal(t, llm.CappedDefaultMaxTokens, escalated.Payload["from_max_tokens"])
	require.Equal(t, llm.EscalatedMaxTokens, escalated.Payload["to_max_tokens"])
}

// TestReActLoop_MalformedToolCallEmitsLiveToolEvents 固化「参数非法、未执行」的降级
// 调用必须拥有自己的 live 工具事件与唯一 call identity。
//
// 动机（会话 session_20260925221754_dyRAoaEO 实证）：渲染层只按事件建工具行，只把
// assistant tool_call + tool_result 写进历史、不发事件的合成调用在实时 transcript 里
// 没有 cell；回合末 durable 回执对账（tool_receipt_recorded）因此命中「cell 缺失」的
// 崩溃恢复重建分支，把一行裸工具行补在正文与 agent.turn.finished 之后。
func TestReActLoop_MalformedToolCallEmitsLiveToolEvents(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	// 同一工具（write_file / call-bad）连续两次降级：id 必须区分，否则历史、回执与
	// transcript 行会折叠成一行。
	provider := &malformedToolCallProvider{
		name: "test-provider",
		// 每次 think 先做满 3 次退化重采样才冒泡到 loop：6 次非法 = 两轮降级恢复。
		malformedRuns: 6,
		responses: []*llm.LLMResponse{
			{Content: "done after malformed recoveries", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name: "malformed-live-events-agent", Provider: "test-provider", Model: "test-model", MaxSteps: 10,
	}, &resetRecoveryMCPManager{}, llmRuntime)
	bus := runtimeevents.NewBus()
	var requested, completed []runtimeevents.Event
	bus.Subscribe("tool.requested", func(event runtimeevents.Event) {
		if event.Payload["malformed_arguments"] == true {
			requested = append(requested, event)
		}
	})
	bus.Subscribe("tool.completed", func(event runtimeevents.Event) {
		if event.Payload["malformed_arguments"] == true {
			completed = append(completed, event)
		}
	})
	agent.SetEventBus(bus)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 10, EnableToolCalls: true})

	result, err := loop.Run(context.Background(), "write the file")

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Len(t, requested, 2, "每次降级都要发 tool.requested，渲染层才能就地建工具行")
	require.Len(t, completed, 2, "每次降级都要发 tool.completed，工具行才能落到终态")

	requestedIDs := make([]string, 0, len(requested))
	for _, event := range requested {
		id, _ := event.Payload["tool_call_id"].(string)
		require.NotEmpty(t, id, "降级调用必须有 call identity，否则渲染层只能降级成 system 行")
		require.Equal(t, "write_file", event.Payload["logical_tool"])
		require.Equal(t, true, event.Payload["not_executed"])
		requestedIDs = append(requestedIDs, id)
	}
	require.NotEqual(t, requestedIDs[0], requestedIDs[1],
		"同名工具的两次降级必须有不同的 call id（否则两次失败折叠成一行）")

	for i, event := range completed {
		require.Equal(t, requestedIDs[i], event.Payload["tool_call_id"], "requested/completed 必须描述同一次调用")
		require.NotEmpty(t, event.Payload["error"], "未执行的调用必须以失败态落终态（• Failed ...）")
		require.Contains(t, event.Payload["output"], "was NOT executed")
		require.Equal(t, false, event.Payload["awaiting_model"])
	}

	// 历史里的 tool_result 必须带 tool_error 元数据：回执对账按它判定 ok=false，
	// 缺省会把「未执行」记成执行成功。
	lastRequest := provider.requests[len(provider.requests)-1]
	toolMetadata := map[string]types.Metadata{}
	for _, message := range lastRequest.Messages {
		if strings.EqualFold(message.Role, "tool") && message.ToolCallID != "" {
			toolMetadata[message.ToolCallID] = message.Metadata
		}
	}
	for _, id := range requestedIDs {
		metadata, ok := toolMetadata[id]
		require.True(t, ok, "history must carry the synthetic tool_result for %s", id)
		require.Contains(t, metadata["tool_error"], "not valid JSON")
	}
}
