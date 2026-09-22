package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func mainAgentRoutingHostSession(enabled bool) *ChatSession {
	return &ChatSession{Config: &config.Config{AICLI: &config.AICLIConfig{
		MainAgent: &config.AICLIMainAgentConfig{
			Routing: &config.AICLIMainAgentRoutingConfig{Enabled: enabled, Levels: []string{"normal"}},
		},
	}}}
}

// TestApplyLocalChatMainAgentRoutingHostWiring 钉住 §6.1 宿主接线：
// aicli.main_agent.routing → LoopReActConfig.MainAgentRouting，且只接主会话。
func TestApplyLocalChatMainAgentRoutingHostWiring(t *testing.T) {
	enabled := mainAgentRoutingHostSession(true)

	base := &agent.LoopReActConfig{}
	applyLocalChatMainAgentRouting(base, enabled, true)
	if base.MainAgentRouting == nil || !base.MainAgentRouting.Enabled {
		t.Fatal("base session with enabled routing must wire MainAgentRouting")
	}
	if base.MainAgentRouting != enabled.Config.AICLI.MainAgent.Routing {
		t.Fatal("wiring must hand the configured object to the loop (NewReActLoop deep-clones it)")
	}

	// 子会话不接：主 Agent 的开关不得改变子 Agent 行为（§6.3 配置隔离）。
	child := &agent.LoopReActConfig{}
	applyLocalChatMainAgentRouting(child, enabled, false)
	if child.MainAgentRouting != nil {
		t.Fatal("child session must not receive main-agent routing")
	}

	// disabled / 无配置 / nil session / nil loopConfig 都必须零行为变化。
	off := &agent.LoopReActConfig{}
	applyLocalChatMainAgentRouting(off, mainAgentRoutingHostSession(false), true)
	if off.MainAgentRouting != nil {
		t.Fatal("disabled routing must stay inert")
	}
	empty := &agent.LoopReActConfig{}
	applyLocalChatMainAgentRouting(empty, &ChatSession{}, true)
	if empty.MainAgentRouting != nil {
		t.Fatal("session without aicli config must stay inert")
	}
	nilSession := &agent.LoopReActConfig{}
	applyLocalChatMainAgentRouting(nilSession, nil, true)
	if nilSession.MainAgentRouting != nil {
		t.Fatal("nil session must stay inert")
	}
	applyLocalChatMainAgentRouting(nil, enabled, true)
}

// TestLocalChatMainAgentRoutingConfigIsIndependentFromSubagents 覆盖 §6.3 配置隔离：
// 子 Agent 路由开关不得顺带打开主 Agent 路由，反之亦然。
func TestLocalChatMainAgentRoutingConfigIsIndependentFromSubagents(t *testing.T) {
	subagentEnabled := true
	session := &ChatSession{Config: &config.Config{AICLI: &config.AICLIConfig{
		Subagents: &config.AICLISubagentsConfig{
			Routing: &config.AICLISubagentRoutingConfig{Enabled: &subagentEnabled},
		},
	}}}
	if got := localChatMainAgentRoutingConfig(session); got != nil {
		t.Fatalf("subagent routing must not enable main-agent routing: %#v", got)
	}
	if got := localChatSubagentRoutingConfig(session); got == nil {
		t.Fatal("subagent routing must stay readable for the scheduler")
	}

	mainAgent := mainAgentRoutingHostSession(true)
	if got := localChatSubagentRoutingConfig(mainAgent); got != nil {
		t.Fatalf("main-agent routing must not enable subagent routing: %#v", got)
	}
	if got := localChatMainAgentRoutingConfig(mainAgent); got == nil {
		t.Fatal("main-agent routing must be readable for the loop")
	}
}

