package commands

import (
	"strings"
	"sync"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

type execEventBridge struct {
	processor ExecEventProcessor
	mu        sync.Mutex
	itemIDs   map[string]string
}

func newExecEventBridge(processor ExecEventProcessor) *execEventBridge {
	return &execEventBridge{
		processor: processor,
		itemIDs:   make(map[string]string),
	}
}

func (b *execEventBridge) HandleChatCoreEvent(event runtimechatcore.ChatEvent) {
	if b == nil || b.processor == nil {
		return
	}
	switch event.Type {
	case runtimechatcore.EventTool:
		b.handleChatCoreToolEvent(event)
	case runtimechatcore.EventWarning:
		if strings.TrimSpace(event.Content) != "" {
			b.processor.OnWarning(event.Content)
		}
	case runtimechatcore.EventResult:
		if event.Content != "" {
			itemID := b.stableItemID("assistant:"+strings.TrimSpace(event.ToolCallID), "assistant")
			b.processor.OnItemUpdated(ItemUpdatedEvent{
				ItemID:   itemID,
				ItemType: "message",
				Details:  MessageDetails{Role: "assistant", Content: event.Content},
			})
		}
	}
}

func (b *execEventBridge) HandleRuntimeEvent(event runtimeevents.Event) {
	if b == nil || b.processor == nil {
		return
	}
	switch event.Type {
	case runtimechat.EventToolStarted, "tool.requested":
		toolCallID := firstNonEmptyChatValue(payloadStringValue(event.Payload["tool_call_id"]), event.TraceID, event.ToolName)
		itemID := b.stableItemID("runtime_tool:"+toolCallID, "tool_call")
		b.processor.OnItemStarted(ItemStartedEvent{
			ItemID:   itemID,
			ItemType: "tool_call",
			Details: ToolCallDetails{
				ToolName: runtimeEventToolName(event),
				Args:     cloneRuntimeEventLogPayload(event.Payload),
			},
		})
	case runtimechat.EventToolFinished, "tool.completed":
		toolCallID := firstNonEmptyChatValue(payloadStringValue(event.Payload["tool_call_id"]), event.TraceID, event.ToolName)
		itemID := b.stableItemID("runtime_tool:"+toolCallID, "tool_call")
		status := "success"
		if runtimeEventError(event.Payload) != nil {
			status = "failed"
		}
		b.processor.OnItemCompleted(ItemCompletedEvent{
			ItemID:   itemID,
			ItemType: "tool_call",
			Status:   status,
			Details: ToolCallDetails{
				ToolName: runtimeEventToolName(event),
				Result:   cloneRuntimeEventLogPayload(event.Payload),
			},
		})
	case runtimechat.EventAssistantMessage, "assistant.message":
		content := strings.TrimSpace(payloadStringValue(event.Payload["content"]))
		if content != "" {
			itemID := b.stableItemID("runtime_assistant:"+firstNonEmptyChatValue(event.TraceID, event.SessionID), "message")
			b.processor.OnItemUpdated(ItemUpdatedEvent{
				ItemID:   itemID,
				ItemType: "message",
				Details:  MessageDetails{Role: "assistant", Content: content},
			})
		}
	}
}

func (b *execEventBridge) handleChatCoreToolEvent(event runtimechatcore.ChatEvent) {
	key := "chatcore_tool:" + firstNonEmptyChatValue(event.ToolCallID, event.ToolName)
	itemID := b.stableItemID(key, "tool_call")
	switch event.Stage {
	case "tool_requested":
		b.processor.OnItemStarted(ItemStartedEvent{
			ItemID:   itemID,
			ItemType: "tool_call",
			Details:  ToolCallDetails{ToolName: event.ToolName, Args: event.Arguments},
		})
	case "tool_result":
		status := "success"
		if !event.Success || strings.TrimSpace(event.Error) != "" {
			status = "failed"
		}
		b.processor.OnItemCompleted(ItemCompletedEvent{
			ItemID:   itemID,
			ItemType: "tool_call",
			Status:   status,
			Details: ToolCallDetails{
				ToolName: event.ToolName,
				Result:   map[string]interface{}{"output": event.Output, "error": event.Error, "metadata": event.Metadata},
			},
		})
	}
}

func (b *execEventBridge) stableItemID(key string, fallbackPrefix string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return generateItemID()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.itemIDs == nil {
		b.itemIDs = make(map[string]string)
	}
	if id := b.itemIDs[key]; id != "" {
		return id
	}
	id := generateItemID()
	if fallbackPrefix != "" {
		id = strings.TrimSpace(fallbackPrefix) + "_" + id
	}
	b.itemIDs[key] = id
	return id
}
