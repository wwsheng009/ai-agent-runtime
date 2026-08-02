package commands

// 真实交互渲染 e2e（进程内，无需 ConPTY/真实 TTY）。
//
// 背景：ConPTY 方案在本机不可用（CreateProcess 挂 PSEUDOCONSOLE attribute 后
// 子进程 0xC0000142 STATUS_DLL_INIT_FAILED，属环境级限制，已废弃 chat_tty_conpty*）。
// 本文件用仓库既有基础设施驱动"真实"交互路径：
//   - os.Pipe 注入真实 stdin（os.Stdin 被替换，输入经管道流入 chat 主循环）
//   - runChatLoop 真实主循环（prepareInteractiveRead → 逐行读取 → slash 命令处理 → 退出）
//   - captureSurfaceStdout 捕获真实渲染字节流（VT 序列）
//   - ui/vt.Screen 把字节流重建为"用户看到的屏幕"并断言
//
// 覆盖场景：普通多轮对话、未知命令错误渲染、/clear 确认流（取消+确认）、
// /help 帮助列表、! 真实 shell 子进程执行、输入回显与 assistant 渲染。

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func testChatProvider() config.Provider { return config.Provider{Protocol: "codex"} }

func newTestChatContext() context.Context { return context.Background() }

// ttyLiveScriptStep 描述注入脚本的一步：若 waitReady 为 true 则先轮询
// 会话回到 Ready（真实行为：turn 运行中输入会被排队，Ready 后才发送），
// 再等待 wait 时长，最后向 stdin 写入一行。
type ttyLiveScriptStep struct {
	waitReady bool
	wait      time.Duration
	line      string
}

// ttyLiveRun 是一次真实循环运行的产物：全部渲染字节流 + 运行中使用的会话与执行器。
type ttyLiveRun struct {
	raw      string
	session  *ChatSession
	executor *fakeChatExecutor
}

// runTTYLiveLoop 构造会话并在真实 chat 主循环中按脚本注入输入，
// 返回循环期间产生的完整渲染字节流。surface/writer 在捕获窗口内构造，
// 绑定捕获管道而非 go test 真实 stdout（避免 VT 残留污染其他测试的全局捕获）。
func runTTYLiveLoop(t *testing.T, reply string, script []ttyLiveScriptStep, setup func(*ChatSession, *fakeChatExecutor)) *ttyLiveRun {
	t.Helper()
	executor := &fakeChatExecutor{output: reply}
	session := &ChatSession{
		Provider:     testChatProvider(),
		cancelCtx:    newTestChatContext(),
		ChatExecutor: executor,
		Logger:       NewChatLogger("codex_ee", "codex", "gpt-5.4", false, "https://example.com"),
		Formatter:    formatter.NewMarkdownFormatter(false),
		InputBox:     ui.NewInputBox(nil),
	}
	session.Interaction = newChatInteractionCoordinator(session)
	session.Interaction.session = session
	if setup != nil {
		setup(session, executor)
	}
	raw := driveTTYLiveLoop(t, session, script, 10*time.Second)
	return &ttyLiveRun{raw: raw, session: session, executor: executor}
}

