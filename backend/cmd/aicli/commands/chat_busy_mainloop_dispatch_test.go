package commands

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// TestMainLoopDispatchBusySlashCommandWhileParked 锁定托管挂起（Waiting for
// subagents，§6.12）期间的功能隔断修复：
//
//   - 挂起期前台 run 已结束，忙时 capture 已停止，主循环重新成为读侧；
//     chatInputCommandAllowed 因会话非 Ready 返回 false（原隔断点）。
//   - 主循环必须把 /agents 这类 S 档只读交互副屏命令接回忙时宿主
//     （runtimeCommandHost.SubmitBusy），与运行中 capture 通道同路，
//     而不是用「不是 Ready」整体拒绝。
//   - D/R 档命令保持 fail-closed：不得被主循环通道消费。
func TestMainLoopDispatchBusySlashCommandWhileParked(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, coordinator, _, store := newRuntimeHostTestSession(t)
	surface := ui.NewFixedBottomSurface(nil)
	surface.EnableForTest(80, 24)
	session.Surface = surface

	// 与真机一致：capture 曾随前台 run 启动并物化影子仲裁层级，随后随 run
	// 结束释放；挂起期仲裁器仍存在（ok=true）且无 modal。
	releaseCapture := beginChatInputShadowLevel(session, chatInputOwnerBusyCapture)
	releaseCapture()

	coordinator.StartWaiting()
	if chatInputCommandAllowed(session, "/agents") {
		t.Fatal("前置：挂起（非 Ready）时主循环命令门应拒绝 /agents")
	}
	if !chatBusyCommandArbitrationAllows(session) {
		t.Fatal("前置：capture 释放后仲裁应放行（无 modal）")
	}

	var dispatched []string
	withChatBusyScreenTestHooks(t, true, func(s *ChatSession, line string) bool {
		dispatched = append(dispatched, line)
		return true
	})

	if !dispatchBusySlashCommandFromMainLoop(session, "/agents") {
		t.Fatal("/agents（S 档只读交互副屏）在挂起态必须被忙时宿主占有")
	}
	if len(dispatched) != 1 || dispatched[0] != "/agents" {
		t.Fatalf("副屏分派未命中：%v", dispatched)
	}

	// D 档（/model 的 picker 变体）不属于主循环忙时通道，保持原拒绝路径。
	if dispatchBusySlashCommandFromMainLoop(session, "/model") {
		t.Fatal("/model（deferred）不得被主循环忙时通道消费")
	}
	if len(dispatched) != 1 {
		t.Fatalf("deferred 命令不得触发副屏分派：%v", dispatched)
	}

	events := store.runtimeInteractions()
	if len(events) != 1 {
		t.Fatalf("应产生 1 条审计事件，实际 %d", len(events))
	}
	if events[0].Payload["result"] != "executed" || events[0].Payload["mode"] != "screen" {
		t.Fatalf("审计内容不符：%+v", events[0].Payload)
	}
}
