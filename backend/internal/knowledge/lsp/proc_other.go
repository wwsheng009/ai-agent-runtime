//go:build !windows

package lsp

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// processAlive 报告 POSIX 进程是否存活（signal 0 探活）。
func processAlive(proc *os.Process) bool {
	if proc == nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// probeProcessMemory 返回进程 RSS 字节数（Linux /proc；其他平台返回 0=未知）。
func probeProcessMemory(pid int) (int64, error) {
	if pid <= 0 {
		return 0, nil
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, nil // 非 Linux 或进程已退出：未知而非失败。
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, nil
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, nil
		}
		return kb * 1024, nil
	}
	return 0, nil
}
