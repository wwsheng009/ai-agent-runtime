package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// acpEventBridge maps chat/runtime events onto ACP session/update notifications
// and bridges tool approvals to session/request_permission.
type acpEventBridge struct {
	mu sync.Mutex

	sessionID string
	emit      acp.Emitter
	perm      acp.PermissionRequester

	// toolCallIDs maps internal keys -> stable toolCallId for updates.
	toolCallIDs map[string]string
	// emittedAssistant is true once any agent_message_chunk was sent.
	emittedAssistant bool
}

func newACPEventBridge(sessionID string) *acpEventBridge {
	return &acpEventBridge{
		sessionID:   strings.TrimSpace(sessionID),
		toolCallIDs: make(map[string]string),
	}
}

func (b *acpEventBridge) SetPermissionRequester(req acp.PermissionRequester) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.perm = req
}

func (b *acpEventBridge) BeginPrompt(sessionID string, emit acp.Emitter) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if id := strings.TrimSpace(sessionID); id != "" {
		b.sessionID = id
	}
	b.emit = emit
	b.emittedAssistant = false
	b.toolCallIDs = make(map[string]string)
}

func (b *acpEventBridge) EndPrompt() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.emit = nil
}

func (b *acpEventBridge) HasEmittedAssistant() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.emittedAssistant
}

func (b *acpEventBridge) EmitAssistant(text string) error {
	if b == nil {
		return nil
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return b.sessionUpdate(acp.AgentMessageChunk(text))
}

// AskApproval implements the chatRuntimeEventBridge.askApproval hook.
func (b *acpEventBridge) AskApproval(approval *runtimechat.ApprovalRequest, contextLines []string) (chatApprovalAnswer, error) {
	if b == nil {
		return chatApprovalAnswer{}, fmt.Errorf("acp event bridge is nil")
	}
	b.mu.Lock()
	perm := b.perm
	sessionID := b.sessionID
	b.mu.Unlock()
	if perm == nil {
		return chatApprovalAnswer{}, fmt.Errorf("acp permission requester not configured")
	}

	toolName := "tool"
	toolCallID := ""
	var rawInput interface{}
	if approval != nil {
		if name := strings.TrimSpace(approval.ToolName); name != "" {
			toolName = name
		}
		toolCallID = strings.TrimSpace(approval.ToolCallID)
		if toolCallID == "" {
			toolCallID = strings.TrimSpace(approval.ID)
		}
		if len(approval.ArgsJSON) > 0 {
			var parsed interface{}
			if err := json.Unmarshal(approval.ArgsJSON, &parsed); err == nil {
				rawInput = parsed
			} else {
				rawInput = string(approval.ArgsJSON)
			}
		}
	}
	if toolCallID == "" {
		toolCallID = "approval_" + generateItemID()
	}

	kind := acpToolKindForName(toolName)
	title := toolName
	if approval != nil && strings.TrimSpace(approval.Reason) != "" {
		title = toolName + ": " + truncateForACP(approval.Reason, 80)
	}
	if len(contextLines) > 0 {
		// Keep title short; context is available in rawInput if needed.
		_ = contextLines
	}

	params := acp.RequestPermissionParams{
		SessionID: sessionID,
		ToolCall: acp.ToolCallPermission{
			ToolCallID: toolCallID,
			Title:      title,
			Kind:       kind,
			Status:     acp.ToolCallStatusPending,
			RawInput:   rawInput,
		},
		Options: acp.DefaultPermissionOptions(),
	}

	result, err := perm.RequestPermission(context.Background(), params)
	if err != nil {
		return chatApprovalAnswer{}, err
	}
	if strings.EqualFold(result.Outcome.Outcome, acp.PermissionOutcomeCancelled) {
		return chatApprovalAnswer{Allowed: false}, nil
	}
	optionID := strings.TrimSpace(result.Outcome.OptionID)
	return chatApprovalAnswer{
		Allowed: acp.IsAllowOption(optionID),
		Reuse:   acp.IsRememberOption(optionID),
	}, nil
}

func (b *acpEventBridge) HandleChatCoreEvent(event runtimechatcore.ChatEvent) {
	if b == nil {
		return
	}
	switch event.Type {
	case runtimechatcore.EventTool:
		b.handleChatCoreToolEvent(event)
	case runtimechatcore.EventResult:
		// Prefer streaming assistant deltas from the runtime event path.
		// Only emit result content if nothing streamed yet.
		if b.HasEmittedAssistant() {
			return
		}
		if text := strings.TrimSpace(event.Content); text != "" {
			_ = b.sessionUpdate(acp.AgentMessageChunk(text))
		}
	}
}

func (b *acpEventBridge) HandleRuntimeEvent(event runtimeevents.Event) {
	if b == nil {
		return
	}
	switch event.Type {
	case runtimechat.EventToolStarted, "tool.requested":
		toolName := runtimeEventToolName(event)
		toolCallID := firstNonEmptyChatValue(
			payloadStringValue(event.Payload["tool_call_id"]),
			event.TraceID,
			toolName,
		)
		id := b.stableToolCallID("runtime_tool:"+toolCallID, toolCallID)
		rawInput := cloneRuntimeEventLogPayload(event.Payload)
		kind := acpToolKindForName(toolName)
		_ = b.sessionUpdate(acp.ToolCallStarted(id, toolName, kind, rawInput))
		_ = b.sessionUpdate(acp.ToolCallProgress(id, acp.ToolCallStatusInProgress))

	case runtimechat.EventToolFinished, "tool.completed":
		toolName := runtimeEventToolName(event)
		toolCallID := firstNonEmptyChatValue(
			payloadStringValue(event.Payload["tool_call_id"]),
			event.TraceID,
			toolName,
		)
		id := b.stableToolCallID("runtime_tool:"+toolCallID, toolCallID)
		status := acp.ToolCallStatusCompleted
		if runtimeEventError(event.Payload) != nil {
			status = acp.ToolCallStatusFailed
		}
		rawOutput := cloneRuntimeEventLogPayload(event.Payload)
		var content []acp.ToolCallContent
		if out := payloadStringValue(event.Payload["output"]); out != "" {
			content = append(content, acp.TextToolContent(out))
		} else if errMsg := payloadStringValue(event.Payload["error"]); errMsg != "" {
			content = append(content, acp.TextToolContent(errMsg))
		}
		_ = b.sessionUpdate(acp.ToolCallFinished(id, status, rawOutput, content))

	case runtimechat.EventAssistantDelta, "assistant.delta":
		delta := payloadStringValue(event.Payload["delta"])
		if delta == "" {
			delta = payloadStringValue(event.Payload["content"])
		}
		if delta != "" {
			_ = b.sessionUpdate(acp.AgentMessageChunk(delta))
		}

	case runtimechat.EventAssistantMessage, "assistant.message":
		// Prefer streaming deltas; only emit full content if nothing streamed yet.
		b.mu.Lock()
		already := b.emittedAssistant
		b.mu.Unlock()
		if already {
			return
		}
		content := strings.TrimSpace(payloadStringValue(event.Payload["content"]))
		if content != "" {
			_ = b.sessionUpdate(acp.AgentMessageChunk(content))
		}
	}
}

func (b *acpEventBridge) handleChatCoreToolEvent(event runtimechatcore.ChatEvent) {
	key := "chatcore_tool:" + firstNonEmptyChatValue(event.ToolCallID, event.ToolName)
	id := b.stableToolCallID(key, firstNonEmptyChatValue(event.ToolCallID, event.ToolName))
	kind := acpToolKindForName(event.ToolName)
	switch event.Stage {
	case "tool_requested":
		_ = b.sessionUpdate(acp.ToolCallStarted(id, event.ToolName, kind, event.Arguments))
		_ = b.sessionUpdate(acp.ToolCallProgress(id, acp.ToolCallStatusInProgress))
	case "tool_result":
		status := acp.ToolCallStatusCompleted
		if !event.Success || strings.TrimSpace(event.Error) != "" {
			status = acp.ToolCallStatusFailed
		}
		rawOutput := map[string]interface{}{
			"output":   event.Output,
			"error":    event.Error,
			"metadata": event.Metadata,
		}
		var content []acp.ToolCallContent
		if strings.TrimSpace(event.Output) != "" {
			content = append(content, acp.TextToolContent(event.Output))
		} else if strings.TrimSpace(event.Error) != "" {
			content = append(content, acp.TextToolContent(event.Error))
		}
		_ = b.sessionUpdate(acp.ToolCallFinished(id, status, rawOutput, content))
	}
}

func (b *acpEventBridge) sessionUpdate(update acp.SessionUpdate) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	emit := b.emit
	sessionID := b.sessionID
	if update.SessionUpdate == acp.SessionUpdateAgentMessageChunk {
		b.emittedAssistant = true
	}
	b.mu.Unlock()
	if emit == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	return emit.SessionUpdate(sessionID, update)
}

