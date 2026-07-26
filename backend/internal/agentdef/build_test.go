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
