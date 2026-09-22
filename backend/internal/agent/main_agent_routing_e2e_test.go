package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// routingRecordingProvider 记录每一次请求（含 tools 与 messages），用于端到端取证
// 「宿主接线 → 请求面」这一跳确实生效。
type routingRecordingProvider struct {
	requests []*llm.LLMRequest
	// script 按调用顺序返回预设响应；用尽后回落到纯文本收尾。
	script []*llm.LLMResponse
	// caps 覆盖默认能力：I3 需要两个 provider 的上下文窗口不同。
	caps *llm.ModelCapabilities
}

func (p *routingRecordingProvider) Name() string { return "test-provider" }

func (p *routingRecordingProvider) DefaultModelName() string { return "test-model" }

func (p *routingRecordingProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.requests = append(p.requests, cloneLLMRequest(req))
	if idx := len(p.requests) - 1; idx < len(p.script) {
		return p.script[idx], nil
	}
	return &llm.LLMResponse{Content: "done", Model: "test-model", FinishReason: "stop"}, nil
}

func (p *routingRecordingProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 1)
	ch <- llm.StreamChunk{Type: llm.EventTypeDone, Done: true}
	close(ch)
	return ch, nil
}

func (p *routingRecordingProvider) CountTokens(text string) int { return len(text) / 4 }

func (p *routingRecordingProvider) GetCapabilities() *llm.ModelCapabilities {
	if p.caps != nil {
		return p.caps
	}
	return &llm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsJSONMode:  true,
	}
}

func (p *routingRecordingProvider) CheckHealth(ctx context.Context) error { return nil }

// TestMainAgentRoutingRequestSurfaceEndToEnd 覆盖 §5.4 的完整链路：run() 内部的
// 回合基线冻结 → 工具面叠加 → 回合级 system 注入，全部体现在真实 LLM 请求上；
// 同时钉住关闭态零行为变化，以及注入片段绝不落盘。
func TestMainAgentRoutingRequestSurfaceEndToEnd(t *testing.T) {
	const fragmentMarker = "Task difficulty routing is active for this session"

	cases := map[string]*agentconfig.AICLIMainAgentRoutingConfig{
		"enabled": mainAgentRouteTestConfig().MainAgentRouting,
		"nil":     nil,
	}
	for name, routing := range cases {
		t.Run(name, func(t *testing.T) {
			llmRuntime := llm.NewLLMRuntime(nil)
			provider := &routingRecordingProvider{}
			require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
			agent := NewAgentWithLLM(&Config{
				Name: "routing-agent", Provider: "test-provider", Model: "test-model", MaxSteps: 4,
			}, &RecoveringMCPManager{}, llmRuntime)
			loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
				MaxSteps:         4,
				EnableToolCalls:  true,
				Provider:         "test-provider",
				Model:            "test-model",
				MainAgentRouting: routing,
			})

			var persisted []types.Message
			result, err := loop.run(context.Background(), "do the work", loopRunOptions{
				IncludePrompt: true,
				PersistHistory: func(messages []types.Message) error {
					persisted = make([]types.Message, len(messages))
					for i, message := range messages {
						persisted[i] = *message.Clone()
					}
					return nil
				},
			})
			require.NoError(t, err)
			require.True(t, result.Success)
			require.NotEmpty(t, provider.requests)

			request := provider.requests[0]
			toolPresent := false
			for _, tool := range request.Tools {
				if tool.Name == predictTaskDifficultyToolName {
					toolPresent = true
				}
			}
			injected := 0
			for _, message := range request.Messages {
				if strings.Contains(message.Content, fragmentMarker) {
					injected++
				}
			}
			leaked := 0
			for _, message := range persisted {
				if strings.Contains(message.Content, fragmentMarker) {
					leaked++
				}
			}
			require.Zero(t, leaked, "回合级注入不得落盘（INV-1）")

			if routing == nil {
				require.False(t, toolPresent, "关闭态不得暴露难度上报工具")
				require.Zero(t, injected, "关闭态不得注入路由提示")
				return
			}
			require.True(t, toolPresent, "启用态必须暴露难度上报工具")
			require.Equal(t, 1, injected, "回合级提示必须恰好注入一次")
		})
	}
}

