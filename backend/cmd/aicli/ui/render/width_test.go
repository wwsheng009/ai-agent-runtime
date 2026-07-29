package render

import "testing"

func TestWidthASCIIAndCJK(t *testing.T) {
	if got := Width("abc"); got != 3 {
		t.Fatalf("ascii width=%d", got)
	}
	if got := Width("中文"); got != 4 {
		t.Fatalf("cjk width=%d want 4", got)
	}
}

func TestWidthIgnoresNothingInPlainText(t *testing.T) {
	// Style is not part of Width(text); callers pass visible text only.
	if got := Width(""); got != 0 {
		t.Fatalf("empty width=%d", got)
	}
}

func TestTruncatePreservesGraphemes(t *testing.T) {
	line := Line{Spans: []Span{{Text: "hello世界"}}}
	got := Truncate(line, 7, "...")
	plain := linePlain(got)
	if Width(plain) > 7 {
		t.Fatalf("truncated wider than budget: %q width=%d", plain, Width(plain))
	}
	if !containsMarker(plain, "...") {
		t.Fatalf("expected marker in %q", plain)
	}
}

func TestWrapRespectsWidth(t *testing.T) {
	line := Line{Spans: []Span{{Text: "one two three four"}}}
	lines := Wrap(line, 9, WrapOptions{BreakWord: true})
	for i, l := range lines {
		if w := LineWidth(l); w > 9 {
			t.Fatalf("line %d width %d > 9: %q", i, w, linePlain(l))
		}
	}
	if len(lines) < 2 {
		t.Fatalf("expected wrap, got %d lines", len(lines))
	}
}

func TestPadRight(t *testing.T) {
	line := Line{Spans: []Span{{Text: "ab"}}}
	got := Pad(line, 5, AlignLeft)
	if LineWidth(got) != 5 {
		t.Fatalf("pad width=%d", LineWidth(got))
	}
}

func containsMarker(s, marker string) bool {
	return len(s) >= len(marker) && (s[len(s)-len(marker):] == marker || Width(s) > 0 && true && (func() bool {
		for i := 0; i+len(marker) <= len(s); i++ {
			if s[i:i+len(marker)] == marker {
				return true
			}
		}
		return false
	})())
}
