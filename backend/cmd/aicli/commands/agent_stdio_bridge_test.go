package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimeexecution "github.com/wwsheng009/ai-agent-runtime/internal/execution"
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

func TestACPEventBridge_StreamDeltaKeepsWhitespace(t *testing.T) {
	t.Parallel()

	bridge := newACPEventBridge("sess_ws")
	emit := &recordingACPEmitter{}
	bridge.BeginPrompt("sess_ws", emit)
	defer bridge.EndPrompt()

	// Whitespace at chunk boundaries is meaningful: clients concatenate deltas
	// to render the answer, so trimming each chunk would corrupt the text.
	bridge.HandleRuntimeEvent(runtimeevents.Event{
		Type:    runtimechat.EventAssistantDelta,
		Payload: map[string]interface{}{"delta": "hello "},
	})
	bridge.HandleRuntimeEvent(runtimeevents.Event{
		Type:    runtimechat.EventAssistantDelta,
		Payload: map[string]interface{}{"delta": " chunk"},
	})

	updates := emit.snapshot()
	if len(updates) != 2 {
		t.Fatalf("expected 2 updates, got %d: %+v", len(updates), updates)
	}
	var combined strings.Builder
	for i, update := range updates {
		if update.Content == nil {
			t.Fatalf("update %d missing content: %+v", i, update)
		}
		combined.WriteString(update.Content.Text)
	}
	if got := combined.String(); got != "hello  chunk" {
		t.Fatalf("streamed text = %q, want chunk whitespace preserved", got)
	}
}

func TestACPEventBridge_RuntimeReasoningEmitsThoughtChunk(t *testing.T) {
	t.Parallel()

	bridge := newACPEventBridge("sess_1")
	emit := &recordingACPEmitter{}
	bridge.BeginPrompt("sess_1", emit)
	defer bridge.EndPrompt()

	// Canonical dotted event with the nested ReasoningBlock payload the agent
	// loop publishes; stream_delta blocks carry the streamed text in "summary".
	bridge.HandleRuntimeEvent(runtimeevents.Event{
		Type: runtimechat.EventAssistantReasoningDelta,
		Payload: map[string]interface{}{
			"reasoning": map[string]interface{}{
				"format":  "stream_delta",
				"summary": "先梳理需求。",
			},
		},
	})
	// Legacy underscore alias with a flat payload spelling.
	bridge.HandleRuntimeEvent(runtimeevents.Event{
		Type: runtimechat.EventAssistantReasoning,
		Payload: map[string]interface{}{
			"delta": "再确认边界。",
		},
	})
	// Answer text streams on its own event and must not absorb reasoning.
	bridge.HandleRuntimeEvent(runtimeevents.Event{
		Type: runtimechat.EventAssistantDelta,
		Payload: map[string]interface{}{
			"delta": "开始处理。",
		},
	})

	updates := emit.snapshot()
	if len(updates) != 3 {
		t.Fatalf("expected 3 updates, got %d: %+v", len(updates), updates)
	}

	first := updates[0]
	if first.SessionUpdate != acp.SessionUpdateAgentThoughtChunk {
		t.Fatalf("u0 kind = %q, want %q", first.SessionUpdate, acp.SessionUpdateAgentThoughtChunk)
	}
	if first.Content == nil || first.Content.Text != "先梳理需求。" {
		t.Fatalf("u0 content = %+v", first.Content)
	}
	if first.MessageID == "" || !strings.HasSuffix(first.MessageID, "_thought") {
		t.Fatalf("u0 messageId = %q, want <turn>_thought", first.MessageID)
	}

	second := updates[1]
	if second.SessionUpdate != acp.SessionUpdateAgentThoughtChunk ||
		second.Content == nil || second.Content.Text != "再确认边界。" {
		t.Fatalf("legacy reasoning update = %+v", second)
	}
	if second.MessageID != first.MessageID {
		t.Fatalf("thought chunks must share one id: %q vs %q", second.MessageID, first.MessageID)
	}

	answer := updates[2]
	if answer.SessionUpdate != acp.SessionUpdateAgentMessageChunk {
		t.Fatalf("u2 kind = %q, want %q", answer.SessionUpdate, acp.SessionUpdateAgentMessageChunk)
	}
	if answer.Content == nil || answer.Content.Text != "开始处理。" {
		t.Fatalf("u2 content = %+v", answer.Content)
	}
	if answer.MessageID == "" || answer.MessageID == first.MessageID {
		t.Fatalf("answer messageId = %q, want a distinct turn id", answer.MessageID)
	}
	if strings.Contains(answer.Content.Text, "梳理") || strings.Contains(answer.Content.Text, "边界") {
		t.Fatalf("answer chunk leaked reasoning text: %q", answer.Content.Text)
	}

	// Wire format: thought updates must marshal as agent_thought_chunk carrying
	// their own messageId so clients collapse them separately from the answer.
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal thought update: %v", err)
	}
	wire := string(encoded)
	if !strings.Contains(wire, `"sessionUpdate":"agent_thought_chunk"`) {
		t.Fatalf("thought wire = %s", wire)
	}
	if !strings.Contains(wire, `"messageId":"`+first.MessageID+`"`) {
		t.Fatalf("thought wire missing messageId: %s", wire)
	}
}

