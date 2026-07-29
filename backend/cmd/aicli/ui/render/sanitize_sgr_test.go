package render

import (
	"strings"
	"testing"
)

func TestSanitizeKeepSGRPreservesColorsDropsClear(t *testing.T) {
	in := "\x1b[31mred\x1b[0m\x1b[2J\x1b[Hok\x1b]0;title\x07"
	got := SanitizeKeepSGR(in)
	if !strings.Contains(got, "\x1b[31m") || !strings.Contains(got, "red") {
		t.Fatalf("lost SGR/text: %q", got)
	}
	if strings.Contains(got, "\x1b[2J") || strings.Contains(got, "title") {
		t.Fatalf("dangerous controls remained: %q", got)
	}
	if !strings.Contains(got, "ok") {
		t.Fatalf("lost plain text: %q", got)
	}
}

func TestSanitizeKeepSGRKeepsOSC8(t *testing.T) {
	in := "\x1b]8;;https://ex.com\x1b\\label\x1b]8;;\x1b\\"
	got := SanitizeKeepSGR(in)
	if !strings.Contains(got, "\x1b]8;;https://ex.com") {
		t.Fatalf("lost hyperlink: %q", got)
	}
	if !strings.Contains(got, "label") {
		t.Fatalf("lost label: %q", got)
	}
}

func TestSanitizeKeepSGRRejectsInjectedOSC8Payload(t *testing.T) {
	in := "\x1b]8;;https://ex.com\x1b\\\x1b]52;c;YQ==\x07label"
	got := SanitizeKeepSGR(in)
	if strings.Contains(got, "\x1b]52") || strings.Contains(got, "YQ==") {
		t.Fatalf("clipboard OSC leaked: %q", got)
	}
	if !strings.Contains(got, "\x1b]8;;\x1b\\") {
		t.Fatalf("unterminated hyperlink was not closed: %q", got)
	}
}

func TestSanitizeKeepSGRClosesUnterminatedStyle(t *testing.T) {
	got := SanitizeKeepSGR("\x1b[31mred")
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatalf("unterminated SGR was not reset: %q", got)
	}
	unsafe := SanitizeKeepSGR("\x1b[?25mtext")
	if strings.Contains(unsafe, "\x1b[?25m") {
		t.Fatalf("private CSI was preserved as SGR: %q", unsafe)
	}
}
