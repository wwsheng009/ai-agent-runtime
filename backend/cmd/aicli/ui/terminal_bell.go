package ui

import "io"

// TerminalBellWriter emits the terminal BEL attention signal. Terminal and
// operating-system settings decide whether BEL is audible, visual, or muted.
type TerminalBellWriter struct {
	terminal *Terminal
	writer   io.Writer
}

func NewTerminalBellWriter(terminal *Terminal, writer io.Writer) *TerminalBellWriter {
	return &TerminalBellWriter{terminal: terminal, writer: writer}
}

func (w *TerminalBellWriter) Supported() bool {
	if w == nil || w.terminal == nil || w.writer == nil {
		return false
	}
	return w.terminal.Capabilities().Interactive
}

func (w *TerminalBellWriter) Notify() error {
	if !w.Supported() {
		return nil
	}
	_, err := WriteTerminalText(w.writer, "\a")
	return err
}
