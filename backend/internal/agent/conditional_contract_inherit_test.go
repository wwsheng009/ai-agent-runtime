package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 条件性契约（结果契约依赖）必须随子代理工具面一起继承。
//
// 场景：父会话允许 shell。shell 超时 promoted 成 job 引用后，模型必须能调用
// job_output 才读得到结果。子代理若只拿到 shell、没拿到 job_output，就会出现
// 与主会话刚修好时同形的断链面。
//
// 之所以无需改动 appendContractualToolNames：条件性契约的补入结果被表达为
// policy.ContractualTools 成员，而该函数正是从这个集合取工具。
func TestResolveChildToolSurfaceInheritsConditionalContractualTool(t *testing.T) {
	parent := NewAgent(&Config{Name: "parent"}, nil)
	policy := NewToolExecutionPolicy([]string{"view", "shell"}, false)
	policy.GrantConditionalContractualTools("job_output")
	parent.SetToolExecutionPolicy(policy)

	resolved, childPolicy, err := resolveChildToolSurface(parent, SubagentTask{
		ID:             "conditional-contract",
		Goal:           "run a command",
		ToolsWhitelist: []string{"shell"},
	})
	require.NoError(t, err)
	resolved = finalizeChildToolSurface(resolved, childPolicy, agentToolSurface(parent))
	require.Contains(t, resolved.ToolsWhitelist, "shell")
	require.Contains(t, resolved.ToolsWhitelist, "job_output",
		"a child that may promote a shell result must be able to read it")
	require.NoError(t, childPolicy.AllowTool("job_output"))
}

// 显式 deny 仍然压制条件性契约：deny 优先于契约是既有不变量，子代理不得绕过。
func TestResolveChildToolSurfaceConditionalContractRespectsDeny(t *testing.T) {
	parent := NewAgent(&Config{Name: "parent"}, nil)
	policy := NewToolExecutionPolicy([]string{"view", "shell"}, false)
	policy.DeniedTools = map[string]bool{"job_output": true}
	policy.GrantConditionalContractualTools("job_output")
	parent.SetToolExecutionPolicy(policy)

	resolved, childPolicy, err := resolveChildToolSurface(parent, SubagentTask{
		ID:             "conditional-contract-denied",
		Goal:           "run a command",
		ToolsWhitelist: []string{"shell"},
	})
	require.NoError(t, err)
	resolved = finalizeChildToolSurface(resolved, childPolicy, agentToolSurface(parent))
	require.NotContains(t, resolved.ToolsWhitelist, "job_output")
	require.Error(t, childPolicy.AllowTool("job_output"))
}

// 显式空白名单仍然赢得语义：依赖补入不能把"无工具"变成"有工具"。
func TestResolveChildToolSurfaceConditionalContractRespectsEmpty(t *testing.T) {
	parent := NewAgent(&Config{Name: "parent"}, nil)
	policy := NewToolExecutionPolicy([]string{"view", "shell"}, false)
	policy.GrantConditionalContractualTools("job_output")
	parent.SetToolExecutionPolicy(policy)

	resolved, childPolicy, err := resolveChildToolSurface(parent, SubagentTask{
		ID:             "conditional-contract-empty",
		Goal:           "no tools",
		ToolsWhitelist: []string{},
	})
	require.NoError(t, err)
	resolved = finalizeChildToolSurface(resolved, childPolicy, agentToolSurface(parent))
	require.NotContains(t, resolved.ToolsWhitelist, "job_output")
}