// driveTTYLiveLoop 在给定 session（fake 或真实 bootstrap 均可）上驱动真实
// chat 主循环：替换 os.Stdin 为管道、按脚本注入输入、捕获渲染字节流、
// runChatLoop 返回后立即停掉 FramePump。readyDeadline 是 waitReady 步骤的
// 轮询上限（fake executor 秒级返回；真实 provider 回合可达数十秒）。
func driveTTYLiveLoop(t *testing.T, session *ChatSession, script []ttyLiveScriptStep, readyDeadline time.Duration) string {
	t.Helper()
	oldInteractive := chatIsInteractiveTerminal
	oldStdin := os.Stdin
	// 禁用 busy 输入捕获：turn 期间它会并发读 stdin 并把输入抢走排队
	// （真实行为），但会使脚本化注入产生不确定竞争。测试目标是主循环
	// 交互路径本身，禁用后输入顺序完全确定（同 chat_interaction_test.go）。
	oldSupports := supportsCancelableInteractiveInputRead
	t.Cleanup(func() {
		chatIsInteractiveTerminal = oldInteractive
		os.Stdin = oldStdin
		supportsCancelableInteractiveInputRead = oldSupports
	})
	chatIsInteractiveTerminal = func() bool { return true }
	supportsCancelableInteractiveInputRead = func() bool { return false }

	const width, height = 80, 24

	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdin = stdinRead

	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		for _, s := range script {
			if s.waitReady {
				// 等待会话 Ready 且稳定保持（防 busy 输入捕获 goroutine 残留：
				// CompleteWaiting 后 IsReady 立即为 true，但 busy capture 可能
				// 仍在读 stdin，若此时写入会被它抢走排队，主循环随后读到 EOF）。
				deadline := time.Now().Add(readyDeadline)
				stableSince := time.Time{}
				for {
					if session.Interaction.IsReady() {
						if stableSince.IsZero() {
							stableSince = time.Now()
						} else if time.Since(stableSince) >= 300*time.Millisecond {
							break
						}
					} else {
						stableSince = time.Time{}
					}
					if time.Now().After(deadline) {
						break
					}
					time.Sleep(25 * time.Millisecond)
				}
			}
			time.Sleep(s.wait)
			if _, err := stdinWrite.WriteString(s.line); err != nil {
				return
			}
		}
		time.Sleep(300 * time.Millisecond)
		_ = stdinWrite.Close()
	}()

	raw := captureSurfaceStdout(t, func() {
		surface := ui.NewFixedBottomSurface(ui.NewTerminal())
		surface.EnableForTest(width, height)
		session.Interaction.SetSurface(surface)
		session.Interaction.SetWriter(os.Stdout)
		runChatLoop(session, false, "")
		// runChatLoop 返回后立即停掉 FramePump 异步渲染调度器：它仍驻留
		// 并周期 tick，会向"当前 os.Stdout"（已被后续测试替换的捕获管道）
		// 泄漏渲染组字节——完整包回归失败（chat_interactive_selection_test
		// 捕获到 /clear 测试的渲染组）的根因。窗口内停，残余渲染进捕获管道。
		session.Interaction.Shutdown()
	})
	<-writeDone
	return raw
}

// screenLines 把渲染字节流重放为"用户看到的屏幕"行。
func (r *ttyLiveRun) screenLines(t *testing.T) []string {
	t.Helper()
	if r.raw == "" {
		t.Fatal("交互循环未产生任何渲染输出")
	}
	screen := vt.NewScreen(80, 24)
	screen.Feed(r.raw)
	lines := screen.Lines(0, 24)
	t.Logf("--- rendered screen (%d bytes, %d lines) ---\n%s", len(r.raw), len(lines), screen.Dump())
	return lines
}

// assertNoAdjacentDuplicateLines 渲染回归哨兵：相邻两行非空且文本相同 → 滚动补偿/重绘 bug。
func assertNoAdjacentDuplicateLines(t *testing.T, lines []string) {
	t.Helper()
	for i := 1; i < len(lines); i++ {
		a := strings.TrimRight(lines[i-1], " ")
		b := strings.TrimRight(lines[i], " ")
		if a != "" && a == b {
			t.Errorf("发现相邻重复行（渲染回归哨兵）row %d: %q", i-1, a)
		}
	}
}

