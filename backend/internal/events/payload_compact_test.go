package events

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// P1-4：流式增量 payload 的正文三键折叠。契约：
//  1. assistant 增量折叠到 delta；reasoning 增量折叠到 text（读侧首选键）；
//  2. 仅在规范键存在且与别名逐字节相等时丢弃别名；
//  3. 不改写在途事件的 map（live 订阅者仍见完整键集）；
//  4. 非增量家族 / 规范键缺失 / 值不等：原样返回。
func TestCompactDurableEventPayload(t *testing.T) {
	assistant := Event{
		Type: "assistant_delta",
		Payload: map[string]interface{}{
			"delta": "Hello", "content": "Hello", "text": "Hello", "sequence": 3,
		},
	}
	compacted := CompactDurableEventPayload(assistant)
	require.Equal(t, "Hello", compacted.Payload["delta"])
	require.NotContains(t, compacted.Payload, "content")
	require.NotContains(t, compacted.Payload, "text")
	require.Equal(t, 3, compacted.Payload["sequence"], "非正文键必须保留")
	// 在途事件不被改写。
	require.Contains(t, assistant.Payload, "content")
	require.Contains(t, assistant.Payload, "text")

	reasoning := Event{
		Type: "assistant.reasoning",
		Payload: map[string]interface{}{
			"text": "step", "content": "step", "delta": "step",
			"reasoning": map[string]interface{}{"format": "stream_delta", "summary": "step"},
		},
	}
	compacted = CompactDurableEventPayload(reasoning)
	require.Equal(t, "step", compacted.Payload["text"])
	require.NotContains(t, compacted.Payload, "content")
	require.NotContains(t, compacted.Payload, "delta")
	require.Equal(t, "step", compacted.Payload["reasoning"].(map[string]interface{})["summary"],
		"嵌套 reasoning 块不参与折叠")

	// 值不等：保持原样（读侧回退链语义不变）。
	mixed := Event{Type: "assistant_delta", Payload: map[string]interface{}{
		"delta": "new", "content": "old",
	}}
	compacted = CompactDurableEventPayload(mixed)
	require.Contains(t, compacted.Payload, "content")

	// 规范键缺失（遗留拼写）：不折叠也不改名。
	legacy := Event{Type: "assistant.reasoning", Payload: map[string]interface{}{
		"summary": "s", "delta": "s",
	}}
	compacted = CompactDurableEventPayload(legacy)
	require.Contains(t, compacted.Payload, "summary")
	require.Contains(t, compacted.Payload, "delta")

	// 非增量家族：原样返回。
	other := Event{Type: "assistant_message", Payload: map[string]interface{}{"content": "final"}}
	compacted = CompactDurableEventPayload(other)
	require.Equal(t, "final", compacted.Payload["content"])
}
