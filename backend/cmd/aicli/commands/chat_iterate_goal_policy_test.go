package commands

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// TestBuildLocalChatToolPolicyGrantsIterateGoalOnlyWhenEnabled 锁定 P2-2' 的
// 执行授权接线：合成 allowlist 只在部署显式开启 iterate_goal 时补入该工具
// （否则它会在 surface 构建后被 filterPolicyBlockedToolDefinitions 剔除，
// 形成"工具在目录里、模型永远看不到"的静默失效）；显式 profile / permissions
// 名单不被扩宽（与 spawn_subagents 同口径）。
func TestBuildLocalChatToolPolicyGrantsIterateGoalOnlyWhenEnabled(t *testing.T) {
	surface := stubLocalChatToolSurface{tools: []runtimeskill.ToolInfo{{Name: "view"}}}

	off := buildLocalChatToolPolicy(&ChatSession{}, surface, nil, false)
	if off == nil || !off.AllowlistEnabled {
		t.Fatalf("expected synthesized allowlist policy, got %#v", off)
	}
	if off.AllowsDefinition(agent.IterateGoalToolName) {
		t.Fatal("iterate_goal must not be authorized while the deployment keeps it disabled")
	}
	if !off.AllowsDefinition(agent.SpawnSubagentsToolName) {
		t.Fatal("spawn_subagents must stay authorized in the synthesized allowlist")
	}

	on := buildLocalChatToolPolicy(&ChatSession{}, surface, nil, true)
	if !on.AllowsDefinition(agent.IterateGoalToolName) {
		t.Fatal("enabled iterate_goal must be admitted by the synthesized allowlist")
	}

	explicit := buildLocalChatToolPolicy(&ChatSession{
		ToolPolicy: runtimepolicy.NewToolExecutionPolicy([]string{"view"}, false),
	}, surface, nil, true)
	if explicit.AllowsDefinition(agent.IterateGoalToolName) {
		t.Fatal("explicit profile allowlists must not be widened with iterate_goal")
	}
}
