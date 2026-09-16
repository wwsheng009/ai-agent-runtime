package gitbrowse

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestScopeResolutionRejectsEscapes(t *testing.T) {
	root := newTestRepo(t)
	service := newTestService(t, root, Limits{})
	ctx := context.Background()

	cases := []struct {
		name  string
		scope string
		path  string
		code  string
		http  int
	}{
		{name: "空 scope", scope: "", path: "", code: CodeScopeInvalid, http: 400},
		{name: "未知 scope", scope: "bogus:x", path: "", code: CodeScopeInvalid, http: 400},
		{name: "workspace 缺 id", scope: "workspace:", path: "", code: CodeScopeInvalid, http: 400},
		{name: "workspace 不存在", scope: "workspace:nope", path: "", code: CodeScopeNotFound, http: 404},
		{name: "绝对路径", scope: "workspace:wd", path: `C:\Windows`, code: CodePathMustBeRelative, http: 400},
		{name: "根相对斜杠", scope: "workspace:wd", path: "/etc/passwd", code: CodePathMustBeRelative, http: 400},
		{name: "盘符相对", scope: "workspace:wd", path: "C:tmp", code: CodePathMustBeRelative, http: 400},
		{name: "向上越界", scope: "workspace:wd", path: "../..", code: CodePathOutsideScope, http: 400},
		{name: "反向斜杠越界", scope: "workspace:wd", path: `..\..`, code: CodePathOutsideScope, http: 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.Status(ctx, RepoRequest{Scope: tc.scope, Path: tc.path})
			require.Error(t, err)
			require.Equal(t, tc.code, Code(err))
			require.Equal(t, tc.http, HTTPStatus(err))
		})
	}
}

func TestScopeSessionWithoutRoot(t *testing.T) {
	root := newTestRepo(t)
	service := NewService(Deps{Roots: mapResolver{err: ErrScopeHasNoRoot}, Cwd: root, Limits: Limits{}})
	_, err := service.Status(context.Background(), RepoRequest{Scope: "session:s1"})
	require.Error(t, err)
	require.Equal(t, CodeScopeHasNoRoot, Code(err))
	require.Equal(t, 400, HTTPStatus(err))
}

func TestScopeSessionResolvesRoot(t *testing.T) {
	root := newTestRepo(t)
	service := NewService(Deps{
		Roots:  mapResolver{sessions: map[string]string{"s1": root}},
		Cwd:    t.TempDir(),
		Limits: Limits{},
	})
	result, err := service.Status(context.Background(), RepoRequest{Scope: "session:s1"})
	require.NoError(t, err)
	require.Equal(t, root, result.Repo.Root)
}

func TestRepoNotFoundForNonRepository(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	service := newTestService(t, dir, Limits{})
	_, err := service.Status(context.Background(), RepoRequest{Scope: "cwd"})
	require.Error(t, err)
	require.Equal(t, CodeRepoNotFound, Code(err))
	require.Equal(t, 404, HTTPStatus(err))
}

func TestRepoNotFoundForBareRepository(t *testing.T) {
	requireGit(t)
	bare := filepath.Join(t.TempDir(), "bare.git")
	runGit(t, filepath.Dir(bare), "init", "-q", "--bare", bare)
	service := newTestService(t, bare, Limits{})
	_, err := service.Status(context.Background(), RepoRequest{Scope: "cwd"})
	require.Error(t, err)
	require.Equal(t, CodeRepoNotFound, Code(err))
}

func TestPathNotFoundInsideRepo(t *testing.T) {
	root := newTestRepo(t)
	service := newTestService(t, root, Limits{})
	_, err := service.Status(context.Background(), RepoRequest{Scope: "cwd", Path: "missing-dir"})
	require.Error(t, err)
	require.Equal(t, CodePathNotFound, Code(err))
	require.Equal(t, 404, HTTPStatus(err))
}

