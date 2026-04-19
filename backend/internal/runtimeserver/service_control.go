package runtimeserver

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const DefaultPIDFile = "./logs/runtime-server.pid"

type InstanceInfo struct {
	PID        int       `json:"pid"`
	ListenAddr string    `json:"listen_addr,omitempty"`
	ConfigPath string    `json:"config_path,omitempty"`
	Cwd        string    `json:"cwd,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
}

func ResolvePIDFilePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		path = DefaultPIDFile
	}
	cleaned := filepath.Clean(path)
	if absolute, err := filepath.Abs(cleaned); err == nil {
		return absolute
	}
	return cleaned
}

func ReadInstanceInfo(path string) (*InstanceInfo, error) {
	path = ResolvePIDFilePath(path)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return nil, fmt.Errorf("pid file is empty: %s", path)
	}

	var info InstanceInfo
	if err := json.Unmarshal(raw, &info); err == nil && info.PID > 0 {
		return &info, nil
	}

	pid, err := strconv.Atoi(string(raw))
	if err != nil || pid <= 0 {
		return nil, fmt.Errorf("invalid pid file content: %s", path)
	}
	return &InstanceInfo{PID: pid}, nil
}

func WriteInstanceInfo(path string, info InstanceInfo) error {
	if info.PID <= 0 {
		return fmt.Errorf("pid must be greater than zero")
	}
	path = ResolvePIDFilePath(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create pid directory: %w", err)
	}
	payload, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal pid file: %w", err)
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}
	return nil
}

func RemoveInstanceInfoIfPID(path string, pid int) error {
	if pid <= 0 {
		return nil
	}
	path = ResolvePIDFilePath(path)
	info, err := ReadInstanceInfo(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.PID != pid {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func ProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", fmt.Sprintf("if (Get-Process -Id %d -ErrorAction SilentlyContinue) { exit 0 } else { exit 1 }", pid))
		return cmd.Run() == nil
	}
	return exec.Command("/bin/sh", "-c", fmt.Sprintf("kill -0 %d 2>/dev/null", pid)).Run() == nil
}

func StartDetachedProcess(executable string, args []string, env []string) (*exec.Cmd, error) {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		return nil, fmt.Errorf("executable is required")
	}
	cmd := exec.Command(executable, args...)
	applyDetachedProcessAttrs(cmd)
	if len(env) > 0 {
		cmd.Env = env
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open null device: %w", err)
	}
	defer devNull.Close()

	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start detached process: %w", err)
	}
	return cmd, nil
}

func PrepareStartCommand(executable, cwd string, args []string) (string, []string, error) {
	executable = strings.TrimSpace(executable)
	cwd = strings.TrimSpace(cwd)
	if shouldUseGoRunLauncher(executable, cwd) {
		managedBinary, err := buildManagedRuntimeServerBinary(cwd)
		if err != nil {
			return "", nil, err
		}
		return managedBinary, args, nil
	}
	return executable, args, nil
}

func TerminateProcess(pid int, timeout time.Duration) error {
	if pid <= 0 {
		return fmt.Errorf("pid must be greater than zero")
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	if runtime.GOOS == "windows" {
		stopScript := fmt.Sprintf("Stop-Process -Id %d -Force -ErrorAction SilentlyContinue", pid)
		if err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", stopScript).Run(); err != nil && !ProcessRunning(pid) {
			return nil
		}
		if waitForProcessExit(pid, timeout) {
			return nil
		}
		if err := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F").Run(); err != nil && !ProcessRunning(pid) {
			return nil
		}
		if waitForProcessExit(pid, 2*time.Second) {
			return nil
		}
		return fmt.Errorf("process %d still running after forced termination attempt", pid)
	}

	_ = exec.Command("/bin/sh", "-c", fmt.Sprintf("kill -TERM %d 2>/dev/null || true", pid)).Run()
	if waitForProcessExit(pid, timeout) {
		return nil
	}
	_ = exec.Command("/bin/sh", "-c", fmt.Sprintf("kill -KILL %d 2>/dev/null || true", pid)).Run()
	if waitForProcessExit(pid, 2*time.Second) {
		return nil
	}
	return fmt.Errorf("process %d still running after SIGKILL", pid)
}

func waitForProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !ProcessRunning(pid) {
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return !ProcessRunning(pid)
}

func shouldUseGoRunLauncher(executable, cwd string) bool {
	if executable == "" || cwd == "" {
		return false
	}
	mainFile := filepath.Join(cwd, "cmd", "runtime-server", "main.go")
	if !pathExists(mainFile) {
		return false
	}
	tempDir, err := filepath.Abs(os.TempDir())
	if err != nil {
		return false
	}
	executableAbs, err := filepath.Abs(executable)
	if err != nil {
		return false
	}
	tempDir = strings.ToLower(filepath.Clean(tempDir))
	executableAbs = strings.ToLower(filepath.Clean(executableAbs))
	return strings.HasPrefix(executableAbs, tempDir) && strings.Contains(executableAbs, "go-build")
}

func buildManagedRuntimeServerBinary(cwd string) (string, error) {
	outputPath := filepath.Join(cwd, "logs", managedRuntimeServerBinaryName())
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return "", fmt.Errorf("create managed runtime-server directory: %w", err)
	}

	cmd := exec.Command("go", "build", "-o", outputPath, "./cmd/runtime-server")
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	if output, err := cmd.CombinedOutput(); err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed == "" {
			return "", fmt.Errorf("build managed runtime-server binary: %w", err)
		}
		return "", fmt.Errorf("build managed runtime-server binary: %w: %s", err, trimmed)
	}
	return outputPath, nil
}

func managedRuntimeServerBinaryName() string {
	if runtime.GOOS == "windows" {
		return "runtime-server-managed.exe"
	}
	return "runtime-server-managed"
}

func FindListeningPID(listenAddr string) (int, error) {
	listenAddr = strings.TrimSpace(listenAddr)
	if listenAddr == "" {
		return 0, nil
	}

	_, rawPort, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return 0, err
	}
	port, err := strconv.Atoi(strings.TrimSpace(rawPort))
	if err != nil || port <= 0 {
		return 0, fmt.Errorf("invalid listen port: %s", rawPort)
	}
	return findListeningPIDByPort(port)
}

func findListeningPIDByPort(port int) (int, error) {
	if port <= 0 {
		return 0, nil
	}
	if runtime.GOOS == "windows" {
		out, err := exec.Command("netstat", "-ano", "-p", "tcp").Output()
		if err != nil {
			return 0, err
		}
		return parseWindowsNetstatListeningPID(out, port)
	}

	for _, args := range [][]string{
		{"-nP", "-iTCP:" + strconv.Itoa(port), "-sTCP:LISTEN", "-t"},
		{"-ti", "tcp:" + strconv.Itoa(port)},
	} {
		out, err := exec.Command("lsof", args...).Output()
		if err == nil {
			text := strings.TrimSpace(string(out))
			if text == "" {
				return 0, nil
			}
			lines := strings.Split(text, "\n")
			return strconv.Atoi(strings.TrimSpace(lines[0]))
		}
	}
	return 0, nil
}

func parseWindowsNetstatListeningPID(output []byte, port int) (int, error) {
	suffix := ":" + strconv.Itoa(port)
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		if !strings.EqualFold(fields[0], "TCP") {
			continue
		}
		if !strings.EqualFold(fields[3], "LISTENING") {
			continue
		}
		if !strings.HasSuffix(fields[1], suffix) {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(fields[4]))
		if err != nil {
			return 0, err
		}
		return pid, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, nil
}
