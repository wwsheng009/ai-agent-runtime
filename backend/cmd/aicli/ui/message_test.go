package ui

import (
	"strings"
	"testing"
)

func TestAssistantMessageFormat_MultilineHasNoPrefix(t *testing.T) {
	msg := NewMessage(MessageAssistant, "line1\nline2").ShowIcon(false)

	formatted := msg.Format()
	lines := strings.Split(formatted, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), formatted)
	}
	if lines[0] != "line1" {
		t.Fatalf("expected first line without prefix, got %q", lines[0])
	}
	if lines[1] != "line2" {
		t.Fatalf("expected second line without indent, got %q", lines[1])
	}
}

func TestAssistantMessageFormat_MultilineShowsNoIconPrefix(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	msg := NewMessage(MessageAssistant, "line1\nline2").ShowIcon(true)

	formatted := msg.Format()
	lines := strings.Split(formatted, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), formatted)
	}
	if lines[0] != "line1" {
		t.Fatalf("expected first line without icon prefix, got %q", lines[0])
	}
	if lines[1] != "line2" {
		t.Fatalf("expected second line without icon indent, got %q", lines[1])
	}
}

func TestMessageFormat_MultilineAlignsContinuationWithIconPrefixAcrossTypes(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	tests := []struct {
		name        string
		messageType MessageType
		firstPrefix string
		plainPrefix string
	}{
		{"user", MessageUser, "> ", ">"},
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
	t.Setenv("NO_COLOR", "1")

	indented := IndentAssistantContent("[thinking] contacting model=gpt-5.2-codex")
	if !strings.HasPrefix(indented, AssistantContentIndent()) {
		t.Fatalf("expected assistant indent prefix, got %q", indented)
	}
	if AssistantContentIndent() != "" {
		t.Fatalf("expected assistant indent to be empty when assistant has no prefix, got %q", AssistantContentIndent())
	}
}

func TestSanitizeTerminalText_IsolatesRTLRunInsideChineseSentence(t *testing.T) {
	input := "这些改动 هنوز在工作区里，尚未提交"

	sanitized := SanitizeTerminalText(input)

	if !strings.Contains(sanitized, "\u2066هنوز\u2069") {
		t.Fatalf("expected RTL run to be isolated, got %q", sanitized)
	}
	if strings.Contains(sanitized, "\u202e") || strings.Contains(sanitized, "\u202d") {
		t.Fatalf("expected unsafe bidi overrides to be removed, got %q", sanitized)
	}
}

func TestDisplayWidth_IgnoresDirectionalIsolates(t *testing.T) {
	plain := "abc"
	sanitized := SanitizeTerminalText("abc")

	if DisplayWidth(sanitized) != DisplayWidth(plain) {
		t.Fatalf("expected directional isolates to have zero width, plain=%d sanitized=%d", DisplayWidth(plain), DisplayWidth(sanitized))
	}
}

func TestSanitizeTerminalText_RemovesANSIEscapeSequences(t *testing.T) {
	input := "safe\x1b[2J\x1b[Hstill safe\x1b]0;owned\a!"

	sanitized := SanitizeTerminalText(input)

	if sanitized != "safestill safe!" {
		t.Fatalf("unexpected sanitized text: %q", sanitized)
	}
	if strings.ContainsRune(sanitized, '\x1b') || strings.ContainsRune(sanitized, '\a') {
		t.Fatalf("expected terminal controls to be removed, got %q", sanitized)
	}
}

func TestSanitizeTerminalText_NormalizesCRLFAndDropsControls(t *testing.T) {
	input := "a\r\nb\bc\t"

	sanitized := SanitizeTerminalText(input)

	if sanitized != "a\nbc    " {
		t.Fatalf("unexpected sanitized text: %q", sanitized)
	}
}

func TestMessageDocument_UserPlainProjection(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	msg := NewMessage(MessageUser, "hello\x1b[31mx").ShowIcon(true)
	plain := RenderDocumentPlain(msg.Document())
	if strings.Contains(plain, "\x1b") {
		t.Fatalf("expected sanitized content, got %q", plain)
	}
	if !strings.HasPrefix(plain, "> hello") {
		t.Fatalf("expected user prefix+body, got %q", plain)
	}
}

func TestMessageDocument_TimestampOrder(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	msg := NewMessage(MessageUser, "body").ShowIcon(true).ShowTimestamp(true)
	formatted := msg.Format()
	// prefix then [HH:MM:SS] then body
	if !strings.HasPrefix(formatted, "> [") || !strings.Contains(formatted, "] body") {
		t.Fatalf("unexpected timestamp layout: %q", formatted)
	}
}

func TestStatusDocument_SuccessPlain(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	SetTheme(ThemeAuto)

	st := NewStatus(StatusSuccess, "done\x1b[0m")
	plain := RenderDocumentPlain(st.Document())
	if strings.Contains(plain, "\x1b") {
		t.Fatalf("expected sanitized status, got %q", plain)
	}
	if !strings.Contains(plain, "done") {
		t.Fatalf("expected body, got %q", plain)
	}
	theme := GetTheme(ThemeAuto)
	if !strings.HasPrefix(plain, theme.SuccessIcon) {
		t.Fatalf("expected success icon prefix, got %q", plain)
	}
}
