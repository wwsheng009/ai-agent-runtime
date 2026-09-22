//go:build !win7compat

package transport

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// stdioStderrTailLimit 是子进程 stderr 环形缓冲的默认上限。
const stdioStderrTailLimit = 16 * 1024

// stdioStderrFlushWait 是「进程已退出但 stderr 还没落进缓冲」时的收尾等待上界。
// 只发生在失败路径上，避免进程刚退出、尾部尚未被拷贝 goroutine 写回时诊断缺失。
// 声明为变量便于测试缩短等待。
var stdioStderrFlushWait = 300 * time.Millisecond

// stderrTailBuffer 是并发安全的有界 stderr 缓冲：只保留最近 limit 字节，
// 既能给出真实报错行（例如 cmd.exe 的 "'C:\Program' is not recognized…"），
// 又不会因为话痨 MCP server 无限增长内存。
type stderrTailBuffer struct {
	mu      sync.Mutex
	buf     []byte
	limit   int
	total   int64
	dropped int64
}

func newStderrTailBuffer(limit int) *stderrTailBuffer {
	if limit <= 0 {
		limit = stdioStderrTailLimit
	}
	return &stderrTailBuffer{limit: limit}
}

// Write 永远成功：stderr 诊断不允许反过来影响子进程。
func (b *stderrTailBuffer) Write(p []byte) (int, error) {
	if b == nil {
		return len(p), nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total += int64(len(p))
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.limit {
		drop := len(b.buf) - b.limit
		b.dropped += int64(drop)
		b.buf = append(b.buf[:0], b.buf[drop:]...)
	}
	return len(p), nil
}

// Len 返回当前保留的字节数。
func (b *stderrTailBuffer) Len() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.buf)
}

// Snapshot 返回当前保留内容的副本与累计统计。
func (b *stderrTailBuffer) Snapshot() (data []byte, total, dropped int64) {
	if b == nil {
		return nil, 0, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf...), b.total, b.dropped
}

// StderrDiagnosticsProvider 由 stdio 传输实现：连接失败时补充子进程 stderr 尾部
// 与进程状态，供上层拼进用户可见的错误信息（计划 §5.3）。
type StderrDiagnosticsProvider interface {
	StderrDiagnostics() string
}

// StderrDisplayProvider 由 stdio 传输实现：面向交互式展示的子进程诊断
// （不含失败归因提示），供 `aicli mcp test-server --show-stderr` 等场景读取。
type StderrDisplayProvider interface {
	StderrTailForDisplay() string
}

// EnrichConnectError 尽力为连接失败补充 stdio 子进程诊断。
// 没有可用诊断（非 stdio、无缓冲、无法判断）时原样返回 err。
func EnrichConnectError(target interface{}, err error) error {
	if err == nil {
		return nil
	}
	diag := StderrDiagnosticsOf(target)
	if diag == "" {
		return err
	}
	return fmt.Errorf("%w\n%s", err, diag)
}

// StderrDiagnosticsOf 返回 target 携带的 stdio 诊断文本（无则空串）。
func StderrDiagnosticsOf(target interface{}) string {
	provider, ok := target.(StderrDiagnosticsProvider)
	if !ok || provider == nil {
		return ""
	}
	return strings.TrimSpace(provider.StderrDiagnostics())
}

// StderrDiagnostics 返回最近一次 stdio 装配的子进程 stderr 尾部诊断。
// 文本面向「连接失败」场景，包含失败归因提示（供 EnrichConnectError 使用）。
func (t *StdioTransport) StderrDiagnostics() string {
	return t.stderrDiagnostics(true)
}

// StderrTailForDisplay 返回面向交互式展示的 stdio 诊断（例如
// `aicli mcp test-server --show-stderr`）：保留进程状态与 stderr 尾部，
// 但不附加「连接失败」归因，连接成功时同样可读。
func (t *StdioTransport) StderrTailForDisplay() string {
	return t.stderrDiagnostics(false)
}

func (t *StdioTransport) stderrDiagnostics(failureHint bool) string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	buf := t.stderr
	pid := 0
	exitCode := 0
	hasExitCode := false
	if t.cmd != nil {
		if t.cmd.Process != nil {
			pid = t.cmd.Process.Pid
		}
		// SDK 的 CommandTransport 在 Close 内部调用 cmd.Wait：Wait 返回后
		// ProcessState 已落定，退出码仍然可读；而按 PID 重新 OpenProcess 通常
		// 已经拿不到（进程对象随 Wait 回收）。「启动即失败」的诊断必须给出退出码，
		// 因此这里优先使用 Wait 落定的结果（P1）。
		if state := t.cmd.ProcessState; state != nil {
			if code := state.ExitCode(); code >= 0 {
				exitCode, hasExitCode = code, true
			}
		}
	}
	t.mu.Unlock()
	if buf == nil {
		return ""
	}
	return formatStderrDiagnostics(buf, pid, failureHint, exitCode, hasExitCode)
}