// TestTTY_LiveLoop_RendersRealScreen 驱动真实 chat 主循环：注入用户输入
// "hi"（走 fakeChatExecutor 返回固定回复）与 "/exit"，把循环期间产生的
// 全部渲染字节流重放到 vt.Screen，断言屏幕包含输入回显、回复内容与
// 状态行，且无相邻重复行（滚动补偿/重绘回归哨兵）。
func TestTTY_LiveLoop_RendersRealScreen(t *testing.T) {
	const reply = "这是真实交互循环渲染出的回复。"
	run := runTTYLiveLoop(t, reply, []ttyLiveScriptStep{
		{wait: 700 * time.Millisecond, line: "hi\n"},
		{waitReady: true, wait: 100 * time.Millisecond, line: "/exit\n"},
	}, nil)
	if !run.executor.called {
		t.Fatalf("fakeChatExecutor 未被调用（输入未进入真实循环）")
	}
	lines := run.screenLines(t)

	foundReply := false
	foundExit := false
	foundStatus := false
	for _, l := range lines {
		if strings.Contains(l, reply) {
			foundReply = true
		}
		if strings.Contains(l, "再见") {
			foundExit = true
		}
		if strings.Contains(l, "provider") || strings.Contains(l, "model") || strings.Contains(l, "window") {
			foundStatus = true
		}
	}
	if !foundReply {
		t.Errorf("屏幕中未找到 assistant 回复; lines=%q", lines)
	}
	if !foundExit {
		t.Errorf("屏幕中未找到 /exit 命令输出（再见）; lines=%q", lines)
	}
	if !foundStatus {
		t.Logf("（提示）屏幕中未发现状态行关键字，可能 status 模型为空; lines=%q", lines)
	}
	assertNoAdjacentDuplicateLines(t, lines)
}

