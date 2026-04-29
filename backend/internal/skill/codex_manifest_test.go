package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManifestParser_ParseFile_LoadsCodexSkillFrontmatterAndOpenAIMetadata(t *testing.T) {
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "SKILL.md")
	metadataPath := filepath.Join(dir, "agents", "openai.yaml")

	require.NoError(t, os.MkdirAll(filepath.Dir(metadataPath), 0o755))
	require.NoError(t, os.WriteFile(skillPath, []byte(`---
name: codex-docs
description: codex docs skill
metadata:
  short-description: concise summary
---
You are codex body.
`), 0o644))
	require.NoError(t, os.WriteFile(metadataPath, []byte(`interface:
  display_name: Codex Docs
  short_description: Docs short
  default_prompt: Use the default prompt.
dependencies:
  tools:
    - type: mcp
      value: search
      description: Search tool
      transport: stdio
      command: search-tool
      url: https://example.com/search
policy:
  allow_implicit_invocation: false
  products:
    - chat
    - docs
`), 0o644))

	parser := NewManifestParser()
	skill, err := parser.ParseFile(skillPath)
	require.NoError(t, err)
	require.NotNil(t, skill)
	require.NotNil(t, skill.Source)
	require.NotNil(t, skill.Codex)

	assert.Equal(t, "codex-docs", skill.Name)
	assert.Equal(t, "codex docs skill", skill.Description)
	assert.Equal(t, "concise summary", skill.ShortDescription)
	assert.Equal(t, "You are codex body.", strings.TrimSpace(skill.Body))
	assert.Equal(t, "You are codex body.", strings.TrimSpace(skill.SystemPrompt))
	assert.Equal(t, "", skill.UserPrompt)
	assert.Equal(t, filepath.Clean(skillPath), skill.Source.Path)
	assert.Equal(t, SkillSourceFormatCodex, skill.Source.Format)
	assert.Equal(t, metadataPath, skill.Source.MetadataPath)
	assert.Equal(t, "Codex Docs", skill.Codex.Interface.DisplayName)
	assert.Equal(t, "Docs short", skill.Codex.Interface.ShortDescription)
	assert.Equal(t, "Use the default prompt.", skill.Codex.Interface.DefaultPrompt)
	require.Len(t, skill.Codex.Dependencies.Tools, 1)
	assert.Equal(t, "search", skill.Codex.Dependencies.Tools[0].Value)
	require.NotNil(t, skill.Codex.Policy)
	assert.False(t, skill.Codex.Policy.AllowImplicitInvocation())
	assert.Equal(t, []string{"chat", "docs"}, skill.Codex.Policy.Products)
}

func TestManifestParser_ParseSummaryFile_IgnoresInvalidCodexOpenAIMetadata(t *testing.T) {
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "SKILL.md")
	metadataPath := filepath.Join(dir, "agents", "openai.yaml")

	require.NoError(t, os.MkdirAll(filepath.Dir(metadataPath), 0o755))
	require.NoError(t, os.WriteFile(skillPath, []byte(`---
name: codex-fail-open
description: codex fail-open skill
---
Discovery only body.
`), 0o644))
	require.NoError(t, os.WriteFile(metadataPath, []byte("interface: ["), 0o644))

	parser := NewManifestParser()
	summary, err := parser.ParseSummaryFile(skillPath)
	require.NoError(t, err)
	require.NotNil(t, summary)
	require.NotNil(t, summary.Source)

	assert.Equal(t, "codex-fail-open", summary.Name)
	assert.Equal(t, "codex fail-open skill", summary.Description)
	assert.Equal(t, SkillSourceFormatCodex, summary.Source.Format)
	assert.Equal(t, filepath.Clean(skillPath), summary.Source.Path)
	assert.Equal(t, metadataPath, summary.Source.MetadataPath)
	require.NotNil(t, summary.Codex)
	assert.Nil(t, summary.Codex.Interface)
	assert.Nil(t, summary.Codex.Dependencies)
	assert.Nil(t, summary.Codex.Policy)
}

func TestManifestParser_ParseSummaryDir_SkipsCodexCompanionMetadata(t *testing.T) {
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "SKILL.md")
	metadataPath := filepath.Join(dir, "agents", "openai.yaml")

	require.NoError(t, os.MkdirAll(filepath.Dir(metadataPath), 0o755))
	require.NoError(t, os.WriteFile(skillPath, []byte(`---
name: codex-dir
description: codex dir skill
---
Directory body.
`), 0o644))
	require.NoError(t, os.WriteFile(metadataPath, []byte(`name: should-not-load
description: should not be parsed as a skill
triggers:
  - type: keyword
    values: ["noop"]
    weight: 1
`), 0o644))

	parser := NewManifestParser()
	summaries, err := parser.ParseSummaryDir(dir)
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	require.NotNil(t, summaries[0].Source)

	assert.Equal(t, "codex-dir", summaries[0].Name)
	assert.Equal(t, SkillSourceFormatCodex, summaries[0].Source.Format)
	assert.Equal(t, filepath.Clean(skillPath), summaries[0].Source.Path)
	assert.Equal(t, metadataPath, summaries[0].Source.MetadataPath)
}
