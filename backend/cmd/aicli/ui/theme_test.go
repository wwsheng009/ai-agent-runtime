package ui

import (
	"strings"
	"testing"

	"github.com/fatih/color"
)

func TestSetThemePreset_ChangesCurrentThemeName(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
		_ = SetThemePreset(ThemePresetFocus)
	}()

	if err := SetThemePreset("contrast"); err != nil {
		t.Fatalf("SetThemePreset: %v", err)
	}
	if got := CurrentThemeName(); got != ThemePresetContrast {
		t.Fatalf("expected current theme %q, got %q", ThemePresetContrast, got)
	}
	if theme := GetTheme(ThemeAuto); theme == nil || theme.Name != ThemePresetContrast {
		t.Fatalf("expected loaded theme preset %q, got %+v", ThemePresetContrast, theme)
	}
}

func TestSetThemePreset_RejectsUnknownTheme(t *testing.T) {
	if err := SetThemePreset("unknown-theme"); err == nil {
		t.Fatal("expected unknown theme to fail")
	}
}

func TestFormatAssistantSupplementBlock_PreservesPlainLayoutWithoutColor(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
		_ = SetThemePreset(ThemePresetFocus)
	}()

	if err := SetThemePreset(ThemePresetContrast); err != nil {
		t.Fatalf("SetThemePreset: %v", err)
	}

	raw := "[tool done] execute_shell_command command=git status\n  failed: exit status 1"
	if got := FormatAssistantSupplementBlock(raw); got != raw {
		t.Fatalf("expected plain layout to be preserved, got %q", got)
	}

	diff := "• Edited internal\\service\\shop\\endpoint\\security.go (+1 -1)\n      259 -     oldValue,\n      259 +     newValue,"
	if got := FormatAssistantSupplementBlock(diff); got != diff {
		t.Fatalf("expected plain diff layout to be preserved, got %q", got)
	}
}

func TestFormatAssistantSupplementBlock_CollapsesRedundantBlankLines(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
		_ = SetThemePreset(ThemePresetFocus)
	}()

	if err := SetThemePreset(ThemePresetContrast); err != nil {
		t.Fatalf("SetThemePreset: %v", err)
	}

	raw := "\n\n[prompt] layers=unknown/system\n\n\n(instruction 471 / total 2490 tokens)\n\n\n"
	want := "[prompt] layers=unknown/system\n\n(instruction 471 / total 2490 tokens)"
	if got := FormatAssistantSupplementBlock(raw); got != want {
		t.Fatalf("expected redundant blank lines to collapse, got %q", got)
	}
}

func TestStyleAssistantSupplementLine_ColorsEditedDiffLinesByTheme(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = false
	defer func() {
		color.NoColor = oldNoColor
		_ = SetThemePreset(ThemePresetFocus)
	}()

	theme := createTheme(ThemeDark)
	addBody := "259 +     \"updated_at\": now,"
	delBody := "260 -     \"last_audit_id\": audit.ID.String(),"
	addLine := theme.StyleAssistantSupplementLine("      " + addBody)
	delLine := theme.StyleAssistantSupplementLine("      " + delBody)
	if addLine == "      "+addBody || delLine == "      "+delBody {
		t.Fatal("expected edited diff lines to be colorized")
	}
	if !strings.Contains(addLine, "\x1b[") || !strings.Contains(delLine, "\x1b[") {
		t.Fatalf("expected ANSI color sequences in diff lines, got add=%q del=%q", addLine, delLine)
	}
	if want := "      " + theme.SuccessColor.Sprint(addBody); addLine != want {
		t.Fatalf("expected added line to use theme success color, got %q want %q", addLine, want)
	}
	if want := "      " + theme.ErrorColor.Sprint(delBody); delLine != want {
		t.Fatalf("expected deleted line to use theme error color, got %q want %q", delLine, want)
	}
}
