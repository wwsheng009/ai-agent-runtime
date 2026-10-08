package renderengine

import (
	"fmt"
	"io"
	"sync"
)

// terminalWriteMu serializes all terminal control batches. It lives here (not
// in the ui package) so Presenter can batch frames under the same lock the ui
// package writes through, without ui -> renderengine -> ui import cycles.
// The ui package forwards its WithTerminalWriteLock to this implementation.
var terminalWriteMu sync.Mutex

// WithTerminalWriteLock serializes terminal control sequences that may move the
// cursor. It is intentionally shared package-wide so the line editor and
// fixed-bottom surface cannot interleave partial ANSI sequences.
//
// Batches carry no DEC 2026 framing: the synchronized-update branch and its
// toggle were retired with the legacy FixedBottomSurface pair (L3-1). The lock
// is non-reentrant, so batches never nest.
func WithTerminalWriteLock(fn func()) {
	withTerminalWriteLock(fn)
}

// withTerminalWriteLock is the internal, non-exported entry point used by
// Presenter batching (which already lives in this package).
func withTerminalWriteLock(fn func()) {
	if fn == nil {
		return
	}
	terminalWriteMu.Lock()
	defer terminalWriteMu.Unlock()
	fn()
}

// WriteTerminalText writes text under the terminal write lock.
func WriteTerminalText(writer io.Writer, text string) (int, error) {
	if writer == nil || text == "" {
		return 0, nil
	}
	terminalWriteMu.Lock()
	defer terminalWriteMu.Unlock()
	return io.WriteString(writer, text)
}

// WriteTerminalLine writes text plus a trailing newline under the lock.
func WriteTerminalLine(writer io.Writer, text string) (int, error) {
	return WriteTerminalText(writer, text+"\n")
}

// WriteTerminalFormat writes formatted text under the terminal write lock.
func WriteTerminalFormat(writer io.Writer, format string, args ...interface{}) (int, error) {
	if writer == nil || format == "" {
		return 0, nil
	}
	terminalWriteMu.Lock()
	defer terminalWriteMu.Unlock()
	return fmt.Fprintf(writer, format, args...)
}
