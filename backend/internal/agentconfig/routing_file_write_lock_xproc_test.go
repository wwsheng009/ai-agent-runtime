package agentconfig

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 跨进程文件写锁（§3.4 M11 / §12 R4 闭环）用例。
//
// 进程内串行化由 routing_file_write_lock_test.go 覆盖；本文件用 Go 标准
// helper-process 模式（exec.Command(os.Args[0], "-test.run=...") + 环境变量传参）证明
// **跨进程**互斥：进程 A 持锁期间，进程 B 的 LockRoutingFileWrite 必须阻塞不返回；
// A 释放后 B 才拿到锁。另覆盖降级容错（锁文件目录不存在 → 静默退化为仅进程内锁）
// 与「锁落在旁路文件、不锁目标文件」的落盘事实。

const (
	routingLockHelperEnvVar    = "AICLI_ROUTING_LOCK_HELPER"
	routingLockHelperTargetVar = "AICLI_ROUTING_LOCK_TARGET"
	routingLockHelperModeVar   = "AICLI_ROUTING_LOCK_MODE"
	routingLockHelperModeHold  = "hold"
	routingLockHelperModeWait  = "wait"
	routingLockHelperTestName  = "TestHelperProcessRoutingFileLock"

	// helper 进程启动/退出的宽限（进程创建 + race 插桩都需要时间）。
	routingLockHelperStartWait = 60 * time.Second
	routingLockHelperExitWait  = 60 * time.Second
	// 持锁期间等待进程必须保持沉默的观察窗口：足以覆盖「立即返回」的实现。
	routingLockCrossBlockWindow = 750 * time.Millisecond
)

// routingFileOSLockPath 返回跨进程旁路锁文件路径（与生产实现同源：归一化路径 + 后缀）。
func routingFileOSLockPath(target string) string {
	return normalizeRoutingWriteLockKey(target) + routingFileOSLockSuffix
}

// TestHelperProcessRoutingFileLock 是跨进程用例的 helper 进程入口：只有带
// AICLI_ROUTING_LOCK_HELPER=1 时才是 helper，否则在父测试进程里是一个空测试。
//
// 协议（stdout 逐行）：
//   - mode=hold：取锁 → 打印 LOCKED → 等 stdin 关闭/换行（父进程的释放信号）→ 释放 →
//     打印 RELEASED → 退出；
//   - mode=wait：取锁（此处必须阻塞到 A 释放）→ 打印 ACQUIRED → 释放 → 退出。
func TestHelperProcessRoutingFileLock(t *testing.T) {
	if os.Getenv(routingLockHelperEnvVar) != "1" {
		return
	}
	target := os.Getenv(routingLockHelperTargetVar)
	mode := os.Getenv(routingLockHelperModeVar)
	out := bufio.NewWriter(os.Stdout)

	switch mode {
	case routingLockHelperModeHold:
		unlock := LockRoutingFileWrite(target)
		fmt.Fprintln(out, "LOCKED")
		_ = out.Flush()
		// 父进程关闭 stdin（或写入一行）即视为释放信号。
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		unlock()
		fmt.Fprintln(out, "RELEASED")
		_ = out.Flush()
	case routingLockHelperModeWait:
		unlock := LockRoutingFileWrite(target)
		fmt.Fprintln(out, "ACQUIRED")
		_ = out.Flush()
		unlock()
	default:
		fmt.Fprintf(out, "UNKNOWN-MODE:%s\n", mode)
		_ = out.Flush()
	}
	os.Exit(0)
}

