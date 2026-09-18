package toolexec

import "testing"

func TestTerminalCircuitErrorCodeSessionLifecycle(t *testing.T) {
	for _, code := range []string{"SESSION_NOT_FOUND", "AGENT_SESSION_NOT_FOUND"} {
		if !isTerminalCircuitErrorCode(code) {
			t.Fatalf("isTerminalCircuitErrorCode(%q) = false, want true", code)
		}
	}
	if isTerminalCircuitErrorCode("TOOL_OK_LOOKING_CODE") {
		t.Fatal("unknown codes must not open the terminal circuit")
	}
}
