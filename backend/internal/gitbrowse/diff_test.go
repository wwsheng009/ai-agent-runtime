package gitbrowse

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiffStagedNewFile(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, filepath.Join(root, "new.txt"), "one\ntwo\n")
	runGit(t, root, "add", "new.txt")

	service := newTestService(t, root, Limits{})
	result, err := service.Diff(context.Background(), DiffRequest{
		RepoRequest: RepoRequest{Scope: "workspace:wd"},
		File:        "new.txt",
		Target:      "staged",
	})
	require.NoError(t, err)

	require.Equal(t, "new.txt", result.File.Path)
	require.Equal(t, "A", result.File.Status)
	require.Nil(t, result.File.OldPath)
	require.False(t, result.File.IsBinary)
	require.Equal(t, "staged", result.Target)
	require.Equal(t, 3, result.Context)
	require.Equal(t, "show", result.Whitespace)
	require.Equal(t, 2, result.Insertions)
	require.Equal(t, 0, result.Deletions)
	require.Empty(t, result.ParseError)
	require.False(t, result.Truncated)
	require.Contains(t, result.Raw, "+++ b/new.txt")

	require.Len(t, result.Hunks, 1)
	hunk := result.Hunks[0]
	require.Equal(t, 0, hunk.OldStart)
	require.Equal(t, 0, hunk.OldLines)
	require.Equal(t, 1, hunk.NewStart)
	require.Equal(t, 2, hunk.NewLines)
	require.Len(t, hunk.Lines, 2)
	require.Equal(t, "add", hunk.Lines[0].Type)
	require.Nil(t, hunk.Lines[0].OldNo)
	require.Equal(t, 1, *hunk.Lines[0].NewNo)
	require.Equal(t, "one", hunk.Lines[0].Text)
	require.Equal(t, 2, *hunk.Lines[1].NewNo)
}

func TestDiffWorkingDeletion(t *testing.T) {
	root := newTestRepo(t)
	require.NoError(t, os.Remove(filepath.Join(root, "a.txt")))

	service := newTestService(t, root, Limits{})
	result, err := service.Diff(context.Background(), DiffRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		File:        "a.txt",
	})
	require.NoError(t, err)

	require.Equal(t, "D", result.File.Status)
	require.Equal(t, "working", result.Target)
	require.Equal(t, 0, result.Insertions)
	require.Equal(t, 3, result.Deletions)

	require.Len(t, result.Hunks, 1)
	require.Len(t, result.Hunks[0].Lines, 3)
	for i, line := range result.Hunks[0].Lines {
		require.Equal(t, "del", line.Type)
		require.Equal(t, i+1, *line.OldNo)
		require.Nil(t, line.NewNo)
	}
	require.Equal(t, "line1", result.Hunks[0].Lines[0].Text)
}

// 中段修改：行号必须与 unified diff 的 hunk 头一致。
func TestDiffLineNumbersAndContext(t *testing.T) {
	root := newTestRepo(t)
	content := "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n"
	writeFile(t, filepath.Join(root, "a.txt"), content)
	runGit(t, root, "add", "a.txt")
	runGit(t, root, "commit", "-q", "-m", "add lines")
	writeFile(t, filepath.Join(root, "a.txt"), strings.Replace(content, "l5", "L5", 1))

	service := newTestService(t, root, Limits{})
	result, err := service.Diff(context.Background(), DiffRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		File:        "a.txt",
		Context:     1,
	})
	require.NoError(t, err)

	require.Equal(t, 1, result.Context)
	require.Equal(t, 1, result.Insertions)
	require.Equal(t, 1, result.Deletions)
	require.Len(t, result.Hunks, 1)
	hunk := result.Hunks[0]
	require.Equal(t, 4, hunk.OldStart)
	require.Equal(t, 3, hunk.OldLines)
	require.Equal(t, 4, hunk.NewStart)
	require.Equal(t, 3, hunk.NewLines)
	require.Equal(t, "l4", hunk.Lines[0].Text)
	require.Equal(t, "context", hunk.Lines[0].Type)
	require.Equal(t, 4, *hunk.Lines[0].OldNo)
	require.Equal(t, 4, *hunk.Lines[0].NewNo)
	require.Equal(t, "del", hunk.Lines[1].Type)
	require.Equal(t, 5, *hunk.Lines[1].OldNo)
	require.Nil(t, hunk.Lines[1].NewNo)
	require.Equal(t, "add", hunk.Lines[2].Type)
	require.Nil(t, hunk.Lines[2].OldNo)
	require.Equal(t, 5, *hunk.Lines[2].NewNo)
	require.Equal(t, "L5", hunk.Lines[2].Text)
}

