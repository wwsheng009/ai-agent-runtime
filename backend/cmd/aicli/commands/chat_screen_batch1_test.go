package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// chat_screen_batch1_test.go 覆盖批次 1（只读页收编）的验收项：
//   - /todos、/history + Ctrl+T 迁入 ScreenDocument Spec；
//   - B 族旧 Open* 字段（/usage、/account、/accounts、/debug display、
//     /web endpoints）经兼容映射层进入同一生命周期（命令 → 副屏 → Esc → 主屏）；
//   - 降级文档与收编前字符级一致；
//   - legacy 开关（§6.1）下批次 1 的框架入口不回退为二次开屏。

// TestChatScreenBatch1TodosUnifiedReturnsScreenSpec 验证统一 TUI 出口的 /todos
// 只携带 Screen Spec（不提交重复的内联命令单元格），且屏内正文与 legacy 内联
// 文档字符级一致。
func TestChatScreenBatch1TodosUnifiedReturnsScreenSpec(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)
	session := newChatScreenTestSession(t)
	// 统一出口判定：terminal 所有权边界已跨越（无需真实 TTY）。
	session.TerminalSession = &ui.TerminalSession{}

	result, handled, err := tryExecuteStructuredChatCommand(session, "/todos")
	if err != nil || !handled {
		t.Fatalf("/todos handled=%v err=%v", handled, err)
	}
	if result.Screen == nil {
		t.Fatal("统一 /todos 必须携带 Screen Spec")
	}
	if got := result.Screen.ID; got != "todos.screen" {
		t.Fatalf("screen id = %q，期望 todos.screen", got)
	}
	if result.Screen.Kind != chatScreenDocument || result.Screen.Trigger != "command" {
		t.Fatalf("spec = %+v，期望 document/command", *result.Screen)
	}
	if strings.TrimSpace(ui.RenderDocumentPlain(result.Document())) != "" {
		t.Fatalf("统一 /todos 不应再提交内联命令单元格：%q", ui.RenderDocumentPlain(result.Document()))
	}
	legacy := buildChatTodosDocument(session, chatTodosFilterAll)
	if got, want := ui.RenderDocumentPlain(result.Screen.Doc), ui.RenderDocumentPlain(legacy); got != want {
		t.Fatalf("副屏正文与 legacy 内联文档不一致\n got: %q\nwant: %q", got, want)
	}
}

// TestChatScreenBatch1TodosPlainKeepsInlineDocument 验证非统一会话仍走既有
// 内联文档（回退路径字符级不变）。
func TestChatScreenBatch1TodosPlainKeepsInlineDocument(t *testing.T) {
	session := &ChatSession{}
	result, handled, err := tryExecuteStructuredChatCommand(session, "/todos")
	if err != nil || !handled {
		t.Fatalf("/todos handled=%v err=%v", handled, err)
	}
	if result.Screen != nil {
		t.Fatalf("非统一会话不应生成 Screen：%+v", result.Screen)
	}
	if got := ui.RenderDocumentPlain(result.Document()); !strings.Contains(got, "当前会话暂无待办") {
		t.Fatalf("plain /todos 文档缺失内容：%q", got)
	}
}

// TestChatScreenBatch1TodosLifecycleEscReleasesLease 验证 /todos 的 Spec 经框架
// 生命周期：命令 → 副屏（Esc）→ 主屏，租约释放且计数器可见。
func TestChatScreenBatch1TodosLifecycleEscReleasesLease(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)
	session := newChatScreenTestSession(t)

	var ran bool
	chatScreenDocumentRunner = func(_ *ChatSession, _ ui.ScreenLease, options ui.DebugOverlayOptions) error {
		ran = true
		if options.Title != "任务列表" {
			t.Fatalf("runner title = %q，期望 任务列表", options.Title)
		}
		if !strings.Contains(options.Body, "当前会话暂无待办") {
			t.Fatalf("runner body 缺失 todos 正文：%q", options.Body)
		}
		return nil // Esc / q
	}

	spec := chatScreenTodosSpec(session, chatTodosFilterAll)
	outcome := openChatScreen(session, spec)
	if !ran {
		t.Fatal("document runner 未执行")
	}
	if outcome.Degraded || outcome.Result != chatScreenClosedEsc {
		t.Fatalf("outcome = %+v，期望副屏 Esc 关闭", outcome)
	}
	if session.Surface.LeaseActive() {
		t.Fatal("Esc 关闭后租约必须已释放（I2）")
	}
	snapshot := chatScreenCounterSnapshotForDebug()
	if snapshot.Opens != 1 || snapshot.Closes != 1 || snapshot.CloseEsc != 1 {
		t.Fatalf("counters = %+v，期望 opens/closes/esc = 1", snapshot)
	}
}

// TestChatScreenBatch1HistorySpecContract 验证 /history 与 Ctrl+T 共用的
// transcript Spec 契约：SilentDegrade（旧行为：能力不足不抢键、不刷屏）、
// RunDocument 复用既有分页器。
func TestChatScreenBatch1HistorySpecContract(t *testing.T) {
	command := chatScreenTranscriptSpec("command")
	hotkey := chatScreenTranscriptSpec("hotkey")

	if command.ID != hotkey.ID || command.ID != "transcript.screen" {
		t.Fatalf("transcript spec id = %q/%q", command.ID, hotkey.ID)
	}
	if !command.SilentDegrade || !hotkey.SilentDegrade {
		t.Fatal("transcript 页必须在能力不足时静默降级（保持旧行为）")
	}
	if command.RunDocument == nil || hotkey.RunDocument == nil {
		t.Fatal("transcript 页必须复用既有分页器 RunDocument")
	}
	if command.Trigger != "command" || hotkey.Trigger != "hotkey" {
		t.Fatalf("trigger = %q/%q", command.Trigger, hotkey.Trigger)
	}
	if err := command.validate(); err != nil {
		t.Fatalf("transcript spec 非法：%v", err)
	}
}

