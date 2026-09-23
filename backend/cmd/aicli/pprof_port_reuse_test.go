package main

import (
	"net"
	"strconv"
	"testing"

	"github.com/spf13/cobra"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands"
)

// newStickyPortTestRoot 复刻 chat/resume/exec resume 的最小命令树，
// 用于验证启动期"目标会话"解析（真实命令树在 main() 里构建）。
func newStickyPortTestRoot() *cobra.Command {
	root := &cobra.Command{Use: "aicli"}
	root.PersistentFlags().Bool("pprof", false, "")
	root.PersistentFlags().Int("web-port", 0, "")

	resume := &cobra.Command{Use: "resume [SESSION_ID]", Run: func(*cobra.Command, []string) {}}
	resume.Flags().String("session", "", "")
	root.AddCommand(resume)

	chat := &cobra.Command{Use: "chat", Run: func(*cobra.Command, []string) {}}
	chat.Flags().String("session", "", "")
	root.AddCommand(chat)

	execCmd := &cobra.Command{Use: "exec", Run: func(*cobra.Command, []string) {}}
	execResume := &cobra.Command{Use: "resume [SESSION_ID] [PROMPT]", Run: func(*cobra.Command, []string) {}}
	execResume.Flags().Bool("last", false, "")
	execCmd.AddCommand(execResume)
	root.AddCommand(execCmd)
	return root
}

func findStickyPortTestCommand(t *testing.T, root *cobra.Command, path ...string) *cobra.Command {
	t.Helper()
	cmd, remaining, err := root.Find(path)
	if err != nil {
		t.Fatalf("Find(%v) error = %v", path, err)
	}
	if cmd == nil || len(remaining) != 0 {
		t.Fatalf("Find(%v) = %v remaining %v, want the leaf command", path, cmd, remaining)
	}
	return cmd
}

func TestResolveChatWebPortTargetSessionID(t *testing.T) {
	root := newStickyPortTestRoot()
	resumeCmd := findStickyPortTestCommand(t, root, "resume")
	chatCmd := findStickyPortTestCommand(t, root, "chat")
	execResumeCmd := findStickyPortTestCommand(t, root, "exec", "resume")

	// aicli resume <session-id>
	if got := resolveChatWebPortTargetSessionID(resumeCmd, []string{"session_20260922173838_lsirc1la"}); got != "session_20260922173838_lsirc1la" {
		t.Fatalf("resume positional = %q, want the session id", got)
	}
	// aicli resume --session <session-id>
	if err := resumeCmd.Flags().Set("session", "session_flag"); err != nil {
		t.Fatal(err)
	}
	if got := resolveChatWebPortTargetSessionID(resumeCmd, nil); got != "session_flag" {
		t.Fatalf("resume --session = %q, want session_flag", got)
	}
	// 裸 aicli resume（恢复最近会话）：选择完成前没有确定 ID。
	if got := resolveChatWebPortTargetSessionID(resumeCmd, []string{"   "}); got != "session_flag" {
		t.Fatalf("blank positional = %q, want fallback to --session", got)
	}
	_ = resumeCmd.Flags().Set("session", "")
	if got := resolveChatWebPortTargetSessionID(resumeCmd, nil); got != "" {
		t.Fatalf("bare resume = %q, want empty (id unknown until selection)", got)
	}

	// aicli chat --session <session-id>
	if err := chatCmd.Flags().Set("session", "session_chat"); err != nil {
		t.Fatal(err)
	}
	if got := resolveChatWebPortTargetSessionID(chatCmd, nil); got != "session_chat" {
		t.Fatalf("chat --session = %q, want session_chat", got)
	}
	// chat 不接受位置参数，不能把其它位置参数当会话 ID。
	if got := resolveChatWebPortTargetSessionID(chatCmd, []string{"random-positional"}); got != "session_chat" {
		t.Fatalf("chat positional = %q, want --session value", got)
	}

	// aicli exec resume <session-id>
	if got := resolveChatWebPortTargetSessionID(execResumeCmd, []string{"session_exec"}); got != "session_exec" {
		t.Fatalf("exec resume positional = %q, want session_exec", got)
	}
	// aicli exec resume --last <prompt>：位置参数是 prompt，不是会话 ID。
	if err := execResumeCmd.Flags().Set("last", "true"); err != nil {
		t.Fatal(err)
	}
	if got := resolveChatWebPortTargetSessionID(execResumeCmd, []string{"继续上次的任务"}); got != "" {
		t.Fatalf("exec resume --last = %q, want empty", got)
	}

	if got := resolveChatWebPortTargetSessionID(nil, nil); got != "" {
		t.Fatalf("nil command = %q, want empty", got)
	}
}