// TestMainAgentRoutingEscalationReachesNextStepRequest 覆盖 I1：step 1 通过
// `predict_task_difficulty` 上报 hard ⇒ **step 2 的真实 LLM 请求**必须已经使用
// hard 档位的 model / reasoning_effort（§5.1 不变式 3：只影响 step N+1）。
func TestMainAgentRoutingEscalationReachesNextStepRequest(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &routingRecordingProvider{
		script: []*llm.LLMResponse{
			{
				Model: "test-model",
				ToolCalls: []types.ToolCall{{
					ID:   "call_route_1",
					Type: "function",
					Name: predictTaskDifficultyToolName,
					Args: map[string]interface{}{"difficulty": "hard", "rationale": "multi-file refactor"},
				}},
				FinishReason: "tool_calls",
			},
			{Content: "done", Model: "claude-hard", FinishReason: "stop"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name: "routing-agent", Provider: "test-provider", Model: "test-model", MaxSteps: 4,
	}, &RecoveringMCPManager{}, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, mainAgentRouteTestConfig())
	// 基线 provider 换成测试 provider：档位 profile 只改 model/effort，provider 沿用基线。
	loop.config.Provider = "test-provider"
	loop.config.Model = "test-model"

	result, err := loop.run(context.Background(), "refactor the routing layer", loopRunOptions{IncludePrompt: true})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.GreaterOrEqual(t, len(provider.requests), 2, "上报后必须继续下一轮 think")

	first, second := provider.requests[0], provider.requests[1]
	require.Equal(t, "test-model", first.Model, "step 1 必须走基线")
	require.Equal(t, "claude-hard", second.Model, "step 2 必须使用上报档位的 model")
	require.Equal(t, "high", second.ReasoningEffort, "step 2 必须使用上报档位的 reasoning_effort")
	require.Equal(t, "test-provider", second.Provider, "同 provider 档位不得改写 provider")

	// R1/INV-3：回给模型的占位行只说「上报了什么档位」，不得出现 provider/model。
	placeholderSeen := false
	for _, message := range second.Messages {
		if message.Role != "tool" {
			continue
		}
		placeholderSeen = true
		require.NotContains(t, message.Content, "claude-hard")
		require.NotContains(t, message.Content, "test-provider")
	}
	require.True(t, placeholderSeen, "上报必须留下占位行，否则模型会以为调用失败并重试（R1）")
}

// TestMainAgentRoutingMetaOnlyReportDoesNotConsumeStep 覆盖 I2（§5.4 R2/R4）：
// 只有上报、没有真实工具、没有最终回答时，模型必须能继续下一轮 think()，且上报
// **不消耗 step**、不产生用户可见输出。
//
// 判别力来自 MaxSteps=1：旧行为下 meta-only 迭代会把 step 推到 2，循环立刻以
// step_limit 收尾（result.Success=false）；只有「step 回退」成立时，唯一一步预算
// 才会留给真正的回答。
func TestMainAgentRoutingMetaOnlyReportDoesNotConsumeStep(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &routingRecordingProvider{
		script: []*llm.LLMResponse{
			{
				Model: "test-model",
				ToolCalls: []types.ToolCall{{
					ID:   "call_meta_1",
					Type: "function",
					Name: predictTaskDifficultyToolName,
					Args: map[string]interface{}{"difficulty": "hard", "rationale": "unknown surface"},
				}},
				FinishReason: "tool_calls",
			},
			{Content: "final answer", Model: "claude-hard", FinishReason: "stop"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name: "routing-agent", Provider: "test-provider", Model: "test-model", MaxSteps: 1,
	}, &RecoveringMCPManager{}, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, mainAgentRouteTestConfig())
	loop.config.Provider = "test-provider"
	loop.config.Model = "test-model"
	loop.config.MaxSteps = 1

	result, err := loop.run(context.Background(), "report then work", loopRunOptions{IncludePrompt: true})
	require.NoError(t, err)
	require.False(t, result.LimitReached, "meta-only 上报不得吃掉唯一一步预算（R2）")
	require.True(t, result.Success)
	require.Equal(t, "final answer", result.Output, "上报本身不得成为用户可见输出（R4）")
	require.Len(t, provider.requests, 2, "meta-only 之后必须直接续轮 think（R4）")
	require.Equal(t, "claude-hard", provider.requests[1].Model, "上报只影响 step N+1（R5）")

	placeholder := false
	for _, message := range provider.requests[1].Messages {
		if message.Role == "tool" && strings.Contains(message.Content, "[route reported: hard]") {
			placeholder = true
		}
	}
	require.True(t, placeholder, "上报必须留下占位行，否则模型会以为调用失败并重试（R1）")
}

// TestMainAgentRoutingReportCoexistsWithRealTools 覆盖 I5（§5.4 R3 + R2 预算口径）：
// 同一响应内既有上报又有真实工具时，上报被剥离到内存态、真实工具照常执行；
// 且上报**不占用**工具调用预算——MaxToolCalls=1 时旧行为会把这一批（2 个调用）
// 判成超限并中止 turn。
func TestMainAgentRoutingReportCoexistsWithRealTools(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &routingRecordingProvider{
		script: []*llm.LLMResponse{
			{
				Model: "test-model",
				ToolCalls: []types.ToolCall{
					{
						ID:   "call_meta_1",
						Type: "function",
						Name: predictTaskDifficultyToolName,
						Args: map[string]interface{}{"difficulty": "hard"},
					},
					{
						ID:   "call_real_1",
						Type: "function",
						Name: "read_logs",
						Args: map[string]interface{}{},
					},
				},
				FinishReason: "tool_calls",
			},
			{Content: "done", Model: "claude-hard", FinishReason: "stop"},
		},
	}
	mcp := &MockSequenceMCPManager{output: "log line"}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name: "routing-agent", Provider: "test-provider", Model: "test-model", MaxSteps: 4,
	}, mcp, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, mainAgentRouteTestConfig())
	loop.config.Provider = "test-provider"
	loop.config.Model = "test-model"
	loop.config.MaxToolCalls = 1

	result, err := loop.run(context.Background(), "report and read logs", loopRunOptions{IncludePrompt: true})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.False(t, result.LimitReached, "上报不得占用工具调用预算（R2）")
	require.Equal(t, 1, mcp.callCount, "并存时真实工具必须照常执行（R3）")
	require.GreaterOrEqual(t, len(provider.requests), 2)
	require.Equal(t, "claude-hard", provider.requests[1].Model, "上报在 step 边界生效（R5）")

	second := provider.requests[1]
	placeholder, realResult := false, false
	for _, message := range second.Messages {
		if message.Role != "tool" {
			continue
		}
		if strings.Contains(message.Content, "[route reported: hard]") {
			placeholder = true
		}
		if strings.Contains(message.Content, "log line") {
			realResult = true
		}
	}
	require.True(t, placeholder, "R1：占位行必须与真实工具结果并存")
	require.True(t, realResult, "R3：真实工具结果必须在下一轮请求里")
}

