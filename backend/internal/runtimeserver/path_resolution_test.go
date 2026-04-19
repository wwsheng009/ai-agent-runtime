package runtimeserver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveUpwardPath_FindsParentMatch(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "docs", "skill_runtime", "skills")
	require.NoError(t, os.MkdirAll(target, 0o755))

	workdir := filepath.Join(root, "backend")
	require.NoError(t, os.MkdirAll(workdir, 0o755))

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workdir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(originalWD))
	})

	resolved := ResolveUpwardPath("./docs/skill_runtime/skills")
	require.Equal(t, target, resolved)
}

func TestResolveUpwardPath_PreservesExistingRelativePath(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "backend")
	target := filepath.Join(workdir, "docs", "skill_runtime", "skills")
	require.NoError(t, os.MkdirAll(target, 0o755))

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workdir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(originalWD))
	})

	resolved := ResolveUpwardPath("./docs/skill_runtime/skills")
	require.Equal(t, filepath.Clean("./docs/skill_runtime/skills"), resolved)
}
