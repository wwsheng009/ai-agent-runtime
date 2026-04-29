package skill

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHotReload_ReloadAllSkills_RegistersDiscoveryStubs(t *testing.T) {
	loader := NewLoader(nil)
	registry := NewRegistry(nil)
	hotReload, err := NewHotReload(loader, registry)
	require.NoError(t, err)
	t.Cleanup(func() { _ = hotReload.Stop() })

	skillDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "skill.yaml"), []byte(`name: hot-lazy
description: hot lazy reload
triggers:
  - type: keyword
    values: ["hot"]
    weight: 1
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "prompt.md"), []byte("You are hot lazy."), 0o644))

	hotReload.skillDirs = []string{skillDir}
	require.NoError(t, hotReload.reloadAllSkills([]string{skillDir}))

	item, ok := registry.Get("hot-lazy")
	require.True(t, ok)
	require.NotNil(t, item)
	require.NotNil(t, item.Source)
	assert.True(t, item.Source.DiscoveryOnly)
	assert.Equal(t, filepath.Join(skillDir, "prompt.md"), item.Source.PromptPath)
	assert.Equal(t, "", item.SystemPrompt)
	assert.Equal(t, "", item.UserPrompt)
}

func TestHotReload_ReloadSkill_RegistersDiscoveryStub(t *testing.T) {
	loader := NewLoader(nil)
	registry := NewRegistry(nil)
	hotReload, err := NewHotReload(loader, registry)
	require.NoError(t, err)
	t.Cleanup(func() { _ = hotReload.Stop() })

	skillDir := t.TempDir()
	manifestPath := filepath.Join(skillDir, "skill.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`name: hot-single
description: hot single reload
triggers:
  - type: keyword
    values: ["hot"]
    weight: 1
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "prompt.md"), []byte("You are hot single."), 0o644))

	hotReload.skillDirs = []string{skillDir}
	hotReload.reloadSkill(manifestPath)

	item, ok := registry.Get("hot-single")
	require.True(t, ok)
	require.NotNil(t, item)
	require.NotNil(t, item.Source)
	assert.True(t, item.Source.DiscoveryOnly)
	assert.Equal(t, filepath.Join(skillDir, "prompt.md"), item.Source.PromptPath)
	assert.Equal(t, "", item.SystemPrompt)
}

func TestShouldParseSkillManifest_IgnoresCodexResourceDirectories(t *testing.T) {
	workspace := t.TempDir()
	assert.False(t, shouldParseSkillManifest(filepath.Join(workspace, "scripts", "helper.yaml")))
	assert.False(t, shouldParseSkillManifest(filepath.Join(workspace, "references", "doc.yml")))
	assert.False(t, shouldParseSkillManifest(filepath.Join(workspace, "assets", "data.yaml")))
	assert.False(t, shouldParseSkillManifest(filepath.Join(workspace, "agents", "openai.yaml")))
	assert.True(t, shouldParseSkillManifest(filepath.Join(workspace, "skill.yaml")))
	assert.True(t, shouldParseSkillManifest(filepath.Join(workspace, "system.yaml")))
	assert.True(t, shouldParseSkillManifest(filepath.Join(workspace, "extra.yml")))
}

func TestSkillManifestPathForWatchedFile_RecognizesLegacyCustomManifests(t *testing.T) {
	dir := t.TempDir()
	systemManifest := filepath.Join(dir, "system.yaml")
	extraManifest := filepath.Join(dir, "extra.yml")
	require.NoError(t, os.WriteFile(systemManifest, []byte(`name: watched-system
description: watched system skill
triggers:
  - type: keyword
    values: ["watch"]
    weight: 1
`), 0o644))
	require.NoError(t, os.WriteFile(extraManifest, []byte(`name: watched-extra
description: watched extra skill
triggers:
  - type: keyword
    values: ["watch"]
    weight: 1
`), 0o644))

	manifestPath, kind := skillManifestPathForWatchedFile(filepath.Join(dir, "prompt.md"))
	require.Equal(t, skillManifestKindCompanion, kind)
	assert.Equal(t, systemManifest, manifestPath)

	manifestPath, kind = skillManifestPathForWatchedFile(systemManifest)
	require.Equal(t, skillManifestKindManifest, kind)
	assert.Equal(t, systemManifest, manifestPath)

	manifestPath, kind = skillManifestPathForWatchedFile(extraManifest)
	require.Equal(t, skillManifestKindManifest, kind)
	assert.Equal(t, extraManifest, manifestPath)

	manifestPath, kind = skillManifestPathForWatchedFile(filepath.Join(dir, "scripts", "helper.yaml"))
	assert.Equal(t, "", manifestPath)
	assert.Equal(t, skillManifestKindUnknown, kind)
}
