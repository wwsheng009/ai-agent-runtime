package render

import (
	"strings"
	"testing"
)

func TestMergeStylesOverlayWins(t *testing.T) {
	base := Style{Foreground: ANSI(2), Bold: true}
	overlay := Style{Foreground: ANSI(1), Dim: true}
	got := MergeStyles(base, overlay)
	if got.Foreground.Index != 1 || !got.Bold || !got.Dim {
		t.Fatalf("unexpected merge: %+v", got)
	}
}

func TestEffectiveSpanStyleKeepsLineBackground(t *testing.T) {
	line := Style{Background: RGB(20, 40, 20)}
	span := Style{Foreground: ANSI(2), Bold: true}
	got := EffectiveSpanStyle(line, span)
	if !got.Background.IsSet() || got.Background.G != 40 {
		t.Fatalf("expected line background retained, got %+v", got)
	}
	if got.Foreground.Index != 2 || !got.Bold {
		t.Fatalf("expected span fg/bold, got %+v", got)
	}
}

func TestPlainBackendDropsStyles(t *testing.T) {
	doc := SingleLineDoc(
		StyledSpan("ok", Style{Foreground: RGB(0, 255, 0), Bold: true}),
		TextSpan(" plain"),
	)
	got := PlainBackend{}.Render(doc)
	if got != "ok plain" {
		t.Fatalf("plain render = %q", got)
	}
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("plain backend emitted control bytes: %q", got)
	}
}

func TestANSIBackendTrueColorAndReset(t *testing.T) {
	doc := SingleLineDoc(StyledSpan("x", Style{Foreground: RGB(1, 2, 3), Bold: true}))
	out := ANSIBackend{Profile: TrueColorProfile()}.Render(doc)
	if !strings.Contains(out, "\x1b[1;38;2;1;2;3m") {
		t.Fatalf("expected truecolor SGR, got %q", out)
	}
	if !strings.HasSuffix(out, "\x1b[0m") && !strings.Contains(out, "\x1b[0m") {
		t.Fatalf("expected reset, got %q", out)
	}
}

func TestANSIBackendANSI16NoRGB(t *testing.T) {
	doc := SingleLineDoc(StyledSpan("x", Style{
		Foreground: RGB(255, 0, 0),
		Background: RGB(0, 0, 40),
	}))
	out := ANSIBackend{Profile: ColorProfile{Enabled: true, Depth: ColorANSI16}}.Render(doc)
	if strings.Contains(out, "38;2") || strings.Contains(out, "48;2") || strings.Contains(out, "38;5") || strings.Contains(out, "48;5") {
		t.Fatalf("ANSI-16 must not emit 256/truecolor channels: %q", out)
	}
}

func TestANSIBackendNoColor(t *testing.T) {
	doc := SingleLineDoc(StyledSpan("hi", Style{Foreground: ANSI(1), Bold: true}))
	out := ANSIBackend{Profile: NoColorProfile()}.Render(doc)
	if out != "hi" {
		t.Fatalf("nocolor = %q", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Fatalf("nocolor leaked ESC: %q", out)
	}
}

func TestBackendsSanitizeStructuredTextInjection(t *testing.T) {
	doc := SingleLineDoc(TextSpan("before\x1b[2Jafter\x07"))
	plain := PlainBackend{}.Render(doc)
	if plain != "beforeafter" || strings.ContainsAny(plain, "\x1b\x07") {
		t.Fatalf("plain backend did not sanitize text: %q", plain)
	}
	ansi := ANSIBackend{Profile: TrueColorProfile()}.Render(doc)
	if strings.Contains(ansi, "\x1b[2J") || strings.ContainsRune(ansi, '\x07') {
		t.Fatalf("ANSI backend leaked injected controls: %q", ansi)
	}
}

func TestANSIBackendRejectsUnsafeHyperlinkPayload(t *testing.T) {
	doc := SingleLineDoc(Span{
		Text: "link",
		Link: "https://example.test\x1b\\\x1b]52;c;YQ==",
	})
	out := ANSIBackend{Profile: TrueColorProfile()}.Render(doc)
	if out != "link" || strings.Contains(out, "\x1b]8") || strings.Contains(out, "\x1b]52") {
		t.Fatalf("unsafe hyperlink was encoded: %q", out)
	}
}

func TestANSIBackendAllowsFileHyperlink(t *testing.T) {
	doc := SingleLineDoc(Span{Text: "file", Link: "file:///C:/tmp/demo.go"})
	out := ANSIBackend{Profile: TrueColorProfile()}.Render(doc)
	if !strings.Contains(out, "\x1b]8;;file:///C:/tmp/demo.go\x1b\\") {
		t.Fatalf("expected safe file hyperlink: %q", out)
	}
}

func TestStyledGolden(t *testing.T) {
	doc := SingleLineDoc(
		RoleSpan("Ready", "Success"),
		RoleSpan(" · model", "TextMuted"),
	)
	got := StyledGolden(doc)
	if !strings.Contains(got, `[Success] "Ready"`) {
		t.Fatalf("golden missing success span:\n%s", got)
	}
	if !strings.Contains(got, `[TextMuted] " · model"`) {
		t.Fatalf("golden missing muted span:\n%s", got)
	}
}

func TestRGBToANSI256Stable(t *testing.T) {
	a := rgbToANSI256(255, 0, 0)
	b := rgbToANSI256(255, 0, 0)
	if a != b {
		t.Fatalf("quantization unstable: %d vs %d", a, b)
	}
	// Bright red should land near cube red, not grayscale.
	if a < 16 {
		t.Fatalf("expected cube/gray index >= 16, got %d", a)
	}
}
