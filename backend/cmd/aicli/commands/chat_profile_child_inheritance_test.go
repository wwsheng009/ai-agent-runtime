package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
)

// TestApplyLocalChildAgentdefToolPolicy_NeverWidensParentProfilePolicy 覆盖 FR-9 的
// 「只收窄」：本地 spawn 子代理在父 profile 已收窄的策略之上叠加 explore agentdef，
// 派生结果既不能放宽父允许集，也不能恢复 explore 明确拒绝的写入工具。
func TestApplyLocalChildAgentdefToolPolicy_NeverWidensParentProfilePolicy(t *testing.T) {
	parentAllowlist := []string{"view", "grep", "write"}
	parentPolicy := agent.NewToolExecutionPolicy(parentAllowlist, false)
	session := &ChatSession{ToolPolicy: parentPolicy}

	apiAgent := agent.NewAgent(&agent.Config{Name: "child", Provider: "test", Model: "test"}, nil)
	apiAgent.SetToolExecutionPolicy(parentPolicy)

	applyLocalChildAgentdefToolPolicy(apiAgent, "explore", session, t.TempDir(), t.TempDir())

	child := apiAgent.GetToolExecutionPolicy()
	require.NotNil(t, child)
	assert.NotSame(t, parentPolicy, child, "子策略必须是派生副本，不得原地改写父会话策略")
	assert.True(t, child.ReadOnly, "explore 的 read_only 必须叠加到父策略之上")
	assert.True(t, child.DeniedTools["write"])
	assert.True(t, child.DeniedTools["edit"])
	assert.False(t, child.AllowsDefinition("write"), "写入工具不得在子会话可见")
	assert.False(t, child.AllowsDefinition("bash"), "子策略不得获得父允许集之外的工具")
	assert.True(t, child.AllowsDefinition("view"), "父允许集内的只读工具必须保留")
	assert.False(t, parentPolicy.ReadOnly, "父会话策略不得被子代理派生过程改写")
	assert.False(t, parentPolicy.DeniedTools["write"], "父会话策略的拒绝集不得被子代理派生过程改写")
}
