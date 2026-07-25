package ui

import "sync/atomic"

// terminalFocus tracks whether the host terminal window currently reports
// focus. Codex-style attention signals default to unfocused-only delivery, so
// interactive input must observe CSI focus in/out events when available.
//
// The default is focused: until a terminal emits FocusLost we treat the session
// as attended, which avoids surprising bells on startup or on hosts that never
// report focus changes.
var terminalFocused atomic.Bool

func init() {
	terminalFocused.Store(true)
}

// TerminalFocused reports the last observed terminal focus state.
func TerminalFocused() bool {
	return terminalFocused.Load()
}

// SetTerminalFocused records the latest focus state observed from the terminal.
func SetTerminalFocused(focused bool) {
	terminalFocused.Store(focused)
}

// ResetTerminalFocusForTest restores the default focused state for unit tests.
func ResetTerminalFocusForTest() {
	terminalFocused.Store(true)
}
