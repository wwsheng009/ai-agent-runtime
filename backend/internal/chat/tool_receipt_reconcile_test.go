package chat

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestToolReceiptPersistedOnToolCompletion 锁定方案 §0.3 / G3：
// 内联工具执行完成后必须落持久化回执，失败工具同样落回执（ok=false）。
func TestToolReceiptPersistedOnToolCompletion(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryRuntimeStore(32)
	actor := &SessionActor{id: "session-receipt", stateStore: store, eventStore: store}

	session := NewSession("session-receipt")
	assistant := types.NewAssistantMessage("run two tools")
	assistant.ToolCalls = []types.ToolCall{
		{ID: "call-ok", Name: "view", Args: map[string]interface{}{"path": "README.md"}},
		{ID: "call-fail", Name: "shell", Args: map[string]interface{}{"command": "exit 1"}},
	}
	session.AddMessage(*assistant)
	success := types.NewToolMessage("call-ok", "file contents")
	failure := types.NewToolMessage("call-fail", "Exit code: 1")
	failure.Metadata["tool_error"] = "exit code 1"
	session.AddMessage(*success)
	session.AddMessage(*failure)

	recorded := actor.reconcileToolReceipts(ctx, session, "turn-1")
	if recorded != 2 {
		t.Fatalf("期望写入 2 条回执，实际 %d", recorded)
	}

	okReceipt, err := store.GetToolReceipt(ctx, "session-receipt", "call-ok")
	if err != nil || okReceipt == nil {
		t.Fatalf("成功工具回执缺失: %v", err)
	}
	if okReceipt.ToolName != "view" {
		t.Fatalf("回执应带工具名 view，实际 %q", okReceipt.ToolName)
	}
	if okReceipt.OK == nil || !*okReceipt.OK {
		t.Fatalf("成功工具回执 ok 应为 true，实际 %v", okReceipt.OK)
	}

	failReceipt, err := store.GetToolReceipt(ctx, "session-receipt", "call-fail")
	if err != nil || failReceipt == nil {
		t.Fatalf("失败工具回执缺失（失败证据不得丢失）: %v", err)
	}
	if failReceipt.OK == nil || *failReceipt.OK {
		t.Fatalf("失败工具回执 ok 应为 false，实际 %v", failReceipt.OK)
	}
	if failReceipt.FailureCategory != "tool_error" {
		t.Fatalf("失败分类应为 tool_error，实际 %q", failReceipt.FailureCategory)
	}
	if len(failReceipt.MessageJSON) == 0 {
		t.Fatalf("回执必须携带可重放的 tool_result 消息")
	}

	events, err := store.ListEvents(ctx, "session-receipt", 0, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	recordedEvents := 0
	failureEventOK := false
	for _, event := range events {
		if event.Type != EventToolReceiptRecorded {
			continue
		}
		recordedEvents++
		if event.Payload["tool_call_id"] == "call-fail" {
			if ok, exists := event.Payload["ok"]; exists {
				flag, isBool := ok.(bool)
				failureEventOK = isBool && !flag
			}
		}
	}
	if recordedEvents != 2 {
		t.Fatalf("期望 2 条 tool_receipt_recorded 事件，实际 %d", recordedEvents)
	}
	if !failureEventOK {
		t.Fatalf("失败回执事件必须携带 ok=false 载荷")
	}

	// 幂等：重复调用不得重复写回执或事件。
	if again := actor.reconcileToolReceipts(ctx, session, "turn-1"); again != 0 {
		t.Fatalf("已有回执时重复调用应写入 0 条，实际 %d", again)
	}
	events, err = store.ListEvents(ctx, "session-receipt", 0, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	replayed := 0
	for _, event := range events {
		if event.Type == EventToolReceiptRecorded {
			replayed++
		}
	}
	if replayed != 2 {
		t.Fatalf("重复调用后回执事件应仍为 2 条，实际 %d", replayed)
	}
}
