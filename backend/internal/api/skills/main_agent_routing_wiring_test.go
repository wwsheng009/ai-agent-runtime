package skills

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func mainAgentRoutingSnapshotConfig() *agentconfig.Config {
	temperature := 0.2
	return &agentconfig.Config{AICLI: &agentconfig.AICLIConfig{
		MainAgent: &agentconfig.AICLIMainAgentConfig{
			Routing: &agentconfig.AICLIMainAgentRoutingConfig{
				Enabled:         true,
				Levels:          []string{"easy", "normal", "hard"},
				ExpensiveLevels: []string{"hard"},
				Profiles: map[string]agentconfig.AICLISubagentRouteProfile{
					"hard": {Model: "strong-model", Temperature: &temperature},
				},
			},
		},
	}}
}

// TestCloneAICLIRoutingConfigKeepsMainAgentRouting 钉住 runtime-server 配置快照必须
// 保留 aicli.main_agent.routing：漏拷 = 宿主接线静默失效（配置侧看起来一切正常）。
func TestCloneAICLIRoutingConfigKeepsMainAgentRouting(t *testing.T) {
	source := mainAgentRoutingSnapshotConfig()
	cloned := cloneAICLIRoutingConfig(source)
	if cloned == nil || cloned.AICLI == nil || cloned.AICLI.MainAgent == nil || cloned.AICLI.MainAgent.Routing == nil {
		t.Fatal("clone must keep aicli.main_agent.routing")
	}
	if cloned.AICLI.MainAgent.Routing == source.AICLI.MainAgent.Routing {
		t.Fatal("clone must not share the routing pointer")
	}
	if !cloned.AICLI.MainAgent.Routing.Enabled {
		t.Fatal("clone must preserve enabled")
	}
	// 深拷贝：改快照不得污染源配置（热重载写入路径共享 map/slice 会互相影响）。
	cloned.AICLI.MainAgent.Routing.Levels[0] = "expert"
	cloned.AICLI.MainAgent.Routing.Profiles["hard"] = agentconfig.AICLISubagentRouteProfile{Model: "mutated"}
	if got := source.AICLI.MainAgent.Routing.Levels[0]; got != "easy" {
		t.Fatalf("source levels mutated: %q", got)
	}
	if got := source.AICLI.MainAgent.Routing.Profiles["hard"].Model; got != "strong-model" {
		t.Fatalf("source profile mutated: %q", got)
	}
}

// TestHandlerMainAgentRoutingConfigReadsSnapshot 钉住 handler → 配置快照这一跳。
func TestHandlerMainAgentRoutingConfigReadsSnapshot(t *testing.T) {
	handler := &Handler{}
	if got := handler.mainAgentRoutingConfig(); got != nil {
		t.Fatalf("handler without config must return nil, got %#v", got)
	}
	handler.SetAICLIConfig(mainAgentRoutingSnapshotConfig())
	got := handler.mainAgentRoutingConfig()
	if got == nil || !got.Enabled {
		t.Fatalf("handler must read main-agent routing from the snapshot, got %#v", got)
	}
	if len(got.Levels) != 3 {
		t.Fatalf("snapshot levels = %v, want 3 entries", got.Levels)
	}
}

// TestApplyAPISessionMainAgentRoutingGate 覆盖 §6.1/§6.3：只接主会话，关闭态零行为变化。
func TestApplyAPISessionMainAgentRoutingGate(t *testing.T) {
	routing := &agentconfig.AICLIMainAgentRoutingConfig{Enabled: true, Levels: []string{"normal"}}

	base := &agent.LoopReActConfig{}
	applyAPISessionMainAgentRouting(base, routing, true)
	if base.MainAgentRouting != routing {
		t.Fatal("base session must receive the configured routing")
	}

	child := &agent.LoopReActConfig{}
	applyAPISessionMainAgentRouting(child, routing, false)
	if child.MainAgentRouting != nil {
		t.Fatal("child session must not receive main-agent routing")
	}

	disabled := &agent.LoopReActConfig{}
	applyAPISessionMainAgentRouting(disabled, &agentconfig.AICLIMainAgentRoutingConfig{Enabled: false, Levels: []string{"normal"}}, true)
	if disabled.MainAgentRouting != nil {
		t.Fatal("disabled routing must stay inert")
	}

	empty := &agent.LoopReActConfig{}
	applyAPISessionMainAgentRouting(empty, nil, true)
	if empty.MainAgentRouting != nil {
		t.Fatal("missing config must stay inert")
	}
	applyAPISessionMainAgentRouting(nil, routing, true)
}
