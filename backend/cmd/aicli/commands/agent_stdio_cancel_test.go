package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
)

// TestACPEventBridge_CancelledToolTerminals verifies that a cancelled prompt
// closes dangling in_progress tool calls with a spec-compliant failed terminal.
func TestACPEventBridge_CancelledToolTerminals(t *testing.T) {
	bridge := newACPEventBridge("sess_1")
	emit := &recordingACPEmitter{}
	bridge.BeginPrompt("sess_1", emit)
	defer bridge.EndPrompt()

	bridge.HandleChatCoreEvent(runtimechatcore.ChatEvent{
		Type:       runtimechatcore.EventTool,
		Stage:      "tool_requested",
		ToolName:   "shell",
		ToolCallID: "call-cancel-1",
		Arguments:  map[string]interface{}{"command": "sleep 100"},
	})

	bridge.EmitCancelledToolTerminals()

	updates := emit.snapshot()
	if len(updates) < 3 {
		t.Fatalf("expected >=3 updates, got %d: %+v", len(updates), updates)
	}
	last := updates[len(updates)-1]
	if last.SessionUpdate != acp.SessionUpdateToolCallUpdate {
		t.Fatalf("terminal kind = %q, want tool_call_update", last.SessionUpdate)
	}
	if last.Status != acp.ToolCallStatusFailed {
		t.Fatalf("terminal status = %q, want failed", last.Status)
	}
	if len(last.ToolContent) == 0 || !strings.Contains(last.ToolContent[0].Content.Text, "cancel") {
		t.Fatalf("terminal content missing cancellation notice: %+v", last.ToolContent)
	}

	// Second call must be a no-op — terminals are one-shot per cancel.
	before := len(emit.snapshot())
	bridge.EmitCancelledToolTerminals()
	if after := len(emit.snapshot()); after != before {
		t.Fatalf("repeat cancel emitted extra updates: %d -> %d", before, after)
	}
}

// TestACPEventBridge_AskApprovalUsesPromptCtx verifies that a permission
// request bound to a cancelled prompt context aborts instead of hanging.
func TestACPEventBridge_AskApprovalUsesPromptCtx(t *testing.T) {
	bridge := newACPEventBridge("sess_1")
	emit := &recordingACPEmitter{}
	bridge.BeginPrompt("sess_1", emit)
	defer bridge.EndPrompt()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // prompt already cancelled
	bridge.BeginPromptCtx(ctx)

	slow := &blockingPermissionRequester{}
	bridge.SetPermissionRequester(slow)

	_, err := bridge.AskApproval(&runtimechat.ApprovalRequest{ToolName: "shell", ToolCallID: "call-1"}, nil)
	if err == nil {
		t.Fatal("expected ctx error after prompt cancellation")
	}
}

type blockingPermissionRequester struct{}

func (r *blockingPermissionRequester) RequestPermission(ctx context.Context, params acp.RequestPermissionParams) (acp.RequestPermissionResult, error) {
	select {
	case <-ctx.Done():
		return acp.RequestPermissionResult{}, ctx.Err()
	case <-time.After(5 * time.Second):
		return acp.RequestPermissionResult{Outcome: acp.PermissionOutcome{Outcome: acp.PermissionOutcomeSelected, OptionID: "allow-once"}}, nil
	}
}
