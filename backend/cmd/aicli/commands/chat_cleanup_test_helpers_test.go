package commands

import (
	"io"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// submitToolRequested and submitToolResultDisplay are test-only compatibility
// wrappers. Production callers use the structured *Block APIs.
func (b *chatRuntimeEventBridge) submitToolRequested(toolCallID, toolName string, args map[string]interface{}) {
	if b == nil || b.renderEncoder == nil || strings.TrimSpace(toolCallID) == "" || strings.TrimSpace(toolName) == "" {
		return
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	payload := cloneRuntimeEventLogPayload(args)
	payload["tool_call_id"] = strings.TrimSpace(toolCallID)
	payload["tool_name"] = strings.TrimSpace(toolName)
	event := runtimeevents.Event{Type: "tool.requested", ToolName: toolName, Payload: payload}
	b.applyChangeSet(b.renderEncoder.SubmitToolCall(toolCallID, toolName, args))
	b.appendEventLog(event)
}

func (b *chatRuntimeEventBridge) submitToolResultDisplay(toolCallID, toolName, output, toolErr string, success bool, display string) {
	if b == nil || b.renderEncoder == nil || strings.TrimSpace(toolCallID) == "" || strings.TrimSpace(display) == "" {
		return
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	payload := map[string]interface{}{
		"tool_call_id": strings.TrimSpace(toolCallID),
		"tool_name":    strings.TrimSpace(toolName),
		"success":      success,
		"display_head": display,
	}
	if strings.TrimSpace(output) != "" {
		payload["output"] = output
	}
	if strings.TrimSpace(toolErr) != "" {
		payload["error"] = toolErr
	}
	typeName := "tool.completed"
	if !success {
		typeName = "tool.failed"
	}
	event := runtimeevents.Event{Type: typeName, ToolName: toolName, Payload: payload}
	b.applyChangeSet(b.renderEncoder.SubmitToolResultDisplay(toolCallID, display))
	b.appendEventLog(event)
}

func newChatSessionManager(dir string) (*runtimechat.SessionManager, string, string, error) {
	return newChatSessionManagerWithRuntimeConfig(dir, nil, "", "")
}

func renderChatRuntimeEvent(event runtimeevents.Event) string {
	return renderChatRuntimeTimelineEvent(event).Line
}

// waitUIActorIdle waits for the UI actor to drain its current queue. Tests use
// it for deterministic assertions; production producers only post.
func (c *chatInteractionCoordinator) waitUIActorIdle() {
	actor := c.currentUIActor()
	if actor == nil {
		return
	}
	actor.WaitIdle()
}

// enableUnifiedRendererWithWriter is the test-only direct-writer entry point.
// Production interactive sessions must use EnableUnifiedRendererGateway so all
// terminal effects flow through the session-scoped gateway.
func (c *chatInteractionCoordinator) enableUnifiedRendererWithWriter(writer io.Writer) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	if c.shutdown {
		c.mu.Unlock()
		return false
	}
	if c.primaryPresenter != nil {
		c.mu.Unlock()
		return true
	}
	surface := c.surface
	c.unifiedRenderer = true
	c.refreshTerminalWriterSnapshotLocked()
	c.mu.Unlock()
	if writer == nil {
		writer = os.Stdout
	}
	if surface != nil {
		surface.SetPhysicalWritesEnabled(false)
	}

	actor := c.ensureUIActor()
	if actor == nil {
		c.mu.Lock()
		c.unifiedRenderer = false
		c.refreshTerminalWriterSnapshotLocked()
		c.mu.Unlock()
		return false
	}
	if !actor.Post(ui.SetThemeContextAction{Theme: ui.CurrentThemeContext()}) ||
		!actor.Post(ui.SetSemanticActiveCellProjectionAction{Enabled: true}) {
		c.mu.Lock()
		c.unifiedRenderer = false
		c.refreshTerminalWriterSnapshotLocked()
		c.mu.Unlock()
		if chatDebugFlagEnabled() {
			aicliDiagRawln("[aicli-diag] enableUnifiedRendererWithWriter: actor.Post failed -> unified renderer OFF")
		}
		return false
	}
	actor.WaitIdle()
	presenter := ui.NewTerminalSessionPresenter(actor, writer, c.primaryTerminalGeometry)
	if c.SetPrimaryPresenter(presenter) {
		if chatDebugFlagEnabled() {
			aicliDiagRawln("[aicli-diag] enableUnifiedRendererWithWriter: presenter attached -> unified renderer ON")
		}
		return true
	}
	c.mu.Lock()
	if c.primaryPresenter == nil {
		c.unifiedRenderer = false
	}
	c.refreshTerminalWriterSnapshotLocked()
	c.mu.Unlock()
	return false
}
