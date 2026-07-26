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
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
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

func TestACPEventBridge_RuntimeToolProgressStream(t *testing.T) {
	t.Parallel()

	bridge := newACPEventBridge("sess_1")
	emit := &recordingACPEmitter{}
	bridge.BeginPrompt("sess_1", emit)
	defer bridge.EndPrompt()

	bridge.HandleRuntimeEvent(runtimeevents.Event{
		Type:    runtimechat.EventToolStarted,
		TraceID: "trace-stream",
		Payload: map[string]interface{}{
			"tool_name":    "shell",
			"tool_call_id": "tc_stream",
		},
	})
	bridge.HandleRuntimeEvent(runtimeevents.Event{
		Type:    "tool.progress",
		TraceID: "trace-stream",
		Payload: map[string]interface{}{
			"tool_name":          "shell",
			"tool_call_id":       "tc_stream",
			"stream":             true,
			"stream_channel":     "combined",
			"stream_chunk_index": 1,
			"partial":            "hello from shell\n",
			"phase":              "stream",
		},
	})

	updates := emit.snapshot()
	// tool_call + in_progress + stream content update
	if len(updates) != 3 {
		t.Fatalf("expected 3 updates, got %d: %+v", len(updates), updates)
	}
	if updates[2].SessionUpdate != acp.SessionUpdateToolCallUpdate {
		t.Fatalf("progress kind = %q", updates[2].SessionUpdate)
	}
	if updates[2].Status != acp.ToolCallStatusInProgress {
		t.Fatalf("progress status = %q", updates[2].Status)
	}
	if updates[2].ToolCallID != "tc_stream" {
		t.Fatalf("toolCallId = %q", updates[2].ToolCallID)
	}
	if len(updates[2].ToolContent) == 0 || updates[2].ToolContent[0].Content == nil {
		t.Fatalf("expected stream content, got %+v", updates[2].ToolContent)
	}
	if !strings.Contains(updates[2].ToolContent[0].Content.Text, "hello from shell") {
		t.Fatalf("stream content = %+v", updates[2].ToolContent[0].Content)
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

func TestReplayACPSessionHistory_UserAssistantAndTools(t *testing.T) {
	t.Parallel()

	hostSess := &acpHostSession{
		id: "sess_hist",
		chat: &ChatSession{
			Messages: []runtimetypes.Message{
				{Role: "system", Content: "hidden system"},
				{Role: "user", Content: "list files"},
				{
					Role:    "assistant",
					Content: "I will list them.",
					ToolCalls: []runtimetypes.ToolCall{
						{ID: "call_1", Name: "shell", Args: map[string]interface{}{"command": "ls"}},
					},
				},
				{Role: "tool", ToolCallID: "call_1", Content: "a.go\nb.go"},
				{Role: "assistant", Content: "done"},
			},
		},
	}
	emit := &recordingACPEmitter{}
	if err := replayACPSessionHistory("sess_hist", hostSess, emit); err != nil {
		t.Fatalf("replay: %v", err)
	}
	updates := emit.snapshot()
	// user + assistant text + tool_call + tool_finished + final assistant
	if len(updates) != 5 {
		t.Fatalf("expected 5 updates, got %d: %+v", len(updates), updates)
	}
	if updates[0].SessionUpdate != acp.SessionUpdateUserMessageChunk {
		t.Fatalf("u0 = %q", updates[0].SessionUpdate)
	}
	if updates[0].Content == nil || updates[0].Content.Text != "list files" {
		t.Fatalf("user text = %+v", updates[0].Content)
	}
	if updates[1].SessionUpdate != acp.SessionUpdateAgentMessageChunk {
		t.Fatalf("u1 = %q", updates[1].SessionUpdate)
	}
	if updates[2].SessionUpdate != acp.SessionUpdateToolCall || updates[2].ToolCallID != "call_1" {
		t.Fatalf("tool start = %+v", updates[2])
	}
	if updates[3].SessionUpdate != acp.SessionUpdateToolCallUpdate || updates[3].Status != acp.ToolCallStatusCompleted {
		t.Fatalf("tool finish = %+v", updates[3])
	}
	if updates[4].SessionUpdate != acp.SessionUpdateAgentMessageChunk {
		t.Fatalf("u4 = %q", updates[4].SessionUpdate)
	}
}

func TestACPSessionHost_LoadSessionInMemoryReplay(t *testing.T) {
	t.Parallel()

	host := newACPSessionHost(&config.Config{}, &agentStdioOptions{ExecOptions: &ExecOptions{Ephemeral: true}})
	defer host.Close()

	sessionID := "acp_mem_1"
	host.mu.Lock()
	host.sess[sessionID] = &acpHostSession{
		id: sessionID,
		chat: &ChatSession{
			Messages: []runtimetypes.Message{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi there"},
			},
		},
	}
	host.mu.Unlock()

	emit := &recordingACPEmitter{}
	if err := host.LoadSession(context.Background(), acp.LoadSessionRequest{SessionID: sessionID}, emit); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	updates := emit.snapshot()
	if len(updates) != 2 {
		t.Fatalf("expected 2 updates, got %d: %+v", len(updates), updates)
	}
	if updates[0].Content == nil || updates[0].Content.Text != "hello" {
		t.Fatalf("user chunk = %+v", updates[0])
	}
	if updates[1].Content == nil || updates[1].Content.Text != "hi there" {
		t.Fatalf("agent chunk = %+v", updates[1])
	}
}

func TestACPSessionHost_LoadSessionUnknownID(t *testing.T) {
	t.Parallel()

	host := newACPSessionHost(&config.Config{}, &agentStdioOptions{ExecOptions: &ExecOptions{Ephemeral: true}})
	defer host.Close()

	err := host.LoadSession(context.Background(), acp.LoadSessionRequest{SessionID: "missing_session"}, &recordingACPEmitter{})
	if err == nil {
		t.Fatal("expected error for unknown session")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "not found") && !strings.Contains(msg, "session") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestACPSessionHost_LoadSessionEmptyID(t *testing.T) {
	t.Parallel()

	host := newACPSessionHost(&config.Config{}, &agentStdioOptions{ExecOptions: &ExecOptions{Ephemeral: true}})
	defer host.Close()

	err := host.LoadSession(context.Background(), acp.LoadSessionRequest{}, &recordingACPEmitter{})
	if err == nil {
		t.Fatal("expected error for empty sessionId")
	}
}
