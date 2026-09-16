package gitbrowse

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// newTestRepoWithCommits 建立一个带 n 次提交的临时仓库（最新的提交信息为 "commit n"）。
func newTestRepoWithCommits(t *testing.T, n int) string {
	t.Helper()
	dir := newTestRepo(t)
	for i := 2; i <= n; i++ {
		writeFile(t, filepath.Join(dir, "a.txt"), "line1\nline2\nline3\nline"+strings.Repeat("x", i)+"\n")
		runGit(t, dir, "add", "a.txt")
		runGit(t, dir, "commit", "-q", "-m", "commit "+string(rune('0'+i)))
	}
	return dir
}

func TestCommitsFirstPageAndFields(t *testing.T) {
	root := newTestRepoWithCommits(t, 3)
	service := newTestService(t, root, Limits{})

	result, err := service.Commits(context.Background(), CommitsRequest{
		RepoRequest: RepoRequest{Scope: "workspace:wd"},
		Limit:       2,
	})
	require.NoError(t, err)

	require.Equal(t, 2, result.Limit)
	require.True(t, result.HasMore)
	require.Empty(t, result.Warnings)
	require.Positive(t, result.GeneratedAt)
	require.Len(t, result.Commits, 2)

	newest := result.Commits[0]
	require.Equal(t, "commit 3", newest.Subject)
	require.Len(t, newest.SHA, 40)
	require.Len(t, newest.Short, 7)
	require.True(t, strings.HasPrefix(newest.SHA, newest.Short))
	require.Equal(t, "gitbrowse tests", newest.Author)
	require.Equal(t, testAuthorDate, newest.Date)
	require.Contains(t, newest.Refs, "HEAD -> main")
	require.Equal(t, "commit 2", result.Commits[1].Subject)

	// next_cursor = 本页最后一条 sha，用它翻下一页。
	require.Equal(t, result.Commits[1].SHA, result.NextCursor)

	next, err := service.Commits(context.Background(), CommitsRequest{
		RepoRequest: RepoRequest{Scope: "workspace:wd"},
		Limit:       2,
		Cursor:      result.NextCursor,
	})
	require.NoError(t, err)
	require.False(t, next.HasMore)
	require.Empty(t, next.NextCursor)
	require.Len(t, next.Commits, 1)
	require.Equal(t, "init", next.Commits[0].Subject)
}

func TestCommitsLimitClamp(t *testing.T) {
	root := newTestRepoWithCommits(t, 2)
	service := newTestService(t, root, Limits{})
	ctx := context.Background()

	def, err := service.Commits(ctx, CommitsRequest{RepoRequest: RepoRequest{Scope: "cwd"}})
	require.NoError(t, err)
	require.Equal(t, 50, def.Limit)

	capped, err := service.Commits(ctx, CommitsRequest{RepoRequest: RepoRequest{Scope: "cwd"}, Limit: 1000})
	require.NoError(t, err)
	require.Equal(t, 200, capped.Limit)
	require.False(t, capped.HasMore)
	require.Len(t, capped.Commits, 2)

	custom, err := service.Commits(ctx, CommitsRequest{RepoRequest: RepoRequest{Scope: "cwd"}, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, 1, custom.Limit)
	require.True(t, custom.HasMore)
	require.Equal(t, custom.Commits[0].SHA, custom.NextCursor)
}

func TestCommitsCursorValidation(t *testing.T) {
	root := newTestRepo(t)
	service := newTestService(t, root, Limits{})

	for _, cursor := range []string{"--all", "HEAD", "abc def", "not-a-sha"} {
		t.Run(cursor, func(t *testing.T) {
			_, err := service.Commits(context.Background(), CommitsRequest{
				RepoRequest: RepoRequest{Scope: "cwd"},
				Cursor:      cursor,
			})
			require.Error(t, err)
			require.Equal(t, CodeCursorInvalid, Code(err))
			require.Equal(t, 400, HTTPStatus(err))
		})
	}
}

// 空仓库（只有 git init）不是错误：返回空列表 + warning。
func TestCommitsEmptyRepository(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	service := newTestService(t, root, Limits{})

	result, err := service.Commits(context.Background(), CommitsRequest{RepoRequest: RepoRequest{Scope: "cwd"}})
	require.NoError(t, err)
	require.Empty(t, result.Commits)
	require.False(t, result.HasMore)
	require.Len(t, result.Warnings, 1)
	require.Contains(t, result.Warnings[0], "no commits yet")
}

func TestParseCommitLog(t *testing.T) {
	raw := "sha1\x1fshort1\x1fAlice\x1f2026-01-02T03:04:05+08:00\x1fsubject one\x1fHEAD -> main, tag: v1\n" +
		"broken-record-without-separators\n" +
		"sha2\x1fshort2\x1fBob\x1f2026-01-01T00:00:00+08:00\x1fsubject two\x1f\n"

	commits, warnings := parseCommitLog([]byte(raw), false)
	require.Len(t, commits, 2)
	require.Equal(t, []string{"HEAD -> main", "tag: v1"}, commits[0].Refs)
	require.Empty(t, commits[1].Refs)
	require.NotNil(t, commits[1].Refs)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "unrecognized git log record")

	// 输出被字节上限截断：丢弃可能残缺的尾行并给出 warning。
	truncated, truncatedWarnings := parseCommitLog([]byte(raw[:len(raw)-1]), true)
	require.Len(t, truncated, 1)
	require.Len(t, truncatedWarnings, 2)
	require.Contains(t, truncatedWarnings[0], "truncated")
}

func TestSplitRefs(t *testing.T) {
	require.Equal(t, []string{}, splitRefs(""))
	require.Equal(t, []string{"HEAD -> main"}, splitRefs(" HEAD -> main "))
	require.Equal(t, []string{"a", "b"}, splitRefs("a, ,b"))
}
