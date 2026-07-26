package commands

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

type recordingACPEmitter struct {
	mu      sync.Mutex
	updates []acp.SessionUpdate
}

func (e *recordingACPEmitter) SessionUpdate(sessionID string, update acp.SessionUpdate) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.updates = append(e.updates, update)
	return nil
}

func (e *recordingACPEmitter) snapshot() []acp.SessionUpdate {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]acp.SessionUpdate, len(e.updates))
	copy(out, e.updates)
	return out
}

type fixedPermissionRequester struct {
	result acp.RequestPermissionResult
	err    error
	calls  []acp.RequestPermissionParams
	mu     sync.Mutex
}

func (r *fixedPermissionRequester) RequestPermission(ctx context.Context, params acp.RequestPermissionParams) (acp.RequestPermissionResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, params)
	r.mu.Unlock()
	if r.err != nil {
		return acp.RequestPermissionResult{}, r.err
	}
	return r.result, nil
}

func TestACPEventBridge_ToolLifecycleStableID(t *testing.T) {
	t.Parallel()

	bridge := newACPEventBridge("sess_1")
	emit := &recordingACPEmitter{}
	bridge.BeginPrompt("sess_1", emit)
	defer bridge.EndPrompt()

	bridge.HandleChatCoreEvent(runtimechatcore.ChatEvent{
		Type:       runtimechatcore.EventTool,
		Stage:      "tool_requested",
		ToolName:   "shell",
		ToolCallID: "call-1",
		Arguments:  map[string]interface{}{"command": "echo hi"},
	})
	bridge.HandleChatCoreEvent(runtimechatcore.ChatEvent{
		Type:       runtimechatcore.EventTool,
		Stage:      "tool_result",
		ToolName:   "shell",
		ToolCallID: "call-1",
		Output:     "hi",
		Success:    true,
	})

	updates := emit.snapshot()
	// tool_call (pending) + tool_call_update (in_progress) + tool_call_update (completed)
	if len(updates) != 3 {
		t.Fatalf("expected 3 updates, got %d: %+v", len(updates), updates)
	}
	if updates[0].SessionUpdate != acp.SessionUpdateToolCall {
		t.Fatalf("first update kind = %q, want tool_call", updates[0].SessionUpdate)
	}
	if updates[0].ToolCallID == "" {
		t.Fatal("expected non-empty toolCallId")
	}
	if updates[0].ToolCallID != updates[1].ToolCallID || updates[0].ToolCallID != updates[2].ToolCallID {
		t.Fatalf("toolCallId not stable across updates: %q / %q / %q",
			updates[0].ToolCallID, updates[1].ToolCallID, updates[2].ToolCallID)
	}
	if updates[1].Status != acp.ToolCallStatusInProgress {
		t.Fatalf("progress status = %q, want %q", updates[1].Status, acp.ToolCallStatusInProgress)
	}
	if updates[2].Status != acp.ToolCallStatusCompleted {
		t.Fatalf("finish status = %q, want %q", updates[2].Status, acp.ToolCallStatusCompleted)
	}
}

func TestACPEventBridge_RuntimeAssistantDeltaAndMessage(t *testing.T) {
	t.Parallel()

	bridge := newACPEventBridge("sess_1")
	emit := &recordingACPEmitter{}
	bridge.BeginPrompt("sess_1", emit)
	defer bridge.EndPrompt()

	bridge.HandleRuntimeEvent(runtimeevents.Event{
		Type: runtimechat.EventAssistantDelta,
		Payload: map[string]interface{}{
			"delta": "Hel",
		},
	})
	bridge.HandleRuntimeEvent(runtimeevents.Event{
		Type: runtimechat.EventAssistantDelta,
		Payload: map[string]interface{}{
			"delta": "lo",
		},
	})
	// Full message should be ignored after deltas already streamed.
	bridge.HandleRuntimeEvent(runtimeevents.Event{
		Type: runtimechat.EventAssistantMessage,
		Payload: map[string]interface{}{
			"content": "Hello",
		},
	})

	updates := emit.snapshot()
	if len(updates) != 2 {
		t.Fatalf("expected 2 delta updates, got %d: %+v", len(updates), updates)
	}
	if updates[0].Content == nil || updates[0].Content.Text != "Hel" {
		t.Fatalf("first delta = %+v, want Hel", updates[0].Content)
	}
	if updates[1].Content == nil || updates[1].Content.Text != "lo" {
		t.Fatalf("second delta = %+v, want lo", updates[1].Content)
	}
	if !bridge.HasEmittedAssistant() {
		t.Fatal("expected HasEmittedAssistant after deltas")
	}
}

func TestACPEventBridge_RuntimeToolStartedFinished(t *testing.T) {
	t.Parallel()

	bridge := newACPEventBridge("sess_1")
	emit := &recordingACPEmitter{}
	bridge.BeginPrompt("sess_1", emit)
	defer bridge.EndPrompt()

	bridge.HandleRuntimeEvent(runtimeevents.Event{
		Type:    runtimechat.EventToolStarted,
		TraceID: "trace-1",
		Payload: map[string]interface{}{
			"tool_name":    "view",
			"tool_call_id": "tc_1",
			"path":         "a.go",
		},
	})
	bridge.HandleRuntimeEvent(runtimeevents.Event{
		Type:    runtimechat.EventToolFinished,
		TraceID: "trace-1",
		Payload: map[string]interface{}{
			"tool_name":    "view",
			"tool_call_id": "tc_1",
			"output":       "package main",
		},
	})

	updates := emit.snapshot()
	if len(updates) != 3 {
		t.Fatalf("expected 3 updates, got %d: %+v", len(updates), updates)
	}
	if updates[0].SessionUpdate != acp.SessionUpdateToolCall {
		t.Fatalf("started kind = %q", updates[0].SessionUpdate)
	}
	if updates[0].ToolCallID != "tc_1" {
		t.Fatalf("toolCallId = %q, want tc_1", updates[0].ToolCallID)
	}
	if updates[2].Status != acp.ToolCallStatusCompleted {
		t.Fatalf("finish status = %q", updates[2].Status)
	}
	if len(updates[2].ToolContent) == 0 || updates[2].ToolContent[0].Content == nil {
		t.Fatalf("expected tool content, got %+v", updates[2].ToolContent)
	}
	if !strings.Contains(updates[2].ToolContent[0].Content.Text, "package main") {
		t.Fatalf("tool content = %+v", updates[2].ToolContent[0].Content)
	}
}

