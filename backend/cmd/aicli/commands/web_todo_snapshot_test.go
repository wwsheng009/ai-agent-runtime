// web_todo_snapshot_test.go — 任务列表面板两条数据通道的契约回归。
//
// 背景：micro web client 的「任务列表」浮层（composer 上沿）有两个数据源：
//   - 实时：/web/api/events 的 tool_end.todo_snapshot；
//   - 回放：/web/api/screen?format=json 的 todo_snapshot（会话 transcript 最近一次）。
//
// 这里锁住三件事：形状（items 只含 content/status/active_form）、裁剪
// （坏条目丢弃、整组不可用时不给字段）、以及回放通道的「嵌套优先 + 平铺回退」。
package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// todoSnapshotPayload 构造 todos 工具完成事件载荷（含 protocol_result.metadata.todo_snapshot）。
func todoSnapshotPayload(items []map[string]interface{}) map[string]interface{} {
	snapshot := map[string]interface{}{
		"items":      items,
		"session_id": "session_20260925120000_Todo0001",
		"goal_id":    "goal-1",
	}
	return map[string]interface{}{
		"turn_id":      "turn-1",
		"tool_name":    "todos",
		"tool_call_id": "call-todos-1",
		"protocol_result": map[string]interface{}{
			"metadata": map[string]interface{}{"todo_snapshot": snapshot},
		},
	}
}

func todoFinishedEvent(payload map[string]interface{}) runtimeevents.Event {
	return runtimeevents.Event{
		Type:      runtimechat.EventToolFinished,
		SessionID: "session_20260925120000_Todo0001",
		Payload:   payload,
	}
}

func todoSnapshotField(t *testing.T, data map[string]interface{}) map[string]interface{} {
	t.Helper()
	raw, ok := data["todo_snapshot"]
	if !ok {
		t.Fatalf("tool_end 载荷缺少 todo_snapshot：%#v", data)
	}
	snapshot, ok := raw.(*chatWebTodoSnapshot)
	if !ok {
		t.Fatalf("todo_snapshot 类型 = %T, want *chatWebTodoSnapshot", raw)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal todo_snapshot: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal todo_snapshot: %v", err)
	}
	return decoded
}

// TestChatWebToolFinishedEventCarriesTodoSnapshot 实时通道：tool_end 带裁剪快照，
// 且只暴露 items/session_id/goal_id 三个键（不透传 protocol_result 原文）。
func TestChatWebToolFinishedEventCarriesTodoSnapshot(t *testing.T) {
	payload := todoSnapshotPayload([]map[string]interface{}{
		{"content": "编写 index.html 页面骨架", "status": "in_progress", "active_form": "编写 index.html 页面骨架中"},
		{"content": "编写 style.css 报表样式", "status": "completed", "active_form": ""},
		{"content": "接线 SSE 与回放", "status": "pending", "active_form": "接线 SSE 与回放中"},
	})
	data := chatWebSSEDataForEvent(todoFinishedEvent(payload))

	if _, leaked := data["protocol_result"]; leaked {
		t.Fatalf("protocol_result 不得整体透传：%#v", data)
	}
	snapshot := todoSnapshotField(t, data)
	if len(snapshot) != 3 {
		t.Fatalf("快照键集合 = %#v, want items/session_id/goal_id", snapshot)
	}
	if snapshot["session_id"] != "session_20260925120000_Todo0001" || snapshot["goal_id"] != "goal-1" {
		t.Fatalf("归属字段 = %#v", snapshot)
	}
	items, ok := snapshot["items"].([]interface{})
	if !ok || len(items) != 3 {
		t.Fatalf("items = %#v", snapshot["items"])
	}
	first, _ := items[0].(map[string]interface{})
	if first["content"] != "编写 index.html 页面骨架" || first["status"] != "in_progress" {
		t.Fatalf("items[0] = %#v", first)
	}
	// active_form 空串按 omitempty 省略（前端回退 content 展示）。
	second, _ := items[1].(map[string]interface{})
	if _, has := second["active_form"]; has {
		t.Fatalf("空 active_form 应省略：%#v", second)
	}
}

