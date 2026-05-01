package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveUpwardPathDetailInWorkdir_LimitsCandidatesToWorktree(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "backend")
	require.NoError(t, os.MkdirAll(filepath.Join(workdir, "frontend", "src", "pages", "settings"), 0o755))

	candidate := filepath.Join(workdir, "frontend", "src", "pages", "settings", "runtime.yaml")
	require.NoError(t, os.WriteFile(candidate, []byte("ok"), 0o644))

	detail := ResolveUpwardPathDetailInWorkdir("frontend/src/pages/setting/runtime.yaml", workdir)
	require.Empty(t, detail.Resolved)
	require.Contains(t, detail.Candidates, candidate)

	for _, got := range detail.Candidates {
		rel, err := filepath.Rel(workdir, got)
		require.NoError(t, err)
		require.False(t, strings.HasPrefix(rel, ".."), "candidate should stay inside workdir: %s", got)
	}
}
