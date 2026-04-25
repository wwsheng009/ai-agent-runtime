package background

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

func TestBuildWindowsDetachedRunnerContentUsesDetectedShellAndCwd(t *testing.T) {
	shell := runtimeexecutor.Shell{Path: `C:\Program Files\PowerShell\7\pwsh.exe`, Type: runtimeexecutor.ShellTypePwsh}

	content := buildWindowsDetachedRunnerContent(
		shell,
		"git status",
		`E:\projects\ai\ai-agent-runtime`,
		`C:\logs\job.log`,
		`C:\logs\job.status`,
	)

	require.Contains(t, content, `$shellPath = 'C:\Program Files\PowerShell\7\pwsh.exe'`)
	require.Contains(t, content, `$shellArgs = @('-NoProfile', '-Command', 'git status')`)
	require.Contains(t, content, `Set-Location -LiteralPath 'E:\projects\ai\ai-agent-runtime'`)
	require.Contains(t, content, `[System.IO.File]::WriteAllText('C:\logs\job.status'`)
	require.Contains(t, content, `$scriptExitCode = 1`)
	require.NotContains(t, content, `cmd.exe /D /S /C`)
}

func TestWriteUnixDetachedRunnerUsesDetectedShellAndRecordsCwdFailure(t *testing.T) {
	tempDir := t.TempDir()
	runnerPath := filepath.Join(tempDir, "runner.sh")
	logPath := filepath.Join(tempDir, "job.log")
	statusPath := filepath.Join(tempDir, "job.status")
	shell := runtimeexecutor.Shell{Path: "/opt/zsh", Type: runtimeexecutor.ShellTypeZsh}

	require.NoError(t, writeUnixDetachedRunner(runnerPath, shell, "git status", "/tmp/work dir", logPath, statusPath))
	data, err := os.ReadFile(runnerPath)
	require.NoError(t, err)
	content := string(data)

	require.Contains(t, content, "if ! cd '/tmp/work dir'; then")
	require.Contains(t, content, "printf 'failed to change directory: %s\\n' '/tmp/work dir'")
	require.Contains(t, content, "printf \"%s\" \"1\" > '"+statusPath+"'")
	require.Contains(t, content, "'/opt/zsh' '-c' 'git status' >> '"+logPath+"' 2>&1")
}
