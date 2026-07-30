package ui

import (
	"fmt"
	"io"
	"os"
	"sync"
)

var terminalWriteMu sync.Mutex

// synchronizedUpdate* frame a batch of terminal writes with DEC private mode
// 2026 so the emulator applies the whole repaint atomically (no half-drawn
// frame / tearing). Terminals that do not implement 2026 ignore the unknown
// private mode, so emitting it is always safe.
const (
	synchronizedUpdateBeginSequence = "\x1b[?2026h"
	synchronizedUpdateEndSequence   = "\x1b[?2026l"
)

// syncFramesEnabled gates synchronized-update framing. It is guarded by
// terminalWriteMu (the same lock every batch already holds) and defaults off so
// non-interactive callers and unit-test surfaces are byte-for-byte unchanged.
// Only the production fixed-bottom surface flips it on at Enable().
var syncFramesEnabled bool

// SetTerminalSynchronizedFrames toggles DEC 2026 framing around
// WithTerminalWriteLock batches. Enabling requires an interactive terminal that
// advertises SynchronizedOutput; the hard env kill switch AICLI_DISABLE_SYNC_UPDATE
// forces it off regardless.
func SetTerminalSynchronizedFrames(enabled bool) {
	if enabled && os.Getenv("AICLI_DISABLE_SYNC_UPDATE") != "" {
		enabled = false
	}
	terminalWriteMu.Lock()
	syncFramesEnabled = enabled
	terminalWriteMu.Unlock()
}

// TerminalSynchronizedFramesEnabled reports the current framing state. Exposed
// for diagnostics/tests.
func TerminalSynchronizedFramesEnabled() bool {
	terminalWriteMu.Lock()
	defer terminalWriteMu.Unlock()
	return syncFramesEnabled
}

// WithTerminalWriteLock serializes terminal control sequences that may move the
// cursor. It is intentionally package-wide so the line editor and fixed-bottom
// surface cannot interleave partial ANSI sequences.
//
// When synchronized framing is enabled it also wraps the batch in DEC 2026 so
// the multi-step repaint the closure performs is applied atomically. The lock is
// non-reentrant, so batches never nest and the begin/end brackets stay balanced.
func WithTerminalWriteLock(fn func()) {
	if fn == nil {
		return
	}
	terminalWriteMu.Lock()
	defer terminalWriteMu.Unlock()
	if syncFramesEnabled {
		_, _ = io.WriteString(os.Stdout, synchronizedUpdateBeginSequence)
		// End the frame before releasing the lock even if the closure panics, so
		// the terminal never gets stuck inside a synchronized update.
		defer func() { _, _ = io.WriteString(os.Stdout, synchronizedUpdateEndSequence) }()
	}
	fn()
}

func WriteTerminalText(writer io.Writer, text string) (int, error) {
	if writer == nil || text == "" {
		return 0, nil
	}
	terminalWriteMu.Lock()
	defer terminalWriteMu.Unlock()
	return io.WriteString(writer, text)
}

func WriteTerminalLine(writer io.Writer, text string) (int, error) {
	return WriteTerminalText(writer, text+"\n")
}

func WriteTerminalFormat(writer io.Writer, format string, args ...interface{}) (int, error) {
	if writer == nil || format == "" {
		return 0, nil
	}
	terminalWriteMu.Lock()
	defer terminalWriteMu.Unlock()
	return fmt.Fprintf(writer, format, args...)
}
