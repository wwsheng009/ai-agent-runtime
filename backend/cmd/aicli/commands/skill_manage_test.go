package commands

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeManageTestSkill(t *testing.T, dir string, name string, frontmatterName string) string {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	content := "---\nname: " + frontmatterName + "\ndescription: test skill\n---\n\nbody\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644))
	return skillDir
}

func isolateSkillDiscoveryHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	return home
}

func TestRunSkillListCommand_ReportsSkillsScopeAndDiagnostics(t *testing.T) {
	isolateSkillDiscoveryHome(t)
	workspace := t.TempDir()
	writeManageTestSkill(t, filepath.Join(workspace, ".agents", "skills"), "demo-skill", "demo-skill")
	brokenDir := filepath.Join(workspace, ".agents", "skills", "broken-skill")
	require.NoError(t, os.MkdirAll(brokenDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(brokenDir, "SKILL.md"), []byte("not a frontmatter document\n"), 0o644))

	result, _, err := runSkillListCommand(skillListOptions{Cwd: workspace, Debug: true})
	require.NoError(t, err)
	require.Equal(t, 1, result.Count)
	assert.Equal(t, "demo-skill", result.Skills[0].Name)
	assert.Equal(t, "repo", result.Skills[0].Scope)
	assert.Contains(t, result.Skills[0].Root, filepath.Join(".agents", "skills"))
	assert.Equal(t, filepath.Join(workspace, ".agents", "skills", "demo-skill", "SKILL.md"), result.Skills[0].Path)
	assert.NotEmpty(t, result.Errors, "broken skill must be reported as a load error")
	require.NotEmpty(t, result.Roots, "--debug must include discovery roots")
	assert.Contains(t, result.Roots[0].Path, filepath.Join(".agents", "skills"))
}

func TestRunSkillListCommand_ReportsStandardWarnings(t *testing.T) {
	isolateSkillDiscoveryHome(t)
	workspace := t.TempDir()
	// 目录名与 frontmatter name 不一致 → warning（可加载但有诊断）。
	writeManageTestSkill(t, filepath.Join(workspace, ".agents", "skills"), "dir-name", "other-name")

	result, _, err := runSkillListCommand(skillListOptions{Cwd: workspace})
	require.NoError(t, err)
	require.Equal(t, 1, result.Count)
	require.NotEmpty(t, result.Warnings)
	assert.Contains(t, result.Warnings[0].Message, "does not match frontmatter name")
}

func TestRunSkillRemoveCommand_RemovesSkillAndGuardsNameMismatch(t *testing.T) {
	isolateSkillDiscoveryHome(t)
	workspace := t.TempDir()
	root := filepath.Join(workspace, ".agents", "skills")
	target := writeManageTestSkill(t, root, "demo-skill", "demo-skill")

	result, _, err := runSkillRemoveCommand(skillRemoveOptions{Name: "demo-skill", Cwd: workspace})
	require.NoError(t, err)
	assert.True(t, result.Removed)
	assert.Greater(t, result.FileCount, 0)
	_, statErr := os.Stat(target)
	assert.True(t, os.IsNotExist(statErr), "skill dir must be deleted")

	// frontmatter 名与目录名不一致：默认拒绝，--force 放行。
	mismatched := writeManageTestSkill(t, root, "renamed-dir", "original-name")
	_, _, err = runSkillRemoveCommand(skillRemoveOptions{Name: "renamed-dir", Cwd: workspace})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--force")
	_, statErr = os.Stat(mismatched)
	assert.NoError(t, statErr, "mismatched dir must be kept without --force")

	result, _, err = runSkillRemoveCommand(skillRemoveOptions{Name: "renamed-dir", Cwd: workspace, Force: true})
	require.NoError(t, err)
	assert.True(t, result.Removed)
	assert.True(t, result.Forced)

	// 不存在的 skill：明确报错。
	_, _, err = runSkillRemoveCommand(skillRemoveOptions{Name: "missing-skill", Cwd: workspace})
	require.Error(t, err)
}

func TestRunSkillRemoveCommand_DryRunKeepsFiles(t *testing.T) {
	isolateSkillDiscoveryHome(t)
	workspace := t.TempDir()
	root := filepath.Join(workspace, ".agents", "skills")
	target := writeManageTestSkill(t, root, "demo-skill", "demo-skill")

	result, _, err := runSkillRemoveCommand(skillRemoveOptions{Name: "demo-skill", Cwd: workspace, DryRun: true})
	require.NoError(t, err)
	assert.False(t, result.Removed)
	assert.True(t, result.DryRun)
	_, statErr := os.Stat(target)
	assert.NoError(t, statErr, "dry-run must not delete anything")
}

