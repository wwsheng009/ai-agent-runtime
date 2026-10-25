package agentdef

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

func TestBuildBindingAndResolvedAgent(t *testing.T) {
	def := &Definition{
		Name:                  "explore",
		Description:           "explorer",
		Tools:                 []string{"view", "grep"},
		DisallowedTools:       []string{"write"},
		PermissionMode:        "plan",
		Model:                 "m1",
		PromptMode:            PromptModeExtend,
		CompletionRequirement: CompletionNone,
		Sandbox:               "read-only",
		Body:                  "Be careful.",
		SourcePath:            "builtin:explore",
		Source:                SourceBuiltin,
	}
	binding, err := BuildBinding(def)
	require.NoError(t, err)
	assert.Equal(t, "explore", binding.AgentID)
	assert.Equal(t, runtimepolicy.ModePlan, binding.PermissionMode)
	assert.Equal(t, []string{"view", "grep"}, binding.ToolAllowlist)
	assert.Equal(t, []string{"write"}, binding.ToolDenylist)
	require.NotNil(t, binding.ReadOnly)
	assert.True(t, *binding.ReadOnly)
	assert.Equal(t, "Be careful.", binding.PromptText)

	resolved := ToResolvedAgent(binding, "")
	require.NotNil(t, resolved)
	assert.Equal(t, "explore", resolved.AgentID)
	assert.Equal(t, "m1", resolved.Model)
	assert.Equal(t, []string{"view", "grep"}, resolved.ToolPolicy.Allowlist)
	assert.Equal(t, []string{"write"}, resolved.ToolPolicy.Denylist)
}

func TestMergePromptModes(t *testing.T) {
	binding := &Binding{
		AgentID:    "explore",
		PromptText: "Role body",
		PromptMode: PromptModeExtend,
	}
	assert.Equal(t, "Base\n\n# Agent Role (explore)\nRole body", MergePrompt("Base", binding))

	binding.PromptMode = PromptModeFull
	assert.Equal(t, "Role body", MergePrompt("Base", binding))
}

// TestBuildBinding_ProfileCannotDefaultToBypassPermissions 是 D16（Batch 9）的
// 单一权威断言：profile 只能收窄安全基线，profile 声明的默认权限模式不得为
// bypass_permissions；同一取值在非 profile 来源（builtin/user/project）仍然
// 合法，证明约束精确落在 profile 通道上，而不是把能力本身禁用。
func TestBuildBinding_ProfileCannotDefaultToBypassPermissions(t *testing.T) {
	profileDef := func() *Definition {
		return &Definition{
			Name:           "reviewer",
			PermissionMode: string(runtimepolicy.ModeBypassPermissions),
			SourcePath:     "profile:review/agents/reviewer/agent.yaml",
			Source:         SourceProfile,
		}
	}

	_, err := BuildBinding(profileDef())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "D16")
	assert.Contains(t, err.Error(), string(runtimepolicy.ModeBypassPermissions))

	// 非 profile 来源不受本约束（显式 --yolo / --permission-mode 的等价通道）。
	userDef := profileDef()
	userDef.Source = SourceUser
	binding, err := BuildBinding(userDef)
	require.NoError(t, err)
	assert.Equal(t, runtimepolicy.ModeBypassPermissions, binding.PermissionMode)

	// profile 声明收窄档位（plan / accept_edits / default）不受影响。
	planDef := profileDef()
	planDef.PermissionMode = string(runtimepolicy.ModePlan)
	binding, err = BuildBinding(planDef)
	require.NoError(t, err)
	assert.Equal(t, runtimepolicy.ModePlan, binding.PermissionMode)
}

// read_only 与 sandbox 解耦（2026-10）：sandbox: read-only 仍派生只读工具面，
// 但显式 read_only 覆盖派生值，使角色可以「只读工具面 + 无路径监狱」——
// 这是 builtin explore 的形态，也是子代理跨项目读取与父代理对齐的基础。
func TestBuildBindingReadOnlyDecoupledFromSandbox(t *testing.T) {
	// 兼容路径：sandbox: read-only 仍隐含 ReadOnly + read-only 沙箱。
	legacy := &Definition{Name: "legacy", Sandbox: "read-only", Source: SourceBuiltin}
	binding, err := BuildBinding(legacy)
	require.NoError(t, err)
	require.NotNil(t, binding.ReadOnly)
	assert.True(t, *binding.ReadOnly)
	assert.Equal(t, "read-only", binding.Sandbox["mode"])

	// 显式 read_only: true 且无 sandbox：只读工具面成立，但不物化路径沙箱。
	explorer := &Definition{Name: "explorer", ReadOnly: readOnlyTrue(), Source: SourceBuiltin}
	binding, err = BuildBinding(explorer)
	require.NoError(t, err)
	require.NotNil(t, binding.ReadOnly)
	assert.True(t, *binding.ReadOnly)
	assert.Empty(t, binding.Sandbox)

	// 显式 read_only: false 压过 sandbox: read-only 的派生（显式声明优先）。
	writable := &Definition{Name: "writable", Sandbox: "read-only", ReadOnly: func() *bool { v := false; return &v }(), Source: SourceBuiltin}
	binding, err = BuildBinding(writable)
	require.NoError(t, err)
	require.NotNil(t, binding.ReadOnly)
	assert.False(t, *binding.ReadOnly)
	assert.Equal(t, "read-only", binding.Sandbox["mode"], "sandbox mode stays for path bounds")
}

// TestBuiltinExploreReadOnlyWithoutSandboxJail 钉住内建 explore 的目标形态：
// 只读工具面保留（含 plan 默认），但不再物化 workspace-only 路径沙箱，
// 使子代理的路径边界与父代理一致（§4.5 门禁 + 会话准入），除非项目/用户
// def 显式声明 sandbox 收窄。
func TestBuiltinExploreReadOnlyWithoutSandboxJail(t *testing.T) {
	var explore *Definition
	for _, def := range BuiltinDefinitions() {
		if def.Name == "explore" {
			explore = def
			break
		}
	}
	require.NotNil(t, explore)
	assert.Empty(t, explore.Sandbox)
	require.NotNil(t, explore.ReadOnly)
	assert.True(t, *explore.ReadOnly)

	binding, err := BuildBinding(explore)
	require.NoError(t, err)
	require.NotNil(t, binding.ReadOnly)
	assert.True(t, *binding.ReadOnly)
	assert.Empty(t, binding.Sandbox, "builtin explore must not materialize a path sandbox")
	assert.Equal(t, runtimepolicy.ModePlan, binding.PermissionMode)
}
