//go:build windows

// L3 ConPTY 化：真实 chat 主循环在真终端语义（isatty）下运行，无头 CI 可跑。
//
// docs/plan/aicli-terminal-e2e-methodology.md §4 曾记载 ConPTY 路线废弃
// （0xC0000142），根因已在 internal/pty 修复（UTF-16LE 环境块 + attribute
// payload 为句柄值本身），e537b361 的可行性测试证明了前置条件成立。本文件
// 把可行性落地为 L3 交互 E2E：
//
//   - 被测程序 = 本测试二进制自身（go test -c 产物，零额外构建步骤），经
//     internal/pty 挂到伪控制台运行（helper 子进程），因此 stdin/stdout 均
//     为真实终端设备：ui.IsInteractiveTerminal() 自然为真、真实渲染决策路径
//     全开——不替换 os.Stdin/os.Stdout，无测试猴补丁。
//   - 同包测试二进制内直接使用 fakeChatExecutor / chatInteractionCoordinator
//     等真实结构（普通子进程无 *_test.go，test-binary-as-helper 是唯一复用
//     路径）。
//   - 渲染断言双通道：① 原始 VT 字节流（含 conhost 注入的 ?9001h/?25l 等
//     序列，断言前按模式归一化剔除）；② vt.Screen 屏幕重建（渲染决策回归
//     哨兵：相邻重复行）。
//   - 节拍：不等固定毫秒，而等"上一轮 assistant 回复出现在渲染流"——真实
//     用户可见信号，turn 慢于/快于预期都不会错拍。
//   - 能力门禁：pty.Supported()==false（Win7/老 CI）自动 skip，os.Pipe 层
//     （chat_tty_live_loop_test.go）测试矩阵不受影响。
//
// 已知注意点：helper 子进程显式 AICLI_CHAT_INPUT_LINE_MODE=1 强制按行队列
// （win32-input-mode/?9001 下按键经 conhost 编码，注入原始字符的逐键编辑器
// 无法成行——这正是此前 helper 卡死的根因）；主循环其余部分（slash 分发/
// executor/渲染管线）不受影响。脚本必须以 "/exit" 结束（helper 经真实退出
// 路径结束进程，EOF 语义在 ConPTY 管道上不可靠）。
package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
	"github.com/wwsheng009/ai-agent-runtime/internal/pty"
)

const (
	// 环境块继手：父测试进程 → pty（环境继承）→ helper 子进程。
	chatTTYConPTYSpecEnv = "AICLI_CHAT_TTY_CONPTY_SPEC"
	// helper 把渲染字节流落盘，父进程断言（helper 侧 os.Stdout 的完整原始
	// 序列；父进程同时留有 PTY 管道回读流作对照/兜底）。
	chatTTYConPTYCaptureEnv = "AICLI_CHAT_TTY_CONPTY_CAPTURE"
)

// chatTTYConPTYScriptStep 一步注入：WaitSignal 非空则等渲染流出现该片段
// （上一轮回复可见 = 真实节拍），否则固定等待 FixedWait 毫秒；随后写入
// Line（行结束符由驱动端规范成 \r/Enter——bracketed paste 下 \n 不提交）。
type chatTTYConPTYScriptStep struct {
	WaitSignal string `json:"wait_signal,omitempty"`
	FixedWait  int    `json:"fixed_wait_ms,omitempty"`
	Line       string `json:"line"`
}

// chatTTYConPTYScript 既是父进程侧的注入脚本+断言清单，也是经环境变量下发
// 给 helper 子进程的启动规格（pty 环境块继承父进程 os.Environ，无需命令行
// 转义）。
type chatTTYConPTYScript struct {
	Reply string                    `json:"reply"`
	Steps []chatTTYConPTYScriptStep `json:"steps"`
	Wants []string                  `json:"wants"`
}

func encodeChatTTYConPTYScript(script chatTTYConPTYScript) string {
	b, err := json.Marshal(script)
	if err != nil {
		return ""
	}
	return string(b)
}

