package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// P2 ④ resume/new 事件化（S14）的门禁：
//
// 实测结论（本切片的判定依据）：/resume、/new、/load 只切换当前会话，不产生
// turn，因此运行时 actor 不会发布 session_start/session_end（二者是 turn 边界
// 事件，见 internal/chat/actor.go 的 publish 点）。前端过去只能靠 8×300ms 轮询
// /web/api/sessions 感知切换完成。
//
// 修复：SSE handler 自己按会话身份变化合成 session_switched（P2 ④），前端改为
// 事件驱动并保留断连兜底定时器。这里锁定服务端合成逻辑、schema 文档与前端契约。

// TestChatWebSessionSwitchNotice 覆盖合成判定的纯函数口径：
// 身份未变或当前会话缺失时不通知，变化时给出前后 id。
func TestChatWebSessionSwitchNotice(t *testing.T) {
	if _, ok := chatWebSessionSwitchNotice("session-a", "session-a"); ok {
		t.Fatal("同一会话身份不应产生 session_switched")
	}
	if _, ok := chatWebSessionSwitchNotice("session-a", ""); ok {
		t.Fatal("当前会话缺失（拆卸/重建中间态）不应产生 session_switched")
	}
	if _, ok := chatWebSessionSwitchNotice("", ""); ok {
		t.Fatal("两侧都为空不应产生 session_switched")
	}

	notice, ok := chatWebSessionSwitchNotice("session-a", "session-b")
	if !ok {
		t.Fatal("会话身份变化应产生 session_switched")
	}
	if notice["session_id"] != "session-b" || notice["previous_session_id"] != "session-a" {
		t.Fatalf("session_switched 载荷错误: %#v", notice)
	}
}

// TestHandleChatWebAPIEvents_EmitsSessionSwitchedOnSwitch 验证端到端合成：
// SSE 连接建立后当前会话身份发生变化（/resume、/new 的等价物），流里出现
// session_switched 且载荷带上前后 id。
//
// 会话身份通过 provider 的原子替换来模拟主循环的切换，避免与 handler
// goroutine 争夺 ChatSession 字段（chatDebugDisplaySessionProvider 是测试接缝，
// 见 web_handlers_test.go::withWebTestSession）。
func TestHandleChatWebAPIEvents_EmitsSessionSwitchedOnSwitch(t *testing.T) {
	var holder atomic.Value // *ChatSession
	before := newWebTestSession()
	before.RuntimeSession = &runtimechat.Session{ID: "session-before"}
	holder.Store(before)

	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return holder.Load().(*ChatSession) }
	t.Cleanup(func() { chatDebugDisplaySessionProvider = old })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIEventsPath, nil).WithContext(ctx)
	rec := newSyncResponseRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		HandleChatWebAPIEvents(rec, req)
	}()

	// 等流建立（connected 先落地），再模拟一次会话切换。
	waitFor := func(needle string) bool {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(rec.BodyString(), needle) {
				return true
			}
			time.Sleep(20 * time.Millisecond)
		}
		return false
	}
	if !waitFor("event: connected") {
		t.Fatalf("SSE 流未建立: %q", rec.BodyString())
	}

	after := newWebTestSession()
	after.RuntimeSession = &runtimechat.Session{ID: "session-after"}
	holder.Store(after)

	switched := waitFor("event: session_switched")
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE handler did not exit after context cancel")
	}

	body := rec.BodyString()
	if !switched {
		t.Fatalf("会话身份变化未合成 session_switched: %q", body)
	}
	if !strings.Contains(body, `"session_id":"session-after"`) ||
		!strings.Contains(body, `"previous_session_id":"session-before"`) {
		t.Fatalf("session_switched 载荷错误: %q", body)
	}
}

// TestChatWebSSESchemaDocumentsSessionSwitched 断言 SSE 事件文档（§4.2.5）
// 收录了 session_switched：它是服务端合成事件，SourceEvent 为空。
func TestChatWebSSESchemaDocumentsSessionSwitched(t *testing.T) {
	var spec *webSSEEventSpec
	for _, candidate := range chatWebSSESchema() {
		if candidate.Event == "session_switched" {
			item := candidate
			spec = &item
			break
		}
	}
	if spec == nil {
		t.Fatal("SSE schema 缺少 session_switched 事件定义（P2 ④）")
	}
	if spec.SourceEvent != "" {
		t.Fatalf("session_switched 是服务端合成事件，SourceEvent 应为空，实际 %q", spec.SourceEvent)
	}
	fields := map[string]bool{}
	for _, field := range spec.Fields {
		fields[field.Name] = true
	}
	for _, name := range []string{"session_id", "previous_session_id"} {
		if !fields[name] {
			t.Errorf("session_switched 缺少字段 %q（P2 ④ 前端据此刷新）", name)
		}
	}
}

// TestChatWebSessionsAssetUsesSessionSwitchedEvent 是前端 asset 契约门禁：
// sse.js 必须订阅并处理 session_switched，sessions.js 必须已经去掉
// 8×300ms 轮询、改为事件驱动 + 断连兜底（删掉这些字符串后页面照常加载，
// 只是悄悄退化为轮询或完全不刷新）。
func TestChatWebSessionsAssetUsesSessionSwitchedEvent(t *testing.T) {
	sse := fetchChatWebAsset(t, "js/sse.js")
	for _, token := range []string{
		// 订阅列表：没有它，EventSource 不会把该事件交给 onSSEEvent。
		`"session_switched", "session_interrupted"`,
		`case "session_switched":`,
		`if (eventName === "session_switched") { notifySessionSwitchedCompleted(); }`,
		`import { loadSessions, notifySessionSwitchedCompleted } from "./sessions.js";`,
	} {
		if !strings.Contains(sse, token) {
			t.Errorf("js/sse.js 缺少 %q（P2 ④ session_switched 契约）", token)
		}
	}

	sessions := fetchChatWebAsset(t, "js/sessions.js")
	for _, token := range []string{
		"armSessionSwitchFallback",
		"export function notifySessionSwitchedCompleted",
		// 兜底只做一次（旧实现是 8×300ms 轮询）。
		"sessionSwitchFallbackTimer = setTimeout(",
		"session_switched", // 注释里说明事件来源（P2 ④）
	} {
		if !strings.Contains(sessions, token) {
			t.Errorf("js/sessions.js 缺少 %q（P2 ④ 事件化契约）", token)
		}
	}
	for _, gone := range []string{
		"pollResumed",
		"pollNew",
		"setTimeout(pollResumed, 300)",
		"setTimeout(pollNew, 300)",
	} {
		if strings.Contains(sessions, gone) {
			t.Errorf("js/sessions.js 仍残留轮询实现 %q（P2 ④ 应改为事件驱动）", gone)
		}
	}
}
