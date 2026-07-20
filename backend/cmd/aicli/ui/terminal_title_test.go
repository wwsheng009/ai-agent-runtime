package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestSanitizeTerminalTitle(t *testing.T) {
	got := sanitizeTerminalTitle("  Project\t|\nWorking\x1b\x07\u009d\u009c |  Thread  ")
	if got != "Project | Working | Thread" {
		t.Fatalf("unexpected sanitized title: %q", got)
	}
}

func TestSanitizeTerminalTitleStripsInvisibleFormatting(t *testing.T) {
	got := sanitizeTerminalTitle("Pro\u202ej\u2066e\u200fc\u061ct\u200b \ufeffT\u2060itle")
	if got != "Project Title" {
		t.Fatalf("unexpected sanitized title: %q", got)
	}
}

func TestSanitizeTerminalTitleTruncatesByRune(t *testing.T) {
	got := sanitizeTerminalTitle(strings.Repeat("界", maxTerminalTitleRunes+10))
	if len([]rune(got)) != maxTerminalTitleRunes {
		t.Fatalf("expected %d runes, got %d", maxTerminalTitleRunes, len([]rune(got)))
	}
}

func TestTerminalTitleWriterWritesDeduplicatesAndClears(t *testing.T) {
	var output bytes.Buffer
	term := &Terminal{driver: &TerminalDriver{caps: TerminalCapabilities{TerminalTitle: true}}}
	writer := NewTerminalTitleWriter(term, &output)

	written, err := writer.Set("hello")
	if err != nil || !written {
		t.Fatalf("first Set() = (%v, %v), want (true, nil)", written, err)
	}
	written, err = writer.Set("hello")
	if err != nil || written {
		t.Fatalf("duplicate Set() = (%v, %v), want (false, nil)", written, err)
	}
	written, err = writer.Set("\x1b\x07")
	if err != nil || !written {
		t.Fatalf("empty sanitized Set() = (%v, %v), want (true, nil)", written, err)
	}
	if err := writer.Clear(); err != nil {
		t.Fatalf("duplicate Clear(): %v", err)
	}
	if got := output.String(); got != "\x1b]0;hello\x07\x1b]0;\x07" {
		t.Fatalf("unexpected OSC output: %q", got)
	}
}

func TestTerminalTitleWriterSkipsUnsupportedTerminal(t *testing.T) {
	var output bytes.Buffer
	term := &Terminal{driver: &TerminalDriver{caps: TerminalCapabilities{TerminalTitle: false}}}
	writer := NewTerminalTitleWriter(term, &output)

	written, err := writer.Set("hello")
	if err != nil || written || output.Len() != 0 {
		t.Fatalf("unsupported Set() = (%v, %v), output=%q", written, err, output.String())
	}
}
