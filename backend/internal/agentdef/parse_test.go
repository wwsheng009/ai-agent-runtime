package agentdef

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMarkdownFrontmatter(t *testing.T) {
	raw := []byte(`---
name: explore
description: Read-only explorer
tools:
  - view
  - grep
permissionMode: plan
promptMode: extend
completionRequirement: none
sandbox: read-only
---
You explore code carefully.
`)
	def, err := Parse(raw, "explore.md")
	require.NoError(t, err)
	assert.Equal(t, "explore", def.Name)
	assert.Equal(t, "Read-only explorer", def.Description)
	assert.Equal(t, []string{"view", "grep"}, def.Tools)
	assert.Equal(t, "plan", def.PermissionMode)
	assert.Equal(t, PromptModeExtend, def.PromptMode)
	assert.Equal(t, CompletionNone, def.CompletionRequirement)
	assert.Equal(t, "read-only", def.Sandbox)
	assert.Equal(t, "You explore code carefully.", def.Body)
}

func TestParsePureYAML(t *testing.T) {
	raw := []byte(`
name: worker
tools: [shell, view]
disallowedTools: [write]
completionRequirement: complete_task
permissionMode: default
`)
	def, err := Parse(raw, "worker.yaml")
	require.NoError(t, err)
	assert.Equal(t, "worker", def.Name)
	assert.Equal(t, CompletionCompleteTask, def.CompletionRequirement)
	assert.Equal(t, []string{"shell", "view"}, def.Tools)
	assert.Equal(t, []string{"write"}, def.DisallowedTools)
	assert.Empty(t, def.Body)
}

func TestParseInvalidPermissionMode(t *testing.T) {
	raw := []byte(`---
name: bad
permissionMode: freeforall
---
body
`)
	_, err := Parse(raw, "bad.md")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permissionMode")
}

func TestParseFileDerivesNameFromFilename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "my-agent.md")
	require.NoError(t, os.WriteFile(path, []byte(`---
description: named by file
---
Hello
`), 0o644))
	def, err := ParseFile(path)
	require.NoError(t, err)
	assert.Equal(t, "my-agent", def.Name)
	assert.Equal(t, "Hello", def.Body)
}

func TestParseMissingFrontmatter(t *testing.T) {
	_, err := Parse([]byte("no frontmatter here"), "x.md")
	require.Error(t, err)
}
