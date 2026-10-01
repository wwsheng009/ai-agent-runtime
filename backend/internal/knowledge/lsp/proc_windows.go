//go:build windows

package lsp

import (
	"os"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
)

// processAlive 报告 Windows 进程是否存活（锁新鲜度判据）。
//
// 与 owner 仲裁同口径：先验证旧 PID 是否真的存在，再决定是否接管。
func processAlive(proc *os.Process) bool {
	if proc == nil {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(proc.Pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

// probeProcessMemory 返回进程工作集字节数（Windows：tasklist 解析）。
//
// 不引入新的 Win32 绑定：tasklist 在所有受支持的 Windows 上可用，
// 且调用频率低（Ensure 与超限检查），不构成热路径。
func probeProcessMemory(pid int) (int64, error) {
	if pid <= 0 {
		return 0, nil
	}
	out, err := runTasklist(pid)
	if err != nil {
		return 0, err
	}
	return parseTasklistMemory(out), nil
}

func runTasklist(pid int) (string, error) {
	return commandOutput("tasklist", "/FI", "PID eq "+itoa(pid), "/FO", "CSV", "/NH")
}

func commandOutput(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}

func itoa(v int) string { return strconv.Itoa(v) }

func parseInt64(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }

// parseTasklistMemory 解析 tasklist CSV 输出中的内存列（形如 "12,345 K"）。
func parseTasklistMemory(out string) int64 {
	fields := strings.Split(strings.TrimSpace(out), ",")
	if len(fields) < 5 {
		return 0
	}
	raw := strings.Trim(strings.TrimSpace(fields[len(fields)-1]), "\"")
	raw = strings.TrimSuffix(raw, " K")
	raw = strings.ReplaceAll(raw, ",", "")
	kb, err := parseInt64(raw)
	if err != nil {
		return 0
	}
	return kb * 1024
}
