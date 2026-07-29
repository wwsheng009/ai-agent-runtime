package render

import (
	"strings"
	"testing"
	"time"
)

func TestFrameSchedulerCoalesceAndFPS(t *testing.T) {
	s := NewFrameScheduler(10) // 100ms gap
	var emits []string
	s.SetOnEmit(func(reason string) { emits = append(emits, reason) })

	s.Request("a")
	s.Request("b")
	now := time.Unix(0, 0)
	if !s.Consume(now) {
		t.Fatal("first consume should emit")
	}
	if len(emits) != 1 || emits[0] != "b" {
		// last reason wins
		if len(emits) != 1 {
			t.Fatalf("emits=%v", emits)
		}
	}
	s.Request("c")
	if s.Consume(now.Add(10 * time.Millisecond)) {
		t.Fatal("should respect min gap")
	}
	if !s.Consume(now.Add(100 * time.Millisecond)) {
		t.Fatal("expected emit after gap")
	}
}

func TestBufferBackendDiffAndTruncate(t *testing.T) {
	b := &BufferBackend{Width: 10, Height: 3}
	doc := Document{Blocks: []Block{{
		Lines: []Line{
			{Spans: []Span{{Text: "hello world extra"}}},
			{Spans: []Span{{Text: "two"}}},
		},
	}}}
	out := b.Render(doc)
	if !strings.Contains(out, "…") && Width(b.Lines[0]) > 10 {
		t.Fatalf("expected truncate: %q lines=%v", out, b.Lines)
	}
	prev := b.Snapshot()
	doc2 := Document{Blocks: []Block{{
		Lines: []Line{
			{Spans: []Span{{Text: "hello world extra"}}},
			{Spans: []Span{{Text: "TWO"}}},
		},
	}}}
	_ = b.Render(doc2)
	diffs := b.Diff(prev)
	if len(diffs) != 1 || diffs[0].Index != 1 {
		t.Fatalf("diffs=%+v", diffs)
	}
}

func TestBufferBackendRetainsStyledLinesAfterCellTruncate(t *testing.T) {
	b := &BufferBackend{Width: 8, Height: 2}
	doc := LinesDoc(Line{Spans: []Span{
		{Text: "标题", Style: Style{Role: "Accent", Bold: true}},
		{Text: "abcdef", Style: Style{Role: "TextPrimary"}},
	}})
	b.Render(doc)

	styled := b.StyledSnapshot()
	if len(styled) != 1 || LineWidth(styled[0]) > 8 {
		t.Fatalf("unexpected styled frame geometry: %#v", styled)
	}
	if len(styled[0].Spans) == 0 || styled[0].Spans[0].Style.Role != "Accent" {
		t.Fatalf("expected leading semantic role to survive truncate: %#v", styled[0].Spans)
	}
	if got := (PlainBackend{}).Render(LinesDoc(styled...)); got != strings.Join(b.Lines, "\n") {
		t.Fatalf("styled/plain frame projections diverged: styled=%q plain=%q", got, b.Lines)
	}
}

func TestBufferBackendLayoutKeepsOnlyVisibleTailAcrossBlocks(t *testing.T) {
	b := &BufferBackend{Width: 20, Height: 2}
	doc := Document{Blocks: []Block{
		{Lines: []Line{{Spans: []Span{{Text: "one"}}}, {Spans: []Span{{Text: "two"}}}}},
		{Lines: []Line{{Spans: []Span{{Text: "three"}}}, {Spans: []Span{{Text: ""}}}}},
	}}
	if got, want := b.Render(doc), "two\nthree"; got != want {
		t.Fatalf("unexpected visible tail: got %q want %q", got, want)
	}
	if len(b.StyledLines) != 2 || b.StyledLines[0].Spans[0].Text != "two" {
		t.Fatalf("unexpected structured visible tail: %#v", b.StyledLines)
	}
}

func TestLinesEqualDetectsStyleOnlyChanges(t *testing.T) {
	a := []Line{{Spans: []Span{{Text: "keyword", Style: Style{Foreground: RGB(1, 2, 3)}}}}}
	b := []Line{{Spans: []Span{{Text: "keyword", Style: Style{Foreground: RGB(4, 5, 6)}}}}}
	if LinesEqual(a, b) {
		t.Fatal("expected explicit color change to invalidate structured frame")
	}
	if !LinesEqual(a, a) {
		t.Fatal("expected identical structured frame to compare equal")
	}
}
