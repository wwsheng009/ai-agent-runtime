//go:build !win7compat

package transport

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// stdioTreeTestCommand 返回一个「根进程会派生子进程且长时间不退出」的命令，
// 用来验证进程树守卫：Windows 是 cmd.exe → ping.exe，Unix 是 sh → sleep。
func stdioTreeTestCommand() (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", []string{"/c", "ping", "-n", "600", "127.0.0.1"}
	}
	return "sh", []string{"-c", "sleep 600"}
}

type recordingObserver struct {
	mu     sync.Mutex
	events []LifecycleEvent
}

func (o *recordingObserver) observe(event LifecycleEvent) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
}

func (o *recordingObserver) find(eventType string) []LifecycleEvent {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []LifecycleEvent
	for _, event := range o.events {
		if event.Type == eventType {
			out = append(out, event)
		}
	}
	return out
}

func (o *recordingObserver) types() []string {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]string, 0, len(o.events))
	for _, event := range o.events {
		out = append(out, event.Type)
	}
	return out
}

// TestStdioTransportTreeGuardLifecycle 验证 stdio 传输在连接建立时把 MCP server
// 注册进进程树守卫，并在连接关闭时按「整棵树」终止（计划 §6.2 验收标准 10 的
// 传输层一半；强杀场景见 stdio_tree_windows_test.go）。
func TestStdioTransportTreeGuardLifecycle(t *testing.T) {
	command, args := stdioTreeTestCommand()
	if _, err := exec.LookPath(command); err != nil {
		t.Skipf("环境缺少 %s: %v", command, err)
	}

	tr := NewStdioTransport(&Config{Type: "stdio", Command: command, Args: args})
	observer := &recordingObserver{}
	tr.AddLifecycleObserver(observer.observe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sdkTransport := tr.ToMCPSdkTransport(ctx)
	if sdkTransport == nil {
		t.Fatal("ToMCPSdkTransport 返回 nil")
	}

	conn, err := sdkTransport.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect 失败: %v", err)
	}

	root, descendants := tr.treeGuardSnapshot()
	if root <= 0 {
		t.Fatalf("守卫未记录根进程 pid（root=%d, events=%v）", root, observer.types())
	}

	if runtime.GOOS == "windows" {
		// Windows 上必须真的拿到 Job Object，否则「父死子亡」不成立。
		attached := observer.find("mcp.stdio.tree_guard_attached")
		if len(attached) == 0 {
			t.Fatalf("期望 mcp.stdio.tree_guard_attached，实际事件=%v", observer.types())
		}
		if report := tr.treeGuardReport(); report.AttachErr != "" {
			t.Fatalf("Job Object 绑定失败: %s", report.AttachErr)
		}
	}

	// 关闭连接：SDK 先关 stdin，守卫再兜底收掉整棵树。
	_ = conn.Close()

	terminated := observer.find("mcp.stdio.tree_terminated")
	if len(terminated) == 0 {
		t.Fatalf("期望 mcp.stdio.tree_terminated，实际事件=%v", observer.types())
	}
	mode, _ := terminated[len(terminated)-1].Payload["mode"].(string)
	want := "process_group"
	if runtime.GOOS == "windows" {
		want = "job_object"
	}
	if mode != want {
		t.Fatalf("终止模式 = %q，期望 %q（payload=%v）",
			mode, want, terminated[len(terminated)-1].Payload)
	}

	if _, leftovers := tr.treeGuardSnapshot(); len(leftovers) != 0 {
		t.Fatalf("关闭后仍有存活后代进程: %v", leftovers)
	}
	_ = descendants
}

// TestStdioTransportTreeGuardSnapshotEmpty 未装配时快照必须安全返回零值
// （诊断路径不得因 nil 守卫 panic）。
func TestStdioTransportTreeGuardSnapshotEmpty(t *testing.T) {
	tr := NewStdioTransport(&Config{Type: "stdio", Command: "no-such-command"})
	if root, leftovers := tr.treeGuardSnapshot(); root != 0 || len(leftovers) != 0 {
		t.Fatalf("未装配快照 = (%d, %v)，期望 (0, nil)", root, leftovers)
	}
	if report := tr.treeGuardReport(); report.TreeKill {
		t.Fatalf("未装配 report 不应报告 TreeKill: %+v", report)
	}
	if strings.TrimSpace(tr.Type()) != "stdio" {
		t.Fatalf("Type() = %q", tr.Type())
	}
}
