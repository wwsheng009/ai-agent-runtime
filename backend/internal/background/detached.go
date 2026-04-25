package background

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

const (
	backgroundMetaPID        = "pid"
	backgroundMetaStatusPath = "status_path"
	backgroundMetaRunnerPath = "runner_path"
)

func (m *Manager) canUseDetachedExecution(managed *managedJob) bool {
	if m == nil || managed == nil {
		return false
	}
	return strings.TrimSpace(managed.logPath) != ""
}

func (m *Manager) runDetachedJob(managed *managedJob) {
	if managed == nil {
		return
	}
	ctx := managed.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	req := managed.request
	startedAt := time.Now().UTC()

	launch, err := launchDetachedShell(managed.info.Command, managed.info.Cwd, managed.logPath)
	if err != nil {
		m.failJob(managed, err)
		return
	}

	managed.mu.Lock()
	if managed.info.Metadata == nil {
		managed.info.Metadata = map[string]interface{}{}
	}
	managed.info.Metadata[backgroundMetaPID] = launch.PID
	managed.info.Metadata[backgroundMetaStatusPath] = launch.StatusPath
	managed.info.Metadata[backgroundMetaRunnerPath] = launch.RunnerPath
	managed.info.Status = StatusRunning
	managed.info.StartedAt = &startedAt
	managed.info.Message = ""
	managed.info.ExitCode = nil
	managed.info.FinishedAt = nil
	managed.scheduled = false
	managed.mu.Unlock()

	if m.store != nil {
		_ = m.store.UpdateJob(context.Background(), managed.info)
	}
	m.appendJobEvent(context.Background(), managed.info.ID, "running", map[string]interface{}{
		"status": managed.info.Status,
		"pid":    launch.PID,
	})

	var deadline time.Time
	timeout := time.Duration(req.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = m.config.DefaultTimeout
	}
	if timeout > 0 {
		deadline = startedAt.Add(timeout)
	}
	m.monitorDetachedJob(ctx, managed, launch.PID, launch.StatusPath, deadline)
}

func (m *Manager) recoverDetachedRunningJob(job Job) bool {
	pid, ok := detachedPID(job.Metadata)
	if !ok {
		return false
	}
	statusPath, ok := stringMetadataValue(job.Metadata, backgroundMetaStatusPath)
	if !ok {
		return false
	}

	managed := m.managedJobFromStored(job)
	if managed == nil {
		return true
	}

	if exitCode, ok := readDetachedExitCode(statusPath); ok {
		m.mu.Lock()
		if _, exists := m.jobs[job.ID]; !exists {
			m.jobs[job.ID] = managed
		}
		m.mu.Unlock()
		m.emitLogOutput(context.Background(), managed)
		if exitCode == 0 {
			m.completeJob(managed, exitCode)
		} else {
			m.failJobWithCode(managed, exitCode, fmt.Sprintf("command exited with code %d", exitCode))
		}
		return true
	}

	if !isProcessRunning(pid) {
		return false
	}

	m.mu.Lock()
	if _, exists := m.jobs[job.ID]; !exists {
		m.jobs[job.ID] = managed
	}
	m.mu.Unlock()
	m.appendJobEvent(context.Background(), job.ID, "running", map[string]interface{}{
		"status":    StatusRunning,
		"pid":       pid,
		"recovered": true,
	})

	var deadline time.Time
	timeout := time.Duration(managed.request.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = m.config.DefaultTimeout
	}
	if timeout > 0 && job.StartedAt != nil && !job.StartedAt.IsZero() {
		deadline = job.StartedAt.Add(timeout)
	}

	m.jobWG.Add(1)
	go func() {
		defer m.jobWG.Done()
		m.monitorDetachedJob(managed.ctx, managed, pid, statusPath, deadline)
	}()
	return true
}

