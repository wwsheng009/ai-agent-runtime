package gitbrowse

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestGitbrowseHelperProcess 是被 runProcess 自执行的辅助进程入口：
// 只有设置了 GITBROWSE_HELPER_MODE 时才真正干活，正常测试运行会立即返回。
// 各分支显式 os.Exit，避免 testing 框架往 stdout 追加 "PASS" 污染输出断言。
func TestGitbrowseHelperProcess(t *testing.T) {
	switch os.Getenv("GITBROWSE_HELPER_MODE") {
	case "":
		return
	case "ok":
		_, _ = os.Stdout.WriteString("hello")
	case "big":
		_, _ = os.Stdout.WriteString(strings.Repeat("x", 60000))
	case "fail":
		_, _ = os.Stderr.WriteString("fatal: helper failure\n")
		os.Exit(3)
	case "sleep":
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}

// helperCommand 返回自执行测试二进制所需的 path/args/env。
func helperCommand(mode string) (string, []string, []string) {
	return os.Args[0], []string{"-test.run=TestGitbrowseHelperProcess"},
		append(os.Environ(), "GITBROWSE_HELPER_MODE="+mode)
}

func TestRunProcessCapturesOutput(t *testing.T) {
	path, args, env := helperCommand("ok")
	result, err := runProcess(context.Background(), "helper", path, args, env, 30*time.Second, 1024)
	require.NoError(t, err)
	require.Equal(t, "hello", string(result.stdout))
	require.False(t, result.truncated)
}

func TestRunProcessTruncatesOutput(t *testing.T) {
	path, args, env := helperCommand("big")
	result, err := runProcess(context.Background(), "helper", path, args, env, 30*time.Second, 1000)
	require.NoError(t, err)
	require.True(t, result.truncated)
	require.Len(t, result.stdout, 1000)
}

func TestRunProcessReportsExitCodeAndStderrTail(t *testing.T) {
	path, args, env := helperCommand("fail")
	result, err := runProcess(context.Background(), "git status", path, args, env, 30*time.Second, 1024)
	require.Error(t, err)
	require.Equal(t, CodeGitFailed, Code(err))
	require.Equal(t, 500, HTTPStatus(err))
	require.Equal(t, 3, ExitCode(err))
	require.Equal(t, 3, result.exitCode)
	require.Contains(t, err.Error(), "git status failed (exit 3)")
	require.Contains(t, err.Error(), "fatal: helper failure")
}

func TestRunProcessKillsOnTimeout(t *testing.T) {
	path, args, env := helperCommand("sleep")
	start := time.Now()
	_, err := runProcess(context.Background(), "git diff", path, args, env, 300*time.Millisecond, 1024)
	require.Error(t, err)
	require.Equal(t, CodeGitFailed, Code(err))
	require.Contains(t, err.Error(), "timed out")
	// 被 kill 而不是等子进程自然结束（helper 会睡 30s）。
	require.Less(t, time.Since(start), 10*time.Second)
}

func TestRunProcessHonoursClientCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	path, args, env := helperCommand("sleep")
	_, err := runProcess(ctx, "git status", path, args, env, time.Minute, 1024)
	require.ErrorIs(t, err, context.Canceled)
}

func TestRunnerWithoutGitIsUnavailable(t *testing.T) {
	runner := newRunner(DefaultLimits())
	runner.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	_, err := runner.git(context.Background(), t.TempDir(), "git status", time.Second, 0, "status")
	require.Error(t, err)
	require.Equal(t, CodeGitUnavailable, Code(err))
	require.Equal(t, 503, HTTPStatus(err))
	require.Contains(t, err.Error(), "git executable not found")
}

func TestGitArgsUseFixedSandboxFlags(t *testing.T) {
	require.Equal(t,
		[]string{"-C", `C:\repo`, "--no-pager", "--no-optional-locks", "status", "--porcelain=v2"},
		gitArgs(`C:\repo`, []string{"status", "--porcelain=v2"}))
}

func TestGitEnvOverrides(t *testing.T) {
	env := strings.Join(gitEnv(), "\n")
	for _, want := range []string{
		"GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "GIT_PAGER=cat",
	} {
		require.Contains(t, env, want)
	}
}

// PATH 置空模拟「部署环境没有 git」：必须降级为 503 git_unavailable，而不是 panic/500。
func TestStatusWithoutGitIsUnavailable(t *testing.T) {
	requireGit(t)
	service := newTestService(t, t.TempDir(), Limits{})
	t.Setenv("PATH", "")
	_, err := service.Status(context.Background(), RepoRequest{Scope: "cwd"})
	require.Error(t, err)
	require.Equal(t, CodeGitUnavailable, Code(err))
	require.Equal(t, 503, HTTPStatus(err))
	require.False(t, service.Available())
}
