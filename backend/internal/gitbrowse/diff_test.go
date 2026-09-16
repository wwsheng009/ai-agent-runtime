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
	// 请求目标就是有改动的一侧：不得回退。
	require.Equal(t, "staged", result.EffectiveTarget)
	require.False(t, result.TargetFallback)
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

// 变更在另一侧时必须回退，否则「已暂存的新文件 + 目标=工作区」会显示成「没有差异」。
func TestDiffFallsBackToStagedWhenWorkingSideIsUnchanged(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, filepath.Join(root, "new.txt"), "one\ntwo\n")
	runGit(t, root, "add", "new.txt")

	service := newTestService(t, root, Limits{})
	result, err := service.Diff(context.Background(), DiffRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		File:        "new.txt",
		Target:      "working",
	})
	require.NoError(t, err)

	require.Equal(t, "working", result.Target)
	require.Equal(t, "staged", result.EffectiveTarget)
	require.True(t, result.TargetFallback)
	require.Equal(t, "A", result.File.Status)
	require.Equal(t, 2, result.Insertions)
	require.Len(t, result.Hunks, 1)
	require.Contains(t, result.Raw, "+++ b/new.txt")
}

// 反方向同理：未暂存的删除 + 目标=已暂存 也必须回退到工作区，而不是空 diff。
func TestDiffFallsBackToWorkingWhenChangeIsUnstaged(t *testing.T) {
	root := newTestRepo(t)
	require.NoError(t, os.Remove(filepath.Join(root, "a.txt")))

	service := newTestService(t, root, Limits{})
	result, err := service.Diff(context.Background(), DiffRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		File:        "a.txt",
		Target:      "staged",
	})
	require.NoError(t, err)

	require.Equal(t, "staged", result.Target)
	require.Equal(t, "working", result.EffectiveTarget)
	require.True(t, result.TargetFallback)
	require.Equal(t, "D", result.File.Status)
	require.Equal(t, 3, result.Deletions)
	require.Len(t, result.Hunks, 1)
}

// 空的新文件在 unified diff 里连文件头都没有：回退后必须靠 name-status 给出 A，
// 否则「已暂存的空新文件」会被显示成「没有差异」。
func TestDiffFallbackKeepsAddedStatusForEmptyNewFile(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, filepath.Join(root, "empty.txt"), "")
	runGit(t, root, "add", "empty.txt")

	service := newTestService(t, root, Limits{})
	result, err := service.Diff(context.Background(), DiffRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		File:        "empty.txt",
		Target:      "working",
	})
	require.NoError(t, err)

	require.True(t, result.TargetFallback)
	require.Equal(t, "staged", result.EffectiveTarget)
	require.Equal(t, "A", result.File.Status)
	require.Equal(t, 0, result.Insertions)
	require.Equal(t, 0, result.Deletions)
	require.Empty(t, result.Hunks)
}

// 未跟踪文件 + 目标=已暂存：它本来就不在索引里，必须回退到工作区并用 --no-index 合成新增内容。
func TestDiffUntrackedFallsBackToWorkingForStagedTarget(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, filepath.Join(root, "fresh.txt"), "one\ntwo\nthree\n")

	service := newTestService(t, root, Limits{})
	result, err := service.Diff(context.Background(), DiffRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		File:        "fresh.txt",
		Target:      "staged",
	})
	require.NoError(t, err)

	require.Equal(t, "staged", result.Target)
	require.Equal(t, "working", result.EffectiveTarget)
	require.True(t, result.TargetFallback)
	require.Equal(t, "A", result.File.Status)
	require.Equal(t, 3, result.Insertions)
	require.Len(t, result.Hunks, 1)
	require.Contains(t, result.Raw, "+++ b/fresh.txt")
}

// commit 目标没有「另一侧」：该提交没动这条路径就是「没有差异」，不得回退到工作区/暂存区。
func TestDiffCommitTargetNeverFallsBack(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, filepath.Join(root, "b.txt"), "next\n")
	runGit(t, root, "add", "b.txt")
	runGit(t, root, "commit", "-q", "-m", "second")
	sha := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))

	service := newTestService(t, root, Limits{})
	result, err := service.Diff(context.Background(), DiffRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		File:        "a.txt",
		Target:      "commit:" + sha,
	})
	require.NoError(t, err)

	require.Equal(t, "commit:"+sha, result.Target)
	require.Equal(t, "commit:"+sha, result.EffectiveTarget)
	require.False(t, result.TargetFallback)
	require.Empty(t, result.Raw)
	require.Empty(t, result.Hunks)
	require.Equal(t, 0, result.Insertions)
	require.Equal(t, 0, result.Deletions)
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
	// 已跟踪但无差异的文件不得被误判为新增。
	require.Equal(t, "M", ignored.File.Status)
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

