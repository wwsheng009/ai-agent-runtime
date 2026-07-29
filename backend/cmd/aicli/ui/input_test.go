package ui

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"strings"
	"testing"
)

func TestUserPromptDocument_PlainProjection(t *testing.T) {
	doc := UserPromptDocument(0)
	plain := RenderDocumentPlain(doc)
	if plain != defaultUserPrompt && !strings.Contains(plain, defaultUserPrompt) {
		t.Fatalf("expected default prompt, got %q", plain)
	}

	withAttach := RenderDocumentPlain(UserPromptDocument(2))
	if !strings.Contains(withAttach, "📎2") {
		t.Fatalf("expected attachment count in prompt, got %q", withAttach)
	}
}

func TestInputShowDocument_PlaceholderAndCommand(t *testing.T) {
	doc := InputShowDocument(InputDefault, "> ", "type here", "❯")
	plain := RenderDocumentPlain(doc)
	if !strings.Contains(plain, "(type here) ") {
		t.Fatalf("expected placeholder, got %q", plain)
	}
	if !strings.Contains(plain, "> ") {
		t.Fatalf("expected user prompt, got %q", plain)
	}

	cmd := InputShowDocument(InputCommand, "", "", "❯")
	cmdPlain := RenderDocumentPlain(cmd)
	if cmdPlain != "❯ " {
		t.Fatalf("expected command prompt %q, got %q", "❯ ", cmdPlain)
	}
}

func TestFormatUserPrompt_DocumentParity(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	SetTheme(ThemeAuto)

	got := FormatUserPrompt()
	if got != defaultUserPrompt {
		t.Fatalf("FormatUserPrompt plain = %q, want %q", got, defaultUserPrompt)
	}
	gotAttach := FormatUserPromptWithAttachments(3)
	if !strings.Contains(gotAttach, "📎3") {
		t.Fatalf("FormatUserPromptWithAttachments missing count: %q", gotAttach)
	}
}

func TestFormatCommandPrompt_UsesCommandRole(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	SetTheme(ThemeAuto)

	theme := GetTheme(ThemeAuto)
	got := FormatCommandPrompt()
	want := theme.CommandIcon + " "
	if got != want {
		t.Fatalf("FormatCommandPrompt = %q, want %q", got, want)
	}

	doc := CommandPromptDocument(theme.CommandIcon)
	if len(doc.Blocks) == 0 || len(doc.Blocks[0].Lines) == 0 || len(doc.Blocks[0].Lines[0].Spans) == 0 {
		t.Fatal("CommandPromptDocument empty")
	}
	if style.Role(doc.Blocks[0].Lines[0].Spans[0].Style.Role) != style.RoleCommand {
		t.Fatalf("expected RoleCommand, got %q", doc.Blocks[0].Lines[0].Spans[0].Style.Role)
	}
}

func TestAssistantMessageDocument_SanitizesControl(t *testing.T) {
	doc := AssistantMessageDocument("hello\x1b[31mred\x07")
	plain := RenderDocumentPlain(doc)
	if strings.Contains(plain, "\x1b") || strings.Contains(plain, "\x07") {
		t.Fatalf("expected sanitized assistant text, got %q", plain)
	}
	if !strings.Contains(plain, "hello") {
		t.Fatalf("expected visible text retained, got %q", plain)
	}
}

func TestRenderInputDocumentUsesNegotiatedColorProfile(t *testing.T) {
	SetTheme(ThemeAuto)

	t.Run("no-color", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		t.Setenv("FORCE_COLOR", "")
		got := FormatPromptLine("> ")
		if strings.ContainsRune(got, '\x1b') {
			t.Fatalf("NO_COLOR prompt contains ESC: %q", got)
		}
		if got != "> " {
			t.Fatalf("NO_COLOR prompt = %q, want %q", got, "> ")
		}
	})

	t.Run("ansi-16", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		t.Setenv("FORCE_COLOR", "1")
		t.Setenv("AICLI_COLOR_DEPTH", "ansi16")
		got := FormatPromptLine("> ")
		if !strings.ContainsRune(got, '\x1b') {
			t.Fatalf("forced ANSI-16 prompt has no SGR: %q", got)
		}
		if strings.Contains(got, "\x1b[38;2;") || strings.Contains(got, "\x1b[38;5;") {
			t.Fatalf("ANSI-16 prompt contains higher-depth color: %q", got)
		}
		if strings.TrimSpace(RenderDocumentPlain(PromptLineDocument("> "))) != ">" {
			t.Fatal("styled prompt changed its plain projection")
		}
	})
}

func TestInputBoxFormatPrompt_Document(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	SetTheme(ThemeAuto)

	ib := NewInputBox(nil)
	if got := ib.FormatPrompt(""); got != defaultUserPrompt {
		t.Fatalf("empty context FormatPrompt = %q, want %q", got, defaultUserPrompt)
	}
	if got := ib.FormatPrompt("ctx"); got != "ctx " {
		t.Fatalf("context FormatPrompt = %q, want %q", got, "ctx ")
	}
}

func TestPromptAssistant_WritesSanitizedLine(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	SetTheme(ThemeAuto)

	output := captureUIStdout(t, func() {
		PromptAssistant("hi\x1b[0m")
	})
	if strings.Contains(output, "\x1b") {
		t.Fatalf("expected no ESC in output, got %q", output)
	}
	if !strings.Contains(output, "hi") {
		t.Fatalf("expected message body, got %q", output)
	}
	if !strings.HasSuffix(output, "\n") {
		t.Fatalf("expected trailing newline, got %q", output)
	}
}
