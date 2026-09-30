package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCapabilitiesForTaskRoleFamilyKeepsShellFloor 钉住角色家族的能力地板：
// 未声明工具表（继承父策略）的任务，其能力面必须按角色家族补齐
// exec_shell/network，否则子代理继承到父工具面却被能力门禁挡住 shell。
func TestCapabilitiesForTaskRoleFamilyKeepsShellFloor(t *testing.T) {
	for _, role := range []string{"explore", "understand", "research", "plan"} {
		caps := CapabilitiesForTask(role, false, nil, nil)
		assert.Contains(t, caps, CapExecShell, "role %q", role)
		assert.Contains(t, caps, CapNetwork, "role %q", role)
	}
	for _, role := range []string{"implement", "generate", "modify", "refactor", "migrate", "integration", "config", "security", "general", "worker", "verify", "test"} {
		caps := CapabilitiesForTask(role, false, nil, nil)
		assert.Contains(t, caps, CapExecShell, "role %q", role)
	}

	// 未知角色不擅自扩权。
	caps := CapabilitiesForTask("banana", false, nil, nil)
	assert.NotContains(t, caps, CapExecShell)
	assert.NotContains(t, caps, CapNetwork)

	// 显式工具表仍然以工具为准（保守最小），不被角色家族覆盖。
	caps = CapabilitiesForTask("general", false, []string{"view"}, nil)
	assert.NotContains(t, caps, CapExecShell)
	assert.NotContains(t, caps, CapNetwork)

	// 只读任务保留 shell（只读命令分类器仍在），但网络按家族补齐也不写盘。
	caps = CapabilitiesForTask("explore", true, nil, nil)
	assert.Contains(t, caps, CapExecShell)
	assert.NotContains(t, caps, CapWriteFS)
}

// TestIntersectAllowedCapabilitiesNeverWidensParentScope 钉住只读派生边界：
// 父策略已带能力面时，派生子面只能取交集——read_only 的「最小面」里虽然列了
// network/agent_management，也不能凭空授予父会话没有的能力。
func TestIntersectAllowedCapabilitiesNeverWidensParentScope(t *testing.T) {
	parent := NewToolExecutionPolicy(nil, false)
	parent.SetCapabilityScope([]Capability{CapReadOnly, CapExecShell})
	assert.ElementsMatch(t,
		[]Capability{CapReadOnly, CapExecShell},
		parent.IntersectAllowedCapabilities(ReadOnlyChildCapabilities()),
		"intersection must keep only what the parent already allowed")

	unscoped := NewToolExecutionPolicy(nil, false)
	assert.ElementsMatch(t, ReadOnlyChildCapabilities(), unscoped.IntersectAllowedCapabilities(ReadOnlyChildCapabilities()),
		"without a parent scope the requested surface is seated unchanged")
}
