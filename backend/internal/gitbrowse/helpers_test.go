package gitbrowse

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// 测试固定时间戳，避免提交时间抖动带来的断言不稳定。
const (
	testAuthorDate = "2026-01-02T03:04:05+08:00"
	testCommitDate = "2026-01-02T03:04:05+08:00"
)

// requireGit 在系统没有 git 时跳过测试（规划 §5.10 要求「无 git → 降级而非崩溃」）。
func requireGit(t *testing.T) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available on PATH")
	}
	return gitPath
}

// runGit 在 dir 下执行 git 子命令（测试夹具专用，不经过被测的 runner）。
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE="+testAuthorDate,
		"GIT_COMMITTER_DATE="+testCommitDate,
	)
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %v failed: %s", args, out)
	return string(out)
}

// newTestRepo 建立临时仓库（main 分支 + 一次提交）：a.txt 带尾换行，bin.dat 为二进制。
func newTestRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "gitbrowse tests")
	writeFile(t, filepath.Join(dir, "a.txt"), "line1\nline2\nline3\n")
	writeFile(t, filepath.Join(dir, "bin.dat"), string([]byte{0, 1, 2, 0, 255, 254}))
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "init")
	return dir
}

// writeFile 写文件（父目录不存在时自动创建）。
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// mapResolver 是按 map 解析的 RootResolver 测试替身。
type mapResolver struct {
	workspaces map[string]string
	sessions   map[string]string
	err        error
}

func (r mapResolver) WorkspaceRoot(_ context.Context, id string) (string, bool, error) {
	if r.err != nil {
		return "", false, r.err
	}
	path, ok := r.workspaces[id]
	return path, ok, nil
}

func (r mapResolver) SessionRoot(_ context.Context, id string) (string, bool, error) {
	if r.err != nil {
		return "", false, r.err
	}
	path, ok := r.sessions[id]
	return path, ok, nil
}

// newTestService 构造以 root 为 workspace:wd 根、cwd=root 的 Service。
func newTestService(t *testing.T, root string, limits Limits) *Service {
	t.Helper()
	service := NewService(Deps{
		Roots:  mapResolver{workspaces: map[string]string{"wd": root}},
		Cwd:    root,
		Limits: limits,
	})
	require.NotNil(t, service)
	return service
}

// newTestLimits 返回默认限额（显式写出便于测试覆盖单字段）。
func newTestLimits() Limits {
	return DefaultLimits()
}
