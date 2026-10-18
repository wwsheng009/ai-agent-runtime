package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// P2-4b：S 档首批白名单 + 副屏执行原语（modal 登记 / 租约预算 / 恢复）。

// withChatBusyScreenTestHooks 注入副屏能力与执行替身，避免单测依赖真实 TTY。
func withChatBusyScreenTestHooks(t *testing.T, capable bool, dispatch func(*ChatSession, string) bool) {
	t.Helper()
	prevCapability, prevDispatch := chatBusyScreenCapability, chatBusyScreenDispatchOverride
	chatBusyScreenCapability = func(*ChatSession) bool { return capable }
	chatBusyScreenDispatchOverride = dispatch
	t.Cleanup(func() {
		chatBusyScreenCapability, chatBusyScreenDispatchOverride = prevCapability, prevDispatch
	})
}

func TestChatBusyPolicyScreenFirstBatch(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")

	cases := map[string]chatBusyCommandPolicy{
		"/todos":         chatBusyPolicyScreen,
		"/todos active":  chatBusyPolicyScreen,
		"/history":       chatBusyPolicyScreen,
		"/usage":         chatBusyPolicyScreen,
		"/debug display": chatBusyPolicyScreen,
		"/web endpoints": chatBusyPolicyScreen,
		"/debug status":  chatBusyPolicyImmediate,
		"/skills":        chatBusyPolicyScreen,    // 批次 4：只读文档变体纳入白名单
		"/agents":        chatBusyPolicyScreen,    // 只读交互列表白名单（A 族）
		"/agents status": chatBusyPolicyImmediate, // inline+read 快照
		"/agents panel":  chatBusyPolicyDeferred,  // screen+live 写入类（核验：改会话选中态+戳 presenter）
		"/theme on":      chatBusyPolicyDeferred,  // /theme 无 on 变体 → 未登记 → queue
		"/export":        chatBusyPolicyDeferred,  // 核验：run 活跃时 gate 拒绝后立即导出在途会话
	}
	for line, want := range cases {
		if got := chatSlashCommandBusyPolicyFor(line); got != want {
			t.Fatalf("%q 策略为 %s，期望 %s", line, got, want)
		}
	}

	// 未显式启用 P1 通道时，S 档必须回退 D（与既有灰度契约一致）。
	t.Setenv(chatBusyCommandEnv, "off")
	if got := chatSlashCommandBusyPolicyFor("/todos"); got != chatBusyPolicyDeferred {
		t.Fatalf("关闭忙时通道后 S 档应回退 D，实际 %s", got)
	}
}

func TestRuntimeCommandHostRunsWhitelistedScreen(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, _, _, store := newRuntimeHostTestSession(t)
	surface := ui.NewFixedBottomSurface(nil)
	surface.EnableForTest(80, 24)
	session.Surface = surface
	releaseCapture := beginChatInputShadowLevel(session, chatInputOwnerBusyCapture)
	defer releaseCapture()

	var (
		dispatched   bool
		insideBudget time.Duration
	)
	withChatBusyScreenTestHooks(t, true, func(s *ChatSession, line string) bool {
		dispatched = true
		if line != "/todos" {
			t.Errorf("dispatch 收到 %q，期望 /todos", line)
		}
		if snap, ok := chatInputArbitrationSnapshotOf(s); !ok || snap.Owner != chatInputOwnerModal {
			t.Errorf("副屏执行期间仲裁属主应为 modal，实际 ok=%v snap=%#v", ok, snap)
		}
		if s.Surface != nil {
			insideBudget = s.Surface.AlternateScreenWaitBudget()
		}
		return true
	})

	if !runtimeCommandHostFor(session).SubmitBusy("/todos") {
		t.Fatal("/todos（首批 S 档）应在宿主内执行")
	}
	if !dispatched {
		t.Fatal("副屏执行入口未被调用")
	}
	if insideBudget != chatBusyScreenWaitBudget {
		t.Fatalf("执行期间租约预算 = %s，期望 %s", insideBudget, chatBusyScreenWaitBudget)
	}
	if got := surface.AlternateScreenWaitBudget(); got != 0 {
		t.Fatalf("执行后租约预算未复位：%s", got)
	}
	if !chatBusyCommandArbitrationAllows(session) {
		t.Fatal("执行后 modal 登记必须已释放")
	}
	if !session.commandMu.TryLock() {
		t.Fatal("执行后 commandMu 必须已释放")
	}
	session.commandMu.Unlock()

	events := store.runtimeInteractions()
	if len(events) != 1 {
		t.Fatalf("应产生 1 条审计事件，实际 %d", len(events))
	}
	if events[0].Payload["result"] != "executed" || events[0].Payload["mode"] != "screen" {
		t.Fatalf("审计内容不符：%+v", events[0].Payload)
	}
}

