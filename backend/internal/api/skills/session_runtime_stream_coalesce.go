package skills

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// Batch 3 · 传输增强（方案 §4）：相邻增量行服务端合帧。
//
// 背景（§2.2 实测）：单会话 2,302 行里绝大多数是打字机增量（assistant_delta /
// assistant_reasoning），每行一帧既放大帧数，也放大 SSE 帧头的固定开销。合帧把
// 「同类型 + 同 turn/stream + 连续 seq」的相邻增量并成一帧：
//
//	delta: "Hel" + "lo" + " world"  →  delta: "Hello world"
//	payload.coalesced_from = 区间首 seq，payload.coalesced_count = 合并行数
//
// 约束（§6 风险表「合帧改变帧序假设」）：
//   - 只合并**连续 seq**（incoming == last+1）：跳号说明区间中间还有别的行，继续
//     合并会让客户端误以为区间完整；
//   - 只合并**同一文本槽位**（两行文本都来自 `delta`/`content`，或都来自
//     `text`/`summary`/`reasoning.summary`）：槽位不同则语义不同，宁可多一帧；
//   - 合并载荷有字节上限（streamCoalesceEventByteLimit）：一段超长正文不会把
//     单帧撑成无界内存；
//   - 默认**不回放期合帧**——由连接用 `coalesce=1` 显式开启（方案 §6 灰度约定：
//     「合帧/latest-wins 按连接可配（默认关 → 灰度开）」）。
//
// 合并后的帧保留**最后一行**的 seq 作为 payload["seq"]，因此客户端游标
// （after=）仍前进到区间末端，不会因为中间行「消失」而重放整段正文。
const (
	// streamCoalesceEventByteLimit 限制单帧合并后的文本字节数（256KB）：超过即
	// 停止折叠、让后续增量自成新帧，内存与编码成本保持有界。
	streamCoalesceEventByteLimit = 256 * 1024
	// streamCoalesceFromKey / streamCoalesceCountKey 是合并帧自述字段：
	// 区间下界 seq 与折叠行数（§4 Batch 3 表）。语义与 CLI 侧既有
	// `_coalesced_sequence_from` 一致（保留区间下界），命名按方案表定稿。
	streamCoalesceFromKey  = "coalesced_from"
	streamCoalesceCountKey = "coalesced_count"
)

// coalesceRuntimeEventPage 把一页事件里可合并的相邻增量折叠成单帧。
// enabled=false 时原样返回（零分配快路径）。
func coalesceRuntimeEventPage(events []runtimeevents.Event, enabled bool) []runtimeevents.Event {
	if !enabled || len(events) < 2 {
		return events
	}
	out := make([]runtimeevents.Event, 0, len(events))
	mergedGroups := 0
	absorbedRows := 0
	for _, event := range events {
		if len(out) > 0 {
			if merged, ok := mergeCoalescibleRuntimeEvent(out[len(out)-1], event); ok {
				// coalesced_frames 计「至少折叠过一行的输出帧」：本次合帧在既有的
				// 合并链上继续折叠时不再重复计数（否则三行折一帧会被算成两帧）。
				if _, chained := asInt64(out[len(out)-1].Payload[streamCoalesceCountKey]); !chained {
					mergedGroups++
				}
				out[len(out)-1] = merged
				absorbedRows++
				continue
			}
		}
		out = append(out, event)
	}
	if absorbedRows > 0 {
		recordRuntimeEventStreamCoalesced(mergedGroups, absorbedRows)
	}
	return out
}

// streamCoalesceMergeableEventType 判定类型是否属于「增量文本」家族。其余类型
// （工具生命周期、审批、会话边界…）一帧对应一个事实，不允许与相邻帧折叠。
func streamCoalesceMergeableEventType(eventType string) bool {
	switch eventType {
	case chat.EventAssistantDelta, chat.EventAssistantReasoning, chat.EventAssistantReasoningDelta:
		return true
	default:
		return false
	}
}

