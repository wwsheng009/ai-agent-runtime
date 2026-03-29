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