// TestRuntimeCommandHostRunsReadOnlyInteractiveScreen 锁定忙时 S 档的只读交互
// 列表通道：/agents（A 族列表，screen+read+screen-interactive）在白名单内，
// 运行中必须走副屏执行入口（modal 属主、租约预算、审计 executed/screen），
// 而不是被降级入队。写变体 /agents panel 仍在降级测试中覆盖。
func TestRuntimeCommandHostRunsReadOnlyInteractiveScreen(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, _, _, store := newRuntimeHostTestSession(t)
	surface := ui.NewFixedBottomSurface(nil)
	surface.EnableForTest(80, 24)
	session.Surface = surface
	releaseCapture := beginChatInputShadowLevel(session, chatInputOwnerBusyCapture)
	defer releaseCapture()

	var (
		dispatched   bool
		insideBudget time.Duration
	)
	withChatBusyScreenTestHooks(t, true, func(s *ChatSession, line string) bool {
		dispatched = true
		if line != "/agents" {
			t.Errorf("dispatch 收到 %q，期望 /agents", line)
		}
		if snap, ok := chatInputArbitrationSnapshotOf(s); !ok || snap.Owner != chatInputOwnerModal {
			t.Errorf("副屏执行期间仲裁属主应为 modal，实际 ok=%v snap=%#v", ok, snap)
		}
		if s.Surface != nil {
			insideBudget = s.Surface.AlternateScreenWaitBudget()
		}
		return true
	})

	if !runtimeCommandHostFor(session).SubmitBusy("/agents") {
		t.Fatal("/agents（只读交互列表白名单）应在宿主内执行")
	}
	if !dispatched {
		t.Fatal("副屏执行入口未被调用")
	}
	if insideBudget != chatBusyScreenWaitBudget {
		t.Fatalf("执行期间租约预算 = %s，期望 %s", insideBudget, chatBusyScreenWaitBudget)
	}
	if got := surface.AlternateScreenWaitBudget(); got != 0 {
		t.Fatalf("执行后租约预算未复位：%s", got)
	}
	if !chatBusyCommandArbitrationAllows(session) {
		t.Fatal("执行后 modal 登记必须已释放")
	}
	if !session.commandMu.TryLock() {
		t.Fatal("执行后 commandMu 必须已释放")
	}
	session.commandMu.Unlock()

	events := store.runtimeInteractions()
	if len(events) != 1 {
		t.Fatalf("应产生 1 条审计事件，实际 %d", len(events))
	}
	if events[0].Payload["result"] != "executed" || events[0].Payload["mode"] != "screen" {
		t.Fatalf("审计内容不符：%+v", events[0].Payload)
	}
}

// P2-5：可写交互副屏（/model、/provider、/reasoning_effort）在回合运行中必须
// 走副屏执行入口，而不是降级入队；执行窗口内 chatBusyInteractiveScreenActive
// 必须为真（chatPickerSurfaceReady 据此在 run 活跃时放行选择器），窗口结束后
// 必须复位（不得泄漏到下一次空闲选择）。
func TestRuntimeCommandHostRunsWritableInteractiveScreen(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, _, _, store := newRuntimeHostTestSession(t)
	surface := ui.NewFixedBottomSurface(nil)
	surface.EnableForTest(80, 24)
	session.Surface = surface
	releaseCapture := beginChatInputShadowLevel(session, chatInputOwnerBusyCapture)
	defer releaseCapture()

	var (
		dispatched   []string
		insideWindow bool
	)
	withChatBusyScreenTestHooks(t, true, func(s *ChatSession, line string) bool {
		dispatched = append(dispatched, line)
		insideWindow = chatBusyInteractiveScreenActive(s)
		return true
	})

	for _, line := range []string{"/model", "/provider", "/reasoning_effort"} {
		if !runtimeCommandHostFor(session).SubmitBusy(line) {
			t.Fatalf("%s（可写交互副屏）应在宿主内执行", line)
		}
		if !insideWindow {
			t.Fatalf("%s 执行窗口内 chatBusyInteractiveScreenActive 必须为真", line)
		}
		if chatBusyInteractiveScreenActive(session) {
			t.Fatalf("%s 执行后忙时交互窗口必须复位", line)
		}
	}
	if len(dispatched) != 3 {
		t.Fatalf("三个可写交互副屏命令都应进入副屏执行入口：%v", dispatched)
	}
	events := store.runtimeInteractions()
	if len(events) != 3 {
		t.Fatalf("应产生 3 条审计事件，实际 %d", len(events))
	}
	for _, event := range events {
		if event.Payload["result"] != "executed" || event.Payload["mode"] != "screen" {
			t.Fatalf("审计内容不符：%+v", event.Payload)
		}
	}
}