func TestParseSkillAddSource(t *testing.T) {
	owner, repo, ref, err := parseSkillAddSource("openai/skills@v1.2.3")
	require.NoError(t, err)
	assert.Equal(t, "openai", owner)
	assert.Equal(t, "skills", repo)
	assert.Equal(t, "v1.2.3", ref)

	owner, repo, ref, err = parseSkillAddSource("https://github.com/owner/repo.git")
	require.NoError(t, err)
	assert.Equal(t, "owner", owner)
	assert.Equal(t, "repo", repo)
	assert.Empty(t, ref)

	owner, repo, ref, err = parseSkillAddSource("git@github.com:owner/repo")
	require.NoError(t, err)
	assert.Equal(t, "owner", owner)
	assert.Equal(t, "repo", repo)
	assert.Empty(t, ref)

	for _, invalid := range []string{"", "owner", "owner/repo/extra", "https://gitlab.com/owner/repo"} {
		if _, _, _, err := parseSkillAddSource(invalid); err == nil {
			t.Fatalf("source %q should be rejected", invalid)
		}
	}
}

// buildSkillTarGz 造一个 GitHub 形态的 tarball（顶层目录 <repo>-<ref>/）。
func buildSkillTarGz(t *testing.T, topDir string, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range files {
		entry := &tar.Header{
			Name: topDir + "/" + name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		require.NoError(t, tarWriter.WriteHeader(entry))
		_, err := tarWriter.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tarWriter.Close())
	require.NoError(t, gzipWriter.Close())
	return buffer.Bytes()
}

func TestExtractSkillTarGz_LocatesAndInstalls(t *testing.T) {
	archive := buildSkillTarGz(t, "repo-main", map[string]string{
		"demo/SKILL.md":     "---\nname: demo\ndescription: demo skill\n---\n\nbody\n",
		"demo/reference.md": "reference\n",
		"README.md":         "readme\n",
	})

	extractRoot := t.TempDir()
	files, err := extractSkillTarGz(archive, extractRoot)
	require.NoError(t, err)
	require.Equal(t, 3, files)

	sourceDir, repoPath, err := locateSkillDirInArchive(extractRoot, "", "", "repo")
	require.NoError(t, err)
	assert.Equal(t, "demo", repoPath)
	assert.True(t, isCodexSkillDir(sourceDir))

	targetRoot := filepath.Join(t.TempDir(), ".agents", "skills")
	result, err := installSkillFromTree(sourceDir, "demo", targetRoot, false, false, skillSourceRecord{Repo: "owner/repo", Ref: "main"})
	require.NoError(t, err)
	assert.True(t, result.Installed)
	assert.Equal(t, 2, result.FileCount)

	assert.FileExists(t, filepath.Join(targetRoot, "demo", "SKILL.md"))
	assert.FileExists(t, filepath.Join(targetRoot, "demo", "reference.md"))
	record, err := os.ReadFile(filepath.Join(targetRoot, "demo", ".aicli-source.json"))
	require.NoError(t, err)
	assert.Contains(t, string(record), "owner/repo")

	// 已存在且未 --force：拒绝覆盖；--force：覆盖并标记。
	_, err = installSkillFromTree(sourceDir, "demo", targetRoot, false, false, skillSourceRecord{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--force")

	result, err = installSkillFromTree(sourceDir, "demo", targetRoot, true, false, skillSourceRecord{})
	require.NoError(t, err)
	assert.True(t, result.Overwritten)

	// dry-run 不写文件。
	otherRoot := filepath.Join(t.TempDir(), "skills")
	result, err = installSkillFromTree(sourceDir, "demo", otherRoot, false, true, skillSourceRecord{})
	require.NoError(t, err)
	assert.False(t, result.Installed)
	_, statErr := os.Stat(filepath.Join(otherRoot, "demo"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestExtractSkillTarGz_RejectsPathTraversal(t *testing.T) {
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	content := "evil\n"
	require.NoError(t, tarWriter.WriteHeader(&tar.Header{Name: "../evil.txt", Mode: 0o644, Size: int64(len(content))}))
	_, err := tarWriter.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, tarWriter.Close())
	require.NoError(t, gzipWriter.Close())

	_, err = extractSkillTarGz(buffer.Bytes(), t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsafe archive entry")
}

func TestLocateSkillDirInArchive_MultipleCandidatesRequiresPath(t *testing.T) {
	archive := buildSkillTarGz(t, "repo-main", map[string]string{
		"one/SKILL.md": "---\nname: one\ndescription: one\n---\n",
		"two/SKILL.md": "---\nname: two\ndescription: two\n---\n",
	})
	extractRoot := t.TempDir()
	_, err := extractSkillTarGz(archive, extractRoot)
	require.NoError(t, err)

	_, _, err = locateSkillDirInArchive(extractRoot, "", "", "repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--path")

	sourceDir, repoPath, err := locateSkillDirInArchive(extractRoot, "two", "", "")
	require.NoError(t, err)
	assert.Equal(t, "two", repoPath)
	assert.True(t, isCodexSkillDir(sourceDir))
}