func decodeChatTTYConPTYScript(t *testing.T, raw string) chatTTYConPTYScript {
	t.Helper()
	var script chatTTYConPTYScript
	if err := json.Unmarshal([]byte(raw), &script); err != nil {
		t.Fatalf("helper: decode script spec: %v", err)
	}
	if script.Reply == "" {
		t.Fatal("helper: script.reply is required")
	}
	return script
}

// TestChatTTYConPTYHelperChild 是 helper 子进程的入口测试：在伪控制台内
// 构造与 runTTYLiveLoop 同构的真实会话并驱动真实 runChatLoop，把渲染字节
// 流回传父进程（文件，路径经环境变量下发）。作为普通 `go test` 运行时
// （无规格环境变量）立即跳过。
func TestChatTTYConPTYHelperChild(t *testing.T) {
	specJSON := strings.TrimSpace(os.Getenv(chatTTYConPTYSpecEnv))
	if specJSON == "" {
		t.Skip("helper entry: not running under the ConPTY driver (no spec env)")
	}

	// 会话隔离（与 usage_analytics_testmain 的隔离思路一致）：helper 子进程
	// 不继承父进程已设置的隔离函数指针，显式把 HOME/USERPROFILE 钉到临时
	// 目录，避免 NewChatLogger/工作区解析触碰真实用户目录。
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	script := decodeChatTTYConPTYScript(t, specJSON)

	// isatty 证据行：写进真实终端（conhost → PTY 管道 → 父进程流与捕获
	// 文件都可见）。os.Pipe 层的等价场景永远是 INTERACTIVE=False。
	fmt.Fprintf(os.Stdout, "INTERACTIVE=%v\n", ui.IsInteractiveTerminal())

	raw := driveChatTTYConPTYChildLoop(t, script)
	if strings.TrimSpace(raw) == "" {
		t.Fatal("helper: real chat loop produced no render output")
	}
	writeChatTTYConPTYCapture(t, raw)
	t.Logf("helper: captured %d render bytes under pseudoconsole", len(raw))
}

// driveChatTTYConPTYChildLoop 在 helper 子进程内跑真实 chat 主循环：构造与
// runTTYLiveLoop 同构的会话（fake executor / coordinator / unified renderer），
// 脚本注入由父进程经 ConPTY 管道完成，主循环直到 "/exit" 退出。
//
// 渲染直连伪控制台 stdout：helper 的 os.Stdout 就是 ConPTY 的 conhost 端，
// unified renderer 经 io.MultiWriter(console, sink) 直写终端（os.Stdout 的
// fd 不变，isatty 语义不受影响）同时 tee 进内存 sink 回传父进程。
func driveChatTTYConPTYChildLoop(t *testing.T, script chatTTYConPTYScript) string {
	t.Helper()
	executor := &fakeChatExecutor{output: script.Reply}
	session := &ChatSession{
		Provider:     testChatProvider(),
		cancelCtx:    newTestChatContext(),
		ChatExecutor: executor,
		// HOME 已隔离：NewChatLogger 的目录解析只落临时目录。
		Logger:    NewChatLogger("codex_ee", "codex", "gpt-5.4", false, "https://example.com"),
		Formatter: formatter.NewMarkdownFormatter(false),
		InputBox:  ui.NewInputBox(nil),
	}
	session.Interaction = newTestChatInteractionCoordinator(t, session)
	session.Interaction.session = session

	var renderSink strings.Builder
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	session.Interaction.SetSurface(surface)
	session.Interaction.SetWriter(io.MultiWriter(os.Stdout, &renderSink))
	if !session.Interaction.enableUnifiedRendererWithWriter(io.MultiWriter(os.Stdout, &renderSink)) {
		t.Fatal("helper: unified renderer did not attach")
	}

	runChatLoop(session, false, "")

	// 退出路径最后一帧经 unified 管线异步提交：排空 actor/presenter 后再
	// 落捕获文件（与 driveTTYLiveLoop 的收尾顺序一致）。
	session.Interaction.waitUIActorIdle()
	session.Interaction.mu.Lock()
	presenter := session.Interaction.primaryPresenter
	session.Interaction.mu.Unlock()
	if presenter != nil {
		presenter.WaitIdle()
	}
	session.Interaction.Shutdown()

	if executor.called {
		t.Logf("helper: executor saw %d prompt(s)", len(executor.prompts))
	}
	return renderSink.String()
}