// TestLocalChatMainAgentRoutingOffsetNotPersisted 覆盖 REG2：主 Agent 的动态 route
// 偏移是 **turn 内**的内存态。它既不得写进 session 持久化上下文（provider /
// model / reasoning_effort），也不得跨 turn 泄漏——下一 turn 的第一个请求必须回到
// 宿主基线。子 Agent 路由的同类断言见 chat_actor_host_test.go 的 legacy-route 用例。
func TestLocalChatMainAgentRoutingOffsetNotPersisted(t *testing.T) {
	ctx := context.Background()
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(ctx, userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	baseProvider := &capturingLocalChatProvider{
		name: "base-provider",
		responses: []*runtimellm.LLMResponse{
			{
				Model: "base-model",
				ToolCalls: []runtimetypes.ToolCall{{
					ID:   "call_meta_1",
					Type: "function",
					Name: "predict_task_difficulty",
					Args: map[string]interface{}{"difficulty": "hard", "rationale": "multi-file work"},
				}},
				FinishReason: "tool_calls",
			},
			{Content: "second turn ok", Model: "base-model", FinishReason: "stop"},
		},
	}
	hardProvider := &capturingLocalChatProvider{
		name:      "hard-provider",
		responses: []*runtimellm.LLMResponse{{Content: "hard ok", Model: "hard-model", FinishReason: "stop"}},
	}
	bootstrapManager, err := runtimebootstrap.NewManager(&runtimebootstrap.Options{
		Config: runtimecfg.DefaultRuntimeConfig(),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer bootstrapManager.Stop()
	if err := bootstrapManager.LLMRuntime().RegisterProvider("base-provider", baseProvider); err != nil {
		t.Fatalf("Register base provider: %v", err)
	}
	if err := bootstrapManager.LLMRuntime().RegisterProvider("hard-provider", hardProvider); err != nil {
		t.Fatalf("Register hard provider: %v", err)
	}
	if err := bootstrapManager.LLMRuntime().RegisterProviderAlias("base-model", "base-provider"); err != nil {
		t.Fatalf("Register base alias: %v", err)
	}
	if err := bootstrapManager.LLMRuntime().RegisterProviderAlias("hard-model", "hard-provider"); err != nil {
		t.Fatalf("Register hard alias: %v", err)
	}

	host := &localChatRuntimeHost{
		Bootstrap:    bootstrapManager,
		RuntimeStore: runtimechat.NewInMemoryRuntimeStore(64),
	}
	session := &ChatSession{
		ProviderName:     "base-provider",
		Model:            "base-model",
		ReasoningEffort:  "low",
		RuntimeSession:   rootSession,
		SessionManager:   manager,
		SessionUserID:    userID,
		LocalRuntimeHost: host,
		Config: &config.Config{AICLI: &config.AICLIConfig{MainAgent: &config.AICLIMainAgentConfig{
			Routing: &config.AICLIMainAgentRoutingConfig{
				Enabled: true,
				Levels:  []string{"normal", "hard"},
				Profiles: map[string]config.AICLISubagentRouteProfile{
					"hard": {Provider: "hard-provider", Model: "hard-model", ReasoningEffort: "high"},
				},
			},
		}}},
	}

	actor, err := host.buildSessionActor(rootSession.ID, session, manager.GetStorage(), nil, "")
	if err != nil {
		t.Fatalf("buildSessionActor: %v", err)
	}
	result, err := actor.SubmitPrompt(ctx, "hello", nil)
	if err != nil {
		t.Fatalf("SubmitPrompt turn 1: %v", err)
	}
	if result == nil || !strings.Contains(result.Output, "hard ok") {
		t.Fatalf("expected the hard-route answer inside the turn, got %#v", result)
	}

	// turn 内：上报后真实请求必须已经改道到 hard 档位。
	if len(hardProvider.requests) == 0 {
		t.Fatal("expected an in-turn request on the hard provider")
	}
	routed := hardProvider.requests[0]
	if routed.Provider != "hard-provider" || routed.Model != "hard-model" || routed.ReasoningEffort != "high" {
		t.Fatalf("unexpected in-turn routed request: %#v", routed)
	}

	// turn 结束：宿主会话对象与 session 持久化上下文都不得出现 route 偏移。
	if session.ProviderName != "base-provider" || session.Model != "base-model" || session.ReasoningEffort != "low" {
		t.Fatalf("host session baseline mutated: %#v", session)
	}
	stored, err := manager.Get(ctx, rootSession.ID)
	if err != nil {
		t.Fatalf("manager.Get base session: %v", err)
	}
	if got := agentcontrol.ContextString(stored, sessionmeta.ProviderName); got != "" {
		t.Fatalf("main-agent route offset must not persist provider, got %q", got)
	}
	if got := agentcontrol.ContextString(stored, toolbroker.AgentSessionContextRequestedModel); got != "" {
		t.Fatalf("main-agent route offset must not persist model, got %q", got)
	}
	if got := agentcontrol.ContextString(stored, sessionmeta.ReasoningEffort); got != "" {
		t.Fatalf("main-agent route offset must not persist reasoning effort, got %q", got)
	}

	// 下一个 turn 必须从基线开始（不跨 turn 泄漏）。
	if _, err := actor.SubmitPrompt(ctx, "again", nil); err != nil {
		t.Fatalf("SubmitPrompt turn 2: %v", err)
	}
	if len(baseProvider.requests) < 2 {
		t.Fatalf("expected a second-turn baseline request, got %d", len(baseProvider.requests))
	}
	next := baseProvider.requests[len(baseProvider.requests)-1]
	if next.Provider != "base-provider" || next.Model != "base-model" || next.ReasoningEffort != "low" {
		t.Fatalf("next turn must start from the host baseline: %#v", next)
	}
}
