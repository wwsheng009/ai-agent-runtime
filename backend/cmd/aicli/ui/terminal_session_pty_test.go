//go:build linux

package ui

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	renderengine "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
)

// P2-4b 真机 TTY 证据（T20~T26）：副屏租约必须作用在内核 pty 上，而不是
// 内存缓冲替身。本文件通过 /dev/ptmx 分配真实终端对，让 TerminalSession
// 写 slave、测试从 master 读回物理字节，验证：
//  1. EnterAlternateScreen 发出 DEC 1049 进入序列并持有租约；
//  2. 租约独占——第二次进入被拒绝（Busy），跨租约写入被拒绝；
//  3. 副屏帧字节确实到达物理终端；
//  4. ExitAlternateScreen 发出 DEC 1049 退出序列、恢复光标并清空租约；
//  5. 退出后的副屏写入被拒绝（禁止绕过 modal 生命周期的裸 stdout）。

// ptyPair 是一个真实内核终端对：master 侧用于读回物理字节，slave 侧交给
// TerminalSession 作为 writer。
type ptyPair struct {
	master *os.File
	slave  *os.File
}

func openPTYPair(t *testing.T) *ptyPair {
	t.Helper()

	masterFD, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	if err := unix.IoctlSetPointerInt(masterFD, unix.TIOCSPTLCK, 0); err != nil {
		_ = unix.Close(masterFD)
		t.Skipf("pty unlock unavailable: %v", err)
	}
	index, err := unix.IoctlGetInt(masterFD, unix.TIOCGPTN)
	if err != nil {
		_ = unix.Close(masterFD)
		t.Skipf("pty index unavailable: %v", err)
	}
	slaveFD, err := unix.Open(fmt.Sprintf("/dev/pts/%d", index), unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = unix.Close(masterFD)
		t.Skipf("pty slave unavailable: %v", err)
	}
	// 固定 80x24 几何，保证后续几何探测/快照在真实 tty 上可复现。
	_ = unix.IoctlSetWinsize(masterFD, unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80})

	pair := &ptyPair{
		master: os.NewFile(uintptr(masterFD), "pty-master"),
		slave:  os.NewFile(uintptr(slaveFD), "pty-slave"),
	}
	t.Cleanup(func() {
		_ = pair.master.Close()
		_ = pair.slave.Close()
	})
	return pair
}

// ptyCapture 在后台把 master 侧读到的物理字节累积起来，供测试按需等待。
type ptyCapture struct {
	mu    sync.Mutex
	bytes strings.Builder
}

func startPTYCapture(master *os.File) *ptyCapture {
	capture := &ptyCapture{}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				capture.mu.Lock()
				capture.bytes.Write(buf[:n])
				capture.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	return capture
}

func (c *ptyCapture) snapshot() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes.String()
}

// waitFor 轮询物理字节直到出现 needle 或超时，返回当前全部捕获内容。
func (c *ptyCapture) waitFor(t *testing.T, needle string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		got := c.snapshot()
		if strings.Contains(got, needle) {
			return got
		}
		if time.Now().After(deadline) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// settle 给异步 writer 一个稳定窗口，然后返回最终捕获内容。
func (c *ptyCapture) settle(d time.Duration) string {
	time.Sleep(d)
	return c.snapshot()
}

func TestTerminalSessionPTYAlternateScreenLeaseLifecycle(t *testing.T) {
	pair := openPTYPair(t)
	capture := startPTYCapture(pair.master)
	session := NewTerminalSession(pair.slave)

	// 1. 进入副屏：真实 pty 上必须出现 DEC 1049 进入序列 + 清屏 + 隐藏光标。
	if err := session.EnterAlternateScreen(7); err != nil {
		t.Fatalf("EnterAlternateScreen(7) error = %v", err)
	}
	if got := session.AlternateScreenLeaseID(); got != 7 {
		t.Fatalf("AlternateScreenLeaseID() = %d, want 7", got)
	}
	entered := capture.waitFor(t, "\x1b[?1049h", 2*time.Second)
	for _, want := range []string{"\x1b[?1049h", "\x1b[?25l", "\x1b[2J", "\x1b[H"} {
		if !strings.Contains(entered, want) {
			t.Fatalf("physical enter bytes missing %q; captured=%q", want, entered)
		}
	}

	// 2. 租约独占：第二个租约无法抢占同一物理终端。
	if err := session.EnterAlternateScreen(8); !errors.Is(err, ErrTerminalAlternateScreenBusy) {
		t.Fatalf("second EnterAlternateScreen error = %v, want ErrTerminalAlternateScreenBusy", err)
	}
	if got := session.AlternateScreenLeaseID(); got != 7 {
		t.Fatalf("lease changed after rejected enter: got %d, want 7", got)
	}

	// 3. 副屏帧必须到达物理终端。
	if err := session.WriteAlternateScreen(7, "BUSY-SCREEN-FRAME"); err != nil {
		t.Fatalf("WriteAlternateScreen(7) error = %v", err)
	}
	framed := capture.waitFor(t, "BUSY-SCREEN-FRAME", 2*time.Second)
	if !strings.Contains(framed, "BUSY-SCREEN-FRAME") {
		t.Fatalf("alternate frame never reached pty; captured=%q", framed)
	}

	// 4. 跨租约写入被拒绝，且不产生任何物理字节（无裸 stdout 旁路）。
	if err := session.WriteAlternateScreen(8, "STALE-LEASE-BYTES"); !errors.Is(err, ErrTerminalAlternateScreenLease) {
		t.Fatalf("stale lease write error = %v, want ErrTerminalAlternateScreenLease", err)
	}
	if after := capture.settle(30 * time.Millisecond); strings.Contains(after, "STALE-LEASE-BYTES") {
		t.Fatalf("stale lease write leaked bytes to pty; captured=%q", after)
	}

	// 5. 退出副屏：恢复光标 + DEC 1049 退出序列 + 租约清零 + 投影置为 Unknown
	//（主屏必须走一次完整恢复重绘，不得沿用副屏缓存）。
	if err := session.ExitAlternateScreen(7); err != nil {
		t.Fatalf("ExitAlternateScreen(7) error = %v", err)
	}
	exited := capture.waitFor(t, "\x1b[?1049l", 2*time.Second)
	for _, want := range []string{"\x1b[?25h", "\x1b[?1049l"} {
		if !strings.Contains(exited, want) {
			t.Fatalf("physical exit bytes missing %q; captured=%q", want, exited)
		}
	}
	if got := session.AlternateScreenLeaseID(); got != 0 {
		t.Fatalf("AlternateScreenLeaseID() after exit = %d, want 0", got)
	}
	if got := session.ProjectionState().Validity; got != renderengine.ProjectionUnknown {
		t.Fatalf("projection validity after exit = %v, want ProjectionUnknown", got)
	}

	// 6. 退出后副屏写入被拒绝，防止 modal 生命周期结束后的 raw stdout 旁路。
	if err := session.WriteAlternateScreen(7, "AFTER-EXIT-BYTES"); !errors.Is(err, ErrTerminalAlternateScreenLease) {
		t.Fatalf("post-exit write error = %v, want ErrTerminalAlternateScreenLease", err)
	}
	if after := capture.settle(30 * time.Millisecond); strings.Contains(after, "AFTER-EXIT-BYTES") {
		t.Fatalf("post-exit write leaked bytes to pty; captured=%q", after)
	}
}
