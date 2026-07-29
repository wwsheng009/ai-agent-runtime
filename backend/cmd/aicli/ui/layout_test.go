package ui

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func TestLayoutInputAreaDocumentSanitizesAndRoles(t *testing.T) {
	layout := NewLayout(LayoutAdvanced)
	doc := layout.InputAreaDocument("You> ", "hello\x1b[31mX\x1b[0m\x07world")
	if doc.LineCount() != 1 {
		t.Fatalf("lines=%d", doc.LineCount())
	}
	line := doc.Blocks[0].Lines[0]
	if len(line.Spans) != 2 {
		t.Fatalf("spans=%d", len(line.Spans))
	}
	if line.Spans[0].Style.Role != string(style.RoleUser) {
		t.Fatalf("prompt role=%q", line.Spans[0].Style.Role)
	}
	if line.Spans[1].Style.Role != string(style.RoleTextPrimary) {
		t.Fatalf("input role=%q", line.Spans[1].Style.Role)
	}
	if strings.Contains(line.Spans[1].Text, "\x1b") || strings.Contains(line.Spans[1].Text, "\x07") {
		t.Fatalf("input still has control sequences: %q", line.Spans[1].Text)
	}
	if !strings.Contains(line.Spans[1].Text, "hello") || !strings.Contains(line.Spans[1].Text, "world") {
		t.Fatalf("input text lost: %q", line.Spans[1].Text)
	}
}

func TestLayoutMessageDocumentStripsESC(t *testing.T) {
	doc := LayoutMessageDocument("a\x1b[31mb\x1b[0m\nc")
	plain := doc.PlainText()
	if strings.Contains(plain, "\x1b") {
		t.Fatalf("plain still has ESC: %q", plain)
	}
	if !strings.Contains(plain, "a") || !strings.Contains(plain, "b") || !strings.Contains(plain, "c") {
		t.Fatalf("content lost: %q", plain)
	}
	formatted := FormatLayoutMessage("line1\x1b[0m\nline2")
	if strings.Contains(formatted, "\x1b") {
		t.Fatalf("formatted still has ESC: %q", formatted)
	}
	if !strings.Contains(formatted, "line1") || !strings.Contains(formatted, "line2") {
		t.Fatalf("formatted lost lines: %q", formatted)
	}
}

func TestLayoutSeparatorAndChromeDocuments(t *testing.T) {
	layout := NewLayout(LayoutSimple)

	// calculateAreas refreshes from the real TTY, so snapshot the live width
	// instead of trying to force private fields that get overwritten.
	wantWidth := 80
	if term := layout.Terminal(); term != nil {
		if w := term.Width(); w > 0 {
			wantWidth = w
		}
	}

	chrome := layout.ChatChromeDocument()
	if chrome.LineCount() != 1 {
		t.Fatalf("chrome lines=%d", chrome.LineCount())
	}
	if chrome.Blocks[0].Lines[0].Spans[0].Style.Role != string(style.RoleTextMuted) {
		t.Fatalf("chrome role=%q", chrome.Blocks[0].Lines[0].Spans[0].Style.Role)
	}

	sep := layout.SeparatorLineDocument()
	plain := sep.PlainText()
	if plain == "" {
		t.Fatal("separator empty")
	}
	if sep.Blocks[0].Lines[0].Spans[0].Style.Role != string(style.RoleBorder) {
		t.Fatalf("sep role=%q", sep.Blocks[0].Lines[0].Spans[0].Style.Role)
	}
	if got := DisplayWidth(plain); got != wantWidth {
		t.Fatalf("sep width=%d want %d (%q)", got, wantWidth, plain)
	}

	formatted := layout.FormatSeparatorLine()
	if formatted == "" {
		t.Fatal("FormatSeparatorLine empty")
	}
}

func TestLayoutFormatInputAreaUsesThemeRoles(t *testing.T) {
	layout := NewLayout(LayoutAdvanced)
	out := layout.FormatInputArea("You> ", "hi")
	if !strings.Contains(out, "You>") {
		t.Fatalf("missing prompt: %q", out)
	}
	if !strings.Contains(out, "hi") {
		t.Fatalf("missing input: %q", out)
	}
	// Prompt should be colorized via RoleUser adapter when theme has color.
	// Plain projection of the document must still be clean.
	plain := layout.InputAreaDocument("You> ", "hi").PlainText()
	if plain != "You> hi" {
		t.Fatalf("plain=%q", plain)
	}
}

func TestLayoutAreasAdvanced(t *testing.T) {
	layout := NewLayout(LayoutAdvanced)
	layout.calculateAreas()
	if layout.ChatArea() == nil || layout.StatusArea() == nil {
		t.Fatal("expected chat and status areas")
	}
	if layout.InputArea() == nil {
		t.Fatal("advanced layout should have input area")
	}
	chat := layout.ChatArea()
	status := layout.StatusArea()
	input := layout.InputArea()
	if chat.Width <= 0 || status.Width <= 0 || input.Width <= 0 {
		t.Fatalf("expected positive widths chat=%d status=%d input=%d", chat.Width, status.Width, input.Width)
	}
	if status.Row <= chat.Row {
		t.Fatalf("status row %d should be below chat row %d", status.Row, chat.Row)
	}
	if input.Row < status.Row {
		t.Fatalf("input row %d should be at/after status row %d", input.Row, status.Row)
	}
}
