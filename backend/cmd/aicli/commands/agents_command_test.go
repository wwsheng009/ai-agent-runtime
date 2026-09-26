package commands

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentdef"
)

func TestAgentsTemplateContentIsLintClean(t *testing.T) {
	for _, template := range []string{"read-only", "writer", "blank"} {
		content, err := agentsTemplateContent(template, "demo-role", "Demo role for tests")
		require.NoError(t, err, template)
		def, err := agentdef.Parse([]byte(content), "demo-role.md")
		require.NoError(t, err, template)
		for _, issue := range agentdef.LintDefinition(def) {
			require.NotEqual(t, "warning", issue.Severity,
				"template %s must not emit warnings: %+v", template, issue)
		}
	}
}

func TestAgentsNewWritesDiscoverableDefinition(t *testing.T) {
	dir := t.TempDir()
	cmd := NewAgentsCommand(nil)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"new", "demo-role", "--dir", dir, "--description", "demo"})
	require.NoError(t, cmd.Execute())

	path := filepath.Join(dir, "demo-role.md")
	require.FileExists(t, path)
	def, err := agentdef.ParseFile(path)
	require.NoError(t, err)
	require.Equal(t, "demo-role", def.Name)
	require.Equal(t, "demo", def.Description)

	catalog, err := agentdef.Discover(agentdef.DiscoverOptions{ExtraDirs: []string{dir}})
	require.NoError(t, err)
	_, ok := catalog.Get("demo-role")
	require.True(t, ok, "created definition must be discoverable")
}

func TestAgentsNewRefusesOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	first := NewAgentsCommand(nil)
	first.SetOut(&bytes.Buffer{})
	first.SetArgs([]string{"new", "demo-role", "--dir", dir})
	require.NoError(t, first.Execute())

	second := NewAgentsCommand(nil)
	second.SetOut(&bytes.Buffer{})
	second.SilenceErrors = true
	second.SilenceUsage = true
	second.SetArgs([]string{"new", "demo-role", "--dir", dir})
	require.Error(t, second.Execute())

	third := NewAgentsCommand(nil)
	third.SetOut(&bytes.Buffer{})
	third.SetArgs([]string{"new", "demo-role", "--dir", dir, "--force"})
	require.NoError(t, third.Execute())
}

func TestAgentsNewRejectsUnknownTemplate(t *testing.T) {
	_, err := agentsTemplateContent("nope", "demo", "desc")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown template")
}

func TestAgentsListShowsCreatedDefinition(t *testing.T) {
	dir := t.TempDir()
	create := NewAgentsCommand(nil)
	create.SetOut(&bytes.Buffer{})
	create.SetArgs([]string{"new", "demo-role", "--dir", dir})
	require.NoError(t, create.Execute())

	list := NewAgentsCommand(nil)
	var out bytes.Buffer
	list.SetOut(&out)
	list.SetArgs([]string{"list", "--dir", dir, "--project-root", t.TempDir()})
	require.NoError(t, list.Execute())
	rendered := out.String()
	require.Contains(t, rendered, "demo-role")
	require.Contains(t, rendered, "PROJECT")
}

func TestNormalizeAgentsCreateName(t *testing.T) {
	require.Equal(t, "code-reviewer", normalizeAgentsCreateName(" Code Reviewer "))
	require.Equal(t, "a-b", normalizeAgentsCreateName("a/b"))
	require.Equal(t, "", normalizeAgentsCreateName("   "))
}