// TestChatWebToolFinishedEventOmitsSnapshotForForeignOrInvalidPayload 键不存在即
// 与 todo 无关（普通工具不带字段）；坏条目全丢时同样不给字段（不清空前端面板）。
func TestChatWebToolFinishedEventOmitsSnapshotForForeignOrInvalidPayload(t *testing.T) {
	foreign := chatWebSSEDataForEvent(todoFinishedEvent(map[string]interface{}{
		"tool_name":       "grep",
		"tool_call_id":    "call-grep-1",
		"result_summary":  "3 matches",
		"protocol_result": map[string]interface{}{"metadata": map[string]interface{}{"outcome": "ok"}},
	}))
	if _, has := foreign["todo_snapshot"]; has {
		t.Fatalf("非 todos 事件不得带 todo_snapshot：%#v", foreign)
	}

	invalid := chatWebSSEDataForEvent(todoFinishedEvent(todoSnapshotPayload([]map[string]interface{}{
		{"content": "", "status": "pending"},
		{"content": "状态未知", "status": "running"},
	})))
	if _, has := invalid["todo_snapshot"]; has {
		t.Fatalf("无合法条目时不得带 todo_snapshot：%#v", invalid)
	}
}

// TestChatWebTodoSnapshotFromMessagesReplay 回放通道：嵌套 tool_metadata.todos
// 优先、平铺 todos 回退、从最近一条向前找；坏快照跳过继续向前。
func TestChatWebTodoSnapshotFromMessagesReplay(t *testing.T) {
	messages := []runtimetypes.Message{
		{Role: "tool", Content: "旧列表", Metadata: runtimetypes.Metadata{
			"tool_metadata": map[string]interface{}{
				"todos":      []map[string]interface{}{{"content": "旧任务", "status": "completed"}},
				"session_id": "session-old",
			},
		}},
		{Role: "assistant", Content: "继续"},
		{Role: "tool", Content: "新列表", Metadata: runtimetypes.Metadata{
			"tool_metadata": map[string]interface{}{
				"total": 2, "session_id": "session-new", "goal_id": "goal-new",
				"todos": []map[string]interface{}{
					{"content": "实现面板", "status": "in_progress", "active_form": "实现面板中"},
					{"content": "接线回放", "status": "pending"},
				},
			},
		}},
	}
	snapshot := chatWebTodoSnapshotFromMessages(messages, "session-fallback")
	if snapshot == nil {
		t.Fatal("应取到最近一次快照")
	}
	if snapshot.SessionID != "session-new" || snapshot.GoalID != "goal-new" {
		t.Fatalf("归属 = %+v", snapshot)
	}
	if len(snapshot.Items) != 2 || snapshot.Items[0].Status != "in_progress" || snapshot.Items[0].ActiveForm != "实现面板中" {
		t.Fatalf("items = %+v", snapshot.Items)
	}

	// 最近一条只有坏条目（状态非法）→ 跳过它，回退到更早的合法快照。
	withBrokenHead := append(messages, runtimetypes.Message{
		Role: "tool", Content: "坏列表",
		Metadata: runtimetypes.Metadata{"tool_metadata": map[string]interface{}{
			"todos": []map[string]interface{}{{"content": "坏", "status": "unknown"}},
		}},
	})
	fallback := chatWebTodoSnapshotFromMessages(withBrokenHead, "")
	if fallback == nil || fallback.Items[0].Content != "实现面板" {
		t.Fatalf("坏快照应被跳过，实际 %+v", fallback)
	}

	// 平铺形状（旧记录）：metadata.todos 直接可用，session_id 回落到调用方给出的值。
	flat := chatWebTodoSnapshotFromMessages([]runtimetypes.Message{
		{Role: "tool", Content: "平铺", Metadata: runtimetypes.Metadata{
			"todos": []map[string]interface{}{{"content": "平铺任务", "status": "pending"}},
		}},
	}, "session-flat")
	if flat == nil || flat.SessionID != "session-flat" || len(flat.Items) != 1 {
		t.Fatalf("平铺回退失败：%+v", flat)
	}

	// 无 todos 元数据（含空列表）→ nil，调用方保留现值。
	if got := chatWebTodoSnapshotFromMessages([]runtimetypes.Message{{Role: "user", Content: "hi"}}, ""); got != nil {
		t.Fatalf("无 todos 元数据应返回 nil，实际 %+v", got)
	}
}

