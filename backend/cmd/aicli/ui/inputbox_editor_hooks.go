package ui

import "io"

// LineEditorSnapshot captures the current editable line state for hooks.
type LineEditorSnapshot struct {
	Text        string
	Cursor      int
	Prompt      string
	HistoryPos  int
	PasteActive bool
	// DisplayRows is the number of terminal rows currently occupied by the
	// editor viewport. ViewportStart is the first logical display row shown.
	// These fields let a fixed-bottom surface expose multiline state without
	// changing the submitted text contract.
	DisplayRows      int
	CursorDisplayRow int
	ViewportStart    int
	ViewportRows     int
	LogicalLine      int
	LogicalLines     int
}

// LineEditorReplacement describes a text replacement requested by a hook.
type LineEditorReplacement struct {
	Text   string
	Cursor int
}

type LineEditorRenderSnapshot struct {
	LastCursorRow int
	LastCursorCol int
	ViewportStart int
}

// LineEditorHooks lets the caller observe and intercept editor actions.
type LineEditorHooks struct {
	InitialText   string
	InitialCursor int
	// RedrawInitialText repaints a cached draft before the editor waits for the
	// next key. Fixed surfaces use this when a restarted composer inherits text
	// that may be present in state but no longer be present on screen.
	RedrawInitialText     bool
	OnChange              func(LineEditorSnapshot)
	OnBeforeRedraw        func(LineEditorSnapshot, LineEditorRenderSnapshot)
	OnBeforeTerminalWrite func(LineEditorSnapshot, LineEditorRenderSnapshot) string
	OnTerminalWrite       func(LineEditorSnapshot, LineEditorRenderSnapshot, io.Writer, string) bool
	OnComplete            func(LineEditorSnapshot) (LineEditorReplacement, bool)
	OnNavigate            func(LineEditorSnapshot, int) bool
	OnMove                func(LineEditorSnapshot, int) bool
	OnSubmit              func(LineEditorSnapshot) (LineEditorReplacement, bool)
	OnCancelPopup         func(LineEditorSnapshot) bool
	OnCancel              func(LineEditorSnapshot) bool
	// MaxVisibleRows bounds the editor viewport. Zero preserves the legacy
	// unbounded rendering behavior used by transient prompts and tests.
	// ResolveMaxVisibleRows, when set, is evaluated for every snapshot and
	// redraw so terminal resize and bottom-surface context changes take effect
	// without restarting the editor.
	MaxVisibleRows        int
	ResolveMaxVisibleRows func() int
}
