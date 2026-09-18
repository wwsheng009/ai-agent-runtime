package skills

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// P1：`expose_skills` 回合把 skill 说明与程序清单投影为系统消息，交给模型自选程序。
func TestBuildSkillExposureMessages_InjectsProgramGuide(t *testing.T) {
	registry := skill.NewRegistry(nil)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "run_shell_command",
		Description: "Run a shell command through the managed bash MCP tool.",
		Tools:       []string{"bash"},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{{
			ID:   "run_command",
			Name: "Run shell command",
			Tool: "bash",
			Args: map[string]interface{}{"command": "{{prompt}}"},
		}}},
	}))

	// 重复名称只注入一次，避免同一份说明在上下文里堆叠。
	messages, err := buildSkillExposureMessages(registry, []string{"run_shell_command", "run_shell_command"}, nil)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.Equal(t, "system", messages[0].Role)
	require.Contains(t, messages[0].Content, "Skill program guide")
	require.Contains(t, messages[0].Content, "run_shell_command")
	require.Contains(t, messages[0].Content, "bash")
	require.Contains(t, messages[0].Content, "run_command")
	// 用户消息以 `/skill <name>` 调用标记开头时，guide 必须说明该前缀不是请求本体。
	require.Contains(t, messages[0].Content, "`/skill <name>` call marker")
}

func TestBuildSkillExposureMessages_UnknownSkillFailsLoudly(t *testing.T) {
	registry := skill.NewRegistry(nil)

	_, err := buildSkillExposureMessages(registry, []string{"missing-skill"}, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "missing-skill")
}

func TestBuildSkillExposureMessages_EmptyInputIsNoop(t *testing.T) {
	messages, err := buildSkillExposureMessages(nil, nil, nil)

	require.NoError(t, err)
	require.Empty(t, messages)
}
