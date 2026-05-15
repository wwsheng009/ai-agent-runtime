package ui

// LineEditorSnapshot captures the current editable line state for hooks.
type LineEditorSnapshot struct {
	Text        string
	Cursor      int
	Prompt      string
	HistoryPos  int
	PasteActive bool
}

// LineEditorReplacement describes a text replacement requested by a hook.
type LineEditorReplacement struct {
	Text   string
	Cursor int
}

type LineEditorRenderSnapshot struct {
	LastCursorRow int
	LastCursorCol int
}

// LineEditorHooks lets the caller observe and intercept editor actions.
type LineEditorHooks struct {
	OnChange              func(LineEditorSnapshot)
	OnBeforeRedraw        func(LineEditorSnapshot, LineEditorRenderSnapshot)
	OnBeforeTerminalWrite func(LineEditorSnapshot, LineEditorRenderSnapshot) string
	OnComplete            func(LineEditorSnapshot) (LineEditorReplacement, bool)
	OnNavigate            func(LineEditorSnapshot, int) bool
	OnMove                func(LineEditorSnapshot, int) bool
	OnSubmit              func(LineEditorSnapshot) (LineEditorReplacement, bool)
	OnCancelPopup         func(LineEditorSnapshot) bool
	OnCancel              func(LineEditorSnapshot) bool
}
