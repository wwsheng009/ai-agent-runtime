package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// chat_schedule_driver.go 的诊断出口契约（§8.5 stderr/stdout 契约）：
//
//   - 交互式会话登记为诊断出口时，调度驱动消息必须投递 TUI 动态栏
//     （NotifyChatDiagnostic），绝不直接写 stderr——驱动监督循环运行在后台
//     goroutine 上，candidate→standby 抖动时每轮 retry 都会输出，裸字节会
//     落在 FixedBottomSurface 的底部保留区上，把状态栏覆盖成半截文本；
//   - 未登记交互出口（启动早期 / 非交互 / JSON 模式）时回退 stderr，
//     保持非交互场景下的可观测性。
func TestChatScheduleDriverWarnfPrefersDiagnosticNoticeOverStderr(t *testing.T) {
	previous := chatDiagnosticSink.Swap(nil)
	t.Cleanup(func() { chatDiagnosticSink.Store(previous) })

	// 未登记交互出口：回退 stderr（启动早期 / 非交互 / JSON 模式）。
	_, stderr := captureStdoutStderr(t, func() {
		chatScheduleDriverWarnf("deferring to creator-affinity scheduler (%s)", "chat")
	})
	if !strings.HasPrefix(stderr, "schedule-driver: deferring to creator-affinity scheduler (chat)") {
		t.Fatalf("stderr fallback should keep the schedule-driver prefix and body, got %q", stderr)
	}

	// 登记交互出口：投递动态栏，不产生 stderr 字节。
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	var history bytes.Buffer
	coordinator.SetWriter(&history)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	coordinator.SetSurface(surface)
	session.Surface = surface
	chatDiagnosticSink.Store(coordinator)

	const body = "candidate → standby (creator session hosted by another scheduler)"
	accepted := false
	_, stderr = captureStdoutStderr(t, func() {
		accepted = NotifyChatDiagnostic("schedule-driver: " + body)
		if accepted {
			// 与 chatScheduleDriverWarnf 完全同路径：通知成功即不再写 stderr。
			return
		}
		chatScheduleDriverWarnf("%s", body)
		coordinator.waitUIActorIdle()
	})
	if !accepted {
		t.Fatal("interactive session did not accept the schedule-driver diagnostic")
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("schedule-driver diagnostic leaked to stderr: %q", stderr)
	}
	// ⚠ 前缀 + 终端宽度紧凑截断（compactStatusValue），动态栏是语义投影不是
	// 原文回放：只断言告警语义可达（⚠ + 消息头）且保持单行。
	row := chatDynamicStatusRowText(coordinator, 160)
	if !strings.Contains(row, "⚠ schedule-driver: candidate") {
		t.Fatalf("schedule-driver diagnostic missing from dynamic status row: %q", row)
	}
	if strings.ContainsAny(row, "\r\n") {
		t.Fatalf("dynamic status row must stay single-line: %q", row)
	}
	if snapshot := bridge.sceneSnapshot(); snapshot != nil && len(snapshot.Cells) != 0 {
		t.Fatalf("schedule-driver diagnostic leaked into the scene transcript: %+v", snapshot.Cells)
	}
	if got := history.String(); strings.Contains(got, "schedule-driver") {
		t.Fatalf("schedule-driver diagnostic leaked into durable history: %q", got)
	}
}
