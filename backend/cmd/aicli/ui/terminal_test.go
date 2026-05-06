package ui

import (
	"strings"
	"testing"
)

func TestTerminal_ClearIfSupported(t *testing.T) {
	term := &Terminal{
		driver: &TerminalDriver{caps: TerminalCapabilities{ANSI: true}},
	}

	output := captureUIStdout(t, func() {
		if !term.ClearIfSupported() {
			t.Fatal("expected ANSI terminal to be cleared")
		}
	})

	if !strings.Contains(output, "\x1b[2J") || !strings.Contains(output, "\x1b[1;1H") {
		t.Fatalf("expected clear screen and home cursor sequences, got %q", output)
	}
}

func TestTerminal_ClearIfSupported_SkipsUnsupportedTerminal(t *testing.T) {
	term := &Terminal{
		driver: &TerminalDriver{caps: TerminalCapabilities{ANSI: false}},
	}

	output := captureUIStdout(t, func() {
		if term.ClearIfSupported() {
			t.Fatal("expected unsupported terminal not to be cleared")
		}
	})

	if output != "" {
		t.Fatalf("expected no output for unsupported terminal, got %q", output)
	}
}
