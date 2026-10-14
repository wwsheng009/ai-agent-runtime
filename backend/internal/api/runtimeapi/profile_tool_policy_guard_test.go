package runtimeapi

// Profile 裁剪双落点一致性护栏
// （docs/architecture/unified-tool-surface-registry.md §4.3 / 阶段 3）。
//
// 背景与不变量：profile 的 ToolPolicySpec 在两个宿主各有消费点——
//   runtime-server：resolveProfileRuntimeState → profileState.ToolPolicy
//     → applyAgentExecutionPolicy（装配期 allowlist → 请求面收敛）
//   aicli TUI：applyProfileStateToChatSession → session.ToolPolicy
//     → buildLocalChatToolPolicy（actor 构建期固化 → 执行策略）
// 两落点必须对同一份 ResolvedToolPolicy 产出语义一致的策略（allowlist 收窄、
// deny 恒优先、read_only 传播），否则同一 profile 在两个入口的裁剪结果漂移
// ——这正是 T4（profile 裁剪先于宿主硬边界，硬边界只能再收窄）要防的。
//
// 这里不重复 profile 解析测试（internal/profile/*_test.go 已覆盖 YAML 语义），
// 只钉住「两个消费点对同一输入的行为等价」这一条跨宿主契约。

import (
	"testing"

	"github.com/stretchr/testify/require"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/profile"
	profileinput "github.com/wwsheng009/ai-agent-runtime/internal/profileinput"
)

// toRuntimeExecutionPolicy 走 profileinput 的既有转换（两宿主同源：
// profile.ResolvedToolPolicy → profileinput.ResolvedToolPolicy → policy）。
func toRuntimeExecutionPolicy(t *testing.T, resolved profile.ResolvedToolPolicy) *runtimepolicy.ToolExecutionPolicy {
	t.Helper()
	input := profileinput.ResolvedToolPolicy{
		Allowlist: append([]string(nil), resolved.Allowlist...),
		Denylist:  append([]string(nil), resolved.Denylist...),
		ReadOnly:  resolved.ReadOnly,
	}
	policy, err := profileinput.BuildToolExecutionPolicy(input)
	require.NoError(t, err)
	require.NotNil(t, policy, "simple spec must yield a policy")
	return policy
}

// buildRuntimeServerPolicy 复刻 applyAgentExecutionPolicy 的 profile 段
// （handler.go:5560-5577：ReadOnly 合并、allowlist 传递、denied 传递），
// 剥离 sandbox/workspace 等宿主本地硬边界——那些按 T4 属于"再收窄"层，
// 不参与双落点一致性判定。
func buildRuntimeServerPolicy(profilePolicy *runtimepolicy.ToolExecutionPolicy, readOnlyBase bool) *runtimepolicy.ToolExecutionPolicy {
	readOnly := readOnlyBase
	if profilePolicy != nil && profilePolicy.ReadOnly {
		readOnly = true
	}
	allowlist := []string(nil)
	if profilePolicy != nil && profilePolicy.AllowlistEnabled {
		allowlist = profilePolicy.AllowedToolNames()
	}
	toolPolicy := runtimepolicy.NewToolExecutionPolicy(allowlist, readOnly)
	if profilePolicy != nil && len(profilePolicy.DeniedTools) > 0 {
		toolPolicy.DeniedTools = make(map[string]bool, len(profilePolicy.DeniedTools))
		for name, denied := range profilePolicy.DeniedTools {
			if denied {
				toolPolicy.DeniedTools[name] = true
			}
		}
	}
	return toolPolicy
}

// buildTUIClientPolicy 复刻 buildLocalChatToolPolicy 的 profile 段
// （chat_actor_host.go:2771：session.ToolPolicy 已由 applyProfileStateToChatSession
// 从 ResolvedToolPolicy 投影而来，此处直接消费）。
func buildTUIClientPolicy(sessionPolicy *runtimepolicy.ToolExecutionPolicy) *runtimepolicy.ToolExecutionPolicy {
	if sessionPolicy == nil {
		return nil
	}
	return sessionPolicy.Clone()
}

// resolvedPolicyFromSpec 用 profile 解析链的合并函数把 ToolPolicySpec 变成
// ResolvedToolPolicy——两宿主各自的 profileRuntimeState / ChatSession 都源自
// 同一解析输出（Resolve 内部走同一 MergeToolPolicies）。
func resolvedPolicyFromSpec(t *testing.T, spec profile.ToolPolicySpec) profile.ResolvedToolPolicy {
	t.Helper()
	return profile.MergeToolPolicies(spec)
}

func TestProfileToolPolicyDualSinkEquivalence(t *testing.T) {
	spec := profile.ToolPolicySpec{
		Allowlist: []string{"shell", "view", "job_start"},
		Denylist:  []string{"openai_image_generate"},
		ReadOnly:  boolPtr(true),
	}
	resolved := resolvedPolicyFromSpec(t, spec)

	// 两宿主从同一 resolved 出发的策略语义必须一致。
	runtimePolicy := buildRuntimeServerPolicy(toRuntimeExecutionPolicy(t, resolved), false)
	tuiPolicy := buildTUIClientPolicy(toRuntimeExecutionPolicy(t, resolved))

	require.Equal(t, runtimePolicy.ReadOnly, tuiPolicy.ReadOnly, "read_only 传播必须一致")
	require.Equal(t, runtimePolicy.AllowlistEnabled, tuiPolicy.AllowlistEnabled, "allowlist 启用态必须一致")
	require.Equal(t, runtimePolicy.AllowedToolNames(), tuiPolicy.AllowedToolNames(), "allowlist 名单必须一致")
	require.Equal(t, runtimePolicy.DeniedTools, tuiPolicy.DeniedTools, "deny 名单必须一致")

	// 收窄语义抽检：deny 恒优先（deny 的工具即便在 allowlist 里也拒绝）。
	require.False(t, runtimePolicy.AllowsDefinition("openai_image_generate"), "deny 恒优先")
	require.True(t, runtimePolicy.AllowsDefinition("shell"))
}

func TestProfileToolPolicyReadOnlyPropagation(t *testing.T) {
	// 宿主基线只读 + profile 未声明 ReadOnly → 保持只读（安全基线只收不放宽）。
	spec := profile.ToolPolicySpec{Allowlist: []string{"view"}}
	resolved := resolvedPolicyFromSpec(t, spec)
	policy := buildRuntimeServerPolicy(toRuntimeExecutionPolicy(t, resolved), true)
	require.True(t, policy.ReadOnly)

	// 宿主基线可写 + profile 声明 ReadOnly → 收窄为只读。
	specReadOnly := profile.ToolPolicySpec{Allowlist: []string{"view"}, ReadOnly: boolPtr(true)}
	resolvedReadOnly := resolvedPolicyFromSpec(t, specReadOnly)
	policyReadOnly := buildRuntimeServerPolicy(toRuntimeExecutionPolicy(t, resolvedReadOnly), false)
	require.True(t, policyReadOnly.ReadOnly)
}

func boolPtr(v bool) *bool { return &v }
