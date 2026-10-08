package ui

import (
	"io"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
)

// The terminal write lock lives in the renderengine package (owned by the
// Presenter batch path). The ui package forwards to it so every TUI write path
// shares one lock without an ui -> renderengine -> ui import cycle.
//
// The DEC 2026 synchronized-framing toggle was retired with the legacy
// FixedBottomSurface pair (L3-1); batches are no longer wrapped.

// WithTerminalWriteLock serializes terminal control sequences that may move the
// cursor. See renderengine.WithTerminalWriteLock.
func WithTerminalWriteLock(fn func()) {
	renderengine.WithTerminalWriteLock(fn)
}

// WriteTerminalText writes text under the terminal write lock.
func WriteTerminalText(writer io.Writer, text string) (int, error) {
	return renderengine.WriteTerminalText(writer, text)
}

// WriteTerminalLine writes text plus a trailing newline under the lock.
func WriteTerminalLine(writer io.Writer, text string) (int, error) {
	return renderengine.WriteTerminalLine(writer, text)
}

// WriteTerminalFormat writes formatted text under the terminal write lock.
func WriteTerminalFormat(writer io.Writer, format string, args ...interface{}) (int, error) {
	return renderengine.WriteTerminalFormat(writer, format, args...)
}
