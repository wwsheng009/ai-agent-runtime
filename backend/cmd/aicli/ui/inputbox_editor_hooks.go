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
	// OnTranscriptRequested may claim Ctrl+T for a host-level read-only
	// transcript overlay. Returning false preserves the editor's normal
	// transpose-character behavior, so non-chat editors are unchanged.
	OnTranscriptRequested func(LineEditorSnapshot) bool
	// ActionForChord 把规范化 chord（如 "shift+tab"、"ctrl+t"）解析为已注册的
	// keymap 动作 id；nil 表示不启用动作路由。
	ActionForChord func(chord string) (string, bool)
	// OnActionKey 在按键解析出已注册动作时调用。claimed 表示宿主认领该键
	// （编辑器吞掉它）；exitEditor 表示宿主需要接管屏幕，编辑器以
	// ErrInteractiveInputTranscriptRequested 退出（例如全屏 transcript pager）。
	OnActionKey func(snapshot LineEditorSnapshot, action string) (claimed bool, exitEditor bool)
	// CollapsePastedText 控制大段粘贴是否折叠为占位符（提交时仍发送全文）。
	// nil 表示使用默认行为（折叠）。
	CollapsePastedText *bool
	OnSubmit           func(LineEditorSnapshot) (LineEditorReplacement, bool)
	OnCancelPopup      func(LineEditorSnapshot) bool
	OnCancel           func(LineEditorSnapshot) bool
	// MaxVisibleRows bounds the editor viewport. Zero preserves the legacy
	// unbounded rendering behavior used by transient prompts and tests.
	// ResolveMaxVisibleRows, when set, is evaluated for every snapshot and
	// redraw so terminal resize and bottom-surface context changes take effect
	// without restarting the editor.
	MaxVisibleRows        int
	ResolveMaxVisibleRows func() int
	// SuppressSubmitEcho skips the bare \r\n submit echo. Fixed-bottom surfaces
	// already own the prompt rows and repaint status themselves; emitting a raw
	// newline would corrupt the adjacent dynamic status line.
	SuppressSubmitEcho bool
}
