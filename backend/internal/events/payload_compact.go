package events

import "strings"

// CompactDurableEventPayload 返回事件的浅拷贝，把流式增量 payload 里的正文别名
// 键（content/delta/text，三键同串是 loop.go 的既有产物）折叠为**规范键**，
// 让持久化层（会话事件库 SQLite / CLI journal）只落一份正文（P1-4，
// docs/plan/aicli-stream-pipeline-performance-audit-and-optimization-plan-20261011.md）。
//
// 规范键与所有读侧的首选回退一致，因此旧读侧无需改动即可继续取值：
//   - assistant 增量：delta（runtimeapi 合帧器 delta→content；前端 live 帧同键）
//   - reasoning 增量：text（合帧器 text→summary→reasoning.summary；
//     CLI streamEventText 同序）
//
// 安全边界：
//   - 仅作用于增量家族（assistant_delta/assistant.delta/assistant_reasoning/
//     assistant.reasoning，含历史别名）；其它类型原样返回；
//   - 仅在规范键存在且与其它别名**逐字节相等**时丢弃别名；值不等或规范键缺失
//     （遗留拼写）时原样返回，保留旧回退链；
//   - 只克隆 payload（浅拷贝），绝不修改调用方在途事件的 map——live 订阅者
//     仍能看到完整键集。
func CompactDurableEventPayload(event Event) Event {
	canonical := ""
	switch strings.ToLower(strings.TrimSpace(event.Type)) {
	case "assistant_delta", "assistant.delta":
		canonical = "delta"
	case "assistant_reasoning", "assistant.reasoning":
		canonical = "text"
	default:
		return event
	}
	payload := event.Payload
	if len(payload) == 0 {
		return event
	}
	canonicalText, ok := payload[canonical].(string)
	if !ok || canonicalText == "" {
		return event
	}
	var drop []string
	for _, key := range []string{"content", "delta", "text"} {
		if key == canonical {
			continue
		}
		if text, ok := payload[key].(string); ok && text == canonicalText {
			drop = append(drop, key)
		}
	}
	if len(drop) == 0 {
		return event
	}
	cloned := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	for _, key := range drop {
		delete(cloned, key)
	}
	event.Payload = cloned
	return event
}
