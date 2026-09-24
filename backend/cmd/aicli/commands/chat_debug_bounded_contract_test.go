package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

// ---------------------------------------------------------------------------
// agents 区块缓存契约：轮询型 HTTP 快照读「样本 + 年龄」，永不阻塞会话库。
//
// 会话库连接池恒为单连接，且与后台 reconciler 争用；agents 区块的三项数据
// （registry 行 + 一致性审计、agent graph、mailbox）都要过这条连接，实测争用
// 时单次采集阻塞数秒。因此 HTTP 快照路径改为：命中即返回，过期样本照常返回并
// 后台刷新，冷启动显式标注 collecting。消费者必须能区分「尚未采集」与
// 「没有 agent / 健康」——降级响应里缺席字段的零值没有证据力。
// ---------------------------------------------------------------------------

// stubChatAgentBlockCollector 注入采集行为，返回 release 用于放行阻塞中的采集
// 与调用计数；测试结束自动还原，避免污染同包其它用例。
func stubChatAgentBlockCollector(t *testing.T, collect func(*ChatSession) chatAgentBlockSample) {
	t.Helper()
	resetChatAgentBlockCache()
	setChatAgentBlockCollectOverride(collect)
	t.Cleanup(func() {
		setChatAgentBlockCollectOverride(nil)
		resetChatAgentBlockCache()
	})
}

// waitForChatAgentBlockSample 轮询等待后台采集落地，返回样本快照。
func waitForChatAgentBlockSample(t *testing.T, session *ChatSession) chatAgentBlockSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if snap := chatAgentBlockSnapshotFor(session); !snap.Collecting() {
			return snap
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("后台采集未在 2s 内落地")
	return chatAgentBlockSnapshot{}
}

// TestChatAgentBlockSnapshotColdStartNeverBlocks 验证冷启动读取立即返回
// collecting（而不是同步等采集），且采集确实在后台推进。
func TestChatAgentBlockSnapshotColdStartNeverBlocks(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	released := false
	releaseFn := func() {
		if !released {
			released = true
			close(release)
		}
	}
	stubChatAgentBlockCollector(t, func(*ChatSession) chatAgentBlockSample {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return chatAgentBlockSample{CollectedAt: time.Now(), RegistryLine: "registry=stub"}
	})
	t.Cleanup(releaseFn)

	session := &ChatSession{}
	begin := time.Now()
	snap := chatAgentBlockSnapshotFor(session)
	if elapsed := time.Since(begin); elapsed > 250*time.Millisecond {
		t.Fatalf("冷启动读取不得等待采集，实际耗时 %s", elapsed)
	}
	if !snap.Collecting() {
		t.Fatal("冷启动必须报告 collecting（尚无样本），否则消费者会把空区块读成健康")
	}
	if snap.AgeLabel() != "collecting" {
		t.Fatalf("冷启动年龄标注应为 collecting，实际为 %q", snap.AgeLabel())
	}
	// 后台采集由读取触发；goroutine 调度有延迟，这里给一个明确的上限等待。
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("冷启动必须启动后台采集")
	}

	releaseFn()
	settled := waitForChatAgentBlockSample(t, session)
	if settled.Sample.RegistryLine != "registry=stub" {
		t.Fatalf("落地样本应为采集结果，实际为 %q", settled.Sample.RegistryLine)
	}
}

// TestChatAgentBlockSnapshotReusesSampleWithinTTL 验证 TTL 内不重复采集，且年龄
// 由读取时刻计算（不是冻结在采集那一刻）。
func TestChatAgentBlockSnapshotReusesSampleWithinTTL(t *testing.T) {
	var collects int32
	stubChatAgentBlockCollector(t, func(*ChatSession) chatAgentBlockSample {
		atomic.AddInt32(&collects, 1)
		return chatAgentBlockSample{CollectedAt: time.Now(), RegistryLine: "registry=stub"}
	})

	session := &ChatSession{}
	first := waitForChatAgentBlockSample(t, session)
	second := chatAgentBlockSnapshotFor(session)
	if !second.Sample.CollectedAt.Equal(first.Sample.CollectedAt) {
		t.Fatal("TTL 内应复用同一份样本，不得重新采集")
	}
	if second.Age < first.Age {
		t.Fatalf("样本年龄必须随读取时刻单调增长，first=%s second=%s", first.Age, second.Age)
	}
	if second.Stale {
		t.Fatalf("新鲜样本不应标记 stale，age=%s", second.Age)
	}
	if got := atomic.LoadInt32(&collects); got != 1 {
		t.Fatalf("TTL 内应只采集一次，实际 %d 次", got)
	}
}

// TestChatDebugAgentsBlockLabelsCollecting 验证线上契约：采集未完成时 JSON 与
// 文本都显式标注 collecting，且不给出零值 registry/graph（零值没有证据力）。
func TestChatDebugAgentsBlockLabelsCollecting(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	release := make(chan struct{})
	stubChatAgentBlockCollector(t, func(*ChatSession) chatAgentBlockSample {
		<-release
		return chatAgentBlockSample{CollectedAt: time.Now()}
	})
	t.Cleanup(func() { close(release) })

	withChatDebugSession(t, &ChatSession{})

	body, err := MarshalChatDebugDisplayJSONWithOptions(ChatDebugDisplayBoundedOptions())
	if err != nil {
		t.Fatalf("MarshalChatDebugDisplayJSONWithOptions 失败: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("有界快照不是有效 JSON: %v", err)
	}
	agents, ok := decoded["agents"].(map[string]interface{})
	if !ok {
		t.Fatalf("有界全量快照必须包含 agents 区块，实际为 %v", decoded["agents"])
	}
	if collecting, _ := agents["collecting"].(bool); !collecting {
		t.Fatalf("采集未完成时 agents 必须显式 collecting=true，实际为 %v", agents)
	}
	if age, _ := agents["age_seconds"].(float64); age != -1 {
		t.Fatalf("采集未完成时 age_seconds 应为 -1，实际为 %v", agents["age_seconds"])
	}
	if _, present := agents["registry"]; present {
		t.Fatalf("采集未完成时不得给出 registry（零值会被读成健康），实际为 %v", agents)
	}
	consistency, _ := agents["consistency"].(string)
	if !strings.Contains(consistency, "collecting") {
		t.Fatalf("采集未完成时 consistency 应标注 collecting，实际为 %q", consistency)
	}

	text := BuildChatDebugDisplayTextWithOptions(ChatDebugDisplayBoundedOptions())
	if !strings.Contains(text, "collecting") {
		t.Fatalf("文本快照应显式标注 collecting，实际为:\n%s", text)
	}
	if strings.Contains(text, "Agents: skipped") {
		t.Fatalf("有界全量路径不应把 agents 区块标记为 skipped，实际为:\n%s", text)
	}
}