func TestACPEventBridge_AskApprovalAllowOnce(t *testing.T) {
	t.Parallel()

	bridge := newACPEventBridge("sess_1")
	req := &fixedPermissionRequester{
		result: acp.RequestPermissionResult{
			Outcome: acp.PermissionOutcome{
				Outcome:  acp.PermissionOutcomeSelected,
				OptionID: "allow-once",
			},
		},
	}
	bridge.SetPermissionRequester(req)

	answer, err := bridge.AskApproval(&runtimechat.ApprovalRequest{
		ID:         "appr_1",
		ToolCallID: "tc_shell",
		ToolName:   "shell",
		ArgsJSON:   json.RawMessage(`{"command":"echo hi"}`),
		Reason:     "execute shell",
	}, nil)
	if err != nil {
		t.Fatalf("AskApproval: %v", err)
	}
	if !answer.Allowed {
		t.Fatal("expected Allowed=true for allow-once")
	}
	if answer.Reuse {
		t.Fatal("expected Reuse=false for allow-once")
	}
	if len(req.calls) != 1 {
		t.Fatalf("expected 1 permission call, got %d", len(req.calls))
	}
	if req.calls[0].SessionID != "sess_1" {
		t.Fatalf("sessionId = %q", req.calls[0].SessionID)
	}
	if req.calls[0].ToolCall.ToolCallID != "tc_shell" {
		t.Fatalf("toolCallId = %q", req.calls[0].ToolCall.ToolCallID)
	}
	if len(req.calls[0].Options) == 0 {
		t.Fatal("expected default permission options")
	}
}

func TestACPEventBridge_AskApprovalAllowAlwaysRemember(t *testing.T) {
	t.Parallel()

	bridge := newACPEventBridge("sess_1")
	req := &fixedPermissionRequester{
		result: acp.RequestPermissionResult{
			Outcome: acp.PermissionOutcome{
				Outcome:  acp.PermissionOutcomeSelected,
				OptionID: "allow-always",
			},
		},
	}
	bridge.SetPermissionRequester(req)

	answer, err := bridge.AskApproval(&runtimechat.ApprovalRequest{
		ToolName: "write",
	}, nil)
	if err != nil {
		t.Fatalf("AskApproval: %v", err)
	}
	if !answer.Allowed || !answer.Reuse {
		t.Fatalf("answer = %+v, want Allowed+Reuse", answer)
	}
}

func TestACPEventBridge_AskApprovalCancelled(t *testing.T) {
	t.Parallel()

	bridge := newACPEventBridge("sess_1")
	req := &fixedPermissionRequester{
		result: acp.RequestPermissionResult{
			Outcome: acp.PermissionOutcome{
				Outcome: acp.PermissionOutcomeCancelled,
			},
		},
	}
	bridge.SetPermissionRequester(req)

	answer, err := bridge.AskApproval(&runtimechat.ApprovalRequest{
		ToolName: "shell",
	}, nil)
	if err != nil {
		t.Fatalf("AskApproval: %v", err)
	}
	if answer.Allowed {
		t.Fatal("expected Allowed=false for cancelled outcome")
	}
}

func TestACPEventBridge_AskApprovalReject(t *testing.T) {
	t.Parallel()

	bridge := newACPEventBridge("sess_1")
	req := &fixedPermissionRequester{
		result: acp.RequestPermissionResult{
			Outcome: acp.PermissionOutcome{
				Outcome:  acp.PermissionOutcomeSelected,
				OptionID: "reject-once",
			},
		},
	}
	bridge.SetPermissionRequester(req)

	answer, err := bridge.AskApproval(&runtimechat.ApprovalRequest{
		ToolName: "shell",
	}, nil)
	if err != nil {
		t.Fatalf("AskApproval: %v", err)
	}
	if answer.Allowed || answer.Reuse {
		t.Fatalf("answer = %+v, want deny", answer)
	}
}

func TestIsACPCancelError(t *testing.T) {
	t.Parallel()

	if !isACPCancelError(context.Canceled) {
		t.Fatal("context.Canceled should match")
	}
	if isACPCancelError(nil) {
		t.Fatal("nil should not match")
	}
}

func TestNewAgentCommandRegistersStdio(t *testing.T) {
	t.Parallel()

	cmd := NewAgentCommand(func() *config.Config { return &config.Config{} })
	if cmd == nil {
		t.Fatal("NewAgentCommand returned nil")
	}
	found := false
	for _, c := range cmd.Commands() {
		if c.Name() == "stdio" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected agent stdio subcommand")
	}
	// Shared exec flags should be present without prompt.
	if cmd.Commands()[0].Flags().Lookup("provider") == nil {
		t.Fatal("expected --provider flag on agent stdio")
	}
	if cmd.Commands()[0].Flags().Lookup("prompt") != nil {
		t.Fatal("agent stdio should not expose --prompt")
	}
}
