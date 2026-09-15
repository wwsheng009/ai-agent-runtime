package toolbroker

import (
	"context"
	"strings"
	"testing"
)

func TestValidateAgentMessageArgTypes(t *testing.T) {
	// Known keys of the documented kind pass.
	if err := validateAgentMessageArgTypes(ToolSendInput, map[string]interface{}{
		"id":        "child-1",
		"message":   "continue",
		"interrupt": true,
	}); err != nil {
		t.Fatalf("documented send_input kinds must pass, got %v", err)
	}
	if err := validateAgentMessageArgTypes(ToolSendMessage, map[string]interface{}{
		"target":  "child-1",
		"message": "note only",
	}); err != nil {
		t.Fatalf("documented send_message kinds must pass, got %v", err)
	}
	// The message tools are not fail-closed on unknown keys: unrelated
	// arguments stay untouched.
	if err := validateAgentMessageArgTypes(ToolSendMessage, map[string]interface{}{
		"target": "child-1",
		"note":   5,
	}); err != nil {
		t.Fatalf("unknown keys must not be rejected here, got %v", err)
	}

	cases := []struct {
		name     string
		tool     string
		args     map[string]interface{}
		wantKey  string
		wantKind string
	}{
		{
			name:     "send_input interrupt as string",
			tool:     ToolSendInput,
			args:     map[string]interface{}{"id": "child-1", "message": "stop", "interrupt": "true"},
			wantKey:  "interrupt",
			wantKind: "JSON boolean",
		},
		{
			name:     "send_input interrupt as number",
			tool:     ToolSendInput,
			args:     map[string]interface{}{"id": "child-1", "message": "stop", "interrupt": 1},
			wantKey:  "interrupt",
			wantKind: "JSON boolean",
		},
		{
			name:     "send_message target as number",
			tool:     ToolSendMessage,
			args:     map[string]interface{}{"target": 7, "message": "note"},
			wantKey:  "target",
			wantKind: "JSON string",
		},
		{
			name:     "followup_task message as number",
			tool:     ToolFollowupTask,
			args:     map[string]interface{}{"target": "child-1", "message": 42},
			wantKey:  "message",
			wantKind: "JSON string",
		},
		{
			name:     "send_input session_id as bool",
			tool:     ToolSendInput,
			args:     map[string]interface{}{"session_id": true, "message": "continue"},
			wantKey:  "session_id",
			wantKind: "JSON string",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAgentMessageArgTypes(tc.tool, tc.args)
			if err == nil {
				t.Fatalf("expected %s.%s kind mismatch to fail", tc.tool, tc.wantKey)
			}
			if !strings.Contains(err.Error(), "\""+tc.wantKey+"\"") {
				t.Fatalf("error should name the offending key, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantKind) {
				t.Fatalf("error should state the expected kind %q, got %v", tc.wantKind, err)
			}
			if !strings.Contains(err.Error(), tc.tool) {
				t.Fatalf("error should name the tool, got %v", err)
			}
		})
	}
}

// TestBroker_SendInputStringInterruptFailsClosed covers the silent-intent-drop
// case: `interrupt: "true"` used to be discarded, so the child received a
// prompt that did not interrupt it while the caller believed otherwise.
func TestBroker_SendInputStringInterruptFailsClosed(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}

	_, _, err := broker.Execute(context.Background(), "parent-session", ToolSendInput, map[string]interface{}{
		"id":        "child-1",
		"message":   "continue",
		"interrupt": "true",
	})
	if err == nil || !strings.Contains(err.Error(), "interrupt") {
		t.Fatalf("expected interrupt kind mismatch to fail closed, got %v", err)
	}
	if controller.lastInput.Message != "" || controller.lastInput.Interrupt != nil {
		t.Fatalf("rejected call must not reach the controller, got %#v", controller.lastInput)
	}
}

func TestBroker_SendMessageNumericTargetFailsClosed(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}

	_, _, err := broker.Execute(context.Background(), "parent-session", ToolSendMessage, map[string]interface{}{
		"target":  7,
		"message": "note only",
	})
	if err == nil || !strings.Contains(err.Error(), "target") {
		t.Fatalf("expected target kind mismatch to fail closed, got %v", err)
	}
	if controller.lastMsg.Message != "" {
		t.Fatalf("rejected call must not reach the controller, got %#v", controller.lastMsg)
	}
}
