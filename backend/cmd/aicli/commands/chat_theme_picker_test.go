package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func TestExecuteStructuredThemeCommandStatusListPreviewStayReadOnly(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	previousPalette := ui.CurrentThemeName()
	previousMode := ui.CurrentThemeModeName()
	previousSyntax := ui.CurrentSyntaxThemeName()
	t.Cleanup(func() {
		_ = ui.ApplyThemeSelection(previousPalette, previousMode)
		_ = ui.SetSyntaxTheme(previousSyntax)
	})
	if err := ui.ApplyThemeSelection(ui.ThemePresetFocus, ui.ThemeModeDark); err != nil {
		t.Fatal(err)
	}

	session := &ChatSession{}
	for _, command := range []string{"/theme status", "/theme list", "/theme preview"} {
		result, handled := executeStructuredThemeCommand(session, command)
		if !handled {
			t.Fatalf("%s was not handled by the structured executor", command)
		}
		if result.OpenThemePicker != nil {
			t.Fatalf("%s must not open the picker: %#v", command, result.OpenThemePicker)
		}
		text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
		if text == "" {
			t.Fatalf("%s produced an empty document", command)
		}
	}
}

func TestExecuteStructuredThemeCommandSelectWithoutSurfaceDegradesToStatus(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	previousPalette := ui.CurrentThemeName()
	previousMode := ui.CurrentThemeModeName()
	previousSyntax := ui.CurrentSyntaxThemeName()
	t.Cleanup(func() {
		_ = ui.ApplyThemeSelection(previousPalette, previousMode)
		_ = ui.SetSyntaxTheme(previousSyntax)
	})
	if err := ui.ApplyThemeSelection(ui.ThemePresetFocus, ui.ThemeModeDark); err != nil {
		t.Fatal(err)
	}

	session := &ChatSession{}
	result, handled := executeStructuredThemeCommand(session, "/theme select")
	if !handled {
		t.Fatal("/theme select was not handled by the structured executor")
	}
	if result.OpenThemePicker != nil {
		t.Fatalf("/theme select without a picker-capable surface must not open the picker, got %#v", result.OpenThemePicker)
	}
	text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
	if !strings.Contains(text, "当前明暗: dark") {
		t.Fatalf("/theme select must degrade to the status document, got:\n%s", text)
	}
}

func TestExecuteStructuredThemeCommandSetAppliesAndRendersStatus(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	previousPalette := ui.CurrentThemeName()
	previousMode := ui.CurrentThemeModeName()
	previousSyntax := ui.CurrentSyntaxThemeName()
	t.Cleanup(func() {
		_ = ui.ApplyThemeSelection(previousPalette, previousMode)
		_ = ui.SetSyntaxTheme(previousSyntax)
	})

	session := &ChatSession{}
	result, handled := executeStructuredThemeCommand(session, "/theme dark focus")
	if !handled {
		t.Fatal("/theme dark focus was not handled by the structured executor")
	}
	if result.OpenThemePicker != nil {
		t.Fatalf("/theme set must not open the picker, got %#v", result.OpenThemePicker)
	}
	if ui.CurrentThemeModeName() != ui.ThemeModeDark {
		t.Fatalf("expected dark mode, got %q", ui.CurrentThemeModeName())
	}
	if ui.CurrentThemeName() != ui.ThemePresetFocus {
		t.Fatalf("expected focus palette, got %q", ui.CurrentThemeName())
	}
	text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
	if !strings.Contains(text, "当前明暗: dark") || !strings.Contains(text, "当前配色: focus") {
		t.Fatalf("theme set result document missing new state, got:\n%s", text)
	}
}

func TestExecuteStructuredThemeCommandSetUnchangedRendersPlainNotice(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	previousPalette := ui.CurrentThemeName()
	previousMode := ui.CurrentThemeModeName()
	previousSyntax := ui.CurrentSyntaxThemeName()
	t.Cleanup(func() {
		_ = ui.ApplyThemeSelection(previousPalette, previousMode)
		_ = ui.SetSyntaxTheme(previousSyntax)
	})
	if err := ui.ApplyThemeSelection(ui.ThemePresetFocus, ui.ThemeModeDark); err != nil {
		t.Fatal(err)
	}

	session := &ChatSession{}
	result, handled := executeStructuredThemeCommand(session, "/theme dark focus")
	if !handled {
		t.Fatal("/theme dark focus was not handled by the structured executor")
	}
	text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
	// Re-applying the same theme yields an informational notice, not an error
	// or a "警告:"-prefixed warning block.
	if !strings.Contains(text, "提示: 主题未变更") {
		t.Fatalf("unchanged theme set must render the notice, got:\n%s", text)
	}
	if strings.Contains(text, "警告:") {
		t.Fatalf("unchanged theme notice must not be rendered as a warning:\n%s", text)
	}
}

func TestExecuteStructuredThemeCommandInvalidArgsReportUsage(t *testing.T) {
	session := &ChatSession{}
	result, handled := executeStructuredThemeCommand(session, "/theme --bogus")
	if !handled {
		t.Fatal("invalid /theme args must be handled by the structured executor")
	}
	if result.OpenThemePicker != nil {
		t.Fatalf("invalid args must not open the picker, got %#v", result.OpenThemePicker)
	}
	if !strings.Contains(ui.RenderDocumentPlain(result.Document()), "未知主题参数") {
		t.Fatalf("invalid args must report the parse error, got:\n%s", ui.RenderDocumentPlain(result.Document()))
	}
}

func TestThemePickerFullScreenItemsCoverModesPalettesSyntax(t *testing.T) {
	items, picks := buildThemePickerFullScreenItems("", "", "")
	if len(items) == 0 || len(picks) == 0 || len(items) != len(picks) {
		t.Fatalf("picker items=%d picks=%d must be non-empty and index-aligned", len(items), len(picks))
	}
	var sawMode, sawPalette, sawSyntax bool
	for _, p := range picks {
		switch p.kind {
		case pickMode:
			sawMode = true
		case pickPalette:
			sawPalette = true
		case pickSyntax:
			sawSyntax = true
		}
	}
	if !sawMode || !sawPalette || !sawSyntax {
		t.Fatalf("picker must cover mode/palette/syntax, got mode=%v palette=%v syntax=%v", sawMode, sawPalette, sawSyntax)
	}
}

func TestCanOpenChatThemePickerRequiresUnifiedSurface(t *testing.T) {
	if canOpenChatThemePicker(nil) {
		t.Fatal("nil session must not open the theme picker")
	}
	session := &ChatSession{}
	if canOpenChatThemePicker(session) {
		t.Fatal("bare session without interaction/surface must not open the theme picker")
	}
}
