package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// 方案 B 结构化状态机（chatSurfaceStatus{kind, detail}）的语义矩阵测试。
// 核心回归：llm.retry 发布 "retrying step=1 ..." 时，旧实现按字符串匹配
// chatSurfaceStateIsRunning 判为非运行 → 计时不启动 → "(0s • esc to interrupt)"
// 永远卡 0s。结构化后语义由 kind 承载，Retrying 必须 isRunning()==true。

func TestChatSurfaceStatusIsRunningMatrix(t *testing.T) {
	cases := []struct {
		name string
		s    chatSurfaceStatus
		want bool
	}{
		{name: "idle", s: chatSurfaceStatus{kind: chatSurfaceStatusIdle}, want: false},
		{name: "notice", s: chatSurfaceStatus{kind: chatSurfaceStatusNotice, detail: "Paste draft 3 lines"}, want: false},
		{name: "waiting", s: chatSurfaceStatus{kind: chatSurfaceStatusWaiting}, want: true},
		{name: "thinking", s: chatSurfaceStatus{kind: chatSurfaceStatusThinking}, want: true},
		{name: "streaming", s: chatSurfaceStatus{kind: chatSurfaceStatusStreaming}, want: true},
		{name: "planning", s: chatSurfaceStatus{kind: chatSurfaceStatusPlanning}, want: true},
		{name: "stopping", s: chatSurfaceStatus{kind: chatSurfaceStatusStopping}, want: true},
		{name: "tool", s: chatSurfaceStatus{kind: chatSurfaceStatusTool, detail: "shell"}, want: true},
		// 回归点：retry 无论 detail 是否为空都必须视为运行中（计时启动）。
		{name: "retrying with detail", s: chatSurfaceStatus{kind: chatSurfaceStatusRetrying, detail: "step=1 attempt=2/3"}, want: true},
		{name: "retrying empty detail", s: chatSurfaceStatus{kind: chatSurfaceStatusRetrying}, want: true},
		{name: "approval", s: chatSurfaceStatus{kind: chatSurfaceStatusApproval}, want: true},
		{name: "answer", s: chatSurfaceStatus{kind: chatSurfaceStatusAnswer}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.isRunning(); got != tc.want {
				t.Fatalf("chatSurfaceStatus{kind=%v detail=%q}.isRunning() = %v, want %v",
					tc.s.kind, tc.s.detail, got, tc.want)
			}
		})
	}
}

