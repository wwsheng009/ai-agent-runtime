package ui

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"strings"
	"testing"
)

func TestMessageSystemDocumentUsesRequiredRole(t *testing.T) {
	doc := NewMessage(MessageSystem, "notice").Document()
	found := false
	for _, block := range doc.Blocks {
		for _, line := range block.Lines {
			for _, span := range line.Spans {
				if style.Role(span.Style.Role) == style.RoleSystem {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("system message document does not contain RoleSystem")
	}
}

func TestSemanticSurfacesUseNegotiatedColorProfile(t *testing.T) {
	SetTheme(ThemeAuto)

	outputs := func() map[string]string {
		layout := NewLayout(LayoutSimple)
		ctx := CurrentThemeContext()
		return map[string]string{
			"message":   FormatSystemMessage("notice"),
			"status":    NewStatus(StatusSuccess, "done").Build(),
			"separator": NewSeparator().SetWidth(12).Build(),
			"layout":    layout.FormatInputArea("> ", "hello"),
			"fixed": formatFixedStatusModelWithContext(style.StatusLineModel{
				State:     style.RunReady,
				StateText: "Ready",
			}, 40, ctx),
		}
	}

	t.Run("no-color", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		t.Setenv("FORCE_COLOR", "")
		for name, got := range outputs() {
			if got == "" {
				t.Fatalf("%s output is empty", name)
			}
			if strings.ContainsRune(got, '\x1b') {
				t.Fatalf("NO_COLOR %s contains ESC: %q", name, got)
			}
		}
	})

	t.Run("ansi-16", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		t.Setenv("FORCE_COLOR", "1")
		t.Setenv("AICLI_COLOR_DEPTH", "ansi16")
		for name, got := range outputs() {
			if !strings.ContainsRune(got, '\x1b') {
				t.Fatalf("forced ANSI-16 %s has no SGR: %q", name, got)
			}
			if strings.Contains(got, "\x1b[38;2;") || strings.Contains(got, "\x1b[38;5;") {
				t.Fatalf("ANSI-16 %s contains higher-depth color: %q", name, got)
			}
		}
	})
}
