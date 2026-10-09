package ui

import (
	"bytes"
	"io"
	"os"
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

// TestTerminal_ControlWritesUseDriverStdout pins the §4.3 writer precedence:
// a driver-backed terminal (production NewTerminal shape) writes control
// sequences through the driver's explicit stdout instead of the process sink.
func TestTerminal_ControlWritesUseDriverStdout(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	term := &Terminal{driver: &TerminalDriver{stdout: writer}}
	term.emitControl("X")
	_ = writer.Close()
	data, _ := io.ReadAll(reader)
	if string(data) != "X" {
		t.Fatalf("driver stdout = %q, want %q", data, "X")
	}
}

// TestTerminal_ControlOverrideWinsOverDriverStdout pins the test seam: an
// explicitly injected sink captures control writes even for driver-backed
// terminals; after restore the driver stdout takes over again.
func TestTerminal_ControlOverrideWinsOverDriverStdout(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	var injected bytes.Buffer
	restore := SetTerminalOutputForTesting(&injected)
	term := &Terminal{driver: &TerminalDriver{stdout: writer}}
	term.emitControl("A")
	restore()
	term.emitControl("B")
	_ = writer.Close()
	data, _ := io.ReadAll(reader)
	if injected.String() != "A" {
		t.Fatalf("injected sink = %q, want %q", injected.String(), "A")
	}
	if string(data) != "B" {
		t.Fatalf("driver stdout after restore = %q, want %q", data, "B")
	}
}

// TestTerminal_DriverlessWithoutOverrideDiscards pins the retired default
// stdout binding (§4.3): without an injected sink, driver-less synthetic
// terminals must not touch the process stdout through either emitControl or
// the compatibility sink.
func TestTerminal_DriverlessWithoutOverrideDiscards(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	original := os.Stdout
	os.Stdout = writer
	(&Terminal{}).emitControl("X")
	_, _ = TerminalOutput().Write([]byte("Y"))
	os.Stdout = original
	_ = writer.Close()
	data, _ := io.ReadAll(reader)
	if len(data) != 0 {
		t.Fatalf("driver-less terminal wrote %q to process stdout, want nothing", data)
	}
}
