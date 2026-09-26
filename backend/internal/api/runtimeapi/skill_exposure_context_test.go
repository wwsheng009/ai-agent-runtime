package runtimeapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
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
	messages, err := buildSkillExposureMessages(registry, []string{"run_shell_command", "run_shell_command"}, nil, skillExposureOptions{})
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

	_, err := buildSkillExposureMessages(registry, []string{"missing-skill"}, nil, skillExposureOptions{})

	require.Error(t, err)
	require.Contains(t, err.Error(), "missing-skill")
}

func TestBuildSkillExposureMessages_EmptyInputIsNoop(t *testing.T) {
	messages, err := buildSkillExposureMessages(nil, nil, nil, skillExposureOptions{})

	require.NoError(t, err)
	require.Empty(t, messages)
}

// Agent Skills 占位符：文档模式正文注入时按 skill_args 单趟替换
// $ARGUMENTS 与命名参数，并把 SKILL_DIR 解析到技能自身目录。
func TestBuildSkillExposureMessages_SubstitutesSkillArguments(t *testing.T) {
	registry := skill.NewRegistry(nil)
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "SKILL.md")
	require.NoError(t, os.WriteFile(skillPath, []byte(
		"---\nname: demo\ndescription: demo skill\n---\nEdit $ARGUMENTS in $path under ${SKILL_DIR}\n"), 0o644))

	item, err := skill.NewLoader(nil).LoadFileFull(skillPath)
	require.NoError(t, err)
	require.NotNil(t, item.Codex)
	item.Codex.Arguments = []skill.CodexSkillArgument{{Name: "path", Default: "README.md"}}
	require.NoError(t, registry.Register(item))

	messages, err := buildSkillExposureMessages(registry, []string{"demo"},
		&agentconfig.SkillsRuntimeConfig{DocumentMode: "auto"},
		skillExposureOptions{
			Args:                []string{"src/app.go"},
			ProjectDir:          "/ws",
			SessionID:           "sess-1",
			SubstitutionEnabled: true,
		})
	require.NoError(t, err)
	require.NotEmpty(t, messages)
	// 第一条是 catalog（cfg 非空时注入），正文注入是最后一条。
	body := messages[len(messages)-1]
	require.Contains(t, body.Content, "Skill instructions (document mode: demo)")
	require.Contains(t, body.Content, "Edit src/app.go in src/app.go under "+dir)
}