// 作用域根是仓库的子目录时必须上溯到仓库根，并在响应里给出仓库根路径（§5.5）。
func TestSubdirectoryAscendsToRepositoryRoot(t *testing.T) {
	root := newTestRepo(t)
	sub := filepath.Join(root, "pkg")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	writeFile(t, filepath.Join(sub, "b.txt"), "changed\n")

	service := newTestService(t, root, Limits{})
	result, err := service.Status(context.Background(), RepoRequest{Scope: "cwd", Path: "pkg"})
	require.NoError(t, err)
	require.Equal(t, root, result.Repo.Root)
	// 作用域相对路径：pkg/ 前缀来自「仓库根 → 作用域根」的映射。
	require.Len(t, result.Untracked, 1)
	require.Equal(t, "pkg/b.txt", result.Untracked[0].Path)
}

// 仓库在作用域之上时（作用域是仓库的子目录），作用域之外的行必须被隐藏并计入 warnings。
func TestChangesOutsideScopeAreWarned(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, filepath.Join(root, "outside.txt"), "x\n")
	sub := filepath.Join(root, "pkg")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	writeFile(t, filepath.Join(sub, "inside.txt"), "y\n")

	// 作用域根 = 子目录 pkg，而仓库根在上层 → outside.txt 落在作用域之外。
	service := newTestService(t, sub, Limits{})
	result, err := service.Status(context.Background(), RepoRequest{Scope: "cwd"})
	require.NoError(t, err)
	require.Len(t, result.Untracked, 1)
	require.Equal(t, "inside.txt", result.Untracked[0].Path)
	require.Contains(t, result.Warnings, "1 changed path(s) outside the selected root were hidden")
}

// 仓库探测按 TTL 缓存：TTL 过期后必须重新探测（删除 .git 后从 200 变成 repo_not_found）。
func TestRepoProbeCacheHonoursTTL(t *testing.T) {
	root := newTestRepo(t)
	ctx := context.Background()

	cached := newTestService(t, root, Limits{RepoCacheTTL: time.Hour})
	gitCtx, err := cached.prepare(ctx, RepoRequest{Scope: "cwd"})
	require.NoError(t, err)
	require.Equal(t, root, gitCtx.RepoRoot)

	require.NoError(t, os.RemoveAll(filepath.Join(root, ".git")))
	// TTL 内命中缓存：不再执行 rev-parse，仍然返回原仓库根。
	gitCtx, err = cached.prepare(ctx, RepoRequest{Scope: "cwd"})
	require.NoError(t, err)
	require.Equal(t, root, gitCtx.RepoRoot)

	// TTL 过期后重新探测 → repo_not_found。
	expired := newTestService(t, root, Limits{RepoCacheTTL: time.Nanosecond})
	_, err = expired.prepare(ctx, RepoRequest{Scope: "cwd"})
	require.Error(t, err)
	require.Equal(t, CodeRepoNotFound, Code(err))
}

func TestWithinRootCaseInsensitiveOnWindows(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Repo")
	target := filepath.Join(filepath.Dir(root), "Repo", "sub")
	if runtime.GOOS == "windows" {
		require.True(t, withinRoot(root, target))
		require.True(t, withinRoot(root, filepath.Join(filepath.Dir(root), "repo", "SUB")))
		require.False(t, withinRoot(root, filepath.Join(filepath.Dir(root), "RepoX")))
		return
	}
	require.True(t, withinRoot(root, target))
	require.False(t, withinRoot(root, filepath.Join(filepath.Dir(root), "RepoX")))
}

func TestSymlinkEscapeIsRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 创建符号链接需要特权，交由 CI/开发机按需验证")
	}
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.txt"), "top secret\n")
	root := newTestRepo(t)
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "link")))

	service := newTestService(t, root, Limits{})
	_, err := service.Status(context.Background(), RepoRequest{Scope: "cwd", Path: "link"})
	require.Error(t, err)
	require.Equal(t, CodePathOutsideScope, Code(err))
}

func TestValidRevision(t *testing.T) {
	require.True(t, validRevision("a1b2c3d"))
	require.True(t, validRevision("feature/foo-1.2"))
	require.False(t, validRevision("--upload-pack=evil"))
	require.False(t, validRevision("HEAD~1"))
	require.False(t, validRevision("a b"))
	require.False(t, validRevision(""))
}