func (m *Manager) monitorDetachedJob(ctx context.Context, managed *managedJob, pid int, statusPath string, deadline time.Time) {
	if managed == nil {
		return
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	missingStatusSince := time.Time{}
	for {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
		}

		m.emitLogOutput(context.Background(), managed)

		if !deadline.IsZero() && time.Now().After(deadline) {
			_ = terminateProcess(pid)
			m.failJobWithCode(managed, -1, "command timed out")
			return
		}
		if exitCode, ok := readDetachedExitCode(statusPath); ok {
			m.emitLogOutput(context.Background(), managed)
			if exitCode == 0 {
				m.completeJob(managed, exitCode)
			} else {
				m.failJobWithCode(managed, exitCode, fmt.Sprintf("command exited with code %d", exitCode))
			}
			return
		}
		if !isProcessRunning(pid) {
			if missingStatusSince.IsZero() {
				missingStatusSince = time.Now().UTC()
			} else if time.Since(missingStatusSince) >= 500*time.Millisecond {
				m.failJobWithCode(managed, -1, "background process exited without status file")
				return
			}
		} else {
			missingStatusSince = time.Time{}
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

func (m *Manager) emitLogOutput(ctx context.Context, managed *managedJob) {
	if m == nil || managed == nil || strings.TrimSpace(managed.logPath) == "" {
		return
	}
	file, err := os.Open(managed.logPath)
	if err != nil {
		return
	}
	defer file.Close()

	managed.outputMu.Lock()
	defer managed.outputMu.Unlock()

	if _, err := file.Seek(managed.outputOffset, io.SeekStart); err != nil {
		return
	}

	buf := make([]byte, defaultOutputEventChunkBytes)
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			start := managed.outputOffset
			next := start + int64(n)
			_, _ = managed.output.Write(chunk)
			managed.outputOffset = next
			m.appendJobEvent(ctx, managed.info.ID, "output", map[string]interface{}{
				"offset":      start,
				"next_offset": next,
				"size":        len(chunk),
				"stream":      "combined",
				"chunk":       string(chunk),
			})
		}
		if readErr != nil {
			break
		}
	}
}

type detachedLaunch struct {
	PID        int
	StatusPath string
	RunnerPath string
}

func launchDetachedShell(command, cwd, logPath string) (*detachedLaunch, error) {
	logPath = strings.TrimSpace(logPath)
	command = strings.TrimSpace(command)
	if logPath == "" || command == "" {
		return nil, fmt.Errorf("detached launch requires command and log path")
	}
	statusPath := strings.TrimSuffix(logPath, filepath.Ext(logPath)) + ".status"
	runnerPath := strings.TrimSuffix(logPath, filepath.Ext(logPath))
	shell := runtimeexecutor.DefaultUserShell()
	if runtime.GOOS == "windows" {
		runnerPath += ".ps1"
	} else {
		runnerPath += ".sh"
	}

	if err := os.Remove(statusPath); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err := writeDetachedRunner(runnerPath, shell, command, cwd, logPath, statusPath); err != nil {
		return nil, err
	}
	pid, err := startDetachedRunner(runnerPath)
	if err != nil {
		return nil, err
	}
	return &detachedLaunch{
		PID:        pid,
		StatusPath: statusPath,
		RunnerPath: runnerPath,
	}, nil
}

func writeDetachedRunner(path string, shell runtimeexecutor.Shell, command, cwd, logPath, statusPath string) error {
	if runtime.GOOS == "windows" {
		return writeWindowsDetachedRunner(path, shell, command, cwd, logPath, statusPath)
	}
	return writeUnixDetachedRunner(path, shell, command, cwd, logPath, statusPath)
}

func writeUnixDetachedRunner(path string, shell runtimeexecutor.Shell, command, cwd, logPath, statusPath string) error {
	lines := []string{"#!/bin/sh", "set +e"}
	if strings.TrimSpace(cwd) != "" {
		lines = append(lines,
			fmt.Sprintf("if ! cd %s; then", shellQuote(cwd)),
			fmt.Sprintf("  printf 'failed to change directory: %%s\\n' %s >> %s 2>&1", shellQuote(cwd), shellQuote(logPath)),
			fmt.Sprintf("  printf \"%%s\" \"1\" > %s", shellQuote(statusPath)),
			"  exit 0",
			"fi",
		)
	}
	commandLine := buildShellCommandLine(shell.DeriveExecArgs(command, false))
	lines = append(lines,
		fmt.Sprintf("%s >> %s 2>&1", commandLine, shellQuote(logPath)),
		`code=$?`,
		fmt.Sprintf("printf \"%%s\" \"$code\" > %s", shellQuote(statusPath)),
	)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o755); err != nil {
		return err
	}
	return os.Chmod(path, 0o755)
}

func writeWindowsDetachedRunner(path string, shell runtimeexecutor.Shell, command, cwd, logPath, statusPath string) error {
	content := buildWindowsDetachedRunnerContent(shell, command, cwd, logPath, statusPath)
	return os.WriteFile(path, []byte(content), 0o644)
}