func TestRuntimeCommandHostDegradesNonWhitelistedScreen(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, coordinator, output, store := newRuntimeHostTestSession(t)
	releaseCapture := beginChatInputShadowLevel(session, chatInputOwnerBusyCapture)
	defer releaseCapture()

	dispatched := false
	withChatBusyScreenTestHooks(t, true, func(*ChatSession, string) bool {
		dispatched = true
		return true
	})
	// /theme select 是 screen+live+screen-interactive 的写入类 picker，未列入
	// 忙时白名单（核验结论见 chat_busy_screen_exec.go 文件头），必须降级入队。
	if runtimeCommandHostFor(session).SubmitBusy("/theme select") {
		t.Fatal("非白名单 screen 命令（/theme select）必须降级入队")
	}
	if dispatched {
		t.Fatal("非白名单 screen 命令不得进入副屏执行入口")
	}
	events := store.runtimeInteractions()
	if len(events) != 1 || events[0].Payload["result"] != "degraded" || events[0].Payload["mode"] != "screen" {
		t.Fatalf("降级审计内容不符：%+v", events)
	}
	// INV-10/T25：降级必须给出显式提示。
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	if got := output.String(); !strings.Contains(got, "已降级入队") || !strings.Contains(got, "尚未开通忙时副屏通道") {
		t.Fatalf("非首批 screen 降级提示缺失：%q", got)
	}
}

// 能力门 fail-closed：未注入替身时（无 surface/无 TTY）必须降级，
// 既有 T6 行为不变。
func TestRuntimeCommandHostScreenCapabilityFailClosed(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, coordinator, output, _ := newRuntimeHostTestSession(t)
	releaseCapture := beginChatInputShadowLevel(session, chatInputOwnerBusyCapture)
	defer releaseCapture()

	if runtimeCommandHostFor(session).SubmitBusy("/todos") {
		t.Fatal("无副屏能力时必须降级入队")
	}
	if chatBusyScreenCapability(session) {
		t.Fatal("无 surface 的会话不应通过副屏能力门")
	}
	// INV-10/T25：无备用屏能力也必须显式提示。
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	if got := output.String(); !strings.Contains(got, "已降级入队") || !strings.Contains(got, "不支持忙时副屏") {
		t.Fatalf("能力门降级提示缺失：%q", got)
	}
}

// T24：L0 modal（审批/提问等）活跃时请求 S 档必须降级 D，且**不发生租约获取**
// ——能力门通过后由仲裁器拦截，副屏执行入口（租约获取发生地）必须完全不被
// 调用，租约等待预算也不得被改写。
func TestRuntimeCommandHostScreenDegradesWhenArbitrationBlocked(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, coordinator, output, store := newRuntimeHostTestSession(t)
	surface := ui.NewFixedBottomSurface(nil)
	surface.EnableForTest(80, 24)
	session.Surface = surface

	// L0 modal 活跃：与审批/提问复用同一属主。
	releaseModal := beginChatInputShadowLevel(session, chatInputOwnerModal)
	defer releaseModal()

	dispatched := false
	withChatBusyScreenTestHooks(t, true, func(*ChatSession, string) bool {
		dispatched = true
		return true
	})

	if runtimeCommandHostFor(session).SubmitBusy("/todos") {
		t.Fatal("modal 活跃时 S 档必须降级入队，不得进入副屏")
	}
	if dispatched {
		t.Fatal("modal 活跃时不得进入副屏执行入口（租约获取）")
	}
	if got := surface.AlternateScreenWaitBudget(); got != 0 {
		t.Fatalf("降级路径不得改写租约等待预算，实际 %s", got)
	}
	events := store.runtimeInteractions()
	if len(events) != 1 || events[0].Payload["result"] != "degraded" || events[0].Payload["mode"] != "screen" {
		t.Fatalf("降级审计内容不符：%+v", events)
	}
	// 模态活跃时降级提示必须保持静默：补充行会与模态画面竞争绘制。
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	if got := output.String(); strings.Contains(got, "已降级入队") {
		t.Fatalf("modal 活跃时不应渲染降级提示：%q", got)
	}
}

