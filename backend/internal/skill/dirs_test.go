package skill

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureSkillDirs_CreatesMissingDirs(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "nested", "skills")
	ensured, err := EnsureSkillDirs([]string{missingDir})
	require.NoError(t, err)
	require.Len(t, ensured, 1)

	info, err := os.Stat(missingDir)
	require.NoError(t, err)
	require.True(t, info.IsDir())
	assert.Equal(t, missingDir, ensured[0])
}

func TestEnsureSkillDirs_AbsolutizesRelativeDir(t *testing.T) {
	relativeDir := filepath.Join(".agents", "skills")
	_, preExistsErr := os.Stat(relativeDir)
	t.Cleanup(func() {
		if os.IsNotExist(preExistsErr) {
			_ = os.RemoveAll(filepath.Join(".agents"))
		}
	})

	ensured, err := EnsureSkillDirs([]string{"./.agents/skills"})
	require.NoError(t, err)
	require.Len(t, ensured, 1)

	abs, err := filepath.Abs(relativeDir)
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(ensured[0]))
	assert.Equal(t, filepath.Clean(abs), filepath.Clean(ensured[0]))

	info, err := os.Stat(abs)
	require.NoError(t, err)
	require.True(t, info.IsDir())
}

func TestEnsureSkillDirs_EmptyAndDedup(t *testing.T) {
	ensured, err := EnsureSkillDirs(nil)
	require.NoError(t, err)
	assert.Nil(t, ensured)

	dir := filepath.Join(t.TempDir(), "skills")
	ensured, err = EnsureSkillDirs([]string{dir, dir, " "})
	require.NoError(t, err)
	require.Len(t, ensured, 1)
	assert.Equal(t, dir, ensured[0])
}