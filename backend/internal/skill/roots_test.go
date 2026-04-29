package skill

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiscoverCodexCompatibleSkillDirs_IncludesWorkspaceUserAndConfigRoots(t *testing.T) {
	baseRoot := isolatedSkillTestRoot(t)
	homeRoot := filepath.Join(baseRoot, "home")
	workspaceRoot := filepath.Join(baseRoot, "workspace")
	repoRoot := filepath.Join(workspaceRoot, "repo")
	configRoot := filepath.Join(baseRoot, "config")

	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspaceRoot, ".agents", "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(homeRoot, ".aicli", "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(homeRoot, ".aicli", "agents", "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(configRoot, "skills"), 0o755))

	got := discoverCodexCompatibleSkillDirs(
		repoRoot,
		filepath.Join(configRoot, "runtime.yaml"),
		homeRoot,
	)

	require.Equal(t, []string{
		filepath.Join(repoRoot, "skills"),
		filepath.Join(workspaceRoot, ".agents", "skills"),
		filepath.Join(homeRoot, ".aicli", "skills"),
		filepath.Join(homeRoot, ".aicli", "agents", "skills"),
		filepath.Join(configRoot, "skills"),
	}, got)
}

func isolatedSkillTestRoot(t *testing.T) string {
	t.Helper()

	if runtime.GOOS != "windows" {
		return t.TempDir()
	}

	drive := filepath.VolumeName(os.TempDir())
	if drive == "" {
		drive = "C:"
	}

	root := filepath.Join(drive+string(filepath.Separator), "codex-skill-test", sanitizeTestComponent(t.Name()))
	require.NoError(t, os.RemoveAll(root))
	require.NoError(t, os.MkdirAll(root, 0o755))
	t.Cleanup(func() {
		_ = os.RemoveAll(root)
	})
	return root
}

func sanitizeTestComponent(name string) string {
	replacer := strings.NewReplacer("\\", "_", "/", "_", ":", "_", " ", "_")
	return replacer.Replace(name)
}
