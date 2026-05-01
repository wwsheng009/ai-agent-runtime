package runtimeserver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveUpwardPath_FindsParentMatch(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, ".agents", "skills")
	require.NoError(t, os.MkdirAll(target, 0o755))

	workdir := filepath.Join(root, "backend")
	require.NoError(t, os.MkdirAll(workdir, 0o755))

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workdir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(originalWD))
	})

	resolved := ResolveUpwardPath("./.agents/skills")
	require.Equal(t, target, resolved)
}

func TestResolveUpwardPath_PreservesExistingRelativePath(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "backend")
	target := filepath.Join(workdir, ".agents", "skills")
	require.NoError(t, os.MkdirAll(target, 0o755))

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workdir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(originalWD))
	})

	resolved := ResolveUpwardPath("./.agents/skills")
	require.Equal(t, filepath.Clean("./.agents/skills"), resolved)
}

func TestResolveUpwardPathDetailInWorkdir_SuggestsSimilarSibling(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "backend")
	require.NoError(t, os.MkdirAll(workdir, 0o755))

	suggested := filepath.Join(workdir, "frontend", "src", "pages", "settings", "runtime.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(suggested), 0o755))
	require.NoError(t, os.WriteFile(suggested, []byte("ok"), 0o644))

	detail := ResolveUpwardPathDetailInWorkdir("frontend/src/pages/setting/runtime.yaml", workdir)
	require.Empty(t, detail.Resolved)
	require.Contains(t, detail.Candidates, suggested)
}
