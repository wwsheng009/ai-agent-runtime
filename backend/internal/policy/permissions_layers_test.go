package policy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func isolatePermissionsHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".aicli"), 0o755))
	return home
}

func TestLoadLayeredPermissionsAccumulatesLayers(t *testing.T) {
	home := isolatePermissionsHome(t)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".aicli", "permissions.yaml"), []byte(`
version: 1
deny_tools: [shell]
disable_bypass: true
rules:
  - name: user-deny-network
    tools: [download]
    decision: deny
`), 0o644))

	project := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".aicli"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".aicli", "permissions.yaml"), []byte(`
version: 1
deny_tools: [aicli_exec]
allow_tools: [view]
rules:
  - name: project-allow-view
    tools: [view]
    decision: allow
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".aicli", "permissions.local.yaml"), []byte(`
version: 1
rules:
  - name: local-ask-writes
    tools: [write]
    decision: ask
`), 0o644))

	merged, layers, err := LoadLayeredPermissions(project)
	require.NoError(t, err)
	require.Len(t, layers, 3)
	assert.Equal(t, []string{PermissionsScopeUser, PermissionsScopeProject, PermissionsScopeLocal},
		[]string{layers[0].Scope, layers[1].Scope, layers[2].Scope})
	require.NotNil(t, merged)
	assert.ElementsMatch(t, []string{"shell", "aicli_exec"}, merged.DenyTools, "deny is a monotonic union")
	assert.Equal(t, []string{"view"}, merged.AllowTools)
	assert.True(t, merged.DisableBypass, "disable_bypass is OR-ed across layers")
	require.Len(t, merged.Rules, 3)
	assert.Equal(t, "user/user-deny-network", merged.Rules[0].Name)
	assert.Equal(t, "project/project-allow-view", merged.Rules[1].Name)
	assert.Equal(t, "local/local-ask-writes", merged.Rules[2].Name)
	assert.Len(t, merged.LayerPaths, 3)

	// Escalation order means the user deny rule precedes the project allow rule.
	rules := merged.ToRules("project")
	require.NotEmpty(t, rules)
	assert.Equal(t, DecisionDeny, rules[0].Decision)
}

func TestLoadLayeredPermissionsWithoutProjectStillLoadsUserLayer(t *testing.T) {
	home := isolatePermissionsHome(t)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".aicli", "permissions.yaml"), []byte(`
version: 1
rules:
  - name: user-ask-shell
    tools: [shell]
    decision: ask
`), 0o644))

	merged, layers, err := LoadLayeredPermissions("")
	require.NoError(t, err)
	require.Len(t, layers, 1)
	assert.Equal(t, PermissionsScopeUser, layers[0].Scope)
	require.NotNil(t, merged)
	require.Len(t, merged.Rules, 1)
	assert.Equal(t, "user/user-ask-shell", merged.Rules[0].Name)
}

func TestLoadLayeredPermissionsWithoutAnyLayer(t *testing.T) {
	isolatePermissionsHome(t)
	merged, layers, err := LoadLayeredPermissions(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, layers)
	assert.Nil(t, merged)
}

func TestBuildPermissionsOverlayPropagatesDisableBypass(t *testing.T) {
	overlay := BuildPermissionsOverlay(&PermissionsFile{
		DisableBypass: true,
		LayerPaths:    []string{"/home/u/.aicli/permissions.yaml", "/proj/.aicli/permissions.yaml"},
	}, nil, nil)
	assert.True(t, overlay.DisableBypass)
	assert.Contains(t, overlay.Sources, "permissions:/home/u/.aicli/permissions.yaml")
	assert.NotContains(t, overlay.Sources, "project", "merged layers replace the legacy single-source label")
}
