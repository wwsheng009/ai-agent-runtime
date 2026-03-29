package skill

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistry_RegisterStoresSummaryCatalog(t *testing.T) {
	registry := NewRegistry(nil)
	item := &Skill{
		Name:         "summary-skill",
		Description:  "summary test",
		Category:     "ops",
		Capabilities: []string{"search"},
		Tags:         []string{"tag-a"},
		Triggers:     []Trigger{{Type: "keyword", Values: []string{"sum"}, Weight: 1}},
		Tools:        []string{"echo_tool"},
		Workflow: &Workflow{Steps: []WorkflowStep{{
			ID:   "step_1",
			Name: "first",
			Tool: "echo_tool",
			Args: map[string]interface{}{"ignored": true},
		}}},
	}
	item.SetSource("skill.yaml", "C:/skills", SkillSourceLayerSystem)
	item.Source.DiscoveryOnly = true

	require.NoError(t, registry.Register(item))

	summary, ok := registry.GetSummary("summary-skill")
	require.True(t, ok)
	require.NotNil(t, summary)
	assert.Equal(t, "summary test", summary.Description)
	assert.Equal(t, 1, summary.WorkflowStepCount)
	require.Len(t, summary.WorkflowSteps, 1)
	assert.Equal(t, "step_1", summary.WorkflowSteps[0].ID)
	assert.Equal(t, "echo_tool", summary.WorkflowSteps[0].Tool)
	require.NotNil(t, summary.Source)
	assert.True(t, summary.Source.DiscoveryOnly)

	summaries := registry.ListSummaries()
	require.Len(t, summaries, 1)
	assert.Equal(t, "summary-skill", summaries[0].Name)
}

func TestRegistry_HydrateUsesRegistryLoadedCache(t *testing.T) {
	registry := NewRegistry(nil)
	loader := NewLoader(nil)

	skillDir := t.TempDir()
	manifestPath := filepath.Join(skillDir, "skill.yaml")
	promptPath := filepath.Join(skillDir, "prompt.md")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`name: registry-cached
description: registry cache test
triggers:
  - type: keyword
    values: ["cache"]
    weight: 1
`), 0o644))
	require.NoError(t, os.WriteFile(promptPath, []byte("You are registry cached."), 0o644))

	summary, err := loader.DiscoverFile(manifestPath)
	require.NoError(t, err)
	stub := summary.ToSkillStub()
	require.NotNil(t, stub)
	require.True(t, stub.Source.DiscoveryOnly)
	require.NoError(t, registry.Register(stub))

	hydrated1, err := registry.Hydrate(stub)
	require.NoError(t, err)
	require.NotNil(t, hydrated1)
	assert.Equal(t, "You are registry cached.", hydrated1.SystemPrompt)

	registry.mu.RLock()
	entry := registry.loadedCache["registry-cached"]
	registry.mu.RUnlock()
	require.NotNil(t, entry)
	assert.Equal(t, "You are registry cached.", entry.skill.SystemPrompt)

	require.NoError(t, os.WriteFile(promptPath, []byte("You are registry updated."), 0o644))
	future := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(promptPath, future, future))

	hydrated2, err := registry.Hydrate(stub)
	require.NoError(t, err)
	require.NotNil(t, hydrated2)
	assert.Equal(t, "You are registry updated.", hydrated2.SystemPrompt)

	registry.mu.RLock()
	entry = registry.loadedCache["registry-cached"]
	registry.mu.RUnlock()
	require.NotNil(t, entry)
	assert.Equal(t, "You are registry updated.", entry.skill.SystemPrompt)

	registry.InvalidateLoadedSkill("registry-cached")
	registry.mu.RLock()
	entry = registry.loadedCache["registry-cached"]
	registry.mu.RUnlock()
	assert.Nil(t, entry)
}