// TestLockRoutingFileWriteBlocksAcrossProcesses 证明跨进程互斥成立。
func TestLockRoutingFileWriteBlocksAcrossProcesses(t *testing.T) {
	if !routingFileOSLockSupported {
		t.Skip("当前平台没有跨进程锁实现（按设计降级为仅进程内锁）")
	}
	target := filepath.Join(t.TempDir(), "chat-prefs.yaml")

	// 进程 A：持锁不放。
	holder := startRoutingLockHelper(t, target, routingLockHelperModeHold)
	holder.expectLine(t, "LOCKED", routingLockHelperStartWait)

	// 进程 B：A 持锁期间必须阻塞（不返回、不退出）。
	waiter := startRoutingLockHelper(t, target, routingLockHelperModeWait)
	waiter.requireSilent(t, routingLockCrossBlockWindow,
		"跨进程互斥失效：持锁进程尚未释放，等待进程已经拿到锁")

	// 释放 A（关 stdin → helper 读到 EOF → unlock）。
	require.NoError(t, holder.stdin.Close())
	holder.expectLine(t, "RELEASED", routingLockHelperExitWait)
	// A 释放后 B 必须拿到锁，而不是被永久卡住。
	waiter.expectLine(t, "ACQUIRED", routingLockHelperExitWait)

	require.NoError(t, holder.waitExit(t, routingLockHelperExitWait))
	require.NoError(t, waiter.waitExit(t, routingLockHelperExitWait))

	// 锁落在旁路文件上：它存在且为 0 字节；目标文件本身从未被创建（锁不在目标文件上）。
	lockInfo, err := os.Stat(routingFileOSLockPath(target))
	require.NoError(t, err, "跨进程旁路锁文件应存在")
	require.Equal(t, int64(0), lockInfo.Size(), "锁文件只做锁载体，不写入内容")
	_, err = os.Stat(target)
	require.True(t, os.IsNotExist(err), "不得在目标文件上落锁（目标文件此时不应存在）")
}

// TestLockRoutingFileWriteUsesSidecarLockFile 固定「锁旁路文件、不锁目标文件」这条设计，
// 以及「释放后不删除锁文件」的残留策略（删除会引入 unlink 竞态）。
func TestLockRoutingFileWriteUsesSidecarLockFile(t *testing.T) {
	if !routingFileOSLockSupported {
		t.Skip("当前平台没有跨进程锁实现（按设计降级为仅进程内锁）")
	}
	target := filepath.Join(t.TempDir(), "chat-prefs.yaml")
	lockPath := routingFileOSLockPath(target)

	unlock := LockRoutingFileWrite(target)
	info, err := os.Stat(lockPath)
	require.NoError(t, err, "应创建旁路锁文件 %s", lockPath)
	require.Equal(t, int64(0), info.Size(), "锁文件只做锁载体，不写入内容")
	_, err = os.Stat(target)
	require.True(t, os.IsNotExist(err), "锁不得落在目标文件上（也不得创建它）")

	unlock()
	unlock() // 释放幂等

	_, err = os.Stat(lockPath)
	require.NoError(t, err, "释放后锁文件必须保留（删除会引入 unlink 竞态）")

	// 复用同一个锁文件再次加锁/释放。
	LockRoutingFileWrite(target)()
}

// TestLockRoutingFileWriteDegradesWhenLockDirectoryMissing 覆盖降级容错：锁文件所在目录
// 不存在（创建锁文件必然失败）时，LockRoutingFileWrite 必须静默降级为仅进程内锁——
// 返回可用的释放函数、不 panic、释放幂等可重复调用，且不凭空创建目录/文件；
// 降级只影响跨进程，进程内串行化必须照旧生效。
func TestLockRoutingFileWriteDegradesWhenLockDirectoryMissing(t *testing.T) {
	base := t.TempDir()
	missingDir := filepath.Join(base, "missing-dir")
	target := filepath.Join(missingDir, "chat-prefs.yaml")

	unlock := LockRoutingFileWrite(target)
	require.NotNil(t, unlock)
	unlock()
	unlock() // 幂等

	// 降级后仍可重复加锁/释放。
	LockRoutingFileWrite(target)()

	_, err := os.Stat(missingDir)
	require.True(t, os.IsNotExist(err), "降级路径不得创建目录")
	_, err = os.Stat(routingFileOSLockPath(target))
	require.True(t, os.IsNotExist(err), "降级路径不得创建锁文件")

	// 降级只丢跨进程互斥；进程内串行化必须保持。
	first := LockRoutingFileWrite(target)
	acquired := make(chan struct{})
	go func() {
		release := LockRoutingFileWrite(target)
		close(acquired)
		release()
	}()
	select {
	case <-acquired:
		first()
		t.Fatal("降级后进程内串行化失效：第二个写者未等待锁释放")
	case <-time.After(200 * time.Millisecond):
	}
	first()
	select {
	case <-acquired:
	case <-time.After(10 * time.Second):
		t.Fatal("降级后释放锁，第二个写者仍未获得锁")
	}
}