func writeChatTTYConPTYCapture(t *testing.T, raw string) {
	t.Helper()
	path := strings.TrimSpace(os.Getenv(chatTTYConPTYCaptureEnv))
	if path == "" {
		t.Fatal("helper: capture path env missing")
	}
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("helper: write capture: %v", err)
	}
}

func readChatTTYConPTYCapture(t *testing.T) string {
	t.Helper()
	path := strings.TrimSpace(os.Getenv(chatTTYConPTYCaptureEnv))
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

// runChatTTYConPTYLiveLoop 在父测试进程内：把本测试二进制经 ConPTY 拉起为
// helper 子进程，按脚本节拍向 conhost 行规程写键，返回 helper 捕获的原始
// VT 字节流（为空时回落 PTY 管道回读流）。helper 崩溃/卡死由超时兜底
// （Wait ctx → Kill）。
func runChatTTYConPTYLiveLoop(t *testing.T, script chatTTYConPTYScript, timeout time.Duration) string {
	t.Helper()
	if len(script.Steps) == 0 || !strings.EqualFold(strings.TrimSpace(script.Steps[len(script.Steps)-1].Line), "/exit") {
		t.Fatalf("ConPTY script must end with /exit (helper exits via the real quit path); steps=%+v", script.Steps)
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	capturePath := filepath.Join(t.TempDir(), "chat-tty-conpty-capture.vt")
	env := append(os.Environ(),
		chatTTYConPTYSpecEnv+"="+encodeChatTTYConPTYScript(script),
		chatTTYConPTYCaptureEnv+"="+capturePath,
		// 强制按行队列：伪控制台下 IsInteractiveTerminal()=true 会选逐键编辑
		// 器，而 win32-input-mode（?9001）下原始字符注入无法成行（helper 卡死
		// 的根因，2026-10-11）。生产不设置该变量。
		"AICLI_CHAT_INPUT_LINE_MODE=1",
	)

	p, err := pty.Start(testBinary, []string{
		"-test.run", "^TestChatTTYConPTYHelperChild$",
		"-test.timeout", (timeout + 30*time.Second).String(),
		"-test.v",
	}, pty.WithSize(80, 24), pty.WithEnv(env))
	if err != nil {
		if errors.Is(err, pty.ErrUnsupported) {
			t.Skip("ConPTY not supported on this host")
		}
		t.Fatalf("start helper under pseudoconsole: %v", err)
	}
	return driveChatTTYConPTYStream(t, p, script, capturePath, timeout)
}

// driveChatTTYConPTYStream 读 ConPTY 输出、按节拍注入按键、收尾校验：
// helper 进程必须正常退出且捕获文件完整。
//
// 清理顺序是正确性关键：ClosePseudoConsole 在客户端进程未退出时会阻塞
// （API 语义），任何 t.Fatal 退出路径都必须先 Kill helper —— 统一经
// t.Cleanup(Kill→Close) 兜底（cleanup 先于函数 defer 运行）。
func driveChatTTYConPTYStream(t *testing.T, p *pty.PTY, script chatTTYConPTYScript, capturePath string, timeout time.Duration) string {
	t.Helper()
	t.Cleanup(func() {
		_ = p.Kill()
		<-time.After(200 * time.Millisecond) // TerminateProcess 异步生效窗口
		_ = p.Close()
	})

	var (
		mu     sync.Mutex
		stream strings.Builder
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 16*1024)
		for {
			n, readErr := p.Read(buf)
			if n > 0 {
				mu.Lock()
				stream.Write(buf[:n])
				mu.Unlock()
			}
			if readErr != nil {
				return
			}
		}
	}()

	waitCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failure := make(chan string, 1)
	go func() {
		code, waitErr := p.Wait(waitCtx)
		if waitErr != nil {
			if errors.Is(waitErr, context.Canceled) {
				failure <- fmt.Sprintf("helper did not exit within %s (killed)", timeout)
			} else {
				failure <- fmt.Sprintf("helper wait: %v", waitErr)
			}
			return
		}
		if code != 0 {
			failure <- fmt.Sprintf("helper exit=%d", code)
			return
		}
		failure <- ""
	}()

	for i, step := range script.Steps {
		if step.WaitSignal != "" {
			waitForChatTTYConPTYSignal(t, &mu, &stream, step.WaitSignal, timeout)
		} else {
			time.Sleep(time.Duration(step.FixedWait) * time.Millisecond)
		}
		// 行结束符必须是 \r（Enter）：composer 开启 bracketed paste
		// （?2004h）后 \n 是多行草稿换行，不会提交（诊断实测结论）。
		line := strings.TrimSuffix(step.Line, "\n")
		line = strings.ReplaceAll(line, "\n", "\r") + "\r"
		if _, err := p.Write([]byte(line)); err != nil {
			mu.Lock()
			tail := tailBytes(stream.String(), 400)
			mu.Unlock()
			t.Fatalf("write step %d to pseudoconsole: %v; tail=%q", i, err, tail)
		}
	}
	// 脚本以 /exit 结束：helper 走真实退出路径结束进程，随后关闭句柄收尾。

	select {
	case msg := <-failure:
		if msg != "" {
			raw := readChatTTYConPTYCapture(t)
			t.Fatalf("%s; captured=%d bytes tail=%q", msg, len(raw), tailBytes(raw, 400))
		}
	case <-time.After(timeout):
		cancel() // Wait 的 ctx 取消 → 兜底 goroutine 报超时
		_ = p.Kill()
		t.Fatalf("helper did not exit within %s (killed)", timeout)
	}
	// helper 已退出：先关 PTY 解除读 goroutine 的 ReadFile 阻塞，再等 done。
	// （与 pty_test.go round-trip 收尾一致——ConPTY 输出管道在 ClosePseudoConsole
	// 前不会 EOF，直接 <-done 会永久死锁，2026-10-11。）
	_ = p.Close()
	<-done

	raw := readChatTTYConPTYCapture(t)
	if strings.TrimSpace(raw) == "" {
		mu.Lock()
		raw = stream.String()
		mu.Unlock()
	}
	return raw
}

// waitForChatTTYConPTYSignal 等待渲染流出现信号片段（上一轮回复可见 =
// 真实节拍），超时 fatal（附当前流尾，便于定位）。
func waitForChatTTYConPTYSignal(t *testing.T, mu *sync.Mutex, stream *strings.Builder, signal string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		mu.Lock()
		seen := strings.Contains(stream.String(), signal)
		mu.Unlock()
		if seen {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	mu.Lock()
	tail := tailBytes(stream.String(), 400)
	mu.Unlock()
	t.Fatalf("渲染流 %s 内未出现节拍信号 %q; tail=%q", timeout, signal, tail)
}

func tailBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// normalizeChatTTYConPTYStream 剔除 conhost 注入的控制序列（?9001 win32
// input mode、?1004 focus reporting、光标显隐等），保留内容与 SGR，使
// grep 断言与 os.Pipe 层的可读性对齐。
func normalizeChatTTYConPTYStream(raw string) string {
	patterns := []string{
		"\x1b[?9001h", "\x1b[?9001l",
		"\x1b[?1004h", "\x1b[?1004l",
		"\x1b[?25l", "\x1b[?25h",
		"\x1b[6n",
	}
	for _, pat := range patterns {
		raw = strings.ReplaceAll(raw, pat, "")
	}
	return raw
}

// TestChatTTY_LiveLoop_ConPTY_RendersRealScreen L3 ConPTY 层主用例：本测试
// 二进制经伪控制台运行真实 chat 主循环（stdin/stdout 是真终端设备，
// IsInteractiveTerminal 自然为真），注入 "hi"（fake executor 回复）与
// "/exit"，断言双通道：① 渲染流（归一化后）含回复、再见、isatty 证据；
// ② vt.Screen 重建屏幕健康（相邻重复行哨兵）。
func TestChatTTY_LiveLoop_ConPTY_RendersRealScreen(t *testing.T) {
	const reply = "这是ConPTY真终端语义下渲染的回复。"
	script := chatTTYConPTYScript{
		Reply: reply,
		Steps: []chatTTYConPTYScriptStep{
			{FixedWait: 1500, Line: "hi"},
			{WaitSignal: reply, Line: "/exit"},
		},
		Wants: []string{reply, "再见"},
	}
	raw := runChatTTYConPTYLiveLoop(t, script, 90*time.Second)
	normalized := normalizeChatTTYConPTYStream(raw)

	// isatty 证据：helper 在伪控制台下观察到真实终端语义（os.Pipe 层永远
	// 是 INTERACTIVE=False）。大小写不敏感匹配（PowerShell bool 渲染为
	// "True"，Go fmt %v 渲染为 "true"——两种证据来源都接受）。
	if !strings.Contains(strings.ToLower(normalized), "interactive=true") {
		t.Errorf("伪控制台下未观察到 isatty 语义（INTERACTIVE=True）; normalized tail=%q", tailBytes(normalized, 600))
	}
	for _, want := range script.Wants {
		if !strings.Contains(normalized, want) {
			t.Errorf("渲染流中未找到 %q; normalized tail=%q", want, tailBytes(normalized, 600))
		}
	}

	screen := vt.NewScreen(80, 24)
	screen.Feed(normalized)
	lines := screen.Lines(0, 24)
	t.Logf("--- ConPTY rendered screen (%d raw bytes, %d lines) ---\n%s", len(raw), len(lines), screen.Dump())
	assertNoAdjacentDuplicateLines(t, lines)

	// 屏幕健康哨兵：至少一行非空内容（回复/状态行/输入回显）。
	nonEmpty := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonEmpty++
		}
	}
	if nonEmpty == 0 {
		t.Fatalf("屏幕全空：渲染未呈现任何内容; raw=%d bytes", len(raw))
	}
}

