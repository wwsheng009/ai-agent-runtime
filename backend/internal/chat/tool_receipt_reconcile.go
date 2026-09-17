package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// reconcileToolReceipts 为会话历史中已完成的内联工具调用补齐持久化回执。
//
// 背景（方案 §0.3 / G3）：`saveStoredToolReceipt` 原先只在审批恢复路径
// （resumeApprovedPendingTool）调用；ReAct loop 的正常内联工具执行直接把
// tool_result 消息写进会话历史（loop.PersistHistory → session.ReplaceHistory），
// 从不生成回执，因此 session_tool_receipts 在生产库中长期为 0。
//
// 语义：
//   - 只读扫描会话历史，不修改历史；
//   - 已有回执的 tool_call_id 直接跳过（幂等，可安全重复调用）；
//   - 失败的工具同样落回执（ok=false 写入事件载荷），失败证据不得丢失；
//   - 任何单条失败只跳过该条，不影响其它回执与主流程（不反压事件总线）。
//
// 返回实际写入的回执数量，便于测试与诊断。
func (a *SessionActor) reconcileToolReceipts(ctx context.Context, session *Session, turnID string) int {
	if a == nil || session == nil {
		return 0
	}
	store := a.toolReceiptStore()
	if store == nil {
		return 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	messages := session.GetMessages()
	toolNames := toolCallNameIndex(messages)
	recorded := 0
	for index := range messages {
		message := messages[index]
		if !isToolResultMessage(message) {
			continue
		}
		toolCallID := strings.TrimSpace(message.ToolCallID)
		if toolCallID == "" {
			continue
		}
		if a.consumeReplayedToolReceipt(toolCallID) {
			// 该调用已通过回执回放消费（结果已进入历史），不得重新写回执。
			continue
		}
		if existing, err := store.GetToolReceipt(ctx, a.id, toolCallID); err == nil && existing != nil {
			continue
		}
		payload, err := json.Marshal(message)
		if err != nil {
			continue
		}
		ok, failureCategory := toolResultMessageOutcome(message)
		receipt := newToolExecutionReceipt(a.id, toolCallID, toolNames[toolCallID], payload, time.Now().UTC())
		receipt.OK = ok
		receipt.FailureCategory = failureCategory
		if err := a.saveStoredToolReceipt(ctx, receipt); err != nil {
			continue
		}
		a.publishToolReceiptEvent(EventToolReceiptRecorded, turnID, "tool_completion", receipt)
		recorded++
	}
	return recorded
}

// markReplayedToolReceipt 记录一个已被回执回放消费的 tool_call_id。
func (a *SessionActor) markReplayedToolReceipt(toolCallID string) {
	toolCallID = strings.TrimSpace(toolCallID)
	if a == nil || toolCallID == "" {
		return
	}
	a.replayedReceiptsMu.Lock()
	defer a.replayedReceiptsMu.Unlock()
	if a.replayedReceipts == nil {
		a.replayedReceipts = make(map[string]struct{})
	}
	a.replayedReceipts[toolCallID] = struct{}{}
}

// consumeReplayedToolReceipt 判断并消费一次回放标记（一次性，避免长期驻留）。
func (a *SessionActor) consumeReplayedToolReceipt(toolCallID string) bool {
	toolCallID = strings.TrimSpace(toolCallID)
	if a == nil || toolCallID == "" {
		return false
	}
	a.replayedReceiptsMu.Lock()
	defer a.replayedReceiptsMu.Unlock()
	if _, ok := a.replayedReceipts[toolCallID]; !ok {
		return false
	}
	delete(a.replayedReceipts, toolCallID)
	return true
}

// toolCallNameIndex 建立 tool_call_id → 工具名 的索引：工具名只存在于发起该
// 调用的 assistant 消息里，tool_result 消息本身不带名称。
func toolCallNameIndex(messages []types.Message) map[string]string {
	if len(messages) == 0 {
		return nil
	}
	index := make(map[string]string)
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			callID := strings.TrimSpace(call.ID)
			if callID == "" {
				continue
			}
			if name := strings.TrimSpace(call.Name); name != "" {
				index[callID] = name
			}
		}
	}
	return index
}

func isToolResultMessage(message types.Message) bool {
	return strings.EqualFold(strings.TrimSpace(message.Role), "tool")
}

// toolResultMessageOutcome 从 tool_result 消息的元数据推导回执结果。
// 返回 nil 表示无法判定（不猜测），此时事件载荷不携带 ok 字段。
func toolResultMessageOutcome(message types.Message) (*bool, string) {
	if len(message.Metadata) == 0 {
		ok := true
		return &ok, ""
	}
	for _, key := range []string{"tool_error", "error", "is_error"} {
		value, exists := message.Metadata[key]
		if !exists {
			continue
		}
		if text, isText := value.(string); isText && strings.TrimSpace(text) == "" {
			continue
		}
		if flag, isBool := value.(bool); isBool && !flag {
			continue
		}
		if flag, isBool := value.(bool); isBool && flag {
			ok := false
			return &ok, "tool_error"
		}
		if _, isText := value.(string); isText {
			ok := false
			return &ok, "tool_error"
		}
	}
	if outcome, exists := message.Metadata["outcome"]; exists {
		switch strings.ToLower(strings.TrimSpace(fmt.Sprint(outcome))) {
		case "failed", "error":
			ok := false
			return &ok, "tool_error"
		case "success", "empty", "partial":
			ok := true
			return &ok, ""
		}
	}
	if value, exists := message.Metadata["ok"]; exists {
		if flag, isBool := value.(bool); isBool {
			return &flag, ""
		}
	}
	ok := true
	return &ok, ""
}