// formatStderrDiagnostics 把缓冲内容与进程状态渲染成多行诊断文本。
// failureHint 为 true 时附加「启动命令本身失败 / 握手超时」一类归因提示。
func formatStderrDiagnostics(buf *stderrTailBuffer, pid int, failureHint bool, exitCode int, hasExitCode bool) string {
	if buf == nil {
		return ""
	}
	deadline := time.Now().Add(stdioStderrFlushWait)
	// hasExitCode 说明 Wait 已经落定：进程必然已退出、stderr 不会再增长，
	// 无需再等「退出但尾部尚未落盘」的收尾窗口。
	for !hasExitCode {
		data, _, _ := buf.Snapshot()
		if len(data) > 0 || pid <= 0 {
			break
		}
		exited, _, _, known := processExitStatus(pid)
		if !known || !exited {
			// 进程还活着（可能只是握手超时）或当前平台无法探测：不再等待。
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	data, total, dropped := buf.Snapshot()
	tail := tailLines(string(data), 20, 8*1024)

	var b strings.Builder
	b.WriteString("[stdio 子进程诊断]")
	if status := describeProcessStatus(pid, failureHint, exitCode, hasExitCode); status != "" {
		b.WriteString("\n")
		b.WriteString(status)
	}
	if tail == "" {
		if total == 0 {
			b.WriteString("\n子进程 stderr 为空")
			if failureHint {
				b.WriteString("（可能在写出任何内容前就退出，或尚未启动）")
			} else {
				b.WriteString("（尚未写出任何内容）")
			}
		}
		return strings.TrimRight(b.String(), "\n")
	}
	fmt.Fprintf(&b, "\nstderr 尾部（共 %d 字节", total)
	if dropped > 0 {
		fmt.Fprintf(&b, "，已丢弃较早的 %d 字节", dropped)
	}
	b.WriteString("）：\n")
	b.WriteString(tail)
	return strings.TrimRight(b.String(), "\n")
}

// describeProcessStatus 渲染进程存活状态；未知时返回空串。
// failureHint 为 true 时附加失败归因提示（仅用于连接失败的错误信息）。
func describeProcessStatus(pid int, failureHint bool, exitCode int, hasExitCode bool) string {
	if pid <= 0 {
		return ""
	}
	if hasExitCode {
		// Wait 落定的退出码优先：即使进程对象已被回收也照样可读（P1）。
		return exitedProcessStatus(pid, exitCode, failureHint)
	}
	exited, code, hasCode, known := processExitStatus(pid)
	if !known {
		return fmt.Sprintf("子进程 PID %d 状态未知", pid)
	}
	if !exited {
		if failureHint {
			return fmt.Sprintf("子进程 PID %d 仍在运行（连接失败可能只是握手超时，可结合 stderr 判断）", pid)
		}
		return fmt.Sprintf("子进程 PID %d 仍在运行", pid)
	}
	if !hasCode {
		status := fmt.Sprintf("子进程已退出（PID %d，退出码已随进程对象回收而不可得）", pid)
		if failureHint {
			status += "：通常说明启动命令本身失败（如路径不存在、引号被截断、缺少依赖）"
		}
		return status
	}
	return exitedProcessStatus(pid, code, failureHint)
}

// exitedProcessStatus 渲染「进程已退出」的状态行；failureHint 为 true 时附加
// 启动失败归因提示（仅用于连接失败的错误信息）。
func exitedProcessStatus(pid, code int, failureHint bool) string {
	status := fmt.Sprintf("子进程已退出（PID %d，exit code = %d）", pid, code)
	if failureHint {
		status += "：通常说明启动命令本身失败（如路径不存在、引号被截断、缺少依赖）"
	}
	return status
}

// tailLines 取末尾至多 maxLines 行、且不超过 maxBytes 字节的文本。
func tailLines(text string, maxLines, maxBytes int) string {
	text = strings.TrimRight(text, "\r\n")
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	out := strings.Join(lines, "\n")
	if maxBytes > 0 && len(out) > maxBytes {
		out = out[len(out)-maxBytes:]
	}
	return strings.TrimRight(out, "\r\n")
}