// 未跟踪文件必须合成「新增文件」diff：否则点击后显示「没有差异」，与事实相反。
func TestDiffUntrackedFileSynthesizesNewFile(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, filepath.Join(root, "untracked.txt"), "one\ntwo\n")

	service := newTestService(t, root, Limits{})
	result, err := service.Diff(context.Background(), DiffRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		File:        "untracked.txt",
	})
	require.NoError(t, err)

	require.Equal(t, "untracked.txt", result.File.Path)
	require.Equal(t, "A", result.File.Status)
	require.Nil(t, result.File.OldPath)
	require.Equal(t, "working", result.Target)
	require.Equal(t, 2, result.Insertions)
	require.Equal(t, 0, result.Deletions)
	require.Empty(t, result.ParseError)
	require.False(t, result.Truncated)
	require.Contains(t, result.Raw, "new file mode")
	require.Contains(t, result.Raw, "--- /dev/null")
	require.Contains(t, result.Raw, "+++ b/untracked.txt")

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
	require.Equal(t, "two", hunk.Lines[1].Text)
}

// 未跟踪且无尾换行：末行仍是新增行，并保留 nonewline 尾标记。
func TestDiffUntrackedFileWithoutTrailingNewline(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, filepath.Join(root, "no-newline.txt"), "one\ntwo")

	service := newTestService(t, root, Limits{})
	result, err := service.Diff(context.Background(), DiffRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		File:        "no-newline.txt",
	})
	require.NoError(t, err)
	require.Equal(t, "A", result.File.Status)
	require.Equal(t, 2, result.Insertions)
	require.Equal(t, 0, result.Deletions)
	require.Empty(t, result.ParseError)

	var adds, markers int
	for _, line := range result.Hunks[0].Lines {
		switch line.Type {
		case "add":
			adds++
		case "nonewline":
			markers++
		}
	}
	require.Equal(t, 2, adds)
	require.GreaterOrEqual(t, markers, 1)
	require.Contains(t, result.Raw, `\ No newline at end of file`)
}

// 未跟踪的二进制文件不该被当成文本 diff 渲染。
func TestDiffUntrackedBinaryFile(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, filepath.Join(root, "untracked-bin.dat"), "start\x00\x01\x02\x03")

	service := newTestService(t, root, Limits{})
	result, err := service.Diff(context.Background(), DiffRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		File:        "untracked-bin.dat",
	})
	require.NoError(t, err)
	require.Equal(t, "A", result.File.Status)
	require.True(t, result.File.IsBinary)
	require.Empty(t, result.Hunks)
	require.Empty(t, result.ParseError)
	require.Contains(t, result.Raw, "Binary files")
}

// `--no-index` 的退出码 1 有两种含义，必须按 stdout/stderr 形状区分（本机实测：
// 存在差异 → stdout 有内容 / stderr 为空；路径读不到 → stdout 为空 / stderr 有 error 行）。
func TestNoIndexFailureDistinguishesDiffFromUnreadablePath(t *testing.T) {
	diff := &cmdResult{stdout: []byte("diff --git a/x b/x\nnew file mode 100644\n"), exitCode: 1}
	require.NoError(t, noIndexFailure(diff))

	// 空的新文件：两侧都没有内容，本来就没有输出，不算失败。
	require.NoError(t, noIndexFailure(&cmdResult{exitCode: 0}))

	missing := &cmdResult{stderr: []byte("error: Could not access 'gone.txt'\n"), exitCode: 1}
	err := noIndexFailure(missing)
	require.Error(t, err)
	require.Equal(t, CodeGitFailed, Code(err))
	require.Equal(t, 500, HTTPStatus(err))
	require.Equal(t, 1, ExitCode(err))
	require.Contains(t, err.Error(), "Could not access 'gone.txt'")
}

// 真机复现「未跟踪文件在点开前被删除」：`--no-index` 退出码同样是 1，但 stdout 为空、
// stderr 有 error 行。这里锁住该形状假设，避免日后有人把「退出码 1」简化成「有差异」。
func TestNoIndexFailureOnRealMissingPath(t *testing.T) {
	root := newTestRepo(t)
	service := newTestService(t, root, Limits{})
	ctx := context.Background()
	gitCtx, err := service.prepare(ctx, RepoRequest{Scope: "cwd"})
	require.NoError(t, err)

	result, err := service.runner.gitAllowingExit(ctx, gitCtx.ScopeRoot, "git diff --no-index",
		service.limits.DiffTimeout, 0, map[int]bool{1: true},
		"diff", "--no-color", "--no-ext-diff", "--unified=3", "--no-index", "--",
		devNullPath, "deleted-after-status.txt")
	require.NoError(t, err)
	require.Empty(t, result.stdout)
	require.NotEmpty(t, result.stderr)

	failure := noIndexFailure(result)
	require.Error(t, failure)
	require.Equal(t, CodeGitFailed, Code(failure))
	require.Equal(t, 500, HTTPStatus(failure))
	require.Contains(t, failure.Error(), "could not read the file")
}

// 空的新文件没有 hunk，但结论仍是「新增」而不是「没有差异」。
func TestDiffUntrackedEmptyFileKeepsAddedStatus(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, filepath.Join(root, "empty.txt"), "")

	service := newTestService(t, root, Limits{})
	result, err := service.Diff(context.Background(), DiffRequest{
		RepoRequest: RepoRequest{Scope: "cwd"},
		File:        "empty.txt",
	})
	require.NoError(t, err)
	require.Equal(t, "A", result.File.Status)
	require.Equal(t, 0, result.Insertions)
	require.Equal(t, 0, result.Deletions)
	require.Empty(t, result.Hunks)
	require.Empty(t, result.ParseError)
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
