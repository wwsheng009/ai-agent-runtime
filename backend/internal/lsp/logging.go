package lsp

import (
	"strings"
	"sync"
)

// Logger is the minimal logging surface the LSP subsystem needs. Callers can
// inject any adapter; a nil logger degrades to a no-op.
type Logger interface {
	Debugf(format string, args ...interface{})
	Infof(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

type nopLogger struct{}

func (nopLogger) Debugf(string, ...interface{}) {}
func (nopLogger) Infof(string, ...interface{})  {}
func (nopLogger) Warnf(string, ...interface{})  {}
func (nopLogger) Errorf(string, ...interface{}) {}

// LoggerOrNop never returns nil.
func LoggerOrNop(logger Logger) Logger {
	if logger == nil {
		return nopLogger{}
	}
	return logger
}

// stderrTail keeps the last few stderr lines of one server process so a crash
// reason can carry the server's own last words. Crash causes must be
// attributable (observability plan §4.1), and gopls writes them to stderr.
type stderrTail struct {
	mu    sync.Mutex
	lines []string
	bytes int
}

const (
	stderrTailMaxLines = 3
	stderrTailMaxBytes = 600
)

// Write implements io.Writer; it is called from the process stderr pump.
func (t *stderrTail) Write(p []byte) (int, error) {
	if t == nil {
		return len(p), nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, line := range strings.Split(string(p), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		t.lines = append(t.lines, line)
		t.bytes += len(line)
	}
	for len(t.lines) > stderrTailMaxLines || (t.bytes > stderrTailMaxBytes && len(t.lines) > 1) {
		t.bytes -= len(t.lines[0])
		t.lines = t.lines[1:]
	}
	return len(p), nil
}

// String returns the tail as a single line ("" when nothing was captured).
func (t *stderrTail) String() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, " | ")
}
