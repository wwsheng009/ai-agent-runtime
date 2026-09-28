package commands

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func TestChatBusyCommandUnsafeEffect(t *testing.T) {
	safe := CommandResult{Action: CommandContinue}
	if got := chatBusyCommandUnsafeEffect(safe); got != "" {
		t.Fatalf("只读结果不应报告效应，实际 %q", got)
	}

	cases := map[string]CommandResult{
		"quit":             {Action: CommandQuit},
		"replay-history":   {ReplayHistory: true},
		"transcript-pager": {Screen: &chatScreenSpec{ID: "transcript.pager", Title: "历史记录"}},
		"debug-display":    {Screen: &chatScreenSpec{ID: "debug.display", Title: "调试面板"}},
		"send-objective":   {SendObjective: "objective"},
		"send-message":     {SendMessageAfterCommit: "message"},
		"send-skill-turn":  {SendSkillTurn: &SendSkillTurnRequest{}},
		"backtrack-apply":  {ApplyBacktrack: &BacktrackApplyRequest{}},
		"composer-draft":   {RestoreComposerDraft: "draft"},
	}
	for want, result := range cases {
		if got := chatBusyCommandUnsafeEffect(result); got != want {
			t.Fatalf("效应 %s 未按预期报告，实际 %q", want, got)
		}
	}
}

// P1-4：Phase A 锁语义（nil 会话空操作、持锁期间 TryLock 失败、释放后可获取）。
func TestLockChatCommandPhaseA(t *testing.T) {
	unlock := lockChatCommandPhaseA(nil)
	unlock()

	session := &ChatSession{}
	unlock = lockChatCommandPhaseA(session)
	if session.commandMu.TryLock() {
		session.commandMu.Unlock()
		t.Fatal("Phase A 持锁期间 TryLock 必须失败")
	}
	unlock()
	if !session.commandMu.TryLock() {
		t.Fatal("Phase A 释放后 TryLock 必须成功")
	}
	session.commandMu.Unlock()
}

// P1-4：dispatchChatCommand 返回时不得持有 commandMu（结构化与 legacy 两条出口）。
func TestDispatchChatCommandReleasesCommandPhaseALock(t *testing.T) {
	for _, command := range []string{"/help", "/not-a-command"} {
		session := &ChatSession{}
		dispatchChatCommand(session, command, true)
		if !session.commandMu.TryLock() {
			t.Fatalf("%q 返回后 commandMu 仍被持有（Phase A 边界泄漏）", command)
		}
		session.commandMu.Unlock()
	}
}

// INV-7：仲裁器未注册或 modal 活跃时必须 fail-closed。
func TestChatBusyCommandArbitrationAllowsFailClosed(t *testing.T) {
	session := &ChatSession{}
	if chatBusyCommandArbitrationAllows(session) {
		t.Fatal("未注册仲裁器时必须降级")
	}
	releaseCapture := beginChatInputShadowLevel(session, chatInputOwnerBusyCapture)
	if !chatBusyCommandArbitrationAllows(session) {
		t.Fatal("busy capture 注册后应放行")
	}
	releaseModal := beginChatInputShadowLevel(session, chatInputOwnerModal)
	if chatBusyCommandArbitrationAllows(session) {
		t.Fatal("modal 活跃时必须降级")
	}
	releaseModal()
	releaseCapture()
}

// 执行器门禁：nil 会话、非 immediate 命令、无统一渲染面都必须降级。
func TestExecuteBusySlashCommandGuards(t *testing.T) {
	if executeBusySlashCommand(nil, "/help") {
		t.Fatal("nil 会话不得执行")
	}

	session := &ChatSession{}
	// 总闸显式关闭：即使首批命令也应降级。
	t.Setenv(chatBusyCommandEnv, "off")
	if executeBusySlashCommand(session, "/help") {
		t.Fatal("总闸显式关闭时必须降级")
	}

	t.Setenv(chatBusyCommandEnv, "on")
	if executeBusySlashCommand(session, "/model") {
		t.Fatal("非 immediate 命令不得走忙时通道")
	}
	if executeBusySlashCommand(session, "/help") {
		t.Fatal("无统一渲染面/交互协调器时必须降级")
	}
}

// P1-5 happy path：统一渲染面 + busy capture 层级注册后，首批只读命令立即执行，
// 且执行后不残留 Phase A 锁。
func TestExecuteBusySlashCommandUnifiedSession(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 18)
	coordinator.SetSurface(surface)
	var presenterOutput bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&presenterOutput) {
		t.Fatal("unified renderer did not attach")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	presenterOutput.Reset()

	releaseCapture := beginChatInputShadowLevel(session, chatInputOwnerBusyCapture)
	defer releaseCapture()

	if !executeBusySlashCommand(session, "/help") {
		t.Fatal("/help 应在忙时通道内立即执行")
	}
	if !session.commandMu.TryLock() {
		t.Fatal("忙时命令执行后 commandMu 必须已释放")
	}
	session.commandMu.Unlock()

	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	if !strings.Contains(presenterOutput.String(), "/help") {
		t.Fatalf("忙时命令结果未到达统一渲染面，输出=%q", presenterOutput.String())
	}
}

// M4：queuedInputDrain / queuedInputEchoed 必须可并发读写（-race 验证）。
func TestChatSessionQueuedInputFlagsConcurrentAccess(t *testing.T) {
	session := &ChatSession{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for j := 0; j < 256; j++ {
				session.setQueuedInputDrainActive((j+seed)%2 == 0)
				_ = session.queuedInputDrainActive()
				session.setQueuedInputEchoed((j+seed)%3 == 0)
				_ = session.queuedInputEchoedValue()
			}
		}(i)
	}
	wg.Wait()
}