// TestChatWebScreenJSONCarriesTodoReplay 回放通道端到端：窗口化取数
// （前端默认 msg_limit）与全量取数都要带 todo_snapshot，且 messages 窗口语义不变。
func TestChatWebScreenJSONCarriesTodoReplay(t *testing.T) {
	session := &ChatSession{Messages: []runtimetypes.Message{
		{Role: "user", Content: "开始"},
		{Role: "tool", Content: "任务列表已更新", Metadata: runtimetypes.Metadata{
			"tool_metadata": map[string]interface{}{
				"todos": []map[string]interface{}{
					{"content": "实现任务面板", "status": "in_progress", "active_form": "实现任务面板中"},
				},
				"session_id": "session-screen-1",
			},
		}},
		{Role: "assistant", Content: "好的"},
	}}
	withWebTestSession(t, session)

	for _, tc := range []struct {
		name   string
		window chatWebMessageWindow
	}{
		{name: "windowed", window: chatWebMessageWindow{Limit: 2}},
		{name: "full", window: chatWebMessageWindow{}},
	} {
		body, err := marshalChatWebScreenJSONWindowFiltered(tc.window, chatWebMessageFilter{})
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		var decoded struct {
			Messages []chatWebScreenMessage `json:"messages"`
			Todo     *chatWebTodoSnapshot   `json:"todo_snapshot"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("%s: unmarshal: %v", tc.name, err)
		}
		if decoded.Todo == nil {
			t.Fatalf("%s: 响应缺少 todo_snapshot：%s", tc.name, string(body))
		}
		if decoded.Todo.SessionID != "session-screen-1" || len(decoded.Todo.Items) != 1 {
			t.Fatalf("%s: todo_snapshot = %+v", tc.name, decoded.Todo)
		}
		if decoded.Todo.Items[0].ActiveForm != "实现任务面板中" {
			t.Fatalf("%s: active_form = %q", tc.name, decoded.Todo.Items[0].ActiveForm)
		}
		if tc.name == "windowed" && len(decoded.Messages) != 2 {
			t.Fatalf("窗口化 messages = %d, want 2", len(decoded.Messages))
		}
	}
}

// TestChatWebPage_ServesTodoPanelAssets 确认任务列表面板的前端资源被 go:embed 收录
// 并可被页面服务器正确伺服（新增 js 文件时最容易漏的回归点），且装配链完整：
// index.html 有面板结构、app.js 初始化面板、js/todos.js 同时接住实时与回放两条通道。
func TestChatWebPage_ServesTodoPanelAssets(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/web/js/todos.js", nil)
	recorder := httptest.NewRecorder()
	HandleChatWebPage(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /web/js/todos.js status = %d", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "javascript") {
		t.Fatalf("todos.js content-type = %q", contentType)
	}
	body := recorder.Body.String()
	for _, want := range []string{"tool_end", "todo_snapshot", "session_switched", "aicli.web.todos.v1"} {
		if !strings.Contains(body, want) {
			t.Fatalf("todos.js 缺少 %q（两条通道 / 会话边界 / 折叠记忆接线）", want)
		}
	}

	pageReq := httptest.NewRequest(http.MethodGet, "/web/", nil)
	pageRecorder := httptest.NewRecorder()
	HandleChatWebPage(pageRecorder, pageReq)
	if pageRecorder.Code != http.StatusOK {
		t.Fatalf("GET /web/ status = %d", pageRecorder.Code)
	}
	page := pageRecorder.Body.String()
	if !strings.Contains(page, `id="todo-panel"`) || !strings.Contains(page, `id="todo-list"`) {
		t.Fatal("index.html 缺少任务列表面板结构")
	}
	if strings.Index(page, `id="todo-panel"`) > strings.Index(page, `id="composer-header"`) {
		t.Fatal("任务列表面板应位于 composer 标题行之前（显示在 composer 面板上沿）")
	}
	appReq := httptest.NewRequest(http.MethodGet, "/web/app.js", nil)
	appRecorder := httptest.NewRecorder()
	HandleChatWebPage(appRecorder, appReq)
	if !strings.Contains(appRecorder.Body.String(), "initTodoPanel()") {
		t.Fatal("app.js 未初始化任务列表面板")
	}
}
