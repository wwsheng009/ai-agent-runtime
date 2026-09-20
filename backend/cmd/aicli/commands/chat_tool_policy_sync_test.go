package commands

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func toolPolicySyncContains(names []string, target string) bool {
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}

// TestSyncLocalChatToolPolicyAllowlistAddsLateMCPTools 覆盖 §4.7 R1 / §4.10：
// MCP 服务器（客户端下发 / 本地配置链）总在 agent 构建之后才完成异步握手，
// 合成 allowlist 必须在 turn 边界跟上实时工具面，否则迟到工具既进不了模型
// 工具面，也不会触发会话冻结面的重建。
func TestSyncLocalChatToolPolicyAllowlistAddsLateMCPTools(t *testing.T) {
	session := &ChatSession{}
	policy := runtimepolicy.NewToolExecutionPolicy([]string{"view"}, false)
	surface := stubLocalChatToolSurface{tools: []runtimeskill.ToolInfo{
		{Name: "view"},
		{Name: "acp_e2e_echo", MCPName: "local-e2e"},
	}}

	added := syncLocalChatToolPolicyAllowlist(session, surface, nil, policy)
	if !toolPolicySyncContains(added, "acp_e2e_echo") {
		t.Fatalf("late MCP tool must join the surface-derived allowlist, got %v", added)
	}
	if !policy.AllowedTools["acp_e2e_echo"] {
		t.Fatal("policy allowlist must contain the late MCP tool")
	}
	if err := policy.AllowTool("acp_e2e_echo"); err != nil {
		t.Fatalf("late MCP tool must pass the execution policy after sync: %v", err)
	}
	// 调度器贡献的运行时自有工具与合成口径一致（chat_actor_host.go 同款）。
	if !policy.AllowedTools[agent.SpawnSubagentsToolName] {
		t.Fatalf("scheduler-owned tool %q must stay in the synthesized allowlist", agent.SpawnSubagentsToolName)
	}
	// 幂等：第二次同步不再新增，避免每个 turn 反复写日志。
	if again := syncLocalChatToolPolicyAllowlist(session, surface, nil, policy); len(again) != 0 {
		t.Fatalf("sync must be idempotent, got %v", again)
	}
}

// TestSyncLocalChatToolPolicyAllowlistNeverWidensExplicitPolicies 锁定不扩权边界：
// 显式 profile 策略、权限覆盖层收窄、DisableTools、显式 deny 与未启用 allowlist
// 都不允许被工具面扩权。
func TestSyncLocalChatToolPolicyAllowlistNeverWidensExplicitPolicies(t *testing.T) {
	surface := stubLocalChatToolSurface{tools: []runtimeskill.ToolInfo{
		{Name: "acp_e2e_echo", MCPName: "local-e2e"},
	}}

	cases := []struct {
		name    string
		session *ChatSession
		policy  *runtimepolicy.ToolExecutionPolicy
	}{
		{
			name:    "explicit profile policy",
			session: &ChatSession{ToolPolicy: runtimepolicy.NewToolExecutionPolicy([]string{"view"}, false)},
			policy:  runtimepolicy.NewToolExecutionPolicy([]string{"view"}, false),
		},
		{
			name:    "permissions overlay narrowed allowlist",
			session: &ChatSession{PermissionsOverlay: runtimepolicy.PermissionsOverlay{AllowTools: []string{"view"}}},
			policy:  runtimepolicy.NewToolExecutionPolicy([]string{"view"}, false),
		},
		{
			name:    "tools disabled",
			session: &ChatSession{DisableTools: true},
			policy:  runtimepolicy.NewToolExecutionPolicy([]string{"view"}, false),
		},
		{
			name:    "allowlist disabled",
			session: &ChatSession{},
			policy:  runtimepolicy.NewToolExecutionPolicy(nil, false),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(tc.policy.AllowedTools)
			added := syncLocalChatToolPolicyAllowlist(tc.session, surface, nil, tc.policy)
			if len(added) != 0 {
				t.Fatalf("policy must not be widened, got %v", added)
			}
			if len(tc.policy.AllowedTools) != before {
				t.Fatalf("allowlist size changed: before=%d after=%d", before, len(tc.policy.AllowedTools))
			}
			if tc.policy.AllowedTools["acp_e2e_echo"] {
				t.Fatal("late MCP tool must not enter a non-synthesized allowlist")
			}
		})
	}
}

// TestSyncLocalChatToolPolicyAllowlistRespectsExplicitDeny 显式 deny 优先于工具面：
// 即使工具出现在实时目录里，也不得进入 allowlist，执行期仍被拒绝。
func TestSyncLocalChatToolPolicyAllowlistRespectsExplicitDeny(t *testing.T) {
	session := &ChatSession{}
	policy := runtimepolicy.NewToolExecutionPolicy([]string{"view"}, false)
	policy.DeniedTools = map[string]bool{"acp_e2e_echo": true}
	surface := stubLocalChatToolSurface{tools: []runtimeskill.ToolInfo{
		{Name: "acp_e2e_echo", MCPName: "local-e2e"},
	}}

	added := syncLocalChatToolPolicyAllowlist(session, surface, nil, policy)
	if toolPolicySyncContains(added, "acp_e2e_echo") || policy.AllowedTools["acp_e2e_echo"] {
		t.Fatalf("explicitly denied tool must never be allowlisted, added=%v", added)
	}
	if err := policy.AllowTool("acp_e2e_echo"); err == nil {
		t.Fatal("explicitly denied tool must stay denied by the execution policy")
	}
}
