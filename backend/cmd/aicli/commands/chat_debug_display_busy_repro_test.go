package commands

// 修复验证：Agent 忙碌（非 Ready）时 /debug 系列命令允许排队，待 Ready 后
// 执行。此前 /debug display 不在 queue-safe 列表内，忙碌时被直接拒绝，
// 导致用户输入 /debug display 后界面无任何更新、也无调试信息输出。

import "testing"

func TestDebugCommandQueuedWhileBusy(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord

	// 忙碌状态
	coord.StartWaiting()
	if coord.IsReady() {
		t.Fatal("StartWaiting 后会话仍为 Ready，忙碌模拟无效")
	}

	for _, input := range []string{
		"/debug display",
		"/debug status",
		"/debug routing",
	} {
		if !chatInputCommandQueuable(session, input) {
			t.Errorf("忙碌时 %q 应允许排队（queue-safe），实际被拒绝", input)
		}
		if chatSlashCommandQueueSafe(input) != true {
			t.Errorf("chatSlashCommandQueueSafe(%q) 应为 true", input)
		}
	}

	// 非诊断/状态变更命令仍应被拒绝
	for _, input := range []string{"/clear", "/exit", "/new", "/debug on", "/debug off", "/debug export --output /tmp/dbg.zip"} {
		if chatSlashCommandQueueSafe(input) {
			t.Errorf("忙碌时 %q 不应允许排队（破坏性/状态变更命令）", input)
		}
	}
}
