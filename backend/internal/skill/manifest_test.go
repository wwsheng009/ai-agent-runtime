package skill

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManifestParser_ParseFile_LoadsPromptMarkdownAsSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "skill.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`name: prompt_skill
description: prompt companion test
triggers:
  - type: keyword
    values: ["prompt"]
    weight: 1
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("You are the prompt companion."), 0o644))

	parser := NewManifestParser()
	skill, err := parser.ParseFile(manifestPath)
	require.NoError(t, err)

	assert.Equal(t, "You are the prompt companion.", skill.SystemPrompt)
	assert.Equal(t, "", skill.UserPrompt)
}

func TestManifestParser_ParseFile_LoadsPromptMarkdownSections(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "skill.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`name: prompt_sections
description: prompt section test
triggers:
  - type: keyword
    values: ["prompt"]
    weight: 1
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "prompt.md"), []byte(`# System
You are a system prompt.

# User
Summarize the request.`), 0o644))

	parser := NewManifestParser()
	skill, err := parser.ParseFile(manifestPath)
	require.NoError(t, err)

	assert.Equal(t, "You are a system prompt.", skill.SystemPrompt)
	assert.Equal(t, "Summarize the request.", skill.UserPrompt)
}

func TestLoader_LoadFile_DefersCompanionPromptUntilResolved(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "skill.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`name: prompt_lazy
description: prompt lazy test
triggers:
  - type: keyword
    values: ["prompt"]
    weight: 1
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "prompt.md"), []byte(`# System
You are lazy loaded.

# User
Use the lazy prompt.`), 0o644))

	loader := NewLoader(nil)
	skill, err := loader.LoadFile(manifestPath)
	require.NoError(t, err)
	require.NotNil(t, skill)
	require.NotNil(t, skill.Source)

	assert.Equal(t, "", skill.SystemPrompt)
	assert.Equal(t, "", skill.UserPrompt)
	assert.Equal(t, filepath.Join(dir, "prompt.md"), skill.Source.PromptPath)

	systemPrompt, userPrompt, err := resolveSkillPrompts(skill)
	require.NoError(t, err)
	assert.Equal(t, "You are lazy loaded.", systemPrompt)
	assert.Equal(t, "Use the lazy prompt.", userPrompt)
}

func TestLoader_LoadFileFull_EagerlyLoadsCompanionPrompt(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "skill.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`name: prompt_full
description: prompt eager test
triggers:
  - type: keyword
    values: ["prompt"]
    weight: 1
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("You are eagerly loaded."), 0o644))

	loader := NewLoader(nil)
	skill, err := loader.LoadFileFull(manifestPath)
	require.NoError(t, err)
	require.NotNil(t, skill)

	assert.Equal(t, "You are eagerly loaded.", skill.SystemPrompt)
	assert.Equal(t, "", skill.UserPrompt)
	require.NotNil(t, skill.Source)
	assert.Equal(t, filepath.Join(dir, "prompt.md"), skill.Source.PromptPath)
}

func TestManifestParser_ParseSummaryFile_DiscoversPromptWithoutHydration(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "skill.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`name: prompt_summary
description: prompt summary test
triggers:
  - type: keyword
    values: ["prompt"]
    weight: 1
workflow:
  steps:
    - id: step_1
      name: first
      tool: echo_tool
      args:
        mode: "summary"
      dependsOn: ["step_0"]
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("You are summary only."), 0o644))

	parser := NewManifestParser()
	summary, err := parser.ParseSummaryFile(manifestPath)
	require.NoError(t, err)
	require.NotNil(t, summary)
	require.NotNil(t, summary.Source)

	assert.Equal(t, "prompt_summary", summary.Name)
	assert.Equal(t, 1, summary.WorkflowStepCount)
	assert.Equal(t, filepath.Join(dir, "prompt.md"), summary.Source.PromptPath)
	require.Len(t, summary.WorkflowSteps, 1)
	assert.Equal(t, "summary", summary.WorkflowSteps[0].Args["mode"])
	assert.Equal(t, []string{"step_0"}, summary.WorkflowSteps[0].DependsOn)

	stub := summary.ToSkillStub()
	require.NotNil(t, stub)
	assert.Equal(t, "", stub.SystemPrompt)
	assert.Equal(t, "", stub.UserPrompt)
	require.NotNil(t, stub.Source)
	assert.Equal(t, filepath.Join(dir, "prompt.md"), stub.Source.PromptPath)
}

func TestManifestParser_ParseBytes_RejectsInvalidPatternTrigger(t *testing.T) {
	parser := NewManifestParser()
	_, err := parser.ParseBytes([]byte(`name: invalid_pattern
description: invalid regex
triggers:
  - type: pattern
    values: ["["]
    weight: 1
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid pattern")
}

func TestManifestParser_SaveFile_WritesCompanionPromptMarkdown(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "skill.yaml")

	parser := NewManifestParser()
	skill := &Skill{
		Name:         "persist_prompt",
		Description:  "persist prompt test",
		SystemPrompt: "You are a system prompt.",
		UserPrompt:   "Summarize the request.",
		Triggers: []Trigger{{
			Type:   "keyword",
			Values: []string{"persist"},
			Weight: 1,
		}},
	}

	require.NoError(t, parser.SaveFile(skill, manifestPath))

	manifestBytes, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	assert.NotContains(t, string(manifestBytes), "systemPrompt")
	assert.NotContains(t, string(manifestBytes), "userPrompt")

	promptBytes, err := os.ReadFile(filepath.Join(dir, "prompt.md"))
	require.NoError(t, err)
	assert.Contains(t, string(promptBytes), "# System")
	assert.Contains(t, string(promptBytes), "You are a system prompt.")
	assert.Contains(t, string(promptBytes), "# User")
	assert.Contains(t, string(promptBytes), "Summarize the request.")

	reloaded, err := parser.ParseFile(manifestPath)
	require.NoError(t, err)
	assert.Equal(t, "You are a system prompt.", reloaded.SystemPrompt)
	assert.Equal(t, "Summarize the request.", reloaded.UserPrompt)
	require.NotNil(t, reloaded.Source)
	assert.Equal(t, filepath.Join(dir, "prompt.md"), reloaded.Source.PromptPath)
}

func TestManifestParser_SaveFile_RemovesCompanionPromptWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "skill.yaml")

	parser := NewManifestParser()
	skill := &Skill{
		Name:         "persist_prompt",
		Description:  "persist prompt test",
		SystemPrompt: "You are a system prompt.",
		Triggers: []Trigger{{
			Type:   "keyword",
			Values: []string{"persist"},
			Weight: 1,
		}},
	}
	require.NoError(t, parser.SaveFile(skill, manifestPath))

	skill.SystemPrompt = ""
	skill.UserPrompt = ""
	require.NoError(t, parser.SaveFile(skill, manifestPath))

	_, err := os.Stat(filepath.Join(dir, "prompt.md"))
	assert.True(t, os.IsNotExist(err))
}
