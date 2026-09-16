package gitbrowse

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStageThenUnstage(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, filepath.Join(root, "a.txt"), "line1\nline2\nline3\nline4\n")
	writeFile(t, filepath.Join(root, "new.txt"), "brand new\n")
	ctx := context.Background()
	service := newTestService(t, root, Limits{})

	staged, err := service.Stage(ctx, StageRequest{
		RepoRequest: RepoRequest{Scope: "workspace:wd"},
		Action:      "stage",
		Files:       []string{"a.txt", "new.txt"},
	})
	require.NoError(t, err)
	require.Equal(t, "stage", staged.Action)
	require.Equal(t, []string{"a.txt", "new.txt"}, staged.Files)
	require.Positive(t, staged.GeneratedAt)
	require.False(t, staged.Status.Clean)
	require.Len(t, staged.Status.Staged, 2)
	require.Equal(t, "a.txt", staged.Status.Staged[0].Path)
	require.Equal(t, 1, staged.Status.Staged[0].Insertions)
	require.Equal(t, "new.txt", staged.Status.Staged[1].Path)
	require.Equal(t, "A", staged.Status.Staged[1].Status)
	require.Empty(t, staged.Status.Untracked)

	unstaged, err := service.Stage(ctx, StageRequest{
		RepoRequest: RepoRequest{Scope: "workspace:wd"},
		Action:      "unstage",
		Files:       []string{"a.txt", "new.txt"},
	})
	require.NoError(t, err)
	require.Equal(t, "unstage", unstaged.Action)
	require.Empty(t, unstaged.Status.Staged)
	require.Len(t, unstaged.Status.Unstaged, 1)
	require.Equal(t, "a.txt", unstaged.Status.Unstaged[0].Path)
	require.Len(t, unstaged.Status.Untracked, 1)
	require.Equal(t, "new.txt", unstaged.Status.Untracked[0].Path)
}

// 已删除文件在暂存后工作区已无该路径，unstage 依然必须可用（不依赖路径存在）。
func TestStageDeletedFileUnstage(t *testing.T) {
	root := newTestRepo(t)
	require.NoError(t, os.Remove(filepath.Join(root, "a.txt")))
	ctx := context.Background()
	service := newTestService(t, root, Limits{})

	staged, err := service.Stage(ctx, StageRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		Action:      "stage",
		Files:       []string{"a.txt"},
	})
	require.NoError(t, err)
	require.Len(t, staged.Status.Staged, 1)
	require.Equal(t, "D", staged.Status.Staged[0].Status)

	unstaged, err := service.Stage(ctx, StageRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		Action:      "unstage",
		Files:       []string{"a.txt"},
	})
	require.NoError(t, err)
	require.Empty(t, unstaged.Status.Staged)
	require.Len(t, unstaged.Status.Unstaged, 1)
	require.Equal(t, "D", unstaged.Status.Unstaged[0].Status)
}

// path 指向子目录只影响仓库探测起点；作用域仍是 scope 根，files 用作用域相对路径。
func TestStageWithSubdirectoryPath(t *testing.T) {
	root := newTestRepo(t)
	sub := filepath.Join(root, "pkg")
	writeFile(t, filepath.Join(sub, "b.txt"), "sub\n")
	service := newTestService(t, root, Limits{})

	result, err := service.Stage(context.Background(), StageRequest{
		RepoRequest: RepoRequest{Scope: "workspace:wd", Path: "pkg"},
		Action:      "stage",
		Files:       []string{"pkg/b.txt"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"pkg/b.txt"}, result.Files)
	require.Len(t, result.Status.Staged, 1)
	require.Equal(t, "pkg/b.txt", result.Status.Staged[0].Path)
	require.Equal(t, root, result.Status.Repo.Root)
}

// 作用域是本仓库的子目录时：files 是「作用域相对」，git 收到的是仓库相对 pathspec。
func TestStageWithSubdirectoryScope(t *testing.T) {
	root := newTestRepo(t)
	sub := filepath.Join(root, "pkg")
	writeFile(t, filepath.Join(sub, "b.txt"), "sub\n")
	service := newTestService(t, sub, Limits{})

	result, err := service.Stage(context.Background(), StageRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		Action:      "stage",
		Files:       []string{"b.txt"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"b.txt"}, result.Files)
	require.Len(t, result.Status.Staged, 1)
	require.Equal(t, "b.txt", result.Status.Staged[0].Path)
	require.Equal(t, root, result.Status.Repo.Root)
}

func TestStageValidation(t *testing.T) {
	root := newTestRepo(t)
	ctx := context.Background()
	service := newTestService(t, root, Limits{})

	cases := []struct {
		name   string
		req    StageRequest
		code   string
		status int
	}{
		{name: "非法 action", req: StageRequest{Action: "commit", Files: []string{"a.txt"}}, code: CodeActionInvalid, status: 400},
		{name: "缺少 action", req: StageRequest{Files: []string{"a.txt"}}, code: CodeActionInvalid, status: 400},
		{name: "空 files", req: StageRequest{Action: "stage"}, code: CodeFilesRequired, status: 400},
		{name: "越界文件", req: StageRequest{Action: "stage", Files: []string{"../escape.txt"}}, code: CodePathOutsideScope, status: 400},
		{name: "绝对路径", req: StageRequest{Action: "stage", Files: []string{"/etc/passwd"}}, code: CodePathMustBeRelative, status: 400},
		{name: "选项式路径", req: StageRequest{Action: "stage", Files: []string{"-A"}}, code: CodePathInvalid, status: 400},
		{name: "空白文件", req: StageRequest{Action: "stage", Files: []string{"  "}}, code: CodeFilesRequired, status: 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req
			req.Scope = "cwd"
			_, err := service.Stage(ctx, req)
			require.Error(t, err)
			require.Equal(t, tc.code, Code(err))
			require.Equal(t, tc.status, HTTPStatus(err))
		})
	}
}

// 先全量校验再执行：一个文件越界时，合法文件也不能被 stage（不允许部分生效）。
func TestStageValidatesAllFilesBeforeWriting(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, filepath.Join(root, "a.txt"), "line1\nline2\nline3\nline4\n")
	service := newTestService(t, root, Limits{})
	ctx := context.Background()

	_, err := service.Stage(ctx, StageRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		Action:      "stage",
		Files:       []string{"a.txt", "../escape.txt"},
	})
	require.Error(t, err)
	require.Equal(t, CodePathOutsideScope, Code(err))

	status, err := service.Status(ctx, RepoRequest{Scope: "cwd"})
	require.NoError(t, err)
	require.Empty(t, status.Staged)
	require.Len(t, status.Unstaged, 1)
}

func TestStageMissingGitIsUnavailable(t *testing.T) {
	root := newTestRepo(t)
	service := newTestService(t, root, Limits{})
	t.Setenv("PATH", "")

	_, err := service.Stage(context.Background(), StageRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		Action:      "stage",
		Files:       []string{"a.txt"},
	})
	require.Error(t, err)
	require.Equal(t, CodeGitUnavailable, Code(err))
	require.Equal(t, 503, HTTPStatus(err))
}
