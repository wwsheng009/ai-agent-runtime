package ui

import (
	"strings"
	"testing"

	"github.com/fatih/color"
)

func TestAssistantMessageFormat_MultilineIncludesPrefixOnFirstLine(t *testing.T) {
	msg := NewMessage(MessageAssistant, "line1\nline2").ShowIcon(false)

	formatted := msg.Format()
	lines := strings.Split(formatted, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), formatted)
	}
	if !strings.HasPrefix(lines[0], "助手> ") {
		t.Fatalf("expected first line to include prefix, got %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "  ") {
		t.Fatalf("expected second line to be indented, got %q", lines[1])
	}
}

func TestAssistantMessageFormat_MultilineAlignsContinuationWithIconPrefix(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()

	msg := NewMessage(MessageAssistant, "line1\nline2").ShowIcon(true)

	formatted := msg.Format()
	lines := strings.Split(formatted, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), formatted)
	}
	if !strings.HasPrefix(lines[0], "🤖  ") {
		t.Fatalf("expected first line to include icon prefix, got %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "    ") {
		t.Fatalf("expected second line to align with icon prefix width, got %q", lines[1])
	}
	if strings.HasPrefix(lines[1], "   ") && !strings.HasPrefix(lines[1], "    ") {
		t.Fatalf("expected continuation indent to include content gutter, got %q", lines[1])
	}
}

func TestMessageFormat_MultilineAlignsContinuationWithIconPrefixAcrossTypes(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()

	tests := []struct {
		name         string
		messageType  MessageType
		firstPrefix  string
		plainPrefix  string
	}{
		{"user", MessageUser, "👤  ", "👤 "},
		{"system", MessageSystem, "ℹ️  ", "ℹ️ "},
		{"tool", MessageTool, "🔧工具>  ", "🔧工具> "},
		{"error", MessageError, "❌  ", "❌ "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := NewMessage(tt.messageType, "line1\nline2").ShowIcon(true)

			formatted := msg.Format()
			lines := strings.Split(formatted, "\n")
			if len(lines) != 2 {
				t.Fatalf("expected 2 lines, got %d: %q", len(lines), formatted)
			}
			if !strings.HasPrefix(lines[0], tt.firstPrefix) {
				t.Fatalf("expected first line prefix %q, got %q", tt.firstPrefix, lines[0])
			}
			expectedIndent := strings.Repeat(" ", messageDisplayWidth(tt.plainPrefix+" "))
			if !strings.HasPrefix(lines[1], expectedIndent) {
				t.Fatalf("expected continuation indent %q, got %q", expectedIndent, lines[1])
			}
		})
	}
}

func TestIndentAssistantContent_UsesSameGutterAsAssistantMessage(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()

	indented := IndentAssistantContent("[thinking] contacting model=gpt-5.2-codex")
	if !strings.HasPrefix(indented, AssistantContentIndent()) {
		t.Fatalf("expected assistant indent prefix, got %q", indented)
	}
	if DisplayWidth(AssistantContentIndent()) <= 0 {
		t.Fatalf("expected assistant indent to have positive visible width")
	}
}
