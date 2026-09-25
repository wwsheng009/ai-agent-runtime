// web_todo_snapshot.go — micro web client「任务列表」面板（composer 上沿浮层）的数据通道。
//
// 与 frontend/ 的 lib/thread-state/todos.ts 同源同形，两条通道共用一个 JSON 形状
// （{items:[{content,status,active_form}], session_id, goal_id}），前端 js/todos.js
// 只写一份解析与合并逻辑：
//
//   - 实时（SSE tool_end）：payload.protocol_result.metadata.todo_snapshot。
//     这是 internal/agent 按工具作用域挂上的裁剪视图（只有 todos 工具会带），
//     原始 todos 数组与工具协议元数据不进入线上载荷。
//   - 回放（GET /web/api/screen?format=json）：会话 transcript 里最近一条带 todos
//     工具元数据的消息。生产侧把它整体嵌在 metadata.tool_metadata 下（历史原文），
//     旧记录可能平铺在 metadata.todos，故按「嵌套优先 + 平铺回退」读取。
//
// 两端都不臆造数据：坏条目丢弃、状态不在契约内丢弃、整组不可用时返回 nil
// （调用方据此保持已有面板内容，不用空列表覆盖）。
package commands

import (
	"encoding/json"
	"strings"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// chatWebTodoItem 是单个任务项的线上形状。active_form 是执行态文案
// （如「编写 index.html 页面骨架中」），展示层缺省时回退 content。
type chatWebTodoItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"active_form,omitempty"`
}

// chatWebTodoSnapshot 是一次任务列表全量快照。items 为空即视为无数据
// （不渲染面板），因此序列化时不做 omitempty，保持字段稳定便于前端与脚本断言。
type chatWebTodoSnapshot struct {
	Items     []chatWebTodoItem `json:"items"`
	SessionID string            `json:"session_id,omitempty"`
	GoalID    string            `json:"goal_id,omitempty"`
}

// chatWebTodoSnapshotFromToolPayload 从 tool_end 事件载荷提取 todo 快照。
//
// 只认 protocol_result.metadata.todo_snapshot（键存在即数据，不做工具名特判）；
// 不整体透传 protocol_result——它含工具协议原文，体积无上界且包含内部字段。
func chatWebTodoSnapshotFromToolPayload(payload map[string]interface{}) *chatWebTodoSnapshot {
	if len(payload) == 0 {
		return nil
	}
	protocol, _ := payload["protocol_result"].(map[string]interface{})
	if len(protocol) == 0 {
		return nil
	}
	metadata, _ := protocol["metadata"].(map[string]interface{})
	if len(metadata) == 0 {
		return nil
	}
	return chatWebTodoSnapshotFromRaw(metadata["todo_snapshot"])
}

// chatWebTodoSnapshotFromRaw 解析实时通道快照（protocol_result.metadata.todo_snapshot）。
// 支持任意可 JSON 化的来源形状（map / 结构体切片），与非 web 通道共用同一份契约。
func chatWebTodoSnapshotFromRaw(raw interface{}) *chatWebTodoSnapshot {
	if raw == nil {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var decoded struct {
		Items     []chatWebTodoItem `json:"items"`
		SessionID string            `json:"session_id"`
		GoalID    string            `json:"goal_id"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil
	}
	return newChatWebTodoSnapshot(decoded.Items, decoded.SessionID, decoded.GoalID)
}

