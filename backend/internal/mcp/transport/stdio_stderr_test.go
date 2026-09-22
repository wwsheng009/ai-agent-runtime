//go:build !win7compat

package transport

import (
	"strings"
	"sync"
	"testing"
)

func TestStderrTailBufferKeepsOnlyTail(t *testing.T) {
	buf := newStderrTailBuffer(16)
	if _, err := buf.Write([]byte("0123456789ABCDEF")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := buf.Write([]byte("XYZ")); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, total, dropped := buf.Snapshot()
	if got := string(data); got != "3456789ABCDEFXYZ" {
		t.Fatalf("tail = %q, want %q", got, "3456789ABCDEFXYZ")
	}
	if total != 19 {
		t.Fatalf("total = %d, want 19", total)
	}
	if dropped != 3 {
		t.Fatalf("dropped = %d, want 3", dropped)
	}
}

func TestStderrTailBufferConcurrentWrites(t *testing.T) {
	buf := newStderrTailBuffer(256)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_, _ = buf.Write([]byte("line-of-stderr\n"))
			}
		}()
	}
	wg.Wait()
	if buf.Len() > 256 {
		t.Fatalf("buffer exceeded limit: %d", buf.Len())
	}
	if data, total, _ := buf.Snapshot(); total != int64(8*200*len("line-of-stderr\n")) || len(data) == 0 {
		t.Fatalf("unexpected snapshot: total=%d len=%d", total, len(data))
	}
}

func TestFormatStderrDiagnosticsIncludesTailAndTruncation(t *testing.T) {
	buf := newStderrTailBuffer(8)
	_, _ = buf.Write([]byte("0123456789"))

	// pid=0：跳过进程状态探测，只验证文本渲染。
	out := formatStderrDiagnostics(buf, 0, true, 0, false)
	if !strings.Contains(out, "[stdio 子进程诊断]") {
		t.Fatalf("missing header: %q", out)
	}
	if !strings.Contains(out, "23456789") {
		t.Fatalf("missing tail: %q", out)
	}
	if !strings.Contains(out, "已丢弃较早的 2 字节") {
		t.Fatalf("missing truncation info: %q", out)
	}
}

func TestStderrTailForDisplayKeepsTailWithoutFailureHints(t *testing.T) {
	tr := NewStdioTransport(&Config{Type: "stdio", Command: "noop"})
	tr.stderr = newStderrTailBuffer(256)
	_, _ = tr.stderr.Write([]byte("DevTools listening on ws://127.0.0.1:9222\n"))

	display := tr.StderrTailForDisplay()
	if !strings.Contains(display, "DevTools listening on ws://127.0.0.1:9222") {
		t.Fatalf("display diagnostics lost the tail: %q", display)
	}
	if strings.Contains(display, "通常说明启动命令本身失败") ||
		strings.Contains(display, "连接失败可能只是握手超时") {
		t.Fatalf("display diagnostics must not carry failure hints: %q", display)
	}

	// 失败路径文案保持不变（既有断言依赖它）。
	failure := tr.StderrDiagnostics()
	if !strings.Contains(failure, "DevTools listening on ws://127.0.0.1:9222") {
		t.Fatalf("failure diagnostics lost the tail: %q", failure)
	}
}

func TestTailLines(t *testing.T) {
	text := "a\nb\nc\nd\n"
	if got := tailLines(text, 2, 100); got != "c\nd" {
		t.Fatalf("tailLines = %q, want %q", got, "c\nd")
	}
	if got := tailLines("", 2, 100); got != "" {
		t.Fatalf("tailLines(empty) = %q", got)
	}
}

func TestEnrichConnectError(t *testing.T) {
	if err := EnrichConnectError(nil, nil); err != nil {
		t.Fatalf("nil err must stay nil, got %v", err)
	}
	base := errTest("boom")
	if got := EnrichConnectError(struct{}{}, base); got != base {
		t.Fatalf("non-provider target must return err unchanged")
	}
	tr := NewStdioTransport(&Config{Type: "stdio", Command: "noop"})
	tr.stderr = newStderrTailBuffer(64)
	_, _ = tr.stderr.Write([]byte("'C:\\Program' is not recognized as an internal or external command\r\n"))

	enriched := EnrichConnectError(tr, base)
	if enriched == base {
		t.Fatalf("expected enriched error")
	}
	if !strings.Contains(enriched.Error(), "boom") ||
		!strings.Contains(enriched.Error(), "is not recognized") {
		t.Fatalf("enriched error missing content: %q", enriched.Error())
	}
	if !strings.Contains(StderrDiagnosticsOf(tr), "is not recognized") {
		t.Fatalf("StderrDiagnosticsOf lost the tail")
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }
