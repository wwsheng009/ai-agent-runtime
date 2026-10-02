package skill

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// codex 对齐：每根 20000 个条目的预算（MAX_SKILLS_ENTRIES_PER_ROOT）被触碰时
// 必须通过 warnings 上报，而不是打印 stdout 或静默截断。
func TestWalkSkillTreeWithLimits_EntryLimitProducesWarning(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(name), 0o644))
	}

	warnings, err := walkSkillTreeWithLimits(root, true, skillWalkLimits{
		MaxDepth:   maxSkillScanDepth,
		MaxDirs:    maxSkillDirsPerRoot,
		MaxEntries: 2,
	}, nil)

	require.NoError(t, err)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "traversal limit")
	require.Contains(t, warnings[0], canonicalizeSkillTreePath(root, true))
}

// 目录预算（MAX_SKILLS_DIRS_PER_ROOT=2000）同样走 warnings 通道。
func TestWalkSkillTreeWithLimits_DirectoryLimitProducesWarning(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"d1", "d2", "d3"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, name), 0o755))
	}

	warnings, err := walkSkillTreeWithLimits(root, true, skillWalkLimits{
		MaxDepth:   maxSkillScanDepth,
		MaxDirs:    2,
		MaxEntries: maxSkillEntriesPerRoot,
	}, nil)

	require.NoError(t, err)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "traversal limit")
}

// 默认预算必须与 codex 常量一致；正常规模的树不产生 warning。
func TestWalkSkillTreeWithLimits_DefaultBudgetsMatchCodex(t *testing.T) {
	require.Equal(t, 6, defaultSkillWalkLimits.MaxDepth)
	require.Equal(t, 2000, defaultSkillWalkLimits.MaxDirs)
	require.Equal(t, 20000, defaultSkillWalkLimits.MaxEntries)

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "demo"), 0o755))

	visited := 0
	warnings, err := walkSkillTree(root, true, func(entry skillTreeEntry) error {
		visited++
		return nil
	})
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Equal(t, 1, visited)
}
