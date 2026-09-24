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
