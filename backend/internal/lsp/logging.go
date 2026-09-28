package lsp

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