func TestACPEventBridge_RuntimeModelFailureIsVisibleToClient(t *testing.T) {
	t.Parallel()

	bridge := newACPEventBridge("sess_1")
	emit := &recordingACPEmitter{}
	bridge.BeginPrompt("sess_1", emit)
	defer bridge.EndPrompt()

	bridge.HandleRuntimeEvent(runtimeevents.Event{
		Type: "llm.request.finished",
		Payload: map[string]interface{}{
			"success":     false,
			"error":       `HTTP 400: {"error":{"message":"Unsupported parameter: metadata"}}`,
			"error_code":  "UPSTREAM_INVALID_REQUEST",
			"retryable":   false,
			"next_action": "Correct the provider request or unsupported parameters before retrying.",
		},
	})

	updates := emit.snapshot()
	if len(updates) != 1 {
		t.Fatalf("expected one visible model failure update, got %d: %+v", len(updates), updates)
	}
	if updates[0].SessionUpdate != acp.SessionUpdateAgentMessageChunk || updates[0].Content == nil {
		t.Fatalf("unexpected failure update: %+v", updates[0])
	}
	text := updates[0].Content.Text
	for _, want := range []string{
		"model error [UPSTREAM_INVALID_REQUEST, retryable=false]",
		"Unsupported parameter: metadata",
		"[action] Correct the provider request",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("failure update %q does not contain %q", text, want)
		}
	}
}

