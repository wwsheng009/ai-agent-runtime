package ui

import (
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
}