// `\ No newline at end of file` 必须成为独立的 nonewline 行（不是在文本里拼标记）。
func TestDiffNoNewlineAtEndOfFile(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, filepath.Join(root, "a.txt"), "x\nold")
	runGit(t, root, "add", "a.txt")
	runGit(t, root, "commit", "-q", "-m", "no trailing newline")
	writeFile(t, filepath.Join(root, "a.txt"), "x\nnew")

	service := newTestService(t, root, Limits{})
	result, err := service.Diff(context.Background(), DiffRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		File:        "a.txt",
	})
	require.NoError(t, err)
	require.Empty(t, result.ParseError)

	var markers, dels, adds int
	for _, line := range result.Hunks[0].Lines {
		switch line.Type {
		case "nonewline":
			markers++
			require.True(t, strings.HasPrefix(line.Text, `\`))
			require.Nil(t, line.OldNo)
			require.Nil(t, line.NewNo)
		case "del":
			dels++
			require.Equal(t, "old", line.Text)
		case "add":
			adds++
			require.Equal(t, "new", line.Text)
		}
	}
	require.Equal(t, 2, markers)
	require.Equal(t, 1, dels)
	require.Equal(t, 1, adds)
	require.Contains(t, result.Raw, `\ No newline at end of file`)
}

// CRLF 行尾必须原样保留在 line.text 中（前端自行决定是否显示 ^M）。
func TestDiffPreservesCarriageReturn(t *testing.T) {
	root := newTestRepo(t)
	runGit(t, root, "config", "core.autocrlf", "false")
	writeFile(t, filepath.Join(root, "a.txt"), "a\r\nb\r\n")
	runGit(t, root, "add", "a.txt")
	runGit(t, root, "commit", "-q", "-m", "crlf")
	writeFile(t, filepath.Join(root, "a.txt"), "a\r\nB\r\n")

	service := newTestService(t, root, Limits{})
	result, err := service.Diff(context.Background(), DiffRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		File:        "a.txt",
	})
	require.NoError(t, err)

	var adds int
	for _, line := range result.Hunks[0].Lines {
		if line.Type == "add" {
			adds++
			require.Equal(t, "B\r", line.Text)
		}
	}
	require.Equal(t, 1, adds)
}

// whitespace=ignore_all 走服务端 --ignore-all-space：纯缩进改动应当没有 diff。
func TestDiffWhitespaceIgnoreAll(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, filepath.Join(root, "a.txt"), "a\nb\n")
	runGit(t, root, "add", "a.txt")
	runGit(t, root, "commit", "-q", "-m", "base")
	writeFile(t, filepath.Join(root, "a.txt"), "a\n    b\n")

	service := newTestService(t, root, Limits{})
	ctx := context.Background()

	shown, err := service.Diff(ctx, DiffRequest{RepoRequest: RepoRequest{Scope: "cwd"}, File: "a.txt"})
	require.NoError(t, err)
	require.Equal(t, "show", shown.Whitespace)
	require.Equal(t, 1, shown.Insertions)
	require.Equal(t, 1, shown.Deletions)
	require.Len(t, shown.Hunks, 1)

	ignored, err := service.Diff(ctx, DiffRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		File:        "a.txt",
		Whitespace:  "ignore_all",
	})
	require.NoError(t, err)
	require.Equal(t, "ignore_all", ignored.Whitespace)
	require.Empty(t, ignored.Raw)
	require.Empty(t, ignored.Hunks)
	require.Equal(t, 0, ignored.Insertions)
}

func TestDiffBinaryFile(t *testing.T) {
	root := newTestRepo(t)
	path := filepath.Join(root, "bin.dat")
	writeFile(t, path, "start\x00\x01\x02\x03")
	runGit(t, root, "add", "bin.dat")
	runGit(t, root, "commit", "-q", "-m", "binary")
	writeFile(t, path, "changed\x00\x01\x02\x03\x04")

	service := newTestService(t, root, Limits{})
	result, err := service.Diff(context.Background(), DiffRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		File:        "bin.dat",
	})
	require.NoError(t, err)
	require.True(t, result.File.IsBinary)
	require.Empty(t, result.Hunks)
	require.Empty(t, result.ParseError)
	require.Contains(t, result.Raw, "Binary files")
}

func TestDiffCommitTarget(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, filepath.Join(root, "a.txt"), "line1\nline2\nline3\nline4\n")
	runGit(t, root, "add", "a.txt")
	runGit(t, root, "commit", "-q", "-m", "modify a.txt")
	sha := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))

	service := newTestService(t, root, Limits{})
	result, err := service.Diff(context.Background(), DiffRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		File:        "a.txt",
		Target:      "commit:" + sha,
	})
	require.NoError(t, err)
	require.Equal(t, "commit:"+sha, result.Target)
	require.Equal(t, 1, result.Insertions)
	require.Equal(t, 0, result.Deletions)
	require.Len(t, result.Hunks, 1)
	require.Equal(t, "line4", result.Hunks[0].Lines[len(result.Hunks[0].Lines)-1].Text)
}

func TestDiffTargetValidation(t *testing.T) {
	root := newTestRepo(t)
	service := newTestService(t, root, Limits{})
	ctx := context.Background()

	cases := []struct {
		name   string
		req    DiffRequest
		code   string
		status int
	}{
		{name: "未知 target", req: DiffRequest{File: "a.txt", Target: "index"}, code: CodeTargetInvalid, status: 400},
		{name: "非法 sha", req: DiffRequest{File: "a.txt", Target: "commit:--all"}, code: CodeTargetInvalid, status: 400},
		{name: "缺少 file", req: DiffRequest{}, code: CodePathInvalid, status: 400},
		{name: "路径越界", req: DiffRequest{File: "../a.txt"}, code: CodePathOutsideScope, status: 400},
		{name: "选项式路径", req: DiffRequest{File: "a.txt", Target: "-"}, code: CodeTargetInvalid, status: 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req
			req.Scope = "cwd"
			_, err := service.Diff(ctx, req)
			require.Error(t, err)
			require.Equal(t, tc.code, Code(err))
			require.Equal(t, tc.status, HTTPStatus(err))
		})
	}
}

// 解析失败降级为 hunks=[] + parse_error，绝不 500（§5.6）。
func TestDiffParseFailureDegrades(t *testing.T) {
	parsed := parseUnifiedDiff("nothing like a diff\n@@ broken header @@\n", false)
	require.NotEmpty(t, parsed.err)
	require.Empty(t, parsed.hunks)
	require.Contains(t, parsed.err, "unparsable hunk header")

	// 下一个 hunk 头出现时上一个 hunk 还没喂满 → 判定为解析失败并整体降级。
	early := parseUnifiedDiff("@@ -1,3 +1,3 @@\n a\n+b\n@@ -10,1 +10,1 @@\n c\n", false)
	require.NotEmpty(t, early.err)
	require.Empty(t, early.hunks)
	require.Contains(t, early.err, "ended early")

	// 被上限截断的输出不做残缺判定，避免误报。
	truncated := parseUnifiedDiff("@@ -1,3 +1,3 @@\n a\n+b\n", true)
	require.Empty(t, truncated.err)
	require.Len(t, truncated.hunks, 1)
}

func TestDiffTruncationByLines(t *testing.T) {
	root := newTestRepo(t)
	lines := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		lines = append(lines, "line-"+strings.Repeat("x", 4)+string(rune('a'+i%26)))
	}
	writeFile(t, filepath.Join(root, "big.txt"), strings.Join(lines, "\n")+"\n")
	runGit(t, root, "add", "big.txt")

	service := newTestService(t, root, Limits{MaxDiffLines: 5})
	result, err := service.Diff(context.Background(), DiffRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		File:        "big.txt",
		Target:      "staged",
	})
	require.NoError(t, err)
	require.True(t, result.Truncated)
	require.Contains(t, result.TruncatedReason, "exceeds 5 lines")
	require.LessOrEqual(t, len(strings.Split(result.Raw, "\n")), 5)
	require.Empty(t, result.ParseError)
}

func TestDiffTruncationByBytes(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, filepath.Join(root, "big.txt"), strings.Repeat("0123456789\n", 200))
	runGit(t, root, "add", "big.txt")

	service := newTestService(t, root, Limits{MaxOutputBytes: 64})
	result, err := service.Diff(context.Background(), DiffRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		File:        "big.txt",
		Target:      "staged",
	})
	require.NoError(t, err)
	require.True(t, result.Truncated)
	require.Contains(t, result.TruncatedReason, "output exceeds")
	require.LessOrEqual(t, len(result.Raw), 64)
	require.Empty(t, result.ParseError)
}

func TestDiffMissingGitIsUnavailable(t *testing.T) {
	root := newTestRepo(t)
	service := newTestService(t, root, Limits{})
	service.runner.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }

	_, err := service.Diff(context.Background(), DiffRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		File:        "a.txt",
	})
	require.Error(t, err)
	require.Equal(t, CodeGitUnavailable, Code(err))
	require.Equal(t, 503, HTTPStatus(err))
}
