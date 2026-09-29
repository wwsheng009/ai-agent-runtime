package commands

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// 回归（忙时 S/prompt 档死锁）：忙时通道曾在持有 commandMu 的状态下调用
// dispatchChatCommand，主分派器的 Phase A（lockChatCommandPhaseA）再次获取
// 同一把非可重入 Mutex → 同一 goroutine 自我死锁。栈形态：
//
//	runBusyScreenCommand → chatBusyScreenRunDispatch → dispatchChatCommand
//	→ lockChatCommandPhaseA (command.go:119) [sync.Mutex.Lock]
//
// 后果：白名单命令永不执行、capture goroutine 永久持锁。
//
// 以下测试**不注入 chatBusyScreenDispatchOverride**（保持 nil，走生产两阶段
// 执行），只替换能力门与文档 runner；若回归，SubmitBusy 永不返回，测试在
// 3s 预算上失败并打印定位栈。

// busyCommandStacksForTest 在死锁回归时抓取忙时通道相关 goroutine 栈。
func busyCommandStacksForTest() string {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	var sb strings.Builder
	for _, block := range strings.Split(string(buf[:n]), "\n\n") {
		if strings.Contains(block, "runBusyScreenCommand") || strings.Contains(block, "runBusyPromptCommand") ||
			strings.Contains(block, "lockChatCommandPhaseA") || strings.Contains(block, "SubmitBusy") {
			sb.WriteString(block)
			sb.WriteString("\n\n")
		}
	}
	return sb.String()
}

// awaitBusySubmit 在独立 goroutine 中执行 SubmitBusy，超时即判定死锁回归。
func awaitBusySubmit(t *testing.T, session *ChatSession, line string) bool {
	t.Helper()
	done := make(chan bool, 1)
	go func() {
		done <- runtimeCommandHostFor(session).SubmitBusy(line)
	}()
	select {
	case ok := <-done:
		return ok
	case <-time.After(3 * time.Second):
		t.Fatalf("SubmitBusy(%q) 3s 未返回（Phase A 重复加锁死锁回归）：\n%s", line, busyCommandStacksForTest())
		return false
	}
}

// 生产路径（dispatch 替身 = nil）：Phase A 在锁内完成解析/渲染，解锁后 Phase B
// 在锁外开屏；命令被占有（审计 executed），commandMu 无泄漏。
func TestRuntimeCommandHostRunsWhitelistedScreenProductionDispatch(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")

	// Phase B 的真实能力门（chatScreenCapability）打开，文档 runner 用替身
	// 立即返回（等价 Esc），避免单测环境等待真实按键。
	chatScreenTestSeamsInstall(t, true)
	var ran bool
	chatScreenDocumentRunner = func(_ *ChatSession, _ ui.ScreenLease, options ui.DebugOverlayOptions) error {
		ran = true
		if options.Title != "任务列表" {
			t.Errorf("文档 runner 标题 = %q，期望 任务列表", options.Title)
		}
		if !strings.Contains(options.Body, "当前会话暂无待办") {
			t.Errorf("文档 runner 正文缺失 /todos 内容：%q", options.Body)
		}
		return nil // 立即关闭副屏，绝不阻塞测试
	}

	session, coordinator, output, store := newRuntimeHostTestSession(t)
	releaseCapture := beginChatInputShadowLevel(session, chatInputOwnerBusyCapture)
	defer releaseCapture()

	// 只替换忙时 S 档能力门（单测无真实 TTY）；dispatch 替身保持 nil → 生产。
	withChatBusyScreenTestHooks(t, true, nil)

	if !awaitBusySubmit(t, session, "/todos") {
		t.Fatal("/todos（S 档）应经生产两阶段执行并占有输入")
	}

	// Phase B 已执行：开屏成功则 runner 被调用；若传输不可用，框架按 D-F
	// 契约把同一文档降级渲染到主屏——两条路径都证明 Phase B 在锁外运行过。
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	t.Logf("Phase B 路径：副屏文档 runner 已执行=%v", ran)
	if !ran && !strings.Contains(output.String(), "当前会话暂无待办") {
		t.Fatalf("Phase B 既未开屏也未降级渲染文档（ran=%v）：%q", ran, output.String())
	}
	if session.Surface != nil {
		if session.Surface.LeaseActive() {
			t.Fatal("Phase B 结束后不得遗留副屏租约")
		}
		if got := session.Surface.AlternateScreenWaitBudget(); got != 0 {
			t.Fatalf("执行后租约等待预算未复位：%s", got)
		}
	}
	if !session.commandMu.TryLock() {
		t.Fatal("执行后 commandMu 必须已释放（Phase A 边界无泄漏）")
	}
	session.commandMu.Unlock()

	events := store.runtimeInteractions()
	if len(events) != 1 || events[0].Payload["result"] != "executed" || events[0].Payload["mode"] != "screen" {
		t.Fatalf("审计内容不符：%+v", events)
	}
}

// 生产路径（dispatch 替身 = nil）的 prompt 档：确认门与 Phase A 持锁，执行
// 步骤解锁后再应用 Phase B；若回归，SubmitBusy 永不返回。
func TestRuntimeCommandHostPromptProductionDispatchAppliesPhaseBOutsideLock(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, _, _, store := newRuntimeHostTestSession(t)
	releaseCapture := beginChatInputShadowLevel(session, chatInputOwnerBusyCapture)
	defer releaseCapture()

	withChatBusyPromptTestHook(t, func(*ChatSession, string, []string) (bool, bool) { return true, true })
	withChatBusyScreenTestHooks(t, true, nil) // dispatch 走生产路径

	if !awaitBusySubmit(t, session, "/queue clear") {
		t.Fatal("/queue clear（prompt 档）应在确认后被生产路径消费")
	}
	if !session.commandMu.TryLock() {
		t.Fatal("确认门退出后 commandMu 必须已释放")
	}
	session.commandMu.Unlock()

	events := store.runtimeInteractions()
	if len(events) != 1 || events[0].Payload["result"] != "executed" || events[0].Payload["mode"] != "prompt" {
		t.Fatalf("审计内容不符：%+v", events)
	}
}