func TestChatSurfaceStatusStringMatrix(t *testing.T) {
	cases := []struct {
		name string
		s    chatSurfaceStatus
		want string
	}{
		{name: "idle", s: chatSurfaceStatus{kind: chatSurfaceStatusIdle}, want: "Ready"},
		{name: "notice detail", s: chatSurfaceStatus{kind: chatSurfaceStatusNotice, detail: "Agent Panel"}, want: "Agent Panel"},
		{name: "notice empty", s: chatSurfaceStatus{kind: chatSurfaceStatusNotice}, want: "Ready"},
		{name: "tool detail", s: chatSurfaceStatus{kind: chatSurfaceStatusTool, detail: "shell"}, want: "Tool shell"},
		{name: "tool empty", s: chatSurfaceStatus{kind: chatSurfaceStatusTool}, want: "Tool running"},
		{name: "retrying detail", s: chatSurfaceStatus{kind: chatSurfaceStatusRetrying, detail: "step=1 attempt=2/3"}, want: "retrying step=1 attempt=2/3"},
		{name: "retrying empty", s: chatSurfaceStatus{kind: chatSurfaceStatusRetrying}, want: "Retrying"},
		{name: "streaming", s: chatSurfaceStatus{kind: chatSurfaceStatusStreaming}, want: "Streaming"},
		{name: "thinking", s: chatSurfaceStatus{kind: chatSurfaceStatusThinking}, want: "Thinking"},
		{name: "waiting", s: chatSurfaceStatus{kind: chatSurfaceStatusWaiting}, want: "Waiting"},
		{name: "planning", s: chatSurfaceStatus{kind: chatSurfaceStatusPlanning}, want: "Planning"},
		{name: "stopping", s: chatSurfaceStatus{kind: chatSurfaceStatusStopping}, want: "Stopping"},
		{name: "approval", s: chatSurfaceStatus{kind: chatSurfaceStatusApproval}, want: "Awaiting approval"},
		{name: "answer", s: chatSurfaceStatus{kind: chatSurfaceStatusAnswer}, want: "Awaiting answer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.String(); got != tc.want {
				t.Fatalf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestChatDynamicStatusActionMatrix(t *testing.T) {
	cases := []struct {
		name         string
		s            chatSurfaceStatus
		wantAction   string
		wantRole     style.Role
		wantInterrupt bool
	}{
		{name: "idle produces no dynamic line", s: chatSurfaceStatus{kind: chatSurfaceStatusIdle}},
		{name: "notice produces no dynamic line", s: chatSurfaceStatus{kind: chatSurfaceStatusNotice, detail: "Agent Panel"}},
		{name: "retrying detail", s: chatSurfaceStatus{kind: chatSurfaceStatusRetrying, detail: "step=1 attempt=2/3"},
			wantAction: "Retrying step=1 attempt=2/3", wantRole: style.RoleWarning, wantInterrupt: true},
		{name: "retrying empty detail", s: chatSurfaceStatus{kind: chatSurfaceStatusRetrying},
			wantAction: "Retrying", wantRole: style.RoleWarning, wantInterrupt: true},
		{name: "tool", s: chatSurfaceStatus{kind: chatSurfaceStatusTool, detail: "shell"},
			wantAction: "Running shell", wantRole: style.RoleTool, wantInterrupt: true},
		{name: "waiting", s: chatSurfaceStatus{kind: chatSurfaceStatusWaiting},
			wantAction: "Analyzing", wantRole: style.RoleReasoning, wantInterrupt: true},
		{name: "thinking", s: chatSurfaceStatus{kind: chatSurfaceStatusThinking},
			wantAction: "Analyzing", wantRole: style.RoleReasoning, wantInterrupt: true},
		{name: "planning", s: chatSurfaceStatus{kind: chatSurfaceStatusPlanning},
			wantAction: "Analyzing", wantRole: style.RoleReasoning, wantInterrupt: true},
		{name: "streaming", s: chatSurfaceStatus{kind: chatSurfaceStatusStreaming},
			wantAction: "Generating response", wantRole: style.RoleProgress, wantInterrupt: true},
		{name: "approval", s: chatSurfaceStatus{kind: chatSurfaceStatusApproval},
			wantAction: "Waiting for approval", wantRole: style.RoleApproval, wantInterrupt: true},
		{name: "answer", s: chatSurfaceStatus{kind: chatSurfaceStatusAnswer},
			wantAction: "Waiting for answer", wantRole: style.RoleWarning, wantInterrupt: true},
		// Stopping 是中断后的清理阶段：再次 Esc 没有可中断目标，不得宣传可中断。
		{name: "stopping is not interruptible", s: chatSurfaceStatus{kind: chatSurfaceStatusStopping},
			wantAction: "Stopping", wantRole: style.RoleWarning, wantInterrupt: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			action, role, interruptible := chatDynamicStatusAction(tc.s, chatInputModeChat)
			if action != tc.wantAction || role != tc.wantRole || interruptible != tc.wantInterrupt {
				t.Fatalf("chatDynamicStatusAction(%+v) = (%q, %v, %v), want (%q, %v, %v)",
					tc.s, action, role, interruptible, tc.wantAction, tc.wantRole, tc.wantInterrupt)
			}
		})
	}
}

func TestChatDynamicStatusActionInputModeOverrides(t *testing.T) {
	cases := []struct {
		mode         chatInputMode
		wantAction   string
		wantRole     style.Role
		wantInterrupt bool
	}{
		{mode: chatInputModeApproval, wantAction: "Waiting for approval", wantRole: style.RoleApproval, wantInterrupt: true},
		{mode: chatInputModeAnswer, wantAction: "Waiting for answer", wantRole: style.RoleWarning, wantInterrupt: true},
		{mode: chatInputModeSelection, wantAction: "Selecting an option", wantRole: style.RoleInfo, wantInterrupt: false},
		{mode: chatInputModeConfirmation, wantAction: "Waiting for confirmation", wantRole: style.RoleWarning, wantInterrupt: false},
		{mode: chatInputModeSecret, wantAction: "Waiting for credentials", wantRole: style.RoleWarning, wantInterrupt: false},
		{mode: chatInputModePanel, wantAction: "Navigating panel", wantRole: style.RoleInfo, wantInterrupt: false},
	}
	for _, tc := range cases {
		t.Run(string(tc.mode), func(t *testing.T) {
			action, role, interruptible := chatDynamicStatusAction(chatSurfaceStatus{kind: chatSurfaceStatusIdle}, tc.mode)
			if action != tc.wantAction || role != tc.wantRole || interruptible != tc.wantInterrupt {
				t.Fatalf("chatDynamicStatusAction(idle, %q) = (%q, %v, %v), want (%q, %v, %v)",
					tc.mode, action, role, interruptible, tc.wantAction, tc.wantRole, tc.wantInterrupt)
			}
		})
	}
}

func TestChatDynamicStatusModelElapsedRendering(t *testing.T) {
	// 纯渲染函数：elapsed 决定 "(Ns • esc to interrupt)" 后缀。回归点：retry
	// 必须是可中断运行态，elapsed 推进必须反映到渲染文本（0s → 1s → 2m 20s）。
	s := chatSurfaceStatus{kind: chatSurfaceStatusRetrying, detail: "step=1 attempt=2/3"}
	cases := []struct {
		elapsed time.Duration
		want    string
	}{
		{elapsed: 0, want: "◦ Retrying step=1 attempt=2/3 (0s • esc to interrupt)"},
		{elapsed: 1 * time.Second, want: "◦ Retrying step=1 attempt=2/3 (1s • esc to interrupt)"},
		{elapsed: 140 * time.Second, want: "◦ Retrying step=1 attempt=2/3 (2m 20s • esc to interrupt)"},
	}
	for _, tc := range cases {
		model := buildChatDynamicStatusModelForWidthInputModeAndCompletion(s, 160, chatInputModeChat, tc.elapsed, false)
		if model == nil {
			t.Fatalf("retry with elapsed=%v must produce a dynamic status model", tc.elapsed)
		}
		plain := style.StatusLineDocument(*model, 160).PlainText()
		if plain != tc.want {
			t.Fatalf("elapsed=%v rendered %q, want %q", tc.elapsed, plain, tc.want)
		}
	}

	// 终态（completed）冻结为 "Worked for ..."，不再滚动。
	done := buildChatDynamicStatusModelForWidthInputModeAndCompletion(s, 160, chatInputModeChat, 140*time.Second, true)
	if done == nil {
		t.Fatal("completed retry must still render a summary model")
	}
	if plain := style.StatusLineDocument(*done, 160).PlainText(); plain != "Worked for 2m 20s" {
		t.Fatalf("completed render = %q, want %q", plain, "Worked for 2m 20s")
	}

	// Idle / Notice 不产生动态活动行（nil 模型 → surface 无动态行）。
	if m := buildChatDynamicStatusModelForWidthInputModeAndCompletion(chatSurfaceStatus{kind: chatSurfaceStatusIdle}, 160, chatInputModeChat, 0, false); m != nil {
		t.Fatalf("idle must not produce a dynamic model, got %+v", m)
	}
	if m := buildChatDynamicStatusModelForWidthInputModeAndCompletion(chatSurfaceStatus{kind: chatSurfaceStatusNotice, detail: "Agent Panel"}, 160, chatInputModeChat, 0, false); m != nil {
		t.Fatalf("notice must not produce a dynamic model, got %+v", m)
	}
}

func TestSetRetryingStartsClockAndRendersInterruptibleStatus(t *testing.T) {
	// 回归核心：SetRetrying 必须启动动态时钟（dynamicStatusStarted 非零），
	// 否则 elapsed 永远 0s。旧实现 RefreshStatus("retrying ...") 字符串匹配
	// 失败导致时钟不启动，UI 永远显示 "(0s • esc to interrupt)"。
	interaction := newChatInteractionCoordinator(&ChatSession{})
	t.Cleanup(interaction.Shutdown)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	interaction.SetSurface(surface)

	interaction.SetRetrying("step=1 attempt=2/3 reason=rate_limit")

	interaction.mu.Lock()
	started := interaction.dynamicStatusStarted
	model := interaction.dynamicStatusModel
	interaction.mu.Unlock()
	if started.IsZero() {
		t.Fatal("SetRetrying must start the dynamic status clock")
	}
	if model == nil {
		t.Fatal("SetRetrying must render a dynamic status model")
	}
	plain := style.StatusLineDocument(*model, 160).PlainText()
	if !strings.Contains(plain, "Retrying step=1 attempt=2/3") {
		t.Fatalf("retry status line missing retry detail: %q", plain)
	}
	if !strings.Contains(plain, "esc to interrupt") {
		t.Fatalf("retry status line must advertise interruptibility: %q", plain)
	}
}

func TestSetNoticeDoesNotStartClock(t *testing.T) {
	// Notice 是非状态机透传文案（如 "Agent Panel"、"Paste draft N lines"），
	// 不得启动计时，也不得产生动态活动行。
	interaction := newChatInteractionCoordinator(&ChatSession{})
	t.Cleanup(interaction.Shutdown)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	interaction.SetSurface(surface)

	interaction.SetNotice("Paste draft 3 lines")

	interaction.mu.Lock()
	started := interaction.dynamicStatusStarted
	model := interaction.dynamicStatusModel
	interaction.mu.Unlock()
	if !started.IsZero() {
		t.Fatalf("SetNotice must not start the dynamic clock, got started=%v", started)
	}
	if model != nil {
		t.Fatalf("SetNotice must not produce a dynamic model, got %+v", model)
	}
}

func TestRetryElapsedAdvancesOverRealTime(t *testing.T) {
	// 端到端时钟验证（无 UI actor）：SetRetrying 后 elapsed 必须随时间真实
	// 推进，而不是冻结在 0s —— 这是 "0s 卡死" bug 的直接回归。
	interaction := newChatInteractionCoordinator(&ChatSession{})
	t.Cleanup(interaction.Shutdown)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	interaction.SetSurface(surface)

	interaction.SetRetrying("step=1")
	if got := interaction.dynamicStatusElapsedLocked(time.Now()); got >= time.Second {
		t.Fatalf("elapsed at start = %v, want well under 1s", got)
	}
	time.Sleep(1100 * time.Millisecond)
	elapsed := interaction.dynamicStatusElapsedLocked(time.Now())
	if elapsed < time.Second {
		t.Fatalf("elapsed did not advance past 1s after sleeping 1.1s, got %v", elapsed)
	}
	// 渲染文本层面验证：手动驱动一次 tick（等价于 UI actor Timer 回调
	// chat_ui_actor.go 的 refreshDynamicStatusTick 路径），断言秒数已推进。
	interaction.mu.Lock()
	seq := interaction.dynamicStatusTimerSeq
	interaction.mu.Unlock()
	interaction.refreshDynamicStatusTick(seq)
	interaction.mu.Lock()
	model := interaction.dynamicStatusModel
	interaction.mu.Unlock()
	if model == nil {
		t.Fatal("tick after advance must re-render the dynamic model")
	}
	plain := style.StatusLineDocument(*model, 160).PlainText()
	if strings.Contains(plain, "(0s • esc to interrupt)") {
		t.Fatalf("retry timer is stuck at 0s after real time passed: %q", plain)
	}
	if !strings.Contains(plain, "• esc to interrupt") {
		t.Fatalf("tick lost interruptibility suffix: %q", plain)
	}
}