func TestStickyLoopbackServerAddrWithoutRecordKeepsBase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	addr, reused := stickyLoopbackServerAddr("127.0.0.1", "127.0.0.1:0", "session_unknown")
	if reused {
		t.Fatalf("stickyLoopbackServerAddr() reused = true for an unknown session")
	}
	if addr != "127.0.0.1:0" {
		t.Fatalf("addr = %q, want the base address unchanged", addr)
	}

	addr, reused = stickyLoopbackServerAddr("127.0.0.1", "127.0.0.1:0", "")
	if reused || addr != "127.0.0.1:0" {
		t.Fatalf("empty session id must not reuse: addr=%q reused=%v", addr, reused)
	}
}

// TestStickyLoopbackServerAddrBindsStoredPort 覆盖核心诉求：
// resume 同一会话（未指定 --web-port）时，服务器回到上次使用的端口。
func TestStickyLoopbackServerAddrBindsStoredPort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	// 取一个刚释放的空闲端口，模拟"该会话上次监听的端口"。
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen error = %v", err)
	}
	storedPort := probe.Addr().(*net.TCPAddr).Port
	if err := probe.Close(); err != nil {
		t.Fatalf("probe close error = %v", err)
	}

	const sessionID = "session_20260922173838_lsirc1la"
	if err := commands.SaveChatWebPortRecord(sessionID, storedPort, "127.0.0.1"); err != nil {
		t.Fatalf("SaveChatWebPortRecord() error = %v", err)
	}

	addr, reused := stickyLoopbackServerAddr("127.0.0.1", "127.0.0.1:0", sessionID)
	if !reused {
		t.Fatal("stickyLoopbackServerAddr() reused = false, want true for a stored session")
	}
	if addr != "127.0.0.1:"+strconv.Itoa(storedPort) {
		t.Fatalf("addr = %q, want 127.0.0.1:%d", addr, storedPort)
	}

	handle, err := startPprofServer(addr)
	if err != nil {
		// 端口刚被其它进程抢占（CI 上极小概率）：跳过而不是误报失败。
		t.Skipf("stored port %d became unavailable: %v", storedPort, err)
	}
	defer handle.Close()

	if handle.WebPort() != strconv.Itoa(storedPort) {
		t.Fatalf("handle.WebPort() = %q, want %d", handle.WebPort(), storedPort)
	}
	host, port := loopbackServerHostPort(handle)
	if host != "127.0.0.1" || port != storedPort {
		t.Fatalf("loopbackServerHostPort() = (%q,%d), want (127.0.0.1,%d)", host, port, storedPort)
	}
}

// TestStickyPortUnavailableFallsBackToRandomPort 验证端口被占用时不会让
// resume 失败：监听冲突由 main 捕获后回退随机端口（此处验证冲突确实可检出）。
func TestStickyPortUnavailableFallsBackToRandomPort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	occupied, err := startPprofServer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("startPprofServer() error = %v", err)
	}
	defer occupied.Close()

	host, port := loopbackServerHostPort(occupied)
	if port == 0 {
		t.Fatal("failed to resolve occupied listener port")
	}

	const sessionID = "session_occupied"
	if err := commands.SaveChatWebPortRecord(sessionID, port, host); err != nil {
		t.Fatalf("SaveChatWebPortRecord() error = %v", err)
	}
	stickyAddr, reused := stickyLoopbackServerAddr("127.0.0.1", "127.0.0.1:0", sessionID)
	if !reused {
		t.Fatal("expected the stored port to be selected")
	}
	if _, err := startPprofServer(stickyAddr); err == nil {
		t.Fatal("starting a second server on the same port should fail so main can fall back")
	}

	fallback, err := startPprofServer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("fallback random port listen error = %v", err)
	}
	defer fallback.Close()
	if fallback.WebPort() == strconv.Itoa(port) {
		t.Fatalf("fallback port %q must differ from the occupied sticky port", fallback.WebPort())
	}
}
