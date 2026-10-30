package commands

import (
	"testing"

	"github.com/stretchr/testify/require"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// 本地结果契约依赖闭包（§9.3 的 local 侧）。
//
// 这三条覆盖的是"用户 allowlist 与实际生效面不一致时必须可解释"这一可观测性
// 要求：Granted / Withdrawn 是两类偏离，HasChanges 决定要不要打摘要。
func TestApplyLocalToolSurfaceDependenciesGrantsCompanion(t *testing.T) {
	policy := runtimepolicy.NewToolExecutionPolicy([]string{"view", "shell"}, false)

	summary := applyLocalToolSurfaceDependencies(policy)

	require.Contains(t, summary.Granted, "job_output")
	require.True(t, summary.HasChanges())
	require.NoError(t, policy.AllowTool("job_output"),
		"job_output should be callable once shell is allowed")
	// 只读 companion 不得被当成 allowlist 成员写回去。
	require.False(t, policy.AllowedTools["job_output"],
		"the grant must go through ContractualTools, not AllowedTools")
}

// deny 恒优先：拒绝 job_output 时必须撤下 shell，而不是留一个断链工具。
func TestApplyLocalToolSurfaceDependenciesWithdrawsBrokenProducer(t *testing.T) {
	policy := runtimepolicy.NewToolExecutionPolicy([]string{"view", "shell"}, false)
	policy.DeniedTools = map[string]bool{"job_output": true}

	summary := applyLocalToolSurfaceDependencies(policy)

	require.Contains(t, summary.Withdrawn, "shell")
	require.Empty(t, summary.Granted)
	require.Error(t, policy.AllowTool("shell"),
		"a producer whose result cannot be consumed must be withdrawn")
	require.Error(t, policy.AllowTool("job_output"), "the deny must survive")
}

// 空面不复活：allowed-tools 为空（但显式启用 allowlist）时不得补入任何东西。
func TestApplyLocalToolSurfaceDependenciesLeavesEmptySurface(t *testing.T) {
	policy := runtimepolicy.NewToolExecutionPolicy([]string{}, false)
	policy.AllowlistEnabled = true
	policy.AllowedTools = map[string]bool{}

	summary := applyLocalToolSurfaceDependencies(policy)

	require.False(t, summary.HasChanges())
	require.Empty(t, summary.Granted)
	require.Error(t, policy.AllowTool("job_output"))
}

// allowlist 未启用（全部允许）时不做闭包：job_output 本来就可调用，
// 补入只会让契约集合虚胖。
//
// 注意：ContractualTools 在构造时已含无条件契约 artifact_read，因此这里断言
// 的是"没有新增条件性契约"，而不是"集合为空"。
func TestApplyLocalToolSurfaceDependenciesSkipsWhenAllowlistDisabled(t *testing.T) {
	policy := runtimepolicy.NewToolExecutionPolicy([]string{"shell"}, true)
	before := len(policy.ContractualTools)

	summary := applyLocalToolSurfaceDependencies(policy)

	require.False(t, summary.HasChanges())
	require.Len(t, policy.ContractualTools, before,
		"a fully-allowed surface needs no conditional contract")
	require.False(t, policy.ContractualTools["job_output"],
		"job_output is already callable without an allowlist; granting it would be redundant")
}

// 幂等：重复应用到同一 policy 不应重复记录授予。
func TestApplyLocalToolSurfaceDependenciesIsIdempotent(t *testing.T) {
	policy := runtimepolicy.NewToolExecutionPolicy([]string{"view", "shell"}, false)

	first := applyLocalToolSurfaceDependencies(policy)
	require.Contains(t, first.Granted, "job_output")

	second := applyLocalToolSurfaceDependencies(policy)
	require.Empty(t, second.Granted, "second pass should grant nothing new")
	require.False(t, second.HasChanges())
}

// 摘要文本要能直接放进日志：只列偏离项，不含无关噪声。
func TestLocalToolSurfaceSummaryString(t *testing.T) {
	require.Equal(t, "", localToolSurfaceSummary{}.String())

	got := localToolSurfaceSummary{
		Granted:   []string{"job_output"},
		Withdrawn: []string{"shell"},
	}.String()
	require.Equal(t, "auto-exposed job_output; withdrawn shell", got)
}
