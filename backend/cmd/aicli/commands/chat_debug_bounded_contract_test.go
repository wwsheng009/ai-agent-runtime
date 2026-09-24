package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// B1/B3 契约回归：有界降级（?fast=1 / 超预算）与 app_state 显式可用性。
//
// 这些测试守护 E2E（scripts/test-aicli-debug-endpoints-e2e.ps1 的 2b 断言块）
// 依赖的线上契约：
//   - 无交互渲染器时 app_state 必须显式出现（available=false + 非空 reason），
//     否则读屏断言无法区分「headless/CI 进程模型」与「渲染器异常」；
//   - fast / 超预算时必须登记 skipped_sections，消费者才能把缺席区块当作
//     「未知」而不是「健康」（降级响应里零值字段没有证据力）。
// ---------------------------------------------------------------------------

// withChatDebugSession 注入 /debug/chat/status 的会话来源，测试结束自动还原。
func withChatDebugSession(t *testing.T, session *ChatSession) {
	t.Helper()
	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return session }
	t.Cleanup(func() { chatDebugDisplaySessionProvider = old })
}

// TestChatDebugAppStateExplicitWhenNoRenderer 验证 headless 会话（无 uiActor）
// 下 app_state 区块仍然出现，且带 available=false 与非空 reason。
func TestChatDebugAppStateExplicitWhenNoRenderer(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{} // Interaction == nil：headless/CI 进程模型的常态
	withChatDebugSession(t, session)

	snap := BuildChatDebugDisplaySnapshot()
	if !snap.Available {
		t.Fatal("有会话时应返回 available=true")
	}
	if snap.AppState == nil {
		t.Fatal("无渲染器时 app_state 必须显式输出，不能整个区块消失")
	}
	if snap.AppState.Available {
		t.Fatal("无渲染器时 app_state.available 应为 false")
	}
	if snap.AppState.Reason == "" {
		t.Fatal("app_state.reason 必须非空")
	}
	if !strings.Contains(snap.AppState.Reason, "Interaction is nil") {
		t.Fatalf("reason 应指出 Interaction 为 nil，实际为 %q", snap.AppState.Reason)
	}
}

// TestChatDebugFastSnapshotRegistersSkippedSections 验证 fast 快照跳过重区块
// 并在 skipped_sections 登记，而不是静默输出零值。
func TestChatDebugFastSnapshotRegistersSkippedSections(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	withChatDebugSession(t, &ChatSession{})

	snap := BuildChatDebugDisplaySnapshotWithOptions(ChatDebugDisplayFastOptions())
	if !snap.Fast {
		t.Fatal("fast 快照应置 fast=true")
	}
	if snap.Files != nil || snap.Storage != nil {
		t.Fatal("fast 快照不应构建 files/storage 重区块")
	}
	if snap.Agents != nil {
		t.Fatal("fast 快照不应构建 agents 重区块（registry 审计与后台 reconciler 争会话库单连接）")
	}
	for _, want := range []string{"files", "storage", "agents"} {
		if !containsString(snap.SkippedSections, want) {
			t.Fatalf("skipped_sections 应包含 %q，实际为 %v", want, snap.SkippedSections)
		}
	}
}

// TestChatDebugFastSnapshotSkipsPlanLayoutWithRenderer 验证有渲染器时 fast
// 只做便宜的规划输入走查，布局探测退化为 skipped（inputs-only）。
func TestChatDebugFastSnapshotSkipsPlanLayoutWithRenderer(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{}
	coordinator := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	coordinator.SetWriter(&bytes.Buffer{})
	if actor := coordinator.ensureUIActor(); actor == nil {
		t.Fatal("expected UI actor")
	}
	withChatDebugSession(t, session)

	snap := BuildChatDebugDisplaySnapshotWithOptions(ChatDebugDisplayFastOptions())
	if snap.AppState == nil || !snap.AppState.Available {
		t.Fatal("有渲染器时 app_state.available 应为 true")
	}
	if snap.AppState.Plan == nil {
		t.Fatal("fast 快照仍应输出 plan_inputs（便宜且恒定）")
	}
	if snap.AppState.Plan.LayoutProbed {
		t.Fatal("fast 快照不应执行布局探测")
	}
	if !containsString(snap.SkippedSections, "plan_layout") {
		t.Fatalf("skipped_sections 应包含 plan_layout，实际为 %v", snap.SkippedSections)
	}
}

// TestChatDebugJSONFastWireContract 验证 fast 降级在线上 JSON 里可被消费者
// 识别（fast/skipped_sections 字段真的被序列化）。
func TestChatDebugJSONFastWireContract(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	withChatDebugSession(t, &ChatSession{})

	body, err := MarshalChatDebugDisplayJSONWithOptions(ChatDebugDisplayFastOptions())
	if err != nil {
		t.Fatalf("MarshalChatDebugDisplayJSONWithOptions 失败: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("fast 快照不是有效 JSON: %v", err)
	}
	if fast, _ := decoded["fast"].(bool); !fast {
		t.Fatalf("JSON 应含 fast=true，实际为 %v", decoded["fast"])
	}
	skipped, _ := decoded["skipped_sections"].([]interface{})
	if len(skipped) == 0 {
		t.Fatalf("JSON 应含非空 skipped_sections，实际为 %v", decoded["skipped_sections"])
	}
}

// TestChatDebugTextAppStateUnavailableLine 验证 ?format=text 在无渲染器时
// 打印显式 unavailable 行（读屏断言的正文本基线）。
func TestChatDebugTextAppStateUnavailableLine(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	withChatDebugSession(t, &ChatSession{})

	text := BuildChatDebugDisplayText()
	if !strings.Contains(text, "AppState:") {
		t.Fatalf("文本应包含 AppState 行，实际为:\n%s", text)
	}
	if !strings.Contains(text, "unavailable") {
		t.Fatalf("无渲染器时文本应包含 unavailable，实际为:\n%s", text)
	}
}

// TestChatDebugTextFastSkipsStorage 验证文本降级路径同样显式标注跳过。
func TestChatDebugTextFastSkipsStorage(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	withChatDebugSession(t, &ChatSession{})

	text := BuildChatDebugDisplayTextWithOptions(ChatDebugDisplayFastOptions())
	if !strings.Contains(text, "skipped (fast/deadline)") {
		t.Fatalf("fast 文本应显式标注 skipped，实际为:\n%s", text)
	}
}

// TestChatDebugTextFastSkipsAgents 验证文本降级路径不再同步等待 agent registry
// 审计（会话库单连接，风暴期被后台 reconciler 占用；实测曾让 status 阻塞数秒），
// 且以显式 skipped 行代替静默缺席。全量路径仍须渲染该区块。
func TestChatDebugTextFastSkipsAgents(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	withChatDebugSession(t, &ChatSession{})

	fastText := BuildChatDebugDisplayTextWithOptions(ChatDebugDisplayFastOptions())
	if !strings.Contains(fastText, "Agents:") || !strings.Contains(fastText, "skipped (fast/deadline)") {
		t.Fatalf("fast 文本应显式标注 agents 区块 skipped，实际为:\n%s", fastText)
	}

	fullText := BuildChatDebugDisplayTextWithOptions(ChatDebugDisplayOptions{})
	if strings.Contains(fullText, "Agents: skipped") {
		t.Fatalf("全量文本不应把 agents 区块标记为 skipped，实际为:\n%s", fullText)
	}
}
