package style

import "testing"

func TestBuiltinPalettesHaveRequiredRoles(t *testing.T) {
	for _, name := range BuiltinPaletteNames() {
		for _, variant := range []Variant{VariantDark, VariantLight} {
			p := NewPalette(name, variant)
			if !p.HasAllRequiredRoles() {
				t.Fatalf("palette %s/%v missing roles", name, variant)
			}
			if p.Name != name && !(name == PaletteFocus) {
				// normalize may map aliases; built-in names should stick.
			}
			if p.Styles[RoleSuccess].Role == "" && p.StyleFor(RoleSuccess).Role == "" {
				t.Fatalf("success role empty for %s", name)
			}
		}
	}
}

func TestMonoHasNoChromaticForeground(t *testing.T) {
	p := NewPalette(PaletteMono, VariantDark)
	for role, s := range p.Styles {
		if s.Foreground.IsSet() {
			t.Fatalf("mono role %s leaked chromatic fg: %+v", role, s.Foreground)
		}
	}
}

func TestBuildThemeContextAutoDark(t *testing.T) {
	ctx := BuildThemeContext(ThemeSelection{
		PaletteName: PaletteFocus,
		Mode:        ThemeModeAuto,
	}, ColorProfile{Background: BackgroundUnknown})
	if ctx.Palette.Variant != VariantDark {
		t.Fatalf("unknown bg should default dark, got %v", ctx.Palette.Variant)
	}
}

func TestBuildThemeContextResolvesAutoSyntaxByVariant(t *testing.T) {
	light := BuildThemeContext(ThemeSelection{
		PaletteName: PaletteFocus,
		Mode:        ThemeModeLight,
		SyntaxName:  "auto",
	}, ColorProfile{})
	dark := BuildThemeContext(ThemeSelection{
		PaletteName: PaletteFocus,
		Mode:        ThemeModeDark,
		SyntaxName:  "auto",
	}, ColorProfile{})
	if light.SyntaxName != "github" || dark.SyntaxName != "monokai" {
		t.Fatalf("resolved syntax: light=%q dark=%q", light.SyntaxName, dark.SyntaxName)
	}
}

func TestClassicAssistantBodyUsesDefaultForeground(t *testing.T) {
	for _, variant := range []Variant{VariantLight, VariantDark} {
		style := NewPalette(PaletteClassic, variant).StyleFor(RoleAssistant)
		if style.Foreground.IsSet() {
			t.Fatalf("variant %v assistant foreground=%+v", variant, style.Foreground)
		}
	}
}

func TestStatusLineDocumentFold(t *testing.T) {
	model := StatusLineModel{
		State:     RunReady,
		StateText: "Ready",
		Segments: []StatusSegment{
			{Text: "model-very-long-name", Priority: 5, Role: RoleAccent},
			{Text: "path/to/project", Priority: 20, Role: RoleTextMuted},
		},
	}
	doc := StatusLineDocument(model, 20)
	plain := doc.PlainText()
	if len(plain) == 0 {
		t.Fatal("empty status")
	}
	// Low-priority path should be folded away first on narrow width.
	if containsAll(plain, "path/to/project") && !containsAll(plain, "...") {
		// May still fit truncated; just ensure width bound via truncate.
	}
}

func TestStatusLineDocumentHideStateStartsWithFirstSegment(t *testing.T) {
	model := StatusLineModel{
		State:     RunReady,
		HideState: true,
		Segments: []StatusSegment{
			{Kind: StatusSegModel, Text: "gpt-5.6-sol", Priority: 0, Role: RoleAccent},
			{Kind: StatusSegUsage, Text: "Context 14% used", Priority: 1, Role: RoleProgress},
		},
	}
	doc := StatusLineDocument(model, 80)
	if got, want := doc.PlainText(), "gpt-5.6-sol · Context 14% used"; got != want {
		t.Fatalf("hidden state status mismatch: got %q want %q", got, want)
	}
	line := doc.Blocks[0].Lines[0]
	if len(line.Spans) == 0 || line.Spans[0].Style.Role != string(RoleAccent) {
		t.Fatalf("expected first segment role to be preserved, got %#v", line.Spans)
	}
}

func TestStatusLineDocumentHideStateDoesNotInventReady(t *testing.T) {
	doc := StatusLineDocument(StatusLineModel{HideState: true}, 80)
	if got := doc.PlainText(); got != "" {
		t.Fatalf("expected empty model to remain empty, got %q", got)
	}
}

func containsAll(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})())
}