// TestChatScreenBatch1HistoryDispatchOpensFrameworkScreen 验证统一模式下
// /history 的结果经框架派发（而不是直调分页器）：派发一次 openChatScreen，
// Esc 后租约释放。分页器本身以测试替身替代（真实分页器需要 TTY）。
func TestChatScreenBatch1HistoryDispatchOpensFrameworkScreen(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)
	session := newChatScreenTestSession(t)

	var ran bool
	spec := chatScreenTranscriptSpec("command")
	spec.RunDocument = func(_ *ChatSession, lease ui.ScreenLease, _ chatScreenSpec) error {
		ran = true
		if lease == nil || !lease.Active() {
			t.Fatal("RunDocument 必须在已持有的租约内运行")
		}
		return nil
	}
	// 用替换后的 Spec 走一次完整生命周期（派发层与 Ctrl+T 都使用同一 Spec）。
	outcome := openChatScreen(session, spec)
	if !ran {
		t.Fatal("transcript RunDocument 未执行")
	}
	if outcome.Degraded || outcome.Result != chatScreenClosedEsc {
		t.Fatalf("outcome = %+v，期望 Esc 关闭", outcome)
	}
	if session.Surface.LeaseActive() {
		t.Fatal("Esc 关闭后租约必须已释放")
	}
	if got := chatScreenCounterSnapshotForDebug().Opens; got != 1 {
		t.Fatalf("opens = %d，期望 1", got)
	}
}

// TestChatScreenBatch1RetiredEnvValueBehavesAsUnified 验证 §6.1：legacy 分支已
// 删除，退休开关值与默认值同义——只记 unknown_env 警告，不再改变行为。测试
// 环境能力门 fail-closed，因此这里只断言不会取租约、不会计入 opens。
func TestChatScreenBatch1RetiredEnvValueBehavesAsUnified(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)
	chatScreenFrameworkEnvLookup = func() string { return "legacy" }
	session := newChatScreenTestSession(t)

	// Ctrl+T：claim 门在测试环境 fail-closed，退休开关值不得复活 legacy 分支。
	openChatTranscriptPager(session)
	if session.Surface.LeaseActive() {
		t.Fatal("能力门 fail-closed 时不得持有租约")
	}
	if got := chatScreenCounterSnapshotForDebug().Opens; got != 0 {
		t.Fatalf("静默降级不应计入 opens：%d", got)
	}
}

// TestChatScreenBatch1FamilySpecsLifecycle 验证 B 族 5 条命令的生产者 Spec 都
// 走统一生命周期（命令 → 副屏 → Esc → 主屏）关闭；各 Spec 的正文与旧渲染
// 函数同源（批次 5：旧字段/映射层已删除，改为直接调用生产者构建函数）。
func TestChatScreenBatch1FamilySpecsLifecycle(t *testing.T) {
	cases := []struct {
		name     string
		screenID string
		build    func(*ChatSession) chatScreenSpec
	}{
		{
			name:     "debug display",
			screenID: "debug.display",
			build:    func(s *ChatSession) chatScreenSpec { return chatScreenDebugDisplaySpec(s) },
		},
		{
			name:     "web endpoints",
			screenID: "web.endpoints",
			build:    func(s *ChatSession) chatScreenSpec { return chatScreenWebEndpointsSpec(s) },
		},
		{
			name:     "usage",
			screenID: "usage.screen",
			build: func(s *ChatSession) chatScreenSpec {
				return chatScreenUsageSpec(s, UsageScreenRequest{Mode: usageScreenModeOverview})
			},
		},
		{
			name:     "account",
			screenID: "account.screen",
			build: func(s *ChatSession) chatScreenSpec {
				return chatScreenAccountSpec(s, AccountScreenRequest{Report: chatAccountReport{Provider: "alpha"}})
			},
		},
		{
			name:     "accounts",
			screenID: "accounts.screen",
			build:    func(s *ChatSession) chatScreenSpec { return chatScreenAccountsSpec(s, AccountListScreenRequest{}) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chatScreenTestSeamsInstall(t, true)
			session := newChatScreenTestSession(t)

			spec := tc.build(session)
			if spec.ID != tc.screenID {
				t.Fatalf("screen id = %q，期望 %q", spec.ID, tc.screenID)
			}
			// 测试环境无真实 TTY，生产者按旧 gate 标记 ForceInline；这里只
			// 聚焦生命周期本身，故显式放开内联降级开关。
			spec.ForceInline = false
			spec.SilentDegrade = false

			var ran bool
			chatScreenDocumentRunner = func(_ *ChatSession, _ ui.ScreenLease, options ui.DebugOverlayOptions) error {
				ran = true
				if strings.TrimSpace(options.Body) == "" {
					t.Fatalf("%s 副屏正文为空", tc.name)
				}
				return nil
			}
			outcome := openChatScreen(session, spec)
			if !ran || outcome.Degraded || outcome.Result != chatScreenClosedEsc {
				t.Fatalf("outcome = %+v ran=%v，期望副屏 Esc 关闭", outcome, ran)
			}
			if session.Surface.LeaseActive() {
				t.Fatalf("%s Esc 关闭后租约必须已释放", tc.name)
			}
		})
	}
}
