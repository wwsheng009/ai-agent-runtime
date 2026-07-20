package ui

import (
	"bytes"
	"testing"
)

func TestTerminalBellWriterWritesBEL(t *testing.T) {
	var output bytes.Buffer
	term := &Terminal{driver: &TerminalDriver{caps: TerminalCapabilities{Interactive: true}}}
	writer := NewTerminalBellWriter(term, &output)

	if err := writer.Notify(); err != nil {
		t.Fatalf("Notify(): %v", err)
	}
	if got := output.String(); got != "\a" {
		t.Fatalf("terminal bell output = %q, want BEL", got)
	}
}

func TestTerminalBellWriterSkipsNonInteractiveTerminal(t *testing.T) {
	var output bytes.Buffer
	term := &Terminal{driver: &TerminalDriver{caps: TerminalCapabilities{Interactive: false}}}
	writer := NewTerminalBellWriter(term, &output)

	if err := writer.Notify(); err != nil {
		t.Fatalf("Notify(): %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("non-interactive output = %q, want empty", output.String())
	}
}
