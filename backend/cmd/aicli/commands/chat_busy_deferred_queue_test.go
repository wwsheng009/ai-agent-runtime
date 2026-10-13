package commands

import "testing"

// 挂起态（非 Ready）下，deferred 档 slash 命令应被暂存而不是丢弃
// （替代此前的「暂不接受 slash 命令」直接丢弃）。R 档（block）与非 slash 输入
// 不得被暂存。
func TestDeferBusySlashCommandFromMainLoop(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, coordinator, _, _ := newRuntimeHostTestSession(t)
	coordinator.StartWaiting() // 挂起态：非 Ready

	if !deferBusySlashCommandFromMainLoop(session, "/clear") {
		t.Fatal("D 档命令（/clear，queue+session）在挂起态应被暂存")
	}
	if deferBusySlashCommandFromMainLoop(session, "/exit") {
		t.Fatal("/exit（block/R 档）不得被暂存")
	}
	if deferBusySlashCommandFromMainLoop(session, "hello") {
		t.Fatal("非 slash 输入不得被暂存")
	}
	// 非 Ready 时不得取出（防挂起态自旋重取同一条命令）。
	if _, ok := takeDeferredSlashCommand(session); ok {
		t.Fatal("非 Ready 时不得取出暂存命令")
	}
}

// 会话 Ready 后，暂存命令按 FIFO 被取出。
func TestDeferredSlashCommandDrainedWhenReady(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, _, _, _ := newRuntimeHostTestSession(t)

	if !session.Interaction.IsReady() {
		t.Fatal("前置：新会话应为 Ready")
	}
	if !session.enqueueDeferredSlashCommand("/clear") {
		t.Fatal("入队 /clear 失败")
	}
	if !session.enqueueDeferredSlashCommand("/status") {
		t.Fatal("入队 /status 失败")
	}
	got, ok := takeDeferredSlashCommand(session)
	if !ok || got != "/clear" {
		t.Fatalf("Ready 时应按 FIFO 取出 /clear，实际 ok=%v got=%q", ok, got)
	}
	got, ok = takeDeferredSlashCommand(session)
	if !ok || got != "/status" {
		t.Fatalf("第二条应为 /status，实际 ok=%v got=%q", ok, got)
	}
	if _, ok := takeDeferredSlashCommand(session); ok {
		t.Fatal("队列取空后不应再返回命令")
	}
}

// 暂存队列有上限，防止挂起期无限增长。
func TestDeferredSlashCommandCap(t *testing.T) {
	session := &ChatSession{}
	for i := 0; i < maxDeferredSlashCommands; i++ {
		if !session.enqueueDeferredSlashCommand("/clear") {
			t.Fatalf("第 %d 条入队失败", i)
		}
	}
	if session.enqueueDeferredSlashCommand("/clear") {
		t.Fatal("超过上限应拒绝入队")
	}
}