// TestChatTTY_LiveLoop_ConPTY_UnknownCommandRendersError 真实未知命令路径：
// "/bogus" 走 slash 分发渲染错误行，且不得触发 executor（渲染流中不得出现
// fake executor 的回复）。
func TestChatTTY_LiveLoop_ConPTY_UnknownCommandRendersError(t *testing.T) {
	const reply = "不应被调用的回复"
	script := chatTTYConPTYScript{
		Reply: reply,
		Steps: []chatTTYConPTYScriptStep{
			{FixedWait: 1500, Line: "/bogus"},
			{FixedWait: 800, Line: "/exit"},
		},
		Wants: []string{"未知命令: /bogus"},
	}
	raw := runChatTTYConPTYLiveLoop(t, script, 90*time.Second)
	normalized := normalizeChatTTYConPTYStream(raw)
	for _, want := range script.Wants {
		if !strings.Contains(normalized, want) {
			t.Errorf("渲染流中未找到 %q; normalized tail=%q", want, tailBytes(normalized, 600))
		}
	}
	if strings.Contains(normalized, reply) {
		t.Errorf("未知命令不应触发 executor 回复 %q", reply)
	}
	screen := vt.NewScreen(80, 24)
	screen.Feed(normalized)
	lines := screen.Lines(0, 24)
	t.Logf("--- ConPTY unknown-command screen (%d raw bytes) ---\n%s", len(raw), screen.Dump())
	assertNoAdjacentDuplicateLines(t, lines)
}
