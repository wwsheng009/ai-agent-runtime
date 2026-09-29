package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// chatTodosTestSession 构造只含一条 todos 工具结果的会话 transcript。
// 元数据形状与生产端一致：metadata.tool_metadata.todos（嵌套优先）。
func chatTodosTestSession(items ...map[string]interface{}) *ChatSession {
	todos := make([]interface{}, 0, len(items))
	for _, item := range items {
		todos = append(todos, item)
	}
	return &ChatSession{
		Messages: []runtimetypes.Message{
			{Role: "assistant", Content: "working"},
			{
				Role:    "tool",
				Content: "todos updated",
				Metadata: runtimetypes.Metadata{
					"tool_metadata": map[string]interface{}{
						"todos": todos,
					},
				},
			},
		},
	}
}

func chatTodosTestPlain(t *testing.T, session *ChatSession, command string) string {
	t.Helper()
	return ui.RenderDocumentPlain(executeStructuredTodosCommand(session, command).Document())
}

func TestChatTodosCommandCatalogRegistration(t *testing.T) {
	var match *chatSlashCommandSpec
	specs := chatSlashCommandCatalog()
	for index := range specs {
		if specs[index].Name == chatTodosCommandName {
			match = &specs[index]
			break
		}
	}
	if match == nil {
		t.Fatalf("catalog 缺少 %s 注册", chatTodosCommandName)
	}
	if !match.AcceptsArgs {
		t.Fatalf("%s 应接受过滤参数（all/active/done/brief）", chatTodosCommandName)
	}
	wantTokens := map[string]bool{"all": false, "active": false, "done": false, "brief": false}
	for _, arg := range match.Args {
		if _, ok := wantTokens[arg.Token]; ok {
			wantTokens[arg.Token] = true
		}
	}
	for token, seen := range wantTokens {
		if !seen {
			t.Fatalf("%s catalog 缺少参数 %q", chatTodosCommandName, token)
		}
	}
}

func TestChatTodosCommandQueueSafeWhileBusy(t *testing.T) {
	for _, text := range []string{"/todos", "/todos active", "/todos done", "/todos brief", "/todos bogus"} {
		if !chatSlashCommandQueueSafe(text) {
			t.Fatalf("忙碌时 %q 应可排队（回合结束后执行），当前被拒绝", text)
		}
	}
}

func TestChatTodosCommandRendersSnapshotAndFilters(t *testing.T) {
	session := chatTodosTestSession(
		map[string]interface{}{"content": "写方案", "status": "in_progress", "active_form": "写方案中"},
		map[string]interface{}{"content": "跑测试", "status": "pending"},
		map[string]interface{}{"content": "建目录", "status": "completed"},
	)

	all := chatTodosTestPlain(t, session, "/todos")
	for _, want := range []string{
		"任务列表（3 项：进行中 1 / 待办 1 / 已完成 1）",
		"[>] 写方案 — 写方案中",
		"[ ] 跑测试",
		"[x] 建目录",
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("/todos 缺少 %q：\n%s", want, all)
		}
	}

	active := chatTodosTestPlain(t, session, "/todos active")
	if !strings.Contains(active, "写方案") || !strings.Contains(active, "跑测试") {
		t.Fatalf("/todos active 应包含进行中与待办：\n%s", active)
	}
	if strings.Contains(active, "建目录") {
		t.Fatalf("/todos active 不应包含已完成项：\n%s", active)
	}

	done := chatTodosTestPlain(t, session, "/todos done")
	if !strings.Contains(done, "[x] 建目录") {
		t.Fatalf("/todos done 应只显示已完成项：\n%s", done)
	}
	if strings.Contains(done, "跑测试") {
		t.Fatalf("/todos done 不应包含待办项：\n%s", done)
	}

	brief := strings.TrimSpace(chatTodosTestPlain(t, session, "/todos brief"))
	if brief != "任务列表: 3 项（进行中 1 / 待办 1 / 已完成 1）" {
		t.Fatalf("/todos brief 摘要不符：%q", brief)
	}
}

func TestChatTodosCommandEmptyAndInvalid(t *testing.T) {
	empty := chatTodosTestPlain(t, &ChatSession{}, "/todos")
	if !strings.Contains(empty, "当前会话暂无待办") {
		t.Fatalf("空会话应提示暂无待办：\n%s", empty)
	}

	invalid := chatTodosTestPlain(t, &ChatSession{}, "/todos bogus")
	if !strings.Contains(invalid, "用法: /todos") {
		t.Fatalf("未知参数应返回用法：\n%s", invalid)
	}

	nilSession := chatTodosTestPlain(t, nil, "/todos")
	if !strings.Contains(nilSession, "当前没有活动会话") {
		t.Fatalf("nil 会话应返回错误：\n%s", nilSession)
	}
}

func TestChatTodosCommandStructuredDispatchRecognizes(t *testing.T) {
	session := chatTodosTestSession(
		map[string]interface{}{"content": "写方案", "status": "in_progress"},
	)
	result, handled, err := tryExecuteStructuredChatCommand(session, "/todos active")
	if err != nil {
		t.Fatalf("tryExecuteStructuredChatCommand 报错: %v", err)
	}
	if !handled {
		t.Fatalf("/todos 应被结构化注册表接管")
	}
	plain := ui.RenderDocumentPlain(result.Document())
	if !strings.Contains(plain, "写方案") {
		t.Fatalf("结构化分发结果缺少任务项：\n%s", plain)
	}
}

// TestChatTodosCommandShowsLiveSnapshotWhileTranscriptLags 忙时回归：本回合的
// todos 结果还没同步进 session.Messages（热投影仍是旧的），tool_end 事件缓存
// 已写入最新列表；/todos 必须渲染最新那份，而不是"当前会话暂无待办"。
func TestChatTodosCommandShowsLiveSnapshotWhileTranscriptLags(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "session-busy"},
		Messages:       []runtimetypes.Message{{Role: "assistant", Content: "工具还在跑"}},
	}
	session.rememberChatTodoSnapshot("session-busy", &chatWebTodoSnapshot{
		Items: []chatWebTodoItem{
			{Content: "核对 invalidation_events 口径", Status: "in_progress", ActiveForm: "核对口径"},
			{Content: "运行 knowledge 相关包全量测试验证", Status: "pending"},
		},
		SessionID: "session-busy",
	})

	plain := chatTodosTestPlain(t, session, "/todos")
	if strings.Contains(plain, "当前会话暂无待办") {
		t.Fatalf("忙时 turn 内不得显示无待办：\n%s", plain)
	}
	for _, want := range []string{
		"任务列表（2 项：进行中 1 / 待办 1 / 已完成 0）",
		"运行 knowledge 相关包全量测试验证",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("/todos 缺少 %q：\n%s", want, plain)
		}
	}
}
