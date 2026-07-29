package ui

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func TestFormatThemePreviewRichContainsDiffSigns(t *testing.T) {
	out := FormatThemePreviewRich(ThemePreviewOptions{
		Width:       80,
		Palette:     ThemePresetFocus,
		Mode:        ThemeModeDark,
		SyntaxTheme: "monokai",
		Compact:     true,
	})
	if out == "" {
		t.Fatal("empty preview")
	}
	if !strings.Contains(out, "+") || !strings.Contains(out, "-") {
		t.Fatalf("expected diff signs in preview: %q", out)
	}
	// Should include semantic swatch words
	if !strings.Contains(out, "tool") && !strings.Contains(out, "ok") {
		// colored may strip plain detection; ensure Hello or hi from code
		if !strings.Contains(out, "Hello") && !strings.Contains(out, "hi") {
			t.Fatalf("unexpected preview content: %q", out)
		}
	}
}

func TestSetSyntaxThemeRoundTrip(t *testing.T) {
	prev := CurrentSyntaxThemeName()
	t.Cleanup(func() { _ = SetSyntaxTheme(prev) })
	if err := SetSyntaxTheme("dracula"); err != nil {
		t.Fatal(err)
	}
	if CurrentSyntaxThemeName() != "dracula" {
		t.Fatalf("got %q", CurrentSyntaxThemeName())
	}
	if err := SetSyntaxTheme("not-a-real-theme-xyz"); err == nil {
		t.Fatal("expected error for unknown theme")
	}
	if CurrentSyntaxThemeName() != "dracula" {
		t.Fatal("unknown theme mutated state")
	}
}

func TestFormatThemePreviewRichHonorsColorProfile(t *testing.T) {
	tests := []struct {
		name    string
		profile style.ColorProfile
		check   func(t *testing.T, output string)
	}{
		{
			name:    "no-color",
			profile: style.ColorProfile{ColorProfile: render.NoColorProfile()},
			check: func(t *testing.T, output string) {
				if strings.ContainsRune(output, '\x1b') {
					t.Fatalf("no-color preview contains ESC: %q", output)
				}
			},
		},
		{
			name: "ansi-16",
			profile: style.ColorProfile{ColorProfile: render.ColorProfile{
				Enabled: true,
				Depth:   render.ColorANSI16,
			}},
			check: func(t *testing.T, output string) {
				if strings.Contains(output, "\x1b[38;2;") || strings.Contains(output, "\x1b[38;5;") {
					t.Fatalf("ANSI-16 preview contains higher-depth color: %q", output)
				}
			},
		},
		{
			name:    "truecolor",
			profile: style.ColorProfile{ColorProfile: render.TrueColorProfile()},
			check: func(t *testing.T, output string) {
				if !strings.Contains(output, "\x1b[38;2;") {
					t.Fatalf("truecolor preview lost syntax RGB: %q", output)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := FormatThemePreviewRich(ThemePreviewOptions{
				Width:       80,
				Palette:     ThemePresetFocus,
				Mode:        ThemeModeDark,
				SyntaxTheme: "monokai",
				Profile:     &test.profile,
				Compact:     true,
			})
			test.check(t, output)
		})
	}
}
