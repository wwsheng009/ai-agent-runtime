package chat

import (
	"bytes"
	"testing"
)

func TestClonePendingToolInvocationCanExcludePersistedResult(t *testing.T) {
	pending := &PendingToolInvocation{
		ToolCallID:        "call-1",
		ToolName:          "shell",
		ArgsJSON:          []byte(`{"command":"build"}`),
		ResultMessageJSON: bytes.Repeat([]byte("result"), 128<<10),
		BatchToolCalls: []PendingToolCall{{
			ToolCallID: "call-2",
			ToolName:   "read",
			ArgsJSON:   []byte(`{"path":"next"}`),
		}},
	}

	recovery := clonePendingToolInvocation(pending, false)
	if recovery == nil {
		t.Fatal("expected recovery clone")
	}
	if len(recovery.ResultMessageJSON) != 0 {
		t.Fatalf("recovery clone retained %d result bytes", len(recovery.ResultMessageJSON))
	}
	if string(recovery.ArgsJSON) != string(pending.ArgsJSON) || string(recovery.BatchToolCalls[0].ArgsJSON) != string(pending.BatchToolCalls[0].ArgsJSON) {
		t.Fatal("recovery clone lost tool arguments")
	}
	recovery.ArgsJSON[0] = 'X'
	recovery.BatchToolCalls[0].ArgsJSON[0] = 'Y'
	if pending.ArgsJSON[0] == 'X' || pending.BatchToolCalls[0].ArgsJSON[0] == 'Y' {
		t.Fatal("recovery clone shares argument buffers")
	}

	stateClone := (&RuntimeState{SessionID: "session-1", PendingTool: pending}).Clone()
	if stateClone == nil || stateClone.PendingTool == nil {
		t.Fatal("expected runtime state clone")
	}
	if !bytes.Equal(stateClone.PendingTool.ResultMessageJSON, pending.ResultMessageJSON) {
		t.Fatal("runtime state clone must preserve recovery result")
	}
	stateClone.PendingTool.ResultMessageJSON[0] = 'Z'
	if pending.ResultMessageJSON[0] == 'Z' {
		t.Fatal("runtime state clone shares result buffer")
	}
}
