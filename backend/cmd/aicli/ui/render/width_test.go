package render

import (
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestWidthASCIIAndCJK(t *testing.T) {
	if got := Width("abc"); got != 3 {
		t.Fatalf("ascii width=%d", got)
	}
	if got := Width("中文"); got != 4 {
		t.Fatalf("cjk width=%d want 4", got)
	}
}

// TestRuneWidthMatchesWidth asserts RuneWidth agrees with Width(string(r))
// for every valid rune. Width(string(r)) is the reference implementation;
// RuneWidth is the fast path used by hot wrapping loops, so any divergence
// would change wrapping behavior.
func TestRuneWidthMatchesWidth(t *testing.T) {
	var mismatches []string
	for r := rune(0); r <= utf8.MaxRune; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue // surrogate range is not valid runes
		}
		want := Width(string(r))
		if got := RuneWidth(r); got != want {
			if len(mismatches) < 30 {
				mismatches = append(mismatches, fmt.Sprintf("U+%04X: RuneWidth=%d Width=%d", r, got, want))
			}
		}
	}
	if len(mismatches) > 0 {
		t.Fatalf("%d mismatches, first 30:\n%s", len(mismatches), strings.Join(mismatches, "\n"))
	}
}

func TestRuneWidthKnownValues(t *testing.T) {
	cases := []struct {
		r    rune
		want int
	}{
		{0, 0},
		{'\n', 0},
		{'a', 1},
		{'中', 2},
		{0x2e3a, 3},  // TWO-EM DASH (uniseg special case)
		{0x2e3b, 4},  // THREE-EM DASH
		{0x200d, 0},  // ZERO WIDTH JOINER
		{0x0301, 0},  // COMBINING ACUTE ACCENT
		{0xfe0f, 0},  // VARIATION SELECTOR-16
		{0x1f3fb, 0}, // EMOJI MODIFIER FITZPATRICK
		{0x1f1e6, 2}, // REGIONAL INDICATOR A
		{0x1f600, 2}, // GRINNING FACE (emoji presentation)
	}
	for _, c := range cases {
		if got := RuneWidth(c.r); got != c.want {
			t.Errorf("RuneWidth(%U)=%d want %d", c.r, got, c.want)
		}
	}
}

func BenchmarkRuneWidthCJK(b *testing.B) {
	s := "中文测试文本用于测量逐字符宽度计算的性能表现"
	rs := []rune(s)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, r := range rs {
			RuneWidth(r)
		}
	}
}

func BenchmarkWidthSingleRuneCJK(b *testing.B) {
	s := "中文测试文本用于测量逐字符宽度计算的性能表现"
	rs := []rune(s)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, r := range rs {
			Width(string(r))
		}
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

// runeWidthAllocSink keeps the benchmarked result observable without letting
// the compiler delete the measured loop.
var runeWidthAllocSink int

// TestRuneWidthAllocationFree pins the property that makes RuneWidth worth
// using in per-rune wrapping loops: measuring one rune must not allocate.
// Callers in ui/ (prompt input virtualization, full-screen list wrapping,
// bottom-surface truncation) run per frame and used to call
// DisplayWidth(string(r)), which allocated a string per rune -- 276 allocations
// for a 276-rune prompt line.
func TestRuneWidthAllocationFree(t *testing.T) {
	sample := []rune("a中\u0301\u200d\U0001F600")
	allocs := testing.AllocsPerRun(200, func() {
		total := 0
		for _, r := range sample {
			total += RuneWidth(r)
		}
		runeWidthAllocSink = total
	})
	if allocs != 0 {
		t.Fatalf("RuneWidth allocated %v times per run, want 0", allocs)
	}
}

// eastAsianWideRune is consulted before the Mark/Format lookup in RuneWidth
// because the range switch is far cheaper for CJK-heavy text. That reorder is
// only exact while the wide intervals hold no Mark or Format codepoint: such a
// rune would report width 2 instead of 0. TestRuneWidthMatchesWidth already
// covers the equivalence; this test names the invariant the ordering depends
// on, so a future change to the interval list gets a direct failure message.
func TestEastAsianWideRangesExcludeMarksAndFormat(t *testing.T) {
	for r := rune(0); r <= utf8.MaxRune; r++ {
		if !eastAsianWideRune(r) {
			continue
		}
		if unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) {
			t.Fatalf("U+%04X is East Asian wide and Mark/Format; RuneWidth must test unicode.In before eastAsianWideRune", r)
		}
	}
}
