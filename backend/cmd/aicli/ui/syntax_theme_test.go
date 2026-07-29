package ui

import (
	"testing"

	uisyntax "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
)

func TestSyntaxAutoRemainsPreferenceAndResolvesByMode(t *testing.T) {
	previousPalette := CurrentThemeName()
	previousMode := CurrentThemeModeName()
	previousSyntax := CurrentSyntaxThemeName()
	t.Cleanup(func() {
		_ = ApplyThemeSelection(previousPalette, previousMode)
		_ = SetSyntaxTheme(previousSyntax)
	})

	if err := ApplyThemeSelection(ThemePresetFocus, ThemeModeLight); err != nil {
		t.Fatal(err)
	}
	if err := SetSyntaxTheme("auto"); err != nil {
		t.Fatal(err)
	}
	if got := CurrentSyntaxThemeName(); got != "auto" {
		t.Fatalf("preference=%q", got)
	}
	if got := CurrentResolvedSyntaxThemeName(); got != "github" {
		t.Fatalf("light resolved=%q", got)
	}
	if got := CurrentThemeContext().SyntaxName; got != "github" {
		t.Fatalf("theme context syntax=%q", got)
	}
	if got := uisyntax.GlobalDefaultTheme(); got != "github" {
		t.Fatalf("global syntax=%q", got)
	}

	if err := SetThemeMode(ThemeModeDark); err != nil {
		t.Fatal(err)
	}
	if got := CurrentResolvedSyntaxThemeName(); got != "monokai" {
		t.Fatalf("dark resolved=%q", got)
	}
	if got := uisyntax.GlobalDefaultTheme(); got != "monokai" {
		t.Fatalf("dark global syntax=%q", got)
	}
}

func TestNormalizeSyntaxThemePreservesAuto(t *testing.T) {
	if got := NormalizeSyntaxThemeName(" AUTO "); got != "auto" {
		t.Fatalf("normalized=%q", got)
	}
	found := false
	for _, name := range CuratedSyntaxThemeNames() {
		if name == "auto" {
			found = true
		}
	}
	if !found {
		t.Fatal("curated syntax themes must include auto")
	}
}
