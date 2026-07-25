package ui

import "testing"

func TestTerminalFocusDefaultAndMutation(t *testing.T) {
	t.Cleanup(ResetTerminalFocusForTest)

	ResetTerminalFocusForTest()
	if !TerminalFocused() {
		t.Fatal("default terminal focus should be true")
	}

	SetTerminalFocused(false)
	if TerminalFocused() {
		t.Fatal("expected terminal to be unfocused after SetTerminalFocused(false)")
	}

	SetTerminalFocused(true)
	if !TerminalFocused() {
		t.Fatal("expected terminal to be focused after SetTerminalFocused(true)")
	}
}
