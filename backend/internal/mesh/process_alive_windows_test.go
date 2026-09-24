//go:build windows

package mesh

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// 回归（E2E-DEBUG-03 M6/M9 抓到）：Windows 只要还有句柄指向进程对象，PID 就不会被
// 回收，OpenProcess 对**已经退出**的进程照样成功。父进程持有子进程句柄是常态
// （PowerShell Start-Process、os.Process、S9 的 spawn 路径、监督脚本），所以判活
// 必须复核退出码，否则被杀掉的 chat 进程会永久停在 `live`：视图不转 `stale`、
// gc 也回收不掉。
//
// 本用例故意不调用 Wait/Release：句柄一直握在测试手里，正是出问题的场景。
func TestProcessAliveTreatsExitedProcessWithOpenHandleAsDead(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Skipf("无法启动子进程：%v", err)
	}
	defer func() { _ = cmd.Process.Release() }()
	pid := cmd.Process.Pid

	// 等它真的退出（用独立句柄读退出码，不碰 cmd.Process 自己的句柄）。
	deadline := time.Now().Add(15 * time.Second)
	for {
		if code, ok := probeExitCode(pid); ok && code != stillActive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("子进程 pid %d 未在超时内退出", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if processAlive(pid) {
		t.Fatalf("已退出但句柄仍打开的 pid %d 被判成 live（必须复核 GetExitCodeProcess）", pid)
	}
}

// 反向对照：正在运行的进程必须判成 live，非法 pid 必须判死——防止「一律判死」式修法。
func TestProcessAliveSeesRunningProcessAndRejectsBadPID(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatalf("当前进程 pid %d 被判成 dead", os.Getpid())
	}
	for _, pid := range []int{0, -1} {
		if processAlive(pid) {
			t.Fatalf("非法 pid %d 必须判死", pid)
		}
	}
}

// probeExitCode 用独立句柄读退出码（不影响被测进程自己的句柄）。
func probeExitCode(pid int) (uint32, bool) {
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return 0, false
	}
	defer func() { _ = syscall.CloseHandle(handle) }()
	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil {
		return 0, false
	}
	return code, true
}