// streamCoalesceTextSlot 描述「增量文本在 payload 里的位置」。
// path 长度为 1（顶层键）或 2（嵌套键，如 reasoning.summary）。
type streamCoalesceTextSlot struct {
	path []string
	text string
}

// streamCoalesceResolveTextSlot 解析一条增量事件携带的文本。解析不到（键不存在、
// 非字符串、类型不在增量家族）时 ok=false —— 该帧不参与合帧，按原样下发。
func streamCoalesceResolveTextSlot(event runtimeevents.Event) (streamCoalesceTextSlot, bool) {
	if event.Payload == nil || !streamCoalesceMergeableEventType(event.Type) {
		return streamCoalesceTextSlot{}, false
	}
	if event.Type == chat.EventAssistantDelta {
		if text, ok := streamCoalescePayloadText(event.Payload, "delta"); ok {
			return streamCoalesceTextSlot{path: []string{"delta"}, text: text}, true
		}
		if text, ok := streamCoalescePayloadText(event.Payload, "content"); ok {
			return streamCoalesceTextSlot{path: []string{"content"}, text: text}, true
		}
		return streamCoalesceTextSlot{}, false
	}
	if text, ok := streamCoalescePayloadText(event.Payload, "text"); ok {
		return streamCoalesceTextSlot{path: []string{"text"}, text: text}, true
	}
	if text, ok := streamCoalescePayloadText(event.Payload, "summary"); ok {
		return streamCoalesceTextSlot{path: []string{"summary"}, text: text}, true
	}
	if nested, ok := event.Payload["reasoning"].(map[string]interface{}); ok {
		if text, ok := streamCoalescePayloadText(nested, "summary"); ok {
			return streamCoalesceTextSlot{path: []string{"reasoning", "summary"}, text: text}, true
		}
		if text, ok := streamCoalescePayloadText(nested, "text"); ok {
			return streamCoalesceTextSlot{path: []string{"reasoning", "text"}, text: text}, true
		}
	}
	return streamCoalesceTextSlot{}, false
}

func streamCoalescePayloadText(payload map[string]interface{}, key string) (string, bool) {
	value, ok := payload[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	return text, ok
}

// streamCoalesceIdentity 是「同一段流」的判据：类型 + 会话/代理身份 + trace +
// turn + stream。turn/stream 键允许缺省（部分总线事件只带 trace）——两边都缺省
// 时视为同段，与 CLI 侧 assistantEventIdentity 的空值口径一致。
//
// 会话/代理身份并入身份键是纵深防御：当前调用方已按单会话查询（合帧页来自
// store.ListEvents(sessionID) 或按 sessionID 过滤的 live 订阅），跨会话折叠不
// 成立；但一旦未来某条路径把多会话事件混进同一页，增量正文折叠会把两个会话的
// 文本拼成一段（串流）。身份键带上 SessionID/AgentName 后，这种拼接在合帧层就
// 被拒绝，而不是只靠上游过滤。
func streamCoalesceIdentity(event runtimeevents.Event) string {
	return strings.Join([]string{
		event.Type,
		strings.TrimSpace(event.SessionID),
		strings.TrimSpace(event.AgentName),
		event.TraceID,
		streamCoalesceFirstPayloadString(event.Payload, "turn_id", "turnId", "turn"),
		streamCoalesceFirstPayloadString(event.Payload, "stream_id", "streamId"),
	}, "\x00")
}

func streamCoalesceFirstPayloadString(payload map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok {
			return value
		}
	}
	return ""
}

// streamCoalesceTextOperation 读取增量行的写入语义：`mode`/`operation`
// （append/delta 为追加；replace/snapshot 为快照替换；缺省按历史兼容追加）。
func streamCoalesceTextOperation(event runtimeevents.Event) string {
	operation := strings.ToLower(strings.TrimSpace(streamCoalesceFirstPayloadString(event.Payload, "mode", "operation")))
	return operation
}