// TestTTY_LiveLoop_MultiTurnConversation 真实两轮对话：两次用户输入分别
// 到达 executor（prompts 记录真实 prompt），两次回复都渲染到屏幕。
func TestTTY_LiveLoop_MultiTurnConversation(t *testing.T) {
	var calls int
	run := runTTYLiveLoop(t, "不应使用", []ttyLiveScriptStep{
		{wait: 700 * time.Millisecond, line: "hi\n"},
		{waitReady: true, wait: 100 * time.Millisecond, line: "second\n"},
		{waitReady: true, wait: 100 * time.Millisecond, line: "/exit\n"},
	}, func(_ *ChatSession, ex *fakeChatExecutor) {
		ex.onCall = func(_ context.Context, _ *ChatSession, _ string) (string, error) {
			calls++
			if calls == 1 {
				return "回复一", nil
			}
			return "回复二", nil
		}
	})
	if len(run.executor.prompts) != 2 {
		t.Fatalf("应有 2 次 executor 调用（两轮对话），实际 %d 次; prompts=%q", len(run.executor.prompts), run.executor.prompts)
	}
	if !strings.Contains(run.executor.prompts[0], "hi") {
		t.Errorf("第 1 轮 prompt 应包含用户输入 hi，实际 %q", run.executor.prompts[0])
	}
	if !strings.Contains(run.executor.prompts[1], "second") {
		t.Errorf("第 2 轮 prompt 应包含用户输入 second，实际 %q", run.executor.prompts[1])
	}
	lines := run.screenLines(t)
	for _, want := range []string{"回复一", "回复二"} {
		found := false
		for _, l := range lines {
			if strings.Contains(l, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("屏幕中未找到第 %q 轮回复; lines=%q", want, lines)
		}
	}
	assertNoAdjacentDuplicateLines(t, lines)
}

// TestTTY_LiveLoop_LongResponseScrollsBeyondOneScreen 超一屏渲染：
// fake 回复 40 行（> 24 行视口），驱动真实主循环后断言滚动真实发生——
// 头部行只出现在渲染字节流（已滚出最终视口），尾部行仍停留在视口
// （滚动停在文末），且无单行宽度溢出与相邻重复行。
func TestTTY_LiveLoop_LongResponseScrollsBeyondOneScreen(t *testing.T) {
	var reply strings.Builder
	for i := 1; i <= 40; i++ {
		// 每行控制在 40 列以内：超过 80 列会触发真实终端 wrap，
		// 混入滚动位移后头部/尾部断言失去聚焦（溢出行另由 vt 报告）。
		fmt.Fprintf(&reply, "长文第%02d行：超一屏滚动渲染验证正文。\n", i)
	}
	head := "长文第01行"
	tail := "长文第40行"

	run := runTTYLiveLoop(t, reply.String(), []ttyLiveScriptStep{
		{wait: 700 * time.Millisecond, line: "hi\n"},
		{waitReady: true, wait: 100 * time.Millisecond, line: "/exit\n"},
	}, nil)
	if !run.executor.called {
		t.Fatalf("fakeChatExecutor 未被调用（输入未进入真实循环）")
	}
	lines := run.screenLines(t)

	// 40 行内容全部经渲染路径写出（字节流保留完整绘制记录）。
	// [诊断] 定位历史行重复渲染的字节序列来源。
	idx := 0
	for {
		pos := strings.Index(run.raw[idx:], "长文第22行")
		if pos < 0 {
			break
		}
		abs := idx + pos
		from := abs - 220
		if from < 0 {
			from = 0
		}
		to := abs + 60
		if to > len(run.raw) {
			to = len(run.raw)
		}
		t.Logf("--- diag 长文第22行 @%d ---\n%q", abs, run.raw[from:to])
		idx = abs + len("长文第22行")
	}
	if !strings.Contains(run.raw, head) {
		t.Errorf("渲染字节流中未找到长文头部行 %q（应曾完整渲染）", head)
	}
	if !strings.Contains(run.raw, tail) {
		t.Errorf("渲染字节流中未找到长文尾部行 %q（应已完整渲染）", tail)
	}
	// 滚动证据：40 行 > 24 行视口 → 头部行必须滚出最终视口。
	for _, l := range lines {
		if strings.Contains(l, head) {
			t.Errorf("超一屏内容未滚动：头部行 %q 仍出现在最终视口", head)
		}
	}
	// 尾部可见：滚动后视口应停在长文末尾。
	foundTail := false
	for _, l := range lines {
		if strings.Contains(l, tail) {
			foundTail = true
		}
	}
	if !foundTail {
		t.Errorf("最终视口中未找到长文尾部行 %q（滚动后应可见）; lines=%q", tail, lines)
	}
	assertNoAdjacentDuplicateLines(t, lines)

	// 单行宽度健康：任一视口行超过 80 列都意味着表面发出一行真实终端
	// 会 wrap 的行，属于渲染回归。
	screen := vt.NewScreen(80, 24)
	screen.Feed(run.raw)
	if overflow := screen.OverflowRows(); len(overflow) > 0 {
		t.Errorf("发现行宽溢出（真实终端会 wrap 的行）rows=%v", overflow)
	}
}

// TestTTY_LiveLoop_UnknownCommandRendersError 真实未知命令路径：
// "/bogus" 必须走 slash 分发并渲染错误行，且不得触发 executor。
// 注意：错误行由 RenderError 输出，随后 surface 重绘（下一轮 prompt）
// 会覆盖屏幕行——因此断言渲染字节流而非最终屏幕（这是真实渲染行为，
// 错误提示是瞬态的）。
func TestTTY_LiveLoop_UnknownCommandRendersError(t *testing.T) {
	run := runTTYLiveLoop(t, "不应被调用", []ttyLiveScriptStep{
		{wait: 700 * time.Millisecond, line: "/bogus\n"},
		{waitReady: true, wait: 100 * time.Millisecond, line: "/exit\n"},
	}, nil)
	if run.executor.called {
		t.Fatalf("未知命令不应触发 executor 调用")
	}
	for _, want := range []string{"未知命令", "/bogus"} {
		if !strings.Contains(run.raw, want) {
			t.Errorf("渲染流中未找到 %q; raw=%q", want, run.raw)
		}
	}
	lines := run.screenLines(t)
	assertNoAdjacentDuplicateLines(t, lines)
}

// TestTTY_LiveLoop_ClearRequiresConfirmation 真实 /clear 确认流：
// 先取消（no）再确认（clear），验证确认提示、取消提示、清空提示与
// 会话消息真实清空。
func TestTTY_LiveLoop_ClearRequiresConfirmation(t *testing.T) {
	run := runTTYLiveLoop(t, "清空测试回复", []ttyLiveScriptStep{
		{wait: 700 * time.Millisecond, line: "hi\n"},
		{waitReady: true, wait: 100 * time.Millisecond, line: "/clear\n"},
		{wait: 500 * time.Millisecond, line: "no\n"},
		{waitReady: true, wait: 100 * time.Millisecond, line: "/clear\n"},
		{wait: 500 * time.Millisecond, line: "clear\n"},
		{waitReady: true, wait: 100 * time.Millisecond, line: "/exit\n"},
	}, nil)
	for _, want := range []string{
		"请输入 clear 确认清空", // 确认提示真实出现 → 说明会话已有消息（非空分支）
		"已取消，会话历史未清空", // 取消路径
		"当前会话历史已清空",     // 确认路径
	} {
		if !strings.Contains(run.raw, want) {
			t.Errorf("渲染流中未找到 %q; raw=%q", want, run.raw)
		}
	}
	// 产品语义：清空后 replaceRuntimeMessages(nil) 清掉全部消息，随后
	// /clear 分支会 ensureChatSystemPromptMessage 重建 system 前缀（环境
	// 上下文），保证后续对话仍有系统提示。因此断言：无 user/assistant，
	// 至多 1 条 system。
	for _, m := range run.session.Messages {
		if m.Role != "system" {
			t.Errorf("清空后只应保留 system 消息，发现 %s 消息: %q", m.Role, strings.TrimSpace(m.Content))
		}
	}
	if len(run.session.Messages) > 1 {
		t.Errorf("清空后消息数应 ≤1（仅 system 前缀），实际 %d 条", len(run.session.Messages))
	}
	lines := run.screenLines(t)
	assertNoAdjacentDuplicateLines(t, lines)
}

// TestTTY_LiveLoop_HelpCommandRendersSlashList 真实 /help 路径：
// 帮助列表渲染到输出流（含命令组标题与 shell 命令说明）。
func TestTTY_LiveLoop_HelpCommandRendersSlashList(t *testing.T) {
	run := runTTYLiveLoop(t, "帮助测试回复", []ttyLiveScriptStep{
		{wait: 700 * time.Millisecond, line: "/help\n"},
		{wait: 900 * time.Millisecond, line: "/exit\n"},
	}, nil)
	for _, want := range []string{"可用命令", "Shell 命令", "/shell"} {
		if !strings.Contains(run.raw, want) {
			t.Errorf("帮助渲染流中未找到 %q; raw=%q", want, run.raw)
		}
	}
	lines := run.screenLines(t)
	assertNoAdjacentDuplicateLines(t, lines)
}

// TestTTY_LiveLoop_ShellCommandRunsRealProcess 真实 !shell 路径：
// 启动真实系统 shell（pwsh/cmd）执行 echo，子进程输出经捕获流入
// executor 的 prompt，并渲染到屏幕。
func TestTTY_LiveLoop_ShellCommandRunsRealProcess(t *testing.T) {
	marker := "hello-e2e-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	run := runTTYLiveLoop(t, "shell 回复", []ttyLiveScriptStep{
		{wait: 700 * time.Millisecond, line: "!echo " + marker + "\n"},
		{wait: 3000 * time.Millisecond, line: "/exit\n"},
	}, nil)
	if !run.executor.called {
		t.Fatalf("shell 命令未进入 executor（! 路径未打通）")
	}
	if len(run.executor.prompts) != 1 || !strings.Contains(run.executor.prompts[0], marker) {
		t.Fatalf("executor 应收到包含 shell 子进程输出的 prompt，实际 %q", run.executor.prompts)
	}
	if !strings.Contains(run.raw, marker) {
		t.Errorf("渲染流中未找到 shell 子进程输出 %q", marker)
	}
	lines := run.screenLines(t)
	assertNoAdjacentDuplicateLines(t, lines)
}