// T35（P2-5a）：忙碌通道准入审计。inline 且非 read 的命令会被宿主**直接执行**
// （忙时安全断言只拦 Phase B 效应），因此必须显式登记准入结论；新登记此类命令
// 时本测试失败，强制先做生效域判断（§3.8.4/D12）。
func TestRuntimeCommandRegistryBusyAdmissionAudit(t *testing.T) {
	// 目前唯一：/title <text>（next-turn）——Phase A 仅写会话元数据，下一回合由
	// 会话字段直接承载，无需 actor 重建（G.12）。
	auditedInlineWrites := map[string]string{
		"/title":   "next-turn：Phase A 写 Metadata.Title，下一回合读取",
		"/gateway": "live：connect/disconnect/reconnect 仅改网关隧道状态，不触碰 turn/会话上下文，忙时可即时执行（docs/gateway/12-connection-control.md）",
		"/stop":    "live：只调用会话级唯一中断决策点（handleChatEscapeInterruptRequest），与 Esc 等价；不写会话上下文、无 Phase B 效应（2026-10-18 入口审计 R3）",
	}

	for name, entry := range runtimeCommandRegistry {
		specs := make([]runtimeCommandSpec, 0, 1+len(entry.Variants))
		if entry.Bare != nil {
			specs = append(specs, *entry.Bare)
		}
		for _, variant := range entry.Variants {
			specs = append(specs, variant)
		}
		for _, spec := range specs {
			if spec.Mode != runtimeModeInline || spec.Effect == runtimeEffectRead {
				continue
			}
			if _, ok := auditedInlineWrites[spec.Command]; !ok {
				t.Errorf("inline+%s 命令 %q（注册键 %q）未登记忙时准入结论：需审计 Phase A 写效应或改为 screen/prompt",
					spec.Effect, spec.Command, name)
			}
		}
	}

	// 忙时 prompt 允许清单必须解析为 prompt 档（否则确认门不会被触发），且
	// matcher 必须命中（防结构化匹配与清单漂移）。
	for name, subs := range chatBusyPromptAllowed {
		for token := range subs {
			line := name
			if token != "" {
				line = name + " " + token
			}
			spec, ok := resolveRuntimeCommandSpec(line)
			if !ok || spec.Mode != runtimeModePrompt {
				t.Errorf("忙时 prompt 白名单 %q 未解析为 prompt 档：ok=%v mode=%s", line, ok, spec.Mode)
			}
			if !busyPromptCommandWhitelisted(line) {
				t.Errorf("忙时 prompt 白名单 %q 未被 matcher 命中", line)
			}
		}
	}

	// P2-6 准入规则：只读 screen 默认放行；写入/交互必须显式 BusyAllow；BusyBlock
	// 一律排除。逐变体断言准入结果 == 规则推导值（防声明漂移）。
	for name, entry := range runtimeCommandRegistry {
		specs := make([]runtimeCommandSpec, 0, 1+len(entry.Variants))
		if entry.Bare != nil {
			specs = append(specs, *entry.Bare)
		}
		for _, variant := range entry.Variants {
			specs = append(specs, variant)
		}
		for _, spec := range specs {
			if spec.Mode != runtimeModeScreen {
				continue
			}
			if !spec.Output.declared() {
				t.Errorf("screen 档命令 %q（注册键 %q）未显式声明输出类别", spec.Command, name)
				continue
			}
			want := !spec.BusyBlock && (spec.Effect == runtimeEffectRead || spec.BusyAllow)
			if got := busyScreenCommandAllowed(spec); got != want {
				t.Errorf("screen 命令 %q（注册键 %q）忙时准入=%v，期望 %v（effect=%s block=%v allow=%v）",
					spec.Command, name, got, want, spec.Effect, spec.BusyBlock, spec.BusyAllow)
			}
		}
	}
	// 核验结论必须真实落地：这些「不能与在途 turn 并发」的变体在忙时必须被排除。
	for _, line := range []string{"/export", "/agents panel", "/agents pick", "/theme select", "/theme set", "/profile pick", "/mcp select", "/routing panel", "/skills select"} {
		spec, ok := resolveRuntimeCommandSpec(line)
		if !ok {
			t.Errorf("核验排除项 %q 未在注册表登记", line)
			continue
		}
		if busyScreenCommandAllowed(spec) {
			t.Errorf("核验排除项 %q 不应放行忙时副屏", line)
		}
	}
	// 写入类放行项必须真实生效。
	for _, line := range []string{"/model", "/model gpt-x", "/provider", "/reasoning_effort", "/reasoning_effort select"} {
		spec, ok := resolveRuntimeCommandSpec(line)
		if !ok || !busyScreenCommandAllowed(spec) {
			t.Errorf("写入类放行项 %q 未放行忙时副屏：ok=%v", line, ok)
		}
	}
}