func TestACPEventBridge_RuntimeSuccessfulModelRequestDoesNotEmitFailure(t *testing.T) {
	t.Parallel()

	bridge := newACPEventBridge("sess_1")
	emit := &recordingACPEmitter{}
	bridge.BeginPrompt("sess_1", emit)
	defer bridge.EndPrompt()

	bridge.HandleRuntimeEvent(runtimeevents.Event{
		Type: runtimechat.EventLLMRequestFinished,
		Payload: map[string]interface{}{
			"success": true,
			"error":   "stale diagnostic must not render",
		},
	})

	if updates := emit.snapshot(); len(updates) != 0 {
		t.Fatalf("successful request emitted failure update: %+v", updates)
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
	if !isACPCancelError(fmt.Errorf("turn aborted: %w", context.Canceled)) {
		t.Fatal("wrapped context.Canceled should match")
	}
	if !isACPCancelError(runtimeexecution.CancellationError("acp_prompt")) {
		t.Fatal("typed runtime cancellation should match")
	}
	if !isACPCancelError(userInterruptError()) {
		t.Fatal("typed user interrupt should match")
	}
	// Regression: this diagnostic used to contain "中断" and was silently
	// rewritten to stopReason=cancelled. Diagnostic text must never drive
	// cancellation classification.
	diagnostic := fmt.Errorf("actor 等待就绪超时（30s）：status=waiting_approval；resume 可能遗留了上一进程未结束的 turn，可 Ctrl+C 中断后重新 resume")
	if isACPCancelError(diagnostic) {
		t.Fatalf("diagnostic error must not be classified as cancellation: %v", diagnostic)
	}
	if isACPCancelError(fmt.Errorf("execution timed out after 30s")) {
		t.Fatal("plain timeout error must not be classified as cancellation")
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
	if updates[2].SessionUpdate != acp.SessionUpdateToolCall || updates[2].ToolCallID != "call_1" || updates[2].Name != "shell" {
		t.Fatalf("tool start = %+v", updates[2])
	}
	if updates[3].SessionUpdate != acp.SessionUpdateToolCallUpdate || updates[3].Status != acp.ToolCallStatusCompleted || updates[3].Name != "shell" {
		t.Fatalf("tool finish = %+v", updates[3])
	}
	if updates[4].SessionUpdate != acp.SessionUpdateAgentMessageChunk {
		t.Fatalf("u4 = %q", updates[4].SessionUpdate)
	}
}

func TestReplayACPSessionHistory_TagsMessageIDs(t *testing.T) {
	t.Parallel()

	hostSess := &acpHostSession{
		id: "sess_ids",
		chat: &ChatSession{
			Messages: []runtimetypes.Message{
				{Role: "user", Content: "first", Metadata: runtimetypes.Metadata{"message_id": "msg_u1"}},
				{Role: "assistant", Content: "answer one", Metadata: runtimetypes.Metadata{"message_id": "msg_a1"}},
				{Role: "user", Content: "second"},
				{Role: "assistant", Content: "answer two"},
			},
		},
	}
	emit := &recordingACPEmitter{}
	if err := replayACPSessionHistory("sess_ids", hostSess, emit); err != nil {
		t.Fatalf("replay: %v", err)
	}
	updates := emit.snapshot()
	if len(updates) != 4 {
		t.Fatalf("expected 4 updates, got %d: %+v", len(updates), updates)
	}
	// Durable metadata ids survive replay verbatim so refresh keeps grouping.
	if updates[0].MessageID != "msg_u1" || updates[1].MessageID != "msg_a1" {
		t.Fatalf("metadata ids lost: %q / %q", updates[0].MessageID, updates[1].MessageID)
	}
	// Messages without stored identity still get a non-empty replay id, and no
	// two distinct messages may share one.
	for i, update := range updates {
		if strings.TrimSpace(update.MessageID) == "" {
			t.Fatalf("update %d missing messageId: %+v", i, update)
		}
		for j := i + 1; j < len(updates); j++ {
			if update.MessageID == updates[j].MessageID {
				t.Fatalf("updates %d and %d share messageId %q", i, j, update.MessageID)
			}
		}
	}
	if !strings.HasPrefix(updates[2].MessageID, "replay_") {
		t.Fatalf("fallback replay id = %q, want replay_ prefix", updates[2].MessageID)
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
	// 2 history chunks + available_commands_update + session_info_update.
	if len(updates) != 4 {
		t.Fatalf("expected 4 updates, got %d: %+v", len(updates), updates)
	}
	if updates[0].Content == nil || updates[0].Content.Text != "hello" {
		t.Fatalf("user chunk = %+v", updates[0])
	}
	if updates[1].Content == nil || updates[1].Content.Text != "hi there" {
		t.Fatalf("agent chunk = %+v", updates[1])
	}
	if updates[2].SessionUpdate != acp.SessionUpdateAvailableCommands {
		t.Fatalf("expected available_commands_update, got %q", updates[2].SessionUpdate)
	}
	if len(updates[2].AvailableCommands) == 0 {
		t.Fatalf("available_commands_update must carry a non-empty catalog: %+v", updates[2])
	}
	if updates[3].SessionUpdate != acp.SessionUpdateSessionInfo {
		t.Fatalf("expected session_info_update, got %q", updates[3].SessionUpdate)
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