// mergeCoalescibleRuntimeEvent 尝试把 incoming 折叠进 last（同一段流的相邻增量）。
// ok=false 表示不满足合帧约束，调用方按独立帧处理。
func mergeCoalescibleRuntimeEvent(last, incoming runtimeevents.Event) (runtimeevents.Event, bool) {
	if last.Type != incoming.Type || !streamCoalesceMergeableEventType(incoming.Type) {
		return incoming, false
	}
	lastSlot, okLast := streamCoalesceResolveTextSlot(last)
	incomingSlot, okIncoming := streamCoalesceResolveTextSlot(incoming)
	if !okLast || !okIncoming {
		return incoming, false
	}
	if strings.Join(lastSlot.path, ".") != strings.Join(incomingSlot.path, ".") {
		return incoming, false
	}
	if streamCoalesceIdentity(last) != streamCoalesceIdentity(incoming) {
		return incoming, false
	}
	lastSeq, okLastSeq := runtimeEventSeq(last)
	incomingSeq, okIncomingSeq := runtimeEventSeq(incoming)
	if !okLastSeq || !okIncomingSeq || lastSeq <= 0 || incomingSeq != lastSeq+1 {
		return incoming, false
	}
	if len(lastSlot.text)+len(incomingSlot.text) > streamCoalesceEventByteLimit {
		return incoming, false
	}

	mergedText := lastSlot.text + incomingSlot.text
	switch streamCoalesceTextOperation(incoming) {
	case "replace", "snapshot":
		// 权威快照：它已包含此前正文，直接替换而不是叠加。
		mergedText = incomingSlot.text
	}

	payload := streamCoalesceClonePayload(incoming.Payload, incomingSlot.path)
	streamCoalesceSetTextSlot(payload, incomingSlot.path, mergedText)
	fromSeq := lastSeq
	if existing, ok := asInt64(last.Payload[streamCoalesceFromKey]); ok && existing > 0 {
		fromSeq = existing
	}
	count := 1
	if existing, ok := asInt64(last.Payload[streamCoalesceCountKey]); ok && existing > 0 {
		count = int(existing)
	}
	payload[streamCoalesceFromKey] = fromSeq
	payload[streamCoalesceCountKey] = count + 1

	merged := incoming
	merged.Payload = payload
	return merged, true
}

// streamCoalesceClonePayload 浅拷贝 payload；合并目标含嵌套键时，只深拷贝那一条
// 嵌套 map，避免改动事件存储/bus 侧共享的原对象。
func streamCoalesceClonePayload(payload map[string]interface{}, path []string) map[string]interface{} {
	cloned := make(map[string]interface{}, len(payload)+2)
	for key, value := range payload {
		cloned[key] = value
	}
	if len(path) == 2 {
		if nested, ok := payload[path[0]].(map[string]interface{}); ok {
			nestedCloned := make(map[string]interface{}, len(nested)+1)
			for key, value := range nested {
				nestedCloned[key] = value
			}
			cloned[path[0]] = nestedCloned
		}
	}
	return cloned
}

// streamCoalesceSetTextSlot 把合并后的文本写回原槽位；嵌套 map 不存在时补建。
func streamCoalesceSetTextSlot(payload map[string]interface{}, path []string, text string) {
	if len(path) == 1 {
		payload[path[0]] = text
		return
	}
	nested, ok := payload[path[0]].(map[string]interface{})
	if !ok {
		nested = make(map[string]interface{}, 1)
		payload[path[0]] = nested
	}
	nested[path[1]] = text
}

// streamCoalesceCarrierCount 读取帧已折叠的行数（未合并帧视为 1），供连接级
// 指标统计「输入行 → 下发帧」的压缩比。
func streamCoalesceCarrierCount(event runtimeevents.Event) int {
	if count, ok := asInt64(event.Payload[streamCoalesceCountKey]); ok && count > 0 {
		return int(count)
	}
	return 1
}
