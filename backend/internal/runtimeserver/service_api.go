package runtimeserver

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
)

type LocalRuntimeServiceControl struct {
	executable string
	cwd        string
	pidFile    string
	configPath string
	listenAddr string
}

func NewLocalRuntimeServiceControl(
	executable string,
	cwd string,
	pidFile string,
	configPath string,
	listenAddr string,
) *LocalRuntimeServiceControl {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		if resolved, err := os.Executable(); err == nil {
			executable = resolved
		}
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		if resolved, err := os.Getwd(); err == nil {
			cwd = resolved
		}
	}
	pidFile = ResolvePIDFilePath(pidFile)
	configPath = resolveAbsolutePath(configPath)
	listenAddr = strings.TrimSpace(listenAddr)

	return &LocalRuntimeServiceControl{
		executable: resolveAbsolutePath(executable),
		cwd:        resolveAbsolutePath(cwd),
		pidFile:    pidFile,
		configPath: configPath,
		listenAddr: listenAddr,
	}
}

func (s *LocalRuntimeServiceControl) Status() (*skillsapi.RuntimeServiceStatus, error) {
	currentPID := os.Getpid()
	status := &skillsapi.RuntimeServiceStatus{
		Running:          true,
		PID:              currentPID,
		PIDFile:          s.pidFile,
		ListenAddr:       s.listenAddr,
		ConfigPath:       s.configPath,
		Cwd:              s.cwd,
		Executable:       s.executable,
		RestartSupported: s.canRestart(),
		Note:             "当前接口由 runtime-server 进程自身返回，重启期间请求会短暂失败。",
	}

	if info, err := ReadInstanceInfo(s.pidFile); err == nil && info != nil {
		if info.PID == currentPID {
			applyInstanceInfoToStatus(status, info)
		} else if info.PID > 0 {
			status.Note = appendStatusNote(
				status.Note,
				fmt.Sprintf(
					"PID 文件记录的进程(pid=%d)与当前响应进程(pid=%d)不一致，状态以当前响应进程为准。",
					info.PID,
					currentPID,
				),
			)
		}
	}

	return status, nil
}

func applyInstanceInfoToStatus(status *skillsapi.RuntimeServiceStatus, info *InstanceInfo) {
	if status == nil || info == nil {
		return
	}
	if info.PID > 0 {
		status.PID = info.PID
		status.Running = true
	}
	if strings.TrimSpace(info.ListenAddr) != "" {
		status.ListenAddr = strings.TrimSpace(info.ListenAddr)
	}
	if strings.TrimSpace(info.ConfigPath) != "" {
		status.ConfigPath = resolveAbsolutePath(info.ConfigPath)
	}
	if strings.TrimSpace(info.Cwd) != "" {
		status.Cwd = resolveAbsolutePath(info.Cwd)
	}
	if !info.StartedAt.IsZero() {
		status.StartedAt = info.StartedAt.UTC().Format(time.RFC3339)
	}
}

func appendStatusNote(current string, extra string) string {
	current = strings.TrimSpace(current)
	extra = strings.TrimSpace(extra)
	if current == "" {
		return extra
	}
	if extra == "" {
		return current
	}
	return current + " " + extra
}

func (s *LocalRuntimeServiceControl) Restart() (*skillsapi.RuntimeServiceRestartResult, error) {
	if !s.canRestart() {
		return nil, fmt.Errorf("restart is not supported for the current runtime process")
	}

	command, args := s.restartHelperCommand()
	if command == "" {
		return nil, fmt.Errorf("restart helper command is empty")
	}
	if _, err := StartDetachedProcess(command, args, os.Environ()); err != nil {
		return nil, fmt.Errorf("start restart helper: %w", err)
	}

	return &skillsapi.RuntimeServiceRestartResult{
		Accepted:    true,
		Message:     "restart helper started; runtime-server will stop and relaunch in the background",
		RequestedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func (s *LocalRuntimeServiceControl) canRestart() bool {
	if strings.TrimSpace(s.executable) == "" || strings.TrimSpace(s.cwd) == "" {
		return false
	}
	if _, err := os.Stat(s.executable); err != nil {
		return false
	}
	return true
}

func (s *LocalRuntimeServiceControl) restartHelperCommand() (string, []string) {
	if runtime.GOOS == "windows" {
		return "powershell", []string{
			"-NoProfile",
			"-NonInteractive",
			"-Command",
			s.buildPowerShellRestartScript(),
		}
	}
	return "sh", []string{
		"-lc",
		s.buildShellRestartScript(),
	}
}

func (s *LocalRuntimeServiceControl) buildPowerShellRestartScript() string {
	startArgs := s.restartStartArgs()
	script := []string{
		"$ErrorActionPreference='SilentlyContinue'",
		fmt.Sprintf("Set-Location -LiteralPath %s", quotePowerShellLiteral(s.cwd)),
		"Start-Sleep -Milliseconds 700",
		fmt.Sprintf("& %s stop --pid-file %s --wait 20s | Out-Null",
			quotePowerShellLiteral(s.executable),
			quotePowerShellLiteral(s.pidFile),
		),
		"Start-Sleep -Milliseconds 500",
		fmt.Sprintf("& %s %s | Out-Null",
			quotePowerShellLiteral(s.executable),
			startArgs,
		),
	}
	return strings.Join(script, "; ")
}

func (s *LocalRuntimeServiceControl) buildShellRestartScript() string {
	return strings.Join([]string{
		fmt.Sprintf("cd %s", quoteShellLiteral(s.cwd)),
		"sleep 0.7",
		fmt.Sprintf("%s stop --pid-file %s --wait 20s >/dev/null 2>&1",
			quoteShellLiteral(s.executable),
			quoteShellLiteral(s.pidFile),
		),
		"sleep 0.5",
		fmt.Sprintf("%s %s >/dev/null 2>&1",
			quoteShellLiteral(s.executable),
			s.restartStartArgs(),
		),
	}, " && ")
}

func (s *LocalRuntimeServiceControl) restartStartArgs() string {
	args := []string{
		"start",
		"--config", quoteCommandArg(s.configPath),
		"--pid-file", quoteCommandArg(s.pidFile),
		"--wait", "30s",
	}
	if strings.TrimSpace(s.listenAddr) != "" {
		args = append(args, "--listen", quoteCommandArg(s.listenAddr))
	}
	return strings.Join(args, " ")
}

func quoteCommandArg(value string) string {
	if runtime.GOOS == "windows" {
		return quotePowerShellLiteral(value)
	}
	return quoteShellLiteral(value)
}

func quotePowerShellLiteral(value string) string {
	return "'" + strings.ReplaceAll(strings.TrimSpace(value), "'", "''") + "'"
}

func quoteShellLiteral(value string) string {
	trimmed := strings.TrimSpace(value)
	return "'" + strings.ReplaceAll(trimmed, "'", `'"'"'`) + "'"
}

var _ skillsapi.RuntimeServiceControlService = (*LocalRuntimeServiceControl)(nil)
