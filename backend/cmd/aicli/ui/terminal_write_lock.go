package ui

import (
	"fmt"
	"io"
	"sync"
)

var terminalWriteMu sync.Mutex

// WithTerminalWriteLock serializes terminal control sequences that may move the
// cursor. It is intentionally package-wide so the line editor and fixed-bottom
// surface cannot interleave partial ANSI sequences.
func WithTerminalWriteLock(fn func()) {
	if fn == nil {
		return
	}
	terminalWriteMu.Lock()
	defer terminalWriteMu.Unlock()
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
