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
	// workspaceRoot 作为项目根（codex 默认 marker .git）：repo 祖先扫描
	// 才能越过 repoRoot 看到 workspaceRoot/.agents/skills。
	require.NoError(t, os.MkdirAll(filepath.Join(workspaceRoot, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(homeRoot, ".aicli", "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(homeRoot, ".aicli", "agents", "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(homeRoot, ".aicli", ".agents", "skills"), 0o755))
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
		filepath.Join(homeRoot, ".aicli", ".agents", "skills"),
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

// 工作目录的 .agents/skills 必须优先于 home 之下的用户级 ~/.agents/skills：
// 工作目录位于 home 之下时，用户目录不得被认作 repo 根抢先发现。
func TestDiscoverCodexCompatibleSkillRootSpecs_WorkdirOutranksUserHome(t *testing.T) {
	baseRoot := isolatedSkillTestRoot(t)
	homeRoot := filepath.Join(baseRoot, "home")
	workRoot := filepath.Join(homeRoot, "workspace", "repo")

	workAgentsSkills := filepath.Join(workRoot, ".agents", "skills")
	userAgentsSkills := filepath.Join(homeRoot, ".agents", "skills")
	require.NoError(t, os.MkdirAll(workAgentsSkills, 0o755))
	require.NoError(t, os.MkdirAll(userAgentsSkills, 0o755))

	specs := discoverCodexCompatibleSkillRootSpecs(workRoot, "", homeRoot)

	workIndex, userIndex := -1, -1
	for i, spec := range specs {
		switch {
		case filepath.Clean(spec.Path) == filepath.Clean(workAgentsSkills):
			workIndex = i
			require.Equal(t, CodexSkillScopeRepo, spec.Scope)
		case filepath.Clean(spec.Path) == filepath.Clean(userAgentsSkills):
			userIndex = i
			require.Equal(t, CodexSkillScopeUser, spec.Scope)
		}
	}
	require.NotEqual(t, -1, workIndex, "workdir root not discovered: %#v", specs)
	require.NotEqual(t, -1, userIndex, "user home root not discovered: %#v", specs)
	require.Less(t, workIndex, userIndex, "workdir root must precede user home root: %#v", specs)
}

// 同名 skill 同时存在于工作目录与 ~/.agents/skills 时，工作目录定义优先，
// 用户目录定义必须被判为被遮蔽（诊断中 first defined 指向工作目录）。
func TestDiscoverCodexSkillLoadOutcome_WorkdirShadowsUserHome(t *testing.T) {
	baseRoot := isolatedSkillTestRoot(t)
	homeRoot := filepath.Join(baseRoot, "home")
	workRoot := filepath.Join(homeRoot, "workspace", "repo")

	workSkill := writeStandardSkill(t, filepath.Join(workRoot, ".agents", "skills"), "dup",
		"---\nname: dup\ndescription: workdir\n---\n\nbody\n")
	userSkill := writeStandardSkill(t, filepath.Join(homeRoot, ".agents", "skills"), "dup",
		"---\nname: dup\ndescription: user\n---\n\nbody\n")

	outcome := discoverCodexSkillLoadOutcome(workRoot, "", homeRoot, nil)
	if len(outcome.Skills) != 2 {
		t.Fatalf("skills = %d, want 2", len(outcome.Skills))
	}

	joined := ""
	for _, warning := range outcome.Warnings {
		joined += warning.Message + " | " + warning.Path + "\n"
	}
	if !strings.Contains(joined, workSkill) {
		t.Fatalf("shadow warning must point at the workdir definition as first: %s", joined)
	}
	if !strings.Contains(joined, userSkill) {
		t.Fatalf("shadow warning must mention the shadowed user definition: %s", joined)
	}
}

// codex 语义：repo 级 `.agents/skills` 的祖先扫描以项目根（默认 marker .git）
// 为界；项目根之外的父目录 `.agents/skills` 不参与当前项目的发现。
func TestDiscoverCodexCompatibleSkillRootSpecs_StopsAtProjectRoot(t *testing.T) {
	baseRoot := isolatedSkillTestRoot(t)
	projectRoot := filepath.Join(baseRoot, "project")
	repoRoot := filepath.Join(projectRoot, "sub", "repo")
	outsideAgentsSkills := filepath.Join(baseRoot, ".agents", "skills")
	insideAgentsSkills := filepath.Join(projectRoot, ".agents", "skills")

	require.NoError(t, os.MkdirAll(outsideAgentsSkills, 0o755))
	require.NoError(t, os.MkdirAll(insideAgentsSkills, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".git"), 0o755))

	specs := discoverCodexCompatibleSkillRootSpecsWithMarkers(
		repoRoot, "", filepath.Join(baseRoot, "home"), defaultProjectRootMarkers)

	paths := make([]string, 0, len(specs))
	for _, spec := range specs {
		paths = append(paths, spec.Path)
	}
	require.Contains(t, paths, insideAgentsSkills)
	require.NotContains(t, paths, outsideAgentsSkills)
}

// 没有 marker 时项目根=anchor：只发现 anchor 自身的 `.agents/skills`，
// 父目录的 `.agents/skills` 不再被当作 repo 根（对齐 codex host_roots.rs）。
func TestDiscoverCodexCompatibleSkillRootSpecs_NoMarkerKeepsAnchorOnly(t *testing.T) {
	baseRoot := isolatedSkillTestRoot(t)
	workspace := filepath.Join(baseRoot, "workspace")
	anchorSkills := filepath.Join(workspace, ".agents", "skills")
	parentSkills := filepath.Join(baseRoot, ".agents", "skills")

	require.NoError(t, os.MkdirAll(anchorSkills, 0o755))
	require.NoError(t, os.MkdirAll(parentSkills, 0o755))

	specs := discoverCodexCompatibleSkillRootSpecsWithMarkers(
		workspace, "", filepath.Join(baseRoot, "home"), defaultProjectRootMarkers)

	paths := make([]string, 0, len(specs))
	for _, spec := range specs {
		paths = append(paths, spec.Path)
	}
	require.NotEmpty(t, paths)
	require.Equal(t, anchorSkills, paths[0], "工作目录自身的 .agents/skills 必须最先发现")
	require.NotContains(t, paths, parentSkills)
}

// 空 markers 表示禁用项目根探测：与无 marker 一致，只含 anchor 自身。
func TestProjectAgentsSkillDirs_EmptyMarkersDisablesRootDetection(t *testing.T) {
	baseRoot := isolatedSkillTestRoot(t)
	workspace := filepath.Join(baseRoot, "workspace")
	anchorSkills := filepath.Join(workspace, ".agents", "skills")

	require.NoError(t, os.MkdirAll(anchorSkills, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(baseRoot, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(baseRoot, ".agents", "skills"), 0o755))

	require.Equal(t, []string{anchorSkills}, projectAgentsSkillDirs(workspace, nil))
}
