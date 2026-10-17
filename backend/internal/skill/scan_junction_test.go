package skill

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// Windows reparse point（NTFS junction）回归：部分 Go 版本对 junction 的
// Lstat 既不带 ModeDir 也不带 ModeSymlink（实测标记 ModeIrregular，
// mode=?rw-rw-rw-）。walkSkillTree 若按普通文件跳过，junction 形式的
// skill 目录会被整棵静默丢弃——第三方工具（如 Tabbit）正是以 junction
// 把 skill 安装进 %USERPROFILE%\.agents\skills，导致用户级标准安装位
// 里的技能全部失联（2026-10-17 实例）。
//
// junction 的创建走 cmd mklink /J（不需要管理员特权）；失败则跳过，
// 非 Windows 平台直接跳过。
func TestWalkSkillTree_FollowsWindowsJunctionSkillDir(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only: NTFS junction")
	}

	// 对齐真实布局：<root>/.agents/skills/<name>（root 视作工作区/用户根）。
	root := t.TempDir()
	skillsRoot := filepath.Join(root, ".agents", "skills")
	require.NoError(t, os.MkdirAll(skillsRoot, 0o755))

	target := filepath.Join(t.TempDir(), "real-skill")
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(target, "SKILL.md"),
		[]byte("---\nname: junction-skill\ndescription: junction\n---\n\nbody\n"),
		0o644))

	junction := filepath.Join(skillsRoot, "junction-skill")
	out, err := exec.Command("cmd", "/C", "mklink", "/J", junction, target).CombinedOutput()
	if err != nil {
		t.Skipf("cannot create junction (privileges?): %v: %s", err, string(out))
	}
	t.Cleanup(func() { _ = os.Remove(junction) })

	// Lstat 前置断言：junction 必须呈现「非目录」的 Lstat 形态，否则本机
	// Go 版本已经原生把 junction 视为目录，该回归场景不再成立。
	lstatInfo, err := os.Lstat(junction)
	require.NoError(t, err)
	if lstatInfo.IsDir() {
		t.Skip("this Go build already reports junctions as directories; nothing to regress")
	}

	// 场景 1：junction 作为 skills root 下的子目录。
	// 注意：junction 条目本身的 Lstat Info 不是目录形态，但必须被入队遍历，
	// 其内部的 SKILL.md 才能被发现。
	var dirs []string
	_, err = walkSkillTree(skillsRoot, true, func(entry skillTreeEntry) error {
		if entry.Info != nil && (entry.Info.IsDir() || isWindowsReparseDir(entry.Path, entry.Info)) {
			dirs = append(dirs, entry.Path)
		}
		return nil
	})
	require.NoError(t, err)
	require.Contains(t, normalizeTestPaths(dirs), normalizeTestPath(canonicalizeSkillTreePath(junction, true)))

	// 场景 2：junction 自身作为扫描 root（整个 skills 根被挂成 junction 的形态）。
	visited := 0
	_, err = walkSkillTree(junction, true, func(entry skillTreeEntry) error {
		visited++
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, visited, "SKILL.md inside junction root must be visited")

	// 场景 3：整条发现链路（walkSkillTree + ParseSummaryFile）能看到 junction 技能。
	outcome := DiscoverCodexSkillLoadOutcome(root, "", nil)
	var names []string
	for _, meta := range outcome.Skills {
		if meta != nil {
			names = append(names, meta.Name)
		}
	}
	require.Contains(t, names, "junction-skill",
		"junction skill must participate in discovery: %v (warnings=%v)", names, outcome.Warnings)
}

// nofollow 语义不受影响：followSymlinks=false 时不进入 junction 子树。
func TestWalkSkillTree_NonFollowModeSkipsWindowsJunction(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only: NTFS junction")
	}

	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "real-skill")
	require.NoError(t, os.MkdirAll(target, 0o755))
	junction := filepath.Join(root, "junction-skill")
	out, err := exec.Command("cmd", "/C", "mklink", "/J", junction, target).CombinedOutput()
	if err != nil {
		t.Skipf("cannot create junction (privileges?): %v: %s", err, string(out))
	}
	t.Cleanup(func() { _ = os.Remove(junction) })

	lstatInfo, err := os.Lstat(junction)
	require.NoError(t, err)
	if lstatInfo.IsDir() {
		t.Skip("this Go build already reports junctions as directories; nothing to regress")
	}

	visited := 0
	_, err = walkSkillTree(junction, false, func(entry skillTreeEntry) error {
		visited++
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 0, visited, "nofollow mode must not enter junction subtree")
}

func normalizeTestPaths(paths []string) []string {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		result = append(result, normalizeTestPath(path))
	}
	return result
}

func normalizeTestPath(path string) string {
	return filepath.Clean(path)
}
