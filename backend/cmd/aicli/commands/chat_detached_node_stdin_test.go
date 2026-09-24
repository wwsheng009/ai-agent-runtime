package commands

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// replaceStdinWithNullDevice points os.Stdin at the null device for one test:
// the exact stdin shape of a detached mesh node (internal/mesh/spawn_exec.go
// 的 cmd.Stdin = nil → 子进程 stdin 是 NUL//dev/null)。
func replaceStdinWithNullDevice(t *testing.T) func() {
	t.Helper()
	file, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	previous := os.Stdin
	os.Stdin = file
	return func() {
		os.Stdin = previous
		_ = file.Close()
	}
}

// TestDetachedMeshNodeStdinEOFParkKeepsQueueServing 覆盖「spawn 上报 started 但
// 节点马上自己退出」的回归：网格拉起的脱离节点 stdin 是空设备，pump 读到 EOF
// 时必须停驻等待 Web 队列，而不是把 io.EOF 记成终态错误让主循环退出。
func TestDetachedMeshNodeStdinEOFParkKeepsQueueServing(t *testing.T) {
	restoreStdin := replaceStdinWithNullDevice(t)
	defer restoreStdin()
	t.Setenv(meshEnvSpawnedBy, "node-23104-20260924T070705Z")

	queue := newChatInputQueue(bufio.NewReader(os.Stdin))
	queue.startPump()

	// pump 读 NUL 会立刻拿到 EOF；留出足够时间让它（错误地）落到 terminalErr。
	time.Sleep(300 * time.Millisecond)
	if err := queue.terminalError(); err != nil {
		t.Fatalf("detached node must not record a terminal stdin error, got %v", err)
	}

	// Web 客户端投递的一行必须仍能被读到：节点继续服务浏览器窗口。
	queue.routeLine(chatQueuedInput{Text: "web line", Source: "web", EnqueuedAt: time.Now().UTC()})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	line, err := queue.readLine(ctx)
	if err != nil {
		t.Fatalf("readLine on parked queue: %v", err)
	}
	if strings.TrimSpace(line) != "web line" {
		t.Fatalf("unexpected line %q", line)
	}
	if err := queue.terminalError(); err != nil {
		t.Fatalf("terminal error after web line: %v", err)
	}
}

// TestPlainProcessStdinEOFStillEndsChat 守住另一半语义：普通进程（没有
// AICLI_MESH_SPAWNED_BY）stdin 到 EOF 时照旧结束，`echo x | aicli chat` 与
// Ctrl+D 的行为不变。
func TestPlainProcessStdinEOFStillEndsChat(t *testing.T) {
	restoreStdin := replaceStdinWithNullDevice(t)
	defer restoreStdin()
	t.Setenv(meshEnvSpawnedBy, "")

	queue := newChatInputQueue(bufio.NewReader(os.Stdin))
	queue.startPump()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := queue.terminalError(); err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("want io.EOF terminal error, got %v", err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("plain process must still surface stdin EOF as a terminal error")
}
