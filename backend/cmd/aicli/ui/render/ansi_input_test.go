package render

import (
	"strings"
	"testing"
)

func TestANSIToSpansKeepsSGRDropsClear(t *testing.T) {
	input := "\x1b[31mERR\x1b[0m\x1b[2J\x1b[Hsafe"
	spans := ANSIToSpans(input)
	plain := ""
	for _, s := range spans {
		plain += s.Text
	}
	if strings.Contains(plain, "\x1b") {
		t.Fatalf("plain still has ESC: %q", plain)
	}
	if !strings.Contains(plain, "ERR") || !strings.Contains(plain, "safe") {
		t.Fatalf("lost text: %q spans=%+v", plain, spans)
	}
	// First span should carry red-ish ANSI fg from SGR 31.
	foundStyled := false
	for _, s := range spans {
		if s.Text == "ERR" && s.Style.Foreground.IsSet() {
			foundStyled = true
		}
	}
	if !foundStyled {
		t.Fatalf("expected styled ERR span, got %+v", spans)
	}
}

func TestANSIToSpansDropsOSCTitleAndClipboard(t *testing.T) {
	input := "\x1b]0;evil-title\x07before\x1b]52;c;YQ==\x07after"
	plain := ANSIToPlain(input)
	if strings.Contains(plain, "evil") || strings.Contains(plain, "YQ==") {
		t.Fatalf("OSC content leaked: %q", plain)
	}
	if plain != "beforeafter" {
		t.Fatalf("plain=%q", plain)
	}
}

func TestANSIToSpansDropsCursorAndAltScreen(t *testing.T) {
	input := "\x1b[?1049h\x1b[10;10Hmove\x1b[2K\x1b[?1049l"
	plain := ANSIToPlain(input)
	if plain != "move" {
		t.Fatalf("plain=%q", plain)
	}
}

func TestANSIToSpansHandles256AndRGB(t *testing.T) {
	input := "\x1b[38;5;196mA\x1b[0m\x1b[38;2;10;20;30mB\x1b[0m"
	spans := ANSIToSpans(input)
	var sawIndexed, sawRGB bool
	for _, s := range spans {
		if s.Text == "A" && s.Style.Foreground.Kind == ColorIndexed {
			sawIndexed = true
		}
		if s.Text == "B" && s.Style.Foreground.Kind == ColorRGB {
			sawRGB = true
			if s.Style.Foreground.R != 10 || s.Style.Foreground.G != 20 || s.Style.Foreground.B != 30 {
				t.Fatalf("rgb mismatch: %+v", s.Style.Foreground)
			}
		}
	}
	if !sawIndexed || !sawRGB {
		t.Fatalf("missing color kinds: spans=%+v", spans)
	}
}

func TestANSIToSpansMalformedDoesNotPanic(t *testing.T) {
	inputs := []string{
		"\x1b",
		"\x1b[",
		"\x1b[999999m",
		"\x1b]8;;\x1b",
		string([]byte{0x1b, 0x40}),
	}
	for _, in := range inputs {
		_ = ANSIToPlain(in)
	}
}

func TestANSIToLinesUsesLineBoundaries(t *testing.T) {
	lines := ANSIToLines("\x1b[31mred\nnext\x1b[0m")
	if len(lines) != 2 {
		t.Fatalf("lines=%d: %+v", len(lines), lines)
	}
	for _, line := range lines {
		for _, span := range line.Spans {
			if strings.ContainsAny(span.Text, "\r\n") {
				t.Fatalf("newline leaked into span: %+v", span)
			}
		}
	}
	if got := ANSIToPlain("a\r\nb"); got != "a\nb" {
		t.Fatalf("plain line projection=%q", got)
	}
}

func TestParseCSIMalformedBytesAreDiscarded(t *testing.T) {
	plain := ANSIToPlain("before\x1b[31\x7fm after")
	if strings.ContainsAny(plain, "\x1b\x7f") {
		t.Fatalf("malformed CSI leaked controls: %q", plain)
	}
}

func TestANSIToPlainDropsLongOSCWithoutPayloadLeak(t *testing.T) {
	payload := strings.Repeat("secret", 400)
	plain := ANSIToPlain("before\x1b]52;c;" + payload + "\x07after")
	if plain != "beforeafter" {
		t.Fatalf("long OSC payload leaked: %q", plain)
	}
}