// ---------------------------------------------------------------------------
// helper 进程管理

type routingLockHelperProcess struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	lines   chan string
	stderr  *lockedBuffer
	done    chan struct{}
	waitErr error
}

// startRoutingLockHelper 以 helper-process 模式启动一个独立进程，并注册清理（杀进程）。
func startRoutingLockHelper(t *testing.T, target, mode string) *routingLockHelperProcess {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+routingLockHelperTestName+"$", "-test.timeout=120s")
	cmd.Env = append(os.Environ(),
		routingLockHelperEnvVar+"=1",
		routingLockHelperTargetVar+"="+target,
		routingLockHelperModeVar+"="+mode,
	)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err, "helper stdin pipe")
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err, "helper stdout pipe")
	stderr := new(lockedBuffer)
	cmd.Stderr = stderr
	require.NoError(t, cmd.Start(), "启动 helper 进程")

	process := &routingLockHelperProcess{
		cmd:    cmd,
		stdin:  stdin,
		lines:  make(chan string, 8),
		stderr: stderr,
		done:   make(chan struct{}),
	}
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			process.lines <- scanner.Text()
		}
		close(process.lines)
	}()
	go func() {
		process.waitErr = cmd.Wait()
		close(process.done)
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		select {
		case <-process.done:
		case <-time.After(10 * time.Second):
		}
	})
	return process
}

// expectLine 等待 helper 打印指定行；helper 提前退出或超时都判失败。
func (p *routingLockHelperProcess) expectLine(t *testing.T, want string, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				t.Fatalf("helper 进程未输出 %q 就退出（stderr=%s）", want, p.stderr.String())
			}
			if strings.TrimSpace(line) == want {
				return
			}
		case <-timer.C:
			t.Fatalf("等待 helper 进程输出 %q 超时（stderr=%s）", want, p.stderr.String())
		}
	}
}

// requireSilent 断言在给定窗口内 helper 没有输出任何行、也没有退出。
func (p *routingLockHelperProcess) requireSilent(t *testing.T, window time.Duration, failureMessage string) {
	t.Helper()
	timer := time.NewTimer(window)
	defer timer.Stop()
	select {
	case line, ok := <-p.lines:
		if !ok {
			t.Fatalf("%s（helper 进程已退出，stderr=%s）", failureMessage, p.stderr.String())
		}
		t.Fatalf("%s（实际输出 %q）", failureMessage, line)
	case <-timer.C:
	}
}

// waitExit 等待 helper 进程退出并返回其退出错误。
func (p *routingLockHelperProcess) waitExit(t *testing.T, timeout time.Duration) error {
	t.Helper()
	select {
	case <-p.done:
		return p.waitErr
	case <-time.After(timeout):
		t.Fatalf("helper 进程未在 %s 内退出（stderr=%s）", timeout, p.stderr.String())
		return nil
	}
}

// lockedBuffer 是并发安全的 stderr 缓冲：exec 在自己的 goroutine 里写，测试线程随时读
// （读只发生在失败路径），加锁避免 race detector 报假阳性。
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
