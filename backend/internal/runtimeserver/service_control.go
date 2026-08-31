package runtimeserver

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
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
		return processRunningWindows(uint32(pid))
	}
	return exec.Command("/bin/sh", "-c", fmt.Sprintf("kill -0 %d 2>/dev/null", pid)).Run() == nil
}

// ResolveInstancePID 交叉验证 PID 文件记录与监听端口，返回实际活跃的服务 PID。
// 只按 PID 判活不可靠：Windows 下 PID 退出后可能被系统快速复用给无关进程，
// 导致 stop/status/start 误判；而受管服务必然绑定监听端口，端口是更准确的
// 身份锚点。规则：
//   - listenAddr 非空且端口有监听者：以端口监听者为准（记录 PID 可能已陈旧，
//     返回真实 PID）；此时记录 PID 是否仍存活都无关紧要。
//   - listenAddr 非空且端口无监听者（探测成功）：服务必然已退出——记录 PID
//     哪怕还"存在"，也只是被复用的无关进程，按未运行处理。
//   - 端口探测不可用（如 Linux 无 lsof）：退回进程存在性判断，保持旧行为。
//   - listenAddr 为空（旧格式 PID 文件或手动调用）：退回进程存在性判断。
// 注意：本函数用于状态类判断（status/start 冲突），不作进程身份核验；
// 终止进程请用 ResolveStopTarget（含身份确认，拒绝误杀陌生进程）。
func ResolveInstancePID(recordedPID int, listenAddr string) (targetPID int, alive bool) {
	if recordedPID <= 0 {
		return 0, false
	}
	listenAddr = strings.TrimSpace(listenAddr)
	if listenAddr != "" {
		listeningPID, err := FindListeningPID(listenAddr)
		if err != nil {
			// 端口探测不可用：退回进程存在性判断（与旧行为一致，宁可保守）。
			return resolveInstancePIDByProcess(recordedPID)
		}
		if listeningPID > 0 {
			return listeningPID, true
		}
		// 端口探测成功且无监听者：服务必然已退出。
		return 0, false
	}
	return resolveInstancePIDByProcess(recordedPID)
}

func resolveInstancePIDByProcess(recordedPID int) (targetPID int, alive bool) {
	if ProcessRunning(recordedPID) {
		return recordedPID, true
	}
	return 0, false
}

// ResolveStopTarget 解析 stop 应终止的目标进程，带身份确认：
//   - 端口监听者存在且与记录 PID 一致：记录可信（PID 文件由服务自己写入），
//     以记录 PID 为目标。
//   - 端口监听者存在但记录 PID 不一致（文件陈旧/重启错位）：仅当监听者的
//     可执行文件看起来是 runtime-server 时才以监听者为目标；否则视为陌生
//     进程接管了端口，拒绝自动终止并返回 err（调用方不得清理 PID 文件、
//     不得强杀）。
//   - 端口探测不可用（如 Linux 无 lsof）：退回记录 PID 的进程存在性判断。
//   - 端口无监听者：服务已退出，返回 alive=false 且 err=nil（可清理 PID 文件）。
func ResolveStopTarget(recordedPID int, listenAddr string) (targetPID int, alive bool, err error) {
	if recordedPID <= 0 {
		return 0, false, nil
	}
	listenAddr = strings.TrimSpace(listenAddr)
	if listenAddr == "" {
		targetPID, alive := resolveInstancePIDByProcess(recordedPID)
		return targetPID, alive, nil
	}
	listeningPID, probeErr := FindListeningPID(listenAddr)
	if probeErr != nil {
		targetPID, alive := resolveInstancePIDByProcess(recordedPID)
		return targetPID, alive, nil
	}
	if listeningPID == 0 {
		// 端口探测成功且无监听者：服务必然已退出。
		return 0, false, nil
	}
	if listeningPID == recordedPID {
		return listeningPID, true, nil
	}
	if looksLikeRuntimeServerProcess(listeningPID) {
		// 记录陈旧但监听者确实是 runtime-server（服务重启过）：以监听者为准。
		return listeningPID, true, nil
	}
	return 0, false, fmt.Errorf(
		"端口 %s 的监听进程 pid=%d 不是 runtime-server（PID 文件记录 pid=%d 已失效），拒绝自动终止；请人工确认后用 --pid %d 显式指定",
		listenAddr, listeningPID, recordedPID, listeningPID)
}

// processRunningWindows 用 Windows API OpenProcess 探测进程是否存在，
// 不依赖外部 PowerShell。此前用 powershell.exe -Command "if (Get-Process ...)"
// 检查：在无 PowerShell（或被裁剪/禁用）的 Win7 工控机上恒失败，导致
// start 命令在 serve 进程已写好 PID 文件的正常启动情况下空转 30s，
// 误报"未写入 PID 文件"。
func processRunningWindows(pid uint32) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		// Vista+ 语义：进程不存在返回 ERROR_INVALID_PARAMETER；
		// 其他错误（如 ERROR_ACCESS_DENIED）表示进程存在但权限不足，
		// 此时按"存在"处理，避免误判。
		return err != windows.ERROR_INVALID_PARAMETER
	}
	_ = windows.CloseHandle(h)
	return true
}

func StartDetachedProcess(executable string, args []string, env []string) (*exec.Cmd, error) {
	return StartDetachedProcessWithOutput(executable, args, env, "", "")
}

