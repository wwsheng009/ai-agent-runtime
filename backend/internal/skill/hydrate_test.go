package skill

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHydrateSkill_CachesAndInvalidatesOnPromptChange(t *testing.T) {
	InvalidateAllHydratedSkills()
	defer InvalidateAllHydratedSkills()

	skillDir := t.TempDir()
	manifestPath := filepath.Join(skillDir, "skill.yaml")
	promptPath := filepath.Join(skillDir, "prompt.md")

	require.NoError(t, os.WriteFile(manifestPath, []byte(`name: cached-skill
description: cached hydration skill
triggers:
  - type: keyword
    values: ["cache"]
    weight: 1
`), 0o644))
	require.NoError(t, os.WriteFile(promptPath, []byte("You are cached."), 0o644))

	loader := NewLoader(nil)
	summary, err := loader.DiscoverFile(manifestPath)
	require.NoError(t, err)
	stub := summary.ToSkillStub()
	require.NotNil(t, stub)
	require.NotNil(t, stub.Source)
	require.True(t, stub.Source.DiscoveryOnly)

	hydrated1, err := HydrateSkill(stub)
	require.NoError(t, err)
	require.NotNil(t, hydrated1)
	assert.Equal(t, "You are cached.", hydrated1.SystemPrompt)

	hydratedSkillCache.mu.RLock()
	entry := hydratedSkillCache.items[filepath.Clean(manifestPath)]
	cacheLen := len(hydratedSkillCache.items)
	hydratedSkillCache.mu.RUnlock()
	require.Equal(t, 1, cacheLen)
	require.NotNil(t, entry)
	assert.Equal(t, "You are cached.", entry.skill.SystemPrompt)

	hydrated1.SystemPrompt = "mutated by caller"
	hydratedSkillCache.mu.RLock()
	assert.Equal(t, "You are cached.", hydratedSkillCache.items[filepath.Clean(manifestPath)].skill.SystemPrompt)
	hydratedSkillCache.mu.RUnlock()

	require.NoError(t, os.WriteFile(promptPath, []byte("You are updated."), 0o644))
	future := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(promptPath, future, future))

	hydrated2, err := HydrateSkill(stub)
	require.NoError(t, err)
	require.NotNil(t, hydrated2)
	assert.Equal(t, "You are updated.", hydrated2.SystemPrompt)

	hydratedSkillCache.mu.RLock()
	entry = hydratedSkillCache.items[filepath.Clean(manifestPath)]
	cacheLen = len(hydratedSkillCache.items)
	hydratedSkillCache.mu.RUnlock()
	require.Equal(t, 1, cacheLen)
	require.NotNil(t, entry)
	assert.Equal(t, "You are updated.", entry.skill.SystemPrompt)
}
