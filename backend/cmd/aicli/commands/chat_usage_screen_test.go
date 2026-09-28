package commands

// /usage 独立屏幕迁移回归：
//  - 统一 interactive TTY 中 /usage 视图变体只携带 CommandResult.Screen
//    （usage.screen Spec），不提交 Scene cell（批次 5：旧 OpenUsageScreen
//    字段已删除，请求载荷改由解析层断言）；
//  - 查看器 body 必须包含缓存总览 + 会话缓存请求列表（§6.4 纯渲染函数）；
//  - 备用屏不可用的统一会话降级为 §6.4 文档 cell，绝不静默吞掉 /usage。

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// newUnifiedUsageCacheSession 在 newCacheTestSession 的缓存数据会话上挂载
// 统一渲染链路（bridge + TerminalSession surface），用于验证 /usage 的
// alternate-screen 请求与降级行为。测试环境无真实 TTY，备用屏打开器始终
// fail-closed，因此这里只验证请求携带与文档降级两条路径。
func newUnifiedUsageCacheSession(t *testing.T) (*ChatSession, *chatInteractionCoordinator, *bytes.Buffer) {
	t.Helper()
	session, _, _ := newCacheTestSession(t)

	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(100, 40)
	coordinator.SetSurface(surface)

	output := &bytes.Buffer{}
	if !coordinator.enableUnifiedRendererWithWriter(output) {
		t.Fatal("unified renderer did not attach")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	output.Reset()
	return session, coordinator, output
}

func TestCanOpenChatUsageScreenGates(t *testing.T) {
	if canOpenChatUsageScreen(nil) {
		t.Fatal("canOpenChatUsageScreen(nil) must be false")
	}
	if canOpenChatUsageScreen(&ChatSession{}) {
		t.Fatal("canOpenChatUsageScreen must be false without a unified surface")
	}
	// 统一 surface 已挂载但无全屏 TTY 终端（测试环境 Layout 为 nil）：必须
	// fail-closed，与 /debug overlay、/model picker 同一门槛。
	session, _, _ := newUnifiedUsageCacheSession(t)
	if canOpenChatUsageScreen(session) {
		t.Fatal("canOpenChatUsageScreen must be false without a full-screen TTY")
	}
}

func TestExecuteStructuredUsageCommandUnifiedTTYRequestsUsageScreen(t *testing.T) {
	session, _, _ := newUnifiedUsageCacheSession(t)

	cases := []struct {
		command string
		mode    string
		check   func(UsageScreenRequest) bool
	}{
		{"/usage", usageScreenModeOverview, func(req UsageScreenRequest) bool { return true }},
		{"/usage cache requests 5", usageScreenModeRequests, func(req UsageScreenRequest) bool { return req.Limit == 5 }},
		{"/usage cache trace msg-x", usageScreenModeTrace, func(req UsageScreenRequest) bool { return req.TraceID == "msg-x" }},
		// 批次 7.3 聚合视图：同一 ScreenLease 契约（只携带请求，不提交 Scene cell）。
		{"/usage tools 7", usageScreenModeTools, func(req UsageScreenRequest) bool { return req.Limit == 7 }},
		{"/usage subagents --failed", usageScreenModeSubagents, func(req UsageScreenRequest) bool { return req.FailedOnly }},
		{"/usage errors top 5", usageScreenModeErrors, func(req UsageScreenRequest) bool { return req.Top == 5 }},
	}
	for _, tc := range cases {
		req, errText := resolveUsageViewRequest(parseUsageCommandArgs(tc.command))
		if errText != "" {
			t.Fatalf("%s parse error: %s", tc.command, errText)
		}
		if req.Mode != tc.mode || !tc.check(req) {
			t.Fatalf("%s typed request = %+v，期望 mode=%s 且载荷满足断言", tc.command, req, tc.mode)
		}

		result, handled, err := tryExecuteStructuredChatCommand(session, tc.command)
		if err != nil || !handled {
			t.Fatalf("%s handled=%v err=%v", tc.command, handled, err)
		}
		if result.Screen == nil || result.Screen.ID != "usage.screen" {
			t.Fatalf("%s must request the usage screen spec, got %+v", tc.command, result.Screen)
		}
		if got := ui.RenderDocumentPlain(result.Document()); strings.TrimSpace(got) != "" {
			t.Fatalf("%s must not carry a Scene-cell document, got %q", tc.command, got)
		}
	}

	// 参数错误仍是文档 cell（校验错误不属于视图内容，也不打开备用屏）。
	for _, command := range []string{"/usage cache requests 0", "/usage tools 0", "/usage subagents --failed 0", "/usage errors top abc"} {
		result, _, _ := tryExecuteStructuredChatCommand(session, command)
		if result.Screen != nil {
			t.Fatalf("%s must not open the usage screen, got %+v", command, result.Screen)
		}
		if plain := ui.RenderDocumentPlain(result.Document()); !strings.Contains(plain, "非法") {
			t.Fatalf("%s invalid args document = %q", command, plain)
		}
	}
}

func TestBuildUsageScreenBodyIncludesCacheRequestList(t *testing.T) {
	session, bus, storage := newCacheTestSession(t)
	sessionID := session.RuntimeSession.ID

	// 历史：user(msg-u1) → assistant(msg-a1)，同一 turn。
	ctx := context.Background()
	userMsg := *runtimetypes.NewUserMessage("hello")
	userMsg.Metadata = runtimetypes.Metadata{"message_id": "msg-u1", "turn_id": "turn-t1"}
	assistantMsg := runtimetypes.Message{Role: "assistant", Content: "hi"}
	assistantMsg.Metadata = runtimetypes.Metadata{"message_id": "msg-a1", "turn_id": "turn-t1"}
	session.RuntimeSession.AddMessage(userMsg)
	session.RuntimeSession.AddMessage(assistantMsg)
	if err := storage.Save(ctx, session.RuntimeSession); err != nil {
		t.Fatalf("storage.Save: %v", err)
	}

	publishCacheStarted(t, bus, sessionID, "req-1", map[string]interface{}{"logical_turn_id": "turn-t1"})
	publishCacheFinished(t, bus, sessionID, "req-1", map[string]interface{}{
		"logical_turn_id":         "turn-t1",
		"usage_cache_read_tokens": 100,
	})

	// 总览视图：缓存总览 + 会话缓存请求列表必须同时出现在同一屏幕 body 中。
	body, ok := buildUsageScreenBody(session, UsageScreenRequest{Mode: usageScreenModeOverview})
	if !ok {
		t.Fatal("overview screen body failed to build")
	}
	if !strings.Contains(body, "会话缓存统计") || !strings.Contains(body, "请求总数: 1") {
		t.Fatalf("overview screen body missing stats:\n%s", body)
	}
	if !strings.Contains(body, "最近 1 条 LLM 请求（共 1 条，新→旧）") || !strings.Contains(body, "req-1") {
		t.Fatalf("overview screen body missing session cache request list:\n%s", body)
	}

	requestsBody, ok := buildUsageScreenBody(session, UsageScreenRequest{Mode: usageScreenModeRequests, Limit: 5})
	if !ok || !strings.Contains(requestsBody, "最近 1 条 LLM 请求") || !strings.Contains(requestsBody, "req-1") {
		t.Fatalf("requests screen body = ok=%v body:\n%s", ok, requestsBody)
	}

	traceBody, ok := buildUsageScreenBody(session, UsageScreenRequest{Mode: usageScreenModeTrace, TraceID: "msg-a1"})
	if !ok || !strings.Contains(traceBody, "msg-a1") || !strings.Contains(traceBody, "产出请求: req-1") {
		t.Fatalf("trace screen body = ok=%v body:\n%s", ok, traceBody)
	}
}

func TestDispatchChatCommandUnifiedUsageDegradesToDocumentWhenScreenUnavailable(t *testing.T) {
	session, coordinator, _ := newUnifiedUsageCacheSession(t)

	// 测试环境无真实 TTY（Layout 为 nil）→ 备用屏不可用：/usage 必须降级为
	// §6.4 文档 cell，绝不静默，也绝不再现 unified gate 错误。
	dispatchChatCommand(session, "/usage", false)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.AppState()
	var transcript strings.Builder
	for _, cell := range state.Transcript.Cells {
		transcript.WriteString(cell.Source)
		transcript.WriteByte('\n')
	}
	merged := transcript.String()
	if strings.Contains(merged, "/usage 尚未迁移到统一渲染命令通道") {
		t.Fatalf("/usage fell through to the unified gate: %s", merged)
	}
	if !strings.Contains(merged, "会话缓存统计") {
		t.Fatalf("unified /usage must degrade to the §6.4 document cell, transcript:\n%s", merged)
	}
}