// StartDetachedProcessWithOutput 与 StartDetachedProcess 相同，但允许把子进程
// stdout/stderr 重定向到文件（stdoutPath/stderrPath）。任一参数为空时对应
// 流仍丢弃到系统空设备。用于 start 模式下未配置日志文件路径时的失败诊断
// fallback：子进程启动失败的真实错误会落入捕获文件，父进程可以 tail 展示。
func StartDetachedProcessWithOutput(executable string, args []string, env []string, stdoutPath, stderrPath string) (*exec.Cmd, error) {
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
	if strings.TrimSpace(stdoutPath) == "" {
		cmd.Stdout = devNull
	} else {
		output, openErr := os.OpenFile(stdoutPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if openErr != nil {
			return nil, fmt.Errorf("open stdout capture file: %w", openErr)
		}
		cmd.Stdout = output
	}
	if strings.TrimSpace(stderrPath) == "" {
		cmd.Stderr = devNull
	} else {
		output, openErr := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if openErr != nil {
			return nil, fmt.Errorf("open stderr capture file: %w", openErr)
		}
		cmd.Stderr = output
	}
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

// TerminateProcess 终止指定服务进程。listenAddr 为该服务的监听地址（来自
// PID 文件）；非空时用"端口仍被该 PID 监听"作为终止完成的判据，对 PID 被
// 系统复用的情况免疫——服务退出后端口立即释放，即使 PID 已被其他进程占用
// 也不会误杀无辜进程、更不会误报"仍在运行"。
func TerminateProcess(pid int, listenAddr string, timeout time.Duration) error {
	if pid <= 0 {
		return fmt.Errorf("pid must be greater than zero")
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	if runtime.GOOS == "windows" {
		host := windowsPowerShellHost()
		stopScript := fmt.Sprintf("Stop-Process -Id %d -Force -ErrorAction SilentlyContinue", pid)
		if err := exec.Command(host, "-NoProfile", "-NonInteractive", "-Command", stopScript).Run(); err != nil && !terminationTargetAlive(pid, listenAddr) {
			return nil
		}
		if waitForTermination(pid, listenAddr, timeout) {
			return nil
		}
		if err := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F").Run(); err != nil && !terminationTargetAlive(pid, listenAddr) {
			return nil
		}
		if waitForTermination(pid, listenAddr, 2*time.Second) {
			return nil
		}
		return fmt.Errorf("process %d still running after forced termination attempt", pid)
	}

	_ = exec.Command("/bin/sh", "-c", fmt.Sprintf("kill -TERM %d 2>/dev/null || true", pid)).Run()
	if waitForTermination(pid, listenAddr, timeout) {
		return nil
	}
	_ = exec.Command("/bin/sh", "-c", fmt.Sprintf("kill -KILL %d 2>/dev/null || true", pid)).Run()
	if waitForTermination(pid, listenAddr, 2*time.Second) {
		return nil
	}
	return fmt.Errorf("process %d still running after SIGKILL", pid)
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


// looksLikeRuntimeServerProcess 判断进程的可执行文件名是否像是 runtime-server
// 构建（覆盖 runtime-server.exe、runtime-server-managed.exe、runtime-server-win7-*.exe 等）。
// 用于 stop 的身份核验：端口被非服务进程接管时拒绝自动终止。
func looksLikeRuntimeServerProcess(pid int) bool {
	exePath, err := processImagePath(pid)
	if err != nil {
		return false
	}
	base := strings.ToLower(filepath.Base(exePath))
	return strings.Contains(base, "runtime-server")
}

// processImagePath 返回进程可执行文件路径。
// Windows: QueryFullProcessImageNameW（Vista+，Win7 可用，PROCESS_QUERY_LIMITED_INFORMATION 权限）。
// Unix: /proc/<pid>/exe。
func processImagePath(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid pid %d", pid)
	}
	if runtime.GOOS == "windows" {
		return processImagePathWindows(uint32(pid))
	}
	target, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return "", err
	}
	return target, nil
}

func processImagePathWindows(pid uint32) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	var buf [1024]uint16
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:size]), nil
}

// terminationTargetAlive 判定"目标服务进程是否还活着"。
// 有监听地址时以"端口仍被该 PID 监听"为准（对 PID 复用免疫：服务退出后
// 端口立即释放，被复用的其他进程不满足条件，视为已终止）；
// 端口探测不可用或没有监听地址时退回进程存在性判断。
func terminationTargetAlive(pid int, listenAddr string) bool {
	if strings.TrimSpace(listenAddr) != "" {
		listeningPID, err := FindListeningPID(listenAddr)
		if err != nil {
			// 探测不可用（如 Linux 无 lsof）：退回进程存在性判断。
			return ProcessRunning(pid)
		}
		return listeningPID == pid
	}
	return ProcessRunning(pid)
}

func waitForTermination(pid int, listenAddr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !terminationTargetAlive(pid, listenAddr) {
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return !terminationTargetAlive(pid, listenAddr)
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
	// lsof 不可用（或两种参数形式都执行失败）：与"端口确实无监听"区分开，
	// 调用方据此退回进程存在性判断，避免误判"未运行"。
	return 0, errors.New("lsof unavailable to probe listening port")
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