// TestMainAgentRoutingPreflightReadsLiveRoute 覆盖 I3：preflight 的预算解析与请求
// 构建必须读**同一** route。
//
// 取证方式：两个 provider 的 MaxContextTokens 差两个数量级，且宿主 agent.config
// 故意停留在基线 provider。只有 preflight 读 loop.config（= 请求构建的同一事实源）
// 时，上报后的预算才会跟着换 provider；同一 turn 的真实请求也必须落在同一个
// provider/model 上。
func TestMainAgentRoutingPreflightReadsLiveRoute(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	base := &routingRecordingProvider{}
	hard := &routingRecordingProvider{
		caps: &llm.ModelCapabilities{
			MaxContextTokens:  4096,
			MaxOutputTokens:   512,
			SupportsTools:     true,
			SupportsStreaming: true,
			SupportsJSONMode:  true,
		},
		script: []*llm.LLMResponse{{Content: "hard done", Model: "claude-hard", FinishReason: "stop"}},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", base))
	require.NoError(t, llmRuntime.RegisterProvider("hard-provider", hard))

	hostConfig := mainAgentRouteTestConfig()
	hostConfig.MainAgentRouting.Profiles["hard"] = agentconfig.AICLISubagentRouteProfile{
		Provider: "hard-provider", Model: "claude-hard", ReasoningEffort: "high",
	}
	agent := NewAgentWithLLM(&Config{
		Name: "routing-agent", Provider: "test-provider", Model: "stale-model", MaxSteps: 4,
	}, &RecoveringMCPManager{}, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, hostConfig)
	loop.config.Provider = "test-provider"
	loop.config.Model = "test-model"
	loop.beginTurnRoute("session_test", "trace_test")
	defer loop.endTurnRoute("session_test", "trace_test")

	baseBudget := resolvePromptPreflightBudget(llmRuntime, agent, loop.config, 0)
	require.Equal(t, "test-provider", baseBudget.ResolvedProvider)
	require.Greater(t, baseBudget.EffectiveInputBudget, 0)

	if _, err := reportDifficulty(t, loop, "hard"); err != nil {
		t.Fatalf("reportDifficulty: %v", err)
	}
	liveBudget := resolvePromptPreflightBudget(llmRuntime, agent, loop.config, 0)
	require.Equal(t, "hard-provider", liveBudget.ResolvedProvider, "preflight 必须读 loop.config 的当前 route")
	require.Equal(t, "claude-hard", liveBudget.ResolvedModel)
	require.Less(t, liveBudget.EffectiveInputBudget, baseBudget.EffectiveInputBudget,
		"跨 provider 的窗口差异必须被 preflight 采纳")

	// 请求构建同源：同一个 turn 的下一步真实请求必须落在 preflight 解析出的
	// provider/model 上（不是宿主 agent.config 里的 stale-model）。
	provider := &routingRecordingProvider{
		script: []*llm.LLMResponse{
			{
				Model: "test-model",
				ToolCalls: []types.ToolCall{{
					ID:   "call_meta_1",
					Type: "function",
					Name: predictTaskDifficultyToolName,
					Args: map[string]interface{}{"difficulty": "hard"},
				}},
				FinishReason: "tool_calls",
			},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("live-route-provider", provider))
	runConfig := mainAgentRouteTestConfig()
	runConfig.MainAgentRouting.Profiles["hard"] = agentconfig.AICLISubagentRouteProfile{
		Provider: "live-route-provider", Model: "claude-hard", ReasoningEffort: "high",
	}
	runAgent := NewAgentWithLLM(&Config{
		Name: "routing-agent", Provider: "live-route-provider", Model: "stale-model", MaxSteps: 4,
	}, &RecoveringMCPManager{}, llmRuntime)
	runLoop := NewReActLoop(runAgent, llmRuntime, runConfig)
	runLoop.config.Provider = "live-route-provider"
	runLoop.config.Model = "test-model"

	result, err := runLoop.run(context.Background(), "route me", loopRunOptions{IncludePrompt: true})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.GreaterOrEqual(t, len(provider.requests), 2)
	require.Equal(t, "live-route-provider", provider.requests[1].Provider,
		"请求构建必须与 preflight 读同一 route")
	require.Equal(t, "claude-hard", provider.requests[1].Model)
	require.Equal(t, "high", provider.requests[1].ReasoningEffort)
}