func startDetachedRunner(path string) (int, error) {
	if runtime.GOOS == "windows" {
		launcher := windowsPowerShellHost()
		script := fmt.Sprintf("$p = Start-Process -FilePath '%s' -ArgumentList @('-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', '%s') -WindowStyle Hidden -PassThru; [Console]::Out.Write($p.Id)", escapePowerShellSingleQuotes(launcher), escapePowerShellSingleQuotes(path))
		out, err := exec.Command(launcher, "-NoProfile", "-NonInteractive", "-Command", script).Output()
		if err != nil {
			return 0, err
		}
		return strconv.Atoi(strings.TrimSpace(string(out)))
	}
	script := fmt.Sprintf("if command -v setsid >/dev/null 2>&1; then setsid /bin/sh %s >/dev/null 2>&1 < /dev/null & echo $!; else nohup /bin/sh %s >/dev/null 2>&1 < /dev/null & echo $!; fi", shellQuote(path), shellQuote(path))
	out, err := exec.Command("/bin/sh", "-c", script).Output()
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

func readDetachedExitCode(path string) (int, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return 0, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	return code, true
}

func detachedPID(metadata map[string]interface{}) (int, bool) {
	return intMetadataValue(metadata, backgroundMetaPID)
}

func isProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", fmt.Sprintf("if (Get-Process -Id %d -ErrorAction SilentlyContinue) { exit 0 } else { exit 1 }", pid))
		return cmd.Run() == nil
	}
	return exec.Command("/bin/sh", "-c", fmt.Sprintf("kill -0 %d 2>/dev/null", pid)).Run() == nil
}

func terminateProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	if runtime.GOOS == "windows" {
		return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
	}
	script := fmt.Sprintf("(kill -TERM -%d 2>/dev/null || kill -TERM %d 2>/dev/null || true); sleep 0.2; (kill -KILL -%d 2>/dev/null || kill -KILL %d 2>/dev/null || true)", pid, pid, pid, pid)
	return exec.Command("/bin/sh", "-c", script).Run()
}

func shellQuote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}

func escapeBatchPath(text string) string {
	return strings.ReplaceAll(text, `"`, `""`)
}

func escapePowerShellSingleQuotes(text string) string {
	return strings.ReplaceAll(text, `'`, `''`)
}

func buildShellCommandLine(args []string) string {
	if len(args) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func buildWindowsDetachedRunnerContent(shell runtimeexecutor.Shell, command, cwd, logPath, statusPath string) string {
	commandArgs := shell.DeriveExecArgs(command, false)
	quotedArgs := make([]string, 0, len(commandArgs)-1)
	for _, arg := range commandArgs[1:] {
		quotedArgs = append(quotedArgs, powershellQuote(arg))
	}

	lines := []string{"$ErrorActionPreference = 'Stop'"}
	lines = append(lines, fmt.Sprintf("$shellPath = %s", powershellQuote(shell.Path)))
	lines = append(lines, fmt.Sprintf("$shellArgs = @(%s)", strings.Join(quotedArgs, ", ")))
	lines = append(lines,
		fmt.Sprintf("$writer = [System.IO.StreamWriter]::new(%s, $true, (New-Object System.Text.UTF8Encoding $false))", powershellQuote(logPath)),
		"$scriptExitCode = 0",
		"try {",
	)
	if strings.TrimSpace(cwd) != "" {
		lines = append(lines, fmt.Sprintf("  Set-Location -LiteralPath %s", powershellQuote(cwd)))
	}
	lines = append(lines,
		"  & $shellPath @shellArgs 2>&1 | ForEach-Object { $writer.WriteLine($_) }",
		"  if ($null -ne $LASTEXITCODE) { $scriptExitCode = $LASTEXITCODE }",
		"} catch {",
		"  $writer.WriteLine($_.ToString())",
		"  $scriptExitCode = 1",
		"} finally {",
		"  $writer.Dispose()",
		"}",
		fmt.Sprintf("[System.IO.File]::WriteAllText(%s, [string]$scriptExitCode, (New-Object System.Text.UTF8Encoding $false))", powershellQuote(statusPath)),
		"exit 0",
	)
	return strings.Join(lines, "\r\n") + "\r\n"
}

func powershellQuote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", "''") + "'"
}

func windowsPowerShellHost() string {
	if path, err := exec.LookPath("pwsh"); err == nil {
		return path
	}
	if path, err := exec.LookPath("powershell"); err == nil {
		return path
	}
	return "powershell"
}