// chatWebTodoSnapshotFromMessages 从会话 transcript 反向扫描最近一次 todos 工具结果
// （回放通道：刷新页面 / 会话切换后恢复面板）。
//
// metadata 包形状：生产侧写的是 metadata.tool_metadata.todos（整体嵌套），
// 旧记录 / 旁路载荷是平铺 metadata.todos；嵌套优先、平铺回退。坏条目跳过并继续
// 向前找，而不是整体降级（历史里更早的快照仍可能可用）。
func chatWebTodoSnapshotFromMessages(messages []runtimetypes.Message, sessionID string) *chatWebTodoSnapshot {
	for index := len(messages) - 1; index >= 0; index-- {
		metadata := messages[index].Metadata
		if len(metadata) == 0 {
			continue
		}
		bag := chatWebTodoMetadataBag(metadata)
		if bag == nil {
			continue
		}
		raw, ok := bag["todos"]
		if !ok || raw == nil {
			continue
		}
		snapshot := chatWebTodoSnapshotFromProducerTodos(raw, chatWebTodoMetadataString(bag, "session_id", sessionID), chatWebTodoMetadataString(bag, "goal_id", ""))
		if snapshot != nil {
			return snapshot
		}
	}
	return nil
}

// chatWebTodoMetadataBag 返回真正承载 todos 键的元数据包。
// 嵌套包带键时以嵌套为准；否则回退平铺的顶层元数据。
func chatWebTodoMetadataBag(metadata map[string]interface{}) map[string]interface{} {
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok && len(nested) > 0 {
		if _, hasTodos := nested["todos"]; hasTodos {
			return nested
		}
	}
	if _, hasTodos := metadata["todos"]; hasTodos {
		return metadata
	}
	return nil
}

// chatWebTodoSnapshotFromProducerTodos 解析生产者原始 todos 数组
// （[]tools.TodoItem / []interface{} / []map），统一裁剪成线上形状。
func chatWebTodoSnapshotFromProducerTodos(raw interface{}, sessionID, goalID string) *chatWebTodoSnapshot {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var items []chatWebTodoItem
	if err := json.Unmarshal(encoded, &items); err != nil {
		return nil
	}
	return newChatWebTodoSnapshot(items, sessionID, goalID)
}

// newChatWebTodoSnapshot 逐项校验并裁剪：content 为空 / 状态不在契约内的条目丢弃；
// 一条合法条目都没有时返回 nil（= 载荷不可用，调用方保留原快照）。
func newChatWebTodoSnapshot(items []chatWebTodoItem, sessionID, goalID string) *chatWebTodoSnapshot {
	rows := make([]chatWebTodoItem, 0, len(items))
	for _, item := range items {
		content := strings.TrimSpace(item.Content)
		status := normalizeChatWebTodoStatus(item.Status)
		if content == "" || status == "" {
			continue
		}
		rows = append(rows, chatWebTodoItem{
			Content:    content,
			Status:     status,
			ActiveForm: strings.TrimSpace(item.ActiveForm),
		})
	}
	if len(rows) == 0 {
		return nil
	}
	return &chatWebTodoSnapshot{
		Items:     rows,
		SessionID: strings.TrimSpace(sessionID),
		GoalID:    strings.TrimSpace(goalID),
	}
}

// normalizeChatWebTodoStatus 归一状态；契约外取值返回 ""（调用方丢弃该条目）。
func normalizeChatWebTodoStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending":
		return "pending"
	case "in_progress":
		return "in_progress"
	case "completed":
		return "completed"
	default:
		return ""
	}
}

// chatWebTodoMetadataString 取字符串字段并去空白；为空时用 fallback。
func chatWebTodoMetadataString(bag map[string]interface{}, key, fallback string) string {
	if bag != nil {
		if text, ok := bag[key].(string); ok {
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				return trimmed
			}
		}
	}
	return strings.TrimSpace(fallback)
}

// chatWebTodoSnapshotForSession 构建回放通道快照（/web/api/screen）。
// 会话缺失 / 无可读 transcript 时返回 nil（面板保持不渲染，不显示过期数据）。
func chatWebTodoSnapshotForSession() *chatWebTodoSnapshot {
	session := chatDebugDisplaySession()
	if session == nil {
		return nil
	}
	return chatWebTodoSnapshotFromMessages(sessionTranscriptMessages(session), currentRuntimeSessionID(session))
}
