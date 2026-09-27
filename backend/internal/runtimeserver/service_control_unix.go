//go:build !windows

package runtimeserver

import (
	"fmt"
	"os"
	"os/exec"
)

// processRunningOS 在类 Unix 平台用 kill -0 探测进程是否存在（保持旧行为）。
func processRunningOS(pid int) bool {
	if pid <= 0 {
		return false
	}
	return exec.Command("/bin/sh", "-c", fmt.Sprintf("kill -0 %d 2>/dev/null", pid)).Run() == nil
}

// processImagePathOS 通过 /proc/<pid>/exe 返回进程可执行文件路径。
func processImagePathOS(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid pid %d", pid)
	}
	target, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return "", err
	}
	return target, nil
}
