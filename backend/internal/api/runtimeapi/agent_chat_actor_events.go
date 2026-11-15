package runtimeapi

import (
	"encoding/json"
	"strings"
	"sync"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

var agentChatActorStreamEventTypes = []string{
	"assistant_delta",
	"assistant.delta",
	"assistant_reasoning",
	"assistant.reasoning",
	"assistant.image_progress",
	runtimeevents.EventAssistantStreamReset,
}

// agentChatActorStreamProjector translates durable actor runtime events into
// the exact chat SSE frames emitted by the direct AgentChat loop. It keeps the
// connection-local chunk index and text character count while preserving the
// provider stream identity (stream_id / sequence / turn_id / step).
type agentChatActorStreamProjector struct {
	mu        sync.Mutex
	emitter   *sseEmitter
	index     int
	textChars int
}

func newAgentChatActorStreamProjector(emitter *sseEmitter) *agentChatActorStreamProjector {
	return &agentChatActorStreamProjector{emitter: emitter}
}

func (p *agentChatActorStreamProjector) EmitLive(eventName string, payload map[string]interface{}) {
	if p == nil || p.emitter == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.emitter.Emit(eventName, payload)
}

func (p *agentChatActorStreamProjector) Project(event runtimeevents.Event) {
	if p == nil || p.emitter == nil {
		return
	}
	if event.Type == "" {
		return
	}
	payload := event.Payload

	p.mu.Lock()
	defer p.mu.Unlock()

	switch event.Type {
	case "assistant_delta", "assistant.delta":
		content := agentChatPayloadRawString(payload, "content", "text", "delta")
		if content == "" {
			return
		}
		p.index++
		p.textChars += len(content)
		chunk := agentChatActorStreamChunk(payload, llm.EventTypeText)
		chunk.Content = content
		p.emitter.Emit("chunk", buildStreamChunkPayload(chunk, p.index, p.textChars))
	case "assistant_reasoning", "assistant.reasoning":
		content := agentChatPayloadRawString(payload, "content", "text", "delta")
		if content == "" {
			return
		}
		p.index++
		chunk := agentChatActorStreamChunk(payload, llm.EventTypeReasoning)
		chunk.Content = content
		p.emitter.Emit("reasoning", buildStreamChunkPayload(chunk, p.index, p.textChars))
	case "assistant.image_progress":
		image, _ := payload["image"].(map[string]interface{})
		if len(image) == 0 {
			return
		}
		p.index++
		chunk := agentChatActorStreamChunk(payload, llm.EventTypeImage)
		chunk.Metadata = image
		p.emitter.Emit("chunk", buildStreamChunkPayload(chunk, p.index, p.textChars))
	case runtimeevents.EventAssistantStreamReset:
		p.index++
		p.textChars = 0
		chunk := agentChatActorStreamChunk(payload, llm.EventTypeReset)
		chunk.Metadata = agentChatActorResetMetadata(payload)
		p.emitter.Emit("reset", buildStreamChunkPayload(chunk, p.index, 0))
	}
}

func subscribeAgentChatActorEvents(
	bus *runtimeevents.Bus,
	sessionID, turnID string,
	handle func(runtimeevents.Event),
) func() {
	if bus == nil || handle == nil {
		return func() {}
	}
	sessionID = strings.TrimSpace(sessionID)
	turnID = strings.TrimSpace(turnID)
	unsubscribes := make([]func(), 0, len(agentChatActorStreamEventTypes))
	for _, eventType := range agentChatActorStreamEventTypes {
		eventType := eventType
		unsubscribes = append(unsubscribes, bus.SubscribeCancelable(eventType, func(event runtimeevents.Event) {
			if sessionID != "" && strings.TrimSpace(event.SessionID) != "" && strings.TrimSpace(event.SessionID) != sessionID {
				return
			}
			if eventTurnID := payloadStringValue(event.Payload, "turn_id"); eventTurnID != "" && turnID != "" && eventTurnID != turnID {
				return
			}
			handle(event)
		}))
	}
	return func() {
		for _, unsubscribe := range unsubscribes {
			unsubscribe()
		}
	}
}

func agentChatActorStreamChunk(payload map[string]interface{}, chunkType llm.StreamEventType) llm.StreamChunk {
	sequence := uint64(0)
	if value, ok := agentChatPayloadUint64(payload, "sequence"); ok {
		sequence = value
	}
	step := 0
	if value, ok := agentChatPayloadInt(payload, "step"); ok {
		step = value
	}
	return llm.StreamChunk{
		Type:     chunkType,
		StreamID: payloadStringValue(payload, "stream_id"),
		Sequence: sequence,
		TurnID:   payloadStringValue(payload, "turn_id"),
		Step:     step,
	}
}

func agentChatActorResetMetadata(payload map[string]interface{}) map[string]interface{} {
	metadata := make(map[string]interface{})
	for _, key := range []string{"reason", "retry_reason", "mode", "stream_id", "turn_id"} {
		if value := agentChatPayloadRawString(payload, key); value != "" {
			metadata[key] = value
		}
	}
	for _, key := range []string{"attempt", "max_attempts", "step"} {
		if value, ok := agentChatPayloadInt(payload, key); ok {
			metadata[key] = value
		}
	}
	return metadata
}

func agentChatPayloadRawString(payload map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		switch value := payload[key].(type) {
		case string:
			return value
		case []byte:
			return string(value)
		}
	}
	return ""
}

func agentChatPayloadInt(payload map[string]interface{}, key string) (int, bool) {
	switch value := payload[key].(type) {
	case int:
		return value, true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	case uint:
		return int(value), true
	case uint32:
		return int(value), true
	case uint64:
		return int(value), true
	case float32:
		return int(value), true
	case float64:
		return int(value), true
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			return int(parsed), true
		}
	}
	return 0, false
}

func agentChatPayloadUint64(payload map[string]interface{}, key string) (uint64, bool) {
	switch value := payload[key].(type) {
	case uint64:
		return value, true
	case uint:
		return uint64(value), true
	case uint32:
		return uint64(value), true
	case int:
		if value >= 0 {
			return uint64(value), true
		}
	case int64:
		if value >= 0 {
			return uint64(value), true
		}
	case float64:
		if value >= 0 {
			return uint64(value), true
		}
	case json.Number:
		if parsed, err := value.Int64(); err == nil && parsed >= 0 {
			return uint64(parsed), true
		}
	}
	return 0, false
}
