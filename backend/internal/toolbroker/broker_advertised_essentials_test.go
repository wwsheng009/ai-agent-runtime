package toolbroker

import (
	"testing"

	"github.com/stretchr/testify/require"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
)

// TestAdvertisedEssentialsDeclaredConsistently 把
// 「广播面 == 注册表声明 == policy 判定」这三者钉在一起，覆盖**全部**广播定义，
// 而不是只覆盖巡检族（既有的 supervision_tool_registry_test.go 只覆盖巡检族，
// 所以漏掉了下面这个 bug）。
//
// 背景（2026-10-16，P1 实测）：broker 广播的是**规范名**（subagent_spawn /
// team_spawn / subagent_list …），而 P1 前 policy 的 essential 名单用
// normalizeToolName 先折向**旧拼写**（spawn_agent / spawn_team / list_agents …）
// 再匹配，方向相反 → 规范名在 essential 表里全部落空 → 窄 allowlist 下
// subagent_spawn / team_spawn / subagent_spawn_batch 的定义会被
// filterPolicyBlockedToolDefinitions **静默摘掉**，宿主以为接好了，模型却看不到
// 委派入口，而 prompt 仍在教它委派。P1 把名单收进 toolregistry 后此 bug 消失；
// 本测试在 P1 前对 subagent_spawn 是红的。
//
// 语义注记（2026-10-16）：Entry.Essential 已不是 allowlist 豁免，"窄名单下
// 定义存活"改为由通配 allowlist 提供（["*"] / ["subagent_*"]），测试尾部
// 改钉通配路径。
func TestAdvertisedEssentialsDeclaredConsistently(t *testing.T) {
	defs := (&Broker{}).Definitions()
	require.NotEmpty(t, defs, "广播面不得为空，否则本测试是空转")

	essentialAdvertised := 0
	for _, def := range defs {
		entry, registered := toolregistry.LookupLive(def.Name)
		wantEssential := registered && entry.Essential

		// 广播名的必需性必须与注册表声明逐一相等：任何一侧私自扩/缩都会红。
		require.Equal(t, wantEssential, toolregistry.IsEssential(def.Name),
			"%s: 广播名的必需性必须等于 Entry.Essential", def.Name)

		if !wantEssential {
			continue
		}
		essentialAdvertised++
	}
	require.NotZero(t, essentialAdvertised,
		"广播面必须至少含一个 runtime-owned essential，否则断言退化为空转")

	// essential 工具在窄名单下被裁、在通配名单下存活——这是 2026-10-16 之后的
	// 新契约（profile 完全控制工具面；需要控制面就用通配或显式名字）。
	narrow := runtimepolicy.NewToolExecutionPolicy([]string{"view", "grep"}, false)
	wildcard := runtimepolicy.NewToolExecutionPolicy([]string{"*"}, false)
	for _, def := range defs {
		entry, registered := toolregistry.LookupLive(def.Name)
		if !registered || !entry.Essential {
			continue
		}
		require.False(t, narrow.AllowsDefinition(def.Name),
			"%s: 窄 allowlist 必须裁掉未列出的 essential 定义", def.Name)
		require.True(t, wildcard.AllowsDefinition(def.Name),
			"%s: 通配 allowlist 必须放行 essential 定义", def.Name)
	}

	// 委派入口单独再钉一次，避免将来有人把 GroupOrchestration 的 Essential
	// 标记顺手删掉而其余断言仍然全绿。
	for _, name := range []string{ToolSubagentSpawn, ToolTeamSpawn} {
		require.False(t, narrow.AllowsDefinition(name),
			"%s: 窄 allowlist 下委派入口必须被裁掉", name)
		require.True(t, wildcard.AllowsDefinition(name),
			"%s: 通配 allowlist 下委派入口必须有定义存活", name)
	}
}
