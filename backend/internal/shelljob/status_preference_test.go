package shelljob

import "testing"

func TestPreferTerminalStatusKeepsObservedOutcome(t *testing.T) {
	// A recovery sweep may rewrite the stored record to "orphaned" after the
	// waiter already saw the real outcome. The known outcome must win, otherwise
	// a successful command is reported as an unknown result.
	cases := []struct {
		name     string
		values   []string
		expected string
	}{
		{name: "store orphaned does not downgrade completed", values: []string{"orphaned", "completed"}, expected: "completed"},
		{name: "store orphaned does not downgrade failed", values: []string{"orphaned", "failed"}, expected: "failed"},
		{name: "orphaned kept when nothing terminal is known", values: []string{"orphaned", "running"}, expected: "orphaned"},
		{name: "first terminal wins", values: []string{"completed", "failed"}, expected: "completed"},
		{name: "blank falls back", values: []string{"", "cancelled"}, expected: "cancelled"},
	}
	for _, testCase := range cases {
		if got := preferTerminalStatus(testCase.values...); got != testCase.expected {
			t.Fatalf("%s: preferTerminalStatus(%v) = %q, want %q", testCase.name, testCase.values, got, testCase.expected)
		}
	}
}

func TestIsTerminalJobStatus(t *testing.T) {
	for _, status := range []string{"completed", "FAILED", " cancelled "} {
		if !isTerminalJobStatus(status) {
			t.Fatalf("isTerminalJobStatus(%q) = false, want true", status)
		}
	}
	for _, status := range []string{"running", "pending", "orphaned", ""} {
		if isTerminalJobStatus(status) {
			t.Fatalf("isTerminalJobStatus(%q) = true, want false", status)
		}
	}
}
