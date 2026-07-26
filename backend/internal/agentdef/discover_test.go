package agentdef

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoverPriorityLaterOverridesEarlier(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()

	userDir := filepath.Join(home, ".aicli", "agents")
	require.NoError(t, os.MkdirAll(userDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(userDir, "explore.md"), []byte(`---
name: explore
description: user explore
permissionMode: default
---
User body
`), 0o644))

	projectDir := filepath.Join(project, ".agents", "agents")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "explore.md"), []byte(`---
name: explore
description: project explore
permissionMode: plan
---
Project body
`), 0o644))

	catalog, err := Discover(DiscoverOptions{
		ProjectRoot: project,
		UserHome:    home,
	})
	require.NoError(t, err)

	def, ok := catalog.Get("explore")
	require.True(t, ok)
	assert.Equal(t, "project explore", def.Description)
	assert.Equal(t, SourceProject, def.Source)
	assert.Equal(t, "Project body", def.Body)

	// builtin general still present
	_, ok = catalog.Get("general")
	assert.True(t, ok)
}

func TestDiscoverProfileAgentAdapter(t *testing.T) {
	profileRoot := t.TempDir()
	agentDir := filepath.Join(profileRoot, "agents", "coder")
	require.NoError(t, os.MkdirAll(filepath.Join(agentDir, "prompts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "agent.yaml"), []byte(`
name: coder
description: profile coder
model: gpt-test
permission_mode: accept_edits
tools:
  - view
  - write
completion_requirement: complete_task
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "prompts", "role.md"), []byte("You are the coder."), 0o644))

	catalog, err := Discover(DiscoverOptions{
		ProjectRoot: t.TempDir(),
		UserHome:    t.TempDir(),
		ProfileRoot: profileRoot,
	})
	require.NoError(t, err)
	def, ok := catalog.Get("coder")
	require.True(t, ok)
	assert.Equal(t, SourceProfile, def.Source)
	assert.Equal(t, "gpt-test", def.Model)
	assert.Equal(t, "accept_edits", def.PermissionMode)
	assert.Equal(t, CompletionCompleteTask, def.CompletionRequirement)
	assert.Equal(t, "You are the coder.", def.Body)
}

func TestResolveNotFound(t *testing.T) {
	_, err := Resolve("missing-xyz", DiscoverOptions{
		ProjectRoot:    t.TempDir(),
		UserHome:       t.TempDir(),
		IncludeBuiltin: boolPtr(false),
	})
	require.Error(t, err)
}

func boolPtr(v bool) *bool { return &v }
