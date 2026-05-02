package ui

// LineEditorSnapshot captures the current editable line state for hooks.
type LineEditorSnapshot struct {
	Text       string
	Cursor     int
	Prompt     string
	HistoryPos int
	PasteActive bool
}

// LineEditorReplacement describes a text replacement requested by a hook.
type LineEditorReplacement struct {
	Text   string
	Cursor int
}

// LineEditorHooks lets the caller observe and intercept editor actions.
type LineEditorHooks struct {
	OnChange      func(LineEditorSnapshot)
	OnComplete    func(LineEditorSnapshot) (LineEditorReplacement, bool)
	OnNavigate    func(LineEditorSnapshot, int) bool
	OnSubmit      func(LineEditorSnapshot) (LineEditorReplacement, bool)
	OnCancelPopup func(LineEditorSnapshot) bool
}
