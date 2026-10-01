package policy

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 2026-09-30 真机缺口：子代理以 nil 工具表继承父工具面时，能力地板却按
// （未识别的）role 推导成 read_only+write_fs，于是继承到的 shell 工具被
// "capability not allowed by execution policy: exec_shell" 拒绝。继承工具面
// 必须继承父能力边界：不宽于父，也不再凭空收窄。
func TestDeriveChildForTaskInheritsParentScopeWhenToolSurfaceInherited(t *testing.T) {
	parent := NewToolExecutionPolicy(nil, false)
	parent.SetCapabilityScope([]Capability{CapReadOnly, CapWriteFS, CapExecShell})

	child := parent.DeriveChildForTask(nil, false, "", nil)

	require.NoError(t, child.AllowCapabilities([]Capability{CapExecShell}),
		"inherited shell tool must stay executable")
	require.Error(t, child.AllowCapabilities([]Capability{CapNetwork}),
		"inherited scope must never widen the parent")
}

// 父策略没有能力门禁时，不得为子代理发明一个更窄的门禁：那会把刚继承到的
// 工具面静默挡掉（本缺口的最常见形态，默认配置下父策略正是无 scope）。
func TestDeriveChildForTaskWithoutParentScopeKeepsChildUnscoped(t *testing.T) {
	parent := NewToolExecutionPolicy(nil, false)

	child := parent.DeriveChildForTask(nil, false, "analyst", nil)

	require.False(t, child.CapabilityScopeEnabled,
		"a parent without a capability gate must not gain one for the child")
	require.NoError(t, child.AllowCapabilities([]Capability{CapExecShell}))
}

// 只读继承只能收窄写面：write/external 必须被剥离，exec_shell 保留给逐条
// 分类的只读命令（命令级边界由 ReadOnly 标志与 shell 分类器执行）。
func TestDeriveChildForTaskInheritedScopeDropsWriteCapabilitiesForReadOnly(t *testing.T) {
	parent := NewToolExecutionPolicy(nil, false)
	parent.SetCapabilityScope([]Capability{CapReadOnly, CapWriteFS, CapExecShell, CapExternalSideEffect})

	child := parent.DeriveChildForTask(nil, true, "", nil)

	require.True(t, child.ReadOnly)
	require.NoError(t, child.AllowCapabilities([]Capability{CapExecShell}))
	require.Error(t, child.AllowCapabilities([]Capability{CapWriteFS}))
	require.Error(t, child.AllowCapabilities([]Capability{CapExternalSideEffect}))
}

// 显式工具表仍走工具/角色推导：声明了 shell 就必须拿到 exec_shell。
func TestDeriveChildForTaskExplicitToolsStillUseToolDerivedFloor(t *testing.T) {
	parent := NewToolExecutionPolicy(nil, false)

	child := parent.DeriveChildForTask([]string{"view", "shell"}, false, "implementer", nil)

	require.NoError(t, child.AllowCapabilities([]Capability{CapExecShell}))
}
