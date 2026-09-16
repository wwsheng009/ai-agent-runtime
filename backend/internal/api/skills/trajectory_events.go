package skills

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// chatSSEStreamEventPrefix 标识 /api/agent/chat SSE 事件在会话事件存储（EventStore）
// 中的类型命名空间。轨迹视图/增量拉取按此前缀过滤；与 runtime 生命周期事件
// （bus → EventStore 管道，handler.go:3293）共库共存、互不干扰。
const chatSSEStreamEventPrefix = "chat.sse."

// chatSSEFrameIsWireOnly 判定某个 chat SSE 帧是否「只走 wire、不落盘」（Batch 1 去重）。
//
// 名单内的事件在 EventStore 中已有语义等价的回放源，重复落盘只放大 store 体积、
// dump 时长与轨迹恢复的分页次数：
//
//   - chunk ↔ assistant_delta（bus 侧，A 通道落盘）
//   - reasoning ↔ assistant.reasoning（bus 侧）
//   - observation（携带工具身份时）↔ tool_end（同源转写；保留 tool_end 落盘）
//
// 命中帧不写 EventStore，且帧不携带 `_event.sequence` / `id:`：连接内计数器不是
// 持久化游标，写进游标位会被前端 reducer 当作真实 seq 幂等丢弃（宁缺勿假）。
// 无工具身份的 G7 结构化 observation 不在名单内，仍按事件落盘。
func chatSSEFrameIsWireOnly(eventName string, data interface{}) bool {
	switch strings.TrimSpace(eventName) {
	case "chunk", "reasoning":
		return true
	case "observation":
		return observationPayloadIsToolDuplicate(data)
	default:
		return false
	}
}

// observationPayloadIsToolDuplicate 判定 observation 帧是否只是 tool_end 的同源重复。
// 判据与前端 `lib/trajectory/recovery.ts` 的 isToolObservation 同口径：载荷携带
// 非空字符串工具身份（`tool` 或 `step`）即为重复来源；不携带则语义不变（仍落盘）。
func observationPayloadIsToolDuplicate(data interface{}) bool {
	payload, ok := data.(map[string]interface{})
	if !ok {
		return false
	}
	for _, key := range []string{"tool", "step"} {
		if text, ok := payload[key].(string); ok && strings.TrimSpace(text) != "" {
			return true
		}
	}
	return false
}

// trimChatSSEEventPayloadForStore 在落盘前裁掉帧内与相邻帧重复的大字段：
//   - done：去掉内嵌 `result`（与相邻 `result` 帧同体量，单帧可达 ~376KB），
//     保留结束元信息与 `content`。wire 帧不变。
//
// 只在需要裁剪时克隆载荷，避免改动即将写 wire 的同一张 map。
func trimChatSSEEventPayloadForStore(eventName string, data interface{}) map[string]interface{} {
	payload := payloadMap(data)
	if strings.TrimSpace(eventName) != "done" {
		return payload
	}
	if _, ok := payload["result"]; !ok {
		return payload
	}
	trimmed := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		if key == "result" {
			continue
		}
		trimmed[key] = value
	}
	return trimmed
}

// newTrajectoryEmitter 构造带轨迹持久化的 SSE emitter（chat 轨迹事件日志）：
//
//   - 每个事件在写出前先 AppendEvent 写入会话事件存储，SSE 帧 _event.sequence
//     使用持久化 seq（EventStore 按 session 单调自增），前端据此增量续传/重放；
//   - 存储不可用（nil）或 AppendEvent 失败时静默降级为连接内计数，
//     绝不阻塞 SSE 主链路（对齐 DeepSeek-Reasonix「Recording failures never
//     block forwarding」）；
//   - Batch 1 起：wire-only 帧（chunk/reasoning/同源 observation）不落盘，也不携带
//     `_event.sequence`/`id:`（见 chatSSEFrameIsWireOnly）；
//   - 事件载荷以原始 payload 形式存储（不含 _event envelope），Type 为
//     "chat.sse.<event>"，Payload 规范为 map 形式（EventStore payload_json 列要求）。
func (h *Handler) newTrajectoryEmitter(w http.ResponseWriter, session *chat.Session, turnIDs ...string) *sseEmitter {
	emitter := newSSEEmitter(w)
	if len(turnIDs) > 0 {
		emitter.turnID = strings.TrimSpace(turnIDs[0])
	}
	store := h.getSessionEventStore()
	sessionID := sessionID(session)
	if store == nil || sessionID == "" {
		return emitter
	}
	emitter.wireOnly = chatSSEFrameIsWireOnly
	emitter.persist = func(eventName string, data interface{}) int64 {
		if chatSSEFrameIsWireOnly(eventName, data) {
			return 0
		}
		seq, err := store.AppendEvent(context.Background(), runtimeevents.Event{
			Type:      chatSSEStreamEventPrefix + eventName,
			SessionID: sessionID,
			Payload:   trimChatSSEEventPayloadForStore(eventName, data),
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
