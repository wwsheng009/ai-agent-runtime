package skills

import (
	"context"
	"net/http"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// chatSSEStreamEventPrefix 标识 /api/agent/chat SSE 事件在会话事件存储（EventStore）
// 中的类型命名空间。轨迹视图/增量拉取按此前缀过滤；与 runtime 生命周期事件
// （bus → EventStore 管道，handler.go:3293）共库共存、互不干扰。
const chatSSEStreamEventPrefix = "chat.sse."

// newTrajectoryEmitter 构造带轨迹持久化的 SSE emitter（chat 轨迹事件日志）：
//
//   - 每个事件在写出前先 AppendEvent 写入会话事件存储，SSE 帧 _event.sequence
//     使用持久化 seq（EventStore 按 session 单调自增），前端据此增量续传/重放；
//   - 存储不可用（nil）或 AppendEvent 失败时静默降级为连接内计数，
//     绝不阻塞 SSE 主链路（对齐 DeepSeek-Reasonix「Recording failures never
//     block forwarding」）；
//   - 事件载荷以原始 payload 形式存储（不含 _event envelope），Type 为
//     "chat.sse.<event>"，Payload 规范为 map 形式（EventStore payload_json 列要求）。
func (h *Handler) newTrajectoryEmitter(w http.ResponseWriter, session *chat.Session) *sseEmitter {
	emitter := newSSEEmitter(w)
	store := h.getSessionEventStore()
	sessionID := sessionID(session)
	if store == nil || sessionID == "" {
		return emitter
	}
	emitter.persist = func(eventName string, data interface{}) int64 {
		seq, err := store.AppendEvent(context.Background(), runtimeevents.Event{
			Type:      chatSSEStreamEventPrefix + eventName,
			SessionID: sessionID,
			Payload:   payloadMap(data),
			Timestamp: time.Now().UTC(),
		})
		if err != nil {
			return 0
		}
		return seq
	}
	return emitter
}

// payloadMap 规范事件载荷为 map 形式。
func payloadMap(data interface{}) map[string]interface{} {
	if data == nil {
		return map[string]interface{}{}
	}
	if m, ok := data.(map[string]interface{}); ok {
		return m
	}
	return map[string]interface{}{"value": data}
}