func (b *acpEventBridge) stableToolCallID(key, preferred string) string {
	key = strings.TrimSpace(key)
	preferred = strings.TrimSpace(preferred)
	if b == nil {
		if preferred != "" {
			return preferred
		}
		return generateItemID()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.toolCallIDs == nil {
		b.toolCallIDs = make(map[string]string)
	}
	if key != "" {
		if id := b.toolCallIDs[key]; id != "" {
			return id
		}
	}
	id := preferred
	if id == "" {
		id = "tool_" + generateItemID()
	}
	if key != "" {
		b.toolCallIDs[key] = id
	}
	return id
}

func acpToolKindForName(toolName string) string {
	toolName = strings.TrimSpace(toolName)
	if tax, ok := runtimepolicy.LookupToolTaxonomy(toolName); ok {
		return acp.MapToolKind(tax.Kind)
	}
	return acp.MapToolKind("")
}

func truncateForACP(text string, max int) string {
	text = strings.TrimSpace(text)
	if max <= 0 || len(text) <= max {
		return text
	}
	if max <= 3 {
		return text[:max]
	}
	return text[:max-3] + "..."
}

// silenceChatRuntimeBridgeWriters prevents human console output during ACP turns.
// Stdout is reserved for NDJSON protocol messages.
func silenceChatRuntimeBridgeWriters(bridge *chatRuntimeEventBridge) {
	if bridge == nil {
		return
	}
	bridge.writeLine = func(string) {}
	bridge.writeDelta = func(string) {}
	bridge.finalizeDelta = func() {}
	bridge.completeDelta = func(string) bool { return false }
	bridge.writeReasoningDelta = func(*runtimetypes.ReasoningBlock) {}
	bridge.finalizeReasoning = func() {}
	bridge.completeReasoning = func(*runtimetypes.ReasoningBlock) bool { return false }
	bridge.renderResponse = func(string) {}
	bridge.writePrompt = func() {}
}
