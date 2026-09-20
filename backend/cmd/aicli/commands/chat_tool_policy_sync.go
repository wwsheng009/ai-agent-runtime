package commands

import (
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// syncLocalChatToolPolicyAllowlist 让「表面派生」的合成 allowlist 跟上实时工具面。
//
// buildLocalChatToolPolicy 在 agent 构建时把 toolSurface.ListTools() 快照成
// allowlist（chat_actor_host.go）。MCP 服务器（ACP 客户端下发或本地配置链）总是
// 在会话引导之后才完成异步握手，迟到的工具会被 AllowToolInfo 直接拒绝：
//   - CollectToolCatalogDefinitions / computeAvailableTools 把它们过滤出模型工具面；
//   - shouldRefreshStableToolSurface 的「实时目录新增能力」判断读同一策略，
//     因此会话冻结面也永远不会为迟到工具重建（§4.7 R1 / §4.10）。
//
// 这里在 turn 边界把实时工具面里新出现的工具补进同一份策略对象（指针共享，
// 模型面与执行面同时生效），使工具面与执行授权保持一致。
//
// 只对合成策略生效：显式 profile / --allow-tool 策略与带 AllowTools 的权限覆盖层
// 是刻意的窄口径，绝不被工具面扩权；显式 DeniedTools 仍然优先。返回本次新增的
// 工具名（已排序），供调用方记日志。
func syncLocalChatToolPolicyAllowlist(
	session *ChatSession,
	surface runtimeskill.MCPManager,
	broker *toolbroker.Broker,
	policy *runtimepolicy.ToolExecutionPolicy,
) []string {
	if session == nil || session.DisableTools || session.ToolPolicy != nil || policy == nil {
		return nil
	}
	if !policy.AllowlistEnabled {
		return nil
	}
	// 覆盖层用 AllowTools 收窄过 allowlist 时保持收窄结果（product 硬闸）。
	if len(session.PermissionsOverlay.AllowTools) > 0 {
		return nil
	}

	candidates := make([]string, 0, 32)
	if surface != nil {
		candidates = append(candidates, runtimeToolNames(surface.ListTools())...)
	}
	if broker != nil {
		candidates = append(candidates, brokerToolNames(broker.Definitions())...)
	}
	// 调度器贡献的运行时自有工具不在 MCP / broker 目录里，合成策略包含它，
	// 同步时保持一致口径。
	candidates = append(candidates, agent.SpawnSubagentsToolName)

	var added []string
	for _, name := range candidates {
		name = strings.TrimSpace(name)
		if name == "" || policy.AllowedTools[name] || policy.DeniedTools[name] {
			continue
		}
		if policy.AllowedTools == nil {
			policy.AllowedTools = make(map[string]bool)
		}
		policy.AllowedTools[name] = true
		added = append(added, name)
	}
	if len(added) == 0 {
		return nil
	}
	sort.Strings(added)
	return added
}
