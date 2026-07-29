package render

import (
	"strings"
	"testing"
)

func blockOf(kind BlockKind, texts ...string) Block {
	lines := make([]Line, 0, len(texts))
	for _, text := range texts {
		lines = append(lines, Line{Spans: []Span{TextSpan(text)}})
	}
	return Block{Kind: kind, Lines: lines}
}

func TestApplyBlockSpacingSeparatesProseBlocks(t *testing.T) {
	doc := Document{Blocks: []Block{
		blockOf(BlockParagraph, "first"),
		blockOf(BlockParagraph, "second"),
		blockOf(BlockCode, "code()"),
	}}
	got := PlainBackend{}.Render(ApplyBlockSpacing(doc, DefaultSpacingPolicy()))
	want := "first\n\nsecond\n\ncode()"
	if got != want {
		t.Fatalf("plain render = %q, want %q", got, want)
	}
	ansi := ANSIBackend{Profile: TrueColorProfile()}.Render(ApplyBlockSpacing(doc, DefaultSpacingPolicy()))
	if ansi != want {
		t.Fatalf("ansi render = %q, want %q", ansi, want)
	}
}

func TestApplyBlockSpacingKeepsSameKindTight(t *testing.T) {
	doc := Document{Blocks: []Block{
		blockOf(BlockList, "• one"),
		blockOf(BlockList, "• two"),
		blockOf(BlockParagraph, "after"),
	}}
	got := PlainBackend{}.Render(ApplyBlockSpacing(doc, DefaultSpacingPolicy()))
	want := "• one\n• two\n\nafter"
	if got != want {
		t.Fatalf("plain render = %q, want %q", got, want)
	}
}

func TestApplyBlockSpacingNoLeadingOrTrailingSpacer(t *testing.T) {
	doc := Document{Blocks: []Block{
		{Kind: BlockSpacer, Lines: []Line{{}}},
		blockOf(BlockParagraph, "only"),
		{Kind: BlockParagraph},
		{Kind: BlockSpacer, Lines: []Line{{}}},
	}}
	out := ApplyBlockSpacing(doc, DefaultSpacingPolicy())
	if len(out.Blocks) != 1 || out.Blocks[0].Kind != BlockParagraph {
		t.Fatalf("unexpected blocks: %+v", out.Blocks)
	}
	if got := (PlainBackend{}).Render(out); got != "only" {
		t.Fatalf("plain render = %q", got)
	}
}

func TestApplyBlockSpacingIsIdempotent(t *testing.T) {
	doc := Document{Blocks: []Block{
		blockOf(BlockParagraph, "a"),
		blockOf(BlockParagraph, "b"),
	}}
	once := ApplyBlockSpacing(doc, DefaultSpacingPolicy())
	twice := ApplyBlockSpacing(once, DefaultSpacingPolicy())
	if once.LineCount() != twice.LineCount() {
		t.Fatalf("line count drifted: %d then %d", once.LineCount(), twice.LineCount())
	}
	if (PlainBackend{}).Render(once) != (PlainBackend{}).Render(twice) {
		t.Fatalf("second pass changed output: %q", (PlainBackend{}).Render(twice))
	}
}

func TestCompactSpacingPolicyKeepsBlocksAdjacent(t *testing.T) {
	doc := Document{Blocks: []Block{
		blockOf(BlockParagraph, "a"),
		blockOf(BlockParagraph, "b"),
	}}
	got := PlainBackend{}.Render(ApplyBlockSpacing(doc, CompactSpacingPolicy()))
	if got != "a\nb" {
		t.Fatalf("compact render = %q", got)
	}
	if strings.Contains(got, "\n\n") {
		t.Fatalf("compact policy inserted blank line: %q", got)
	}
}

func TestSpacingPolicyBetweenOverride(t *testing.T) {
	policy := DefaultSpacingPolicy()
	policy.Between = map[BlockPair]int{{Prev: BlockList, Next: BlockList}: 2}
	if gap := policy.Gap(BlockList, BlockList); gap != 2 {
		t.Fatalf("pair override gap = %d", gap)
	}
	if gap := policy.Gap(BlockParagraph, BlockList); gap != 1 {
		t.Fatalf("default gap = %d", gap)
	}
	if gap := policy.Gap(BlockQuote, BlockQuote); gap != 0 {
		t.Fatalf("same-kind gap = %d", gap)
	}
}

func TestSpacerBlockMinimumOneLine(t *testing.T) {
	if got := len(SpacerBlock(0).Lines); got != 1 {
		t.Fatalf("spacer lines = %d", got)
	}
	if got := len(SpacerBlock(3).Lines); got != 3 {
		t.Fatalf("spacer lines = %d", got)
	}
}

func TestBufferLayoutTailSkipsLeadingSpacer(t *testing.T) {
	doc := ApplyBlockSpacing(Document{Blocks: []Block{
		blockOf(BlockParagraph, "one"),
		blockOf(BlockParagraph, "two"),
		blockOf(BlockParagraph, "three"),
	}}, DefaultSpacingPolicy())
	buffer := &BufferBackend{Width: 20, Height: 3}
	lines := buffer.Layout(doc)
	if len(lines) != 3 {
		t.Fatalf("expected a full viewport, got %d lines", len(lines))
	}
	if LineWidth(lines[0]) == 0 {
		t.Fatalf("viewport starts with blank line: %+v", lines)
	}
	got := (PlainBackend{}).Render(LinesDoc(lines...))
	if got != "two\n\nthree" {
		t.Fatalf("tail layout = %q", got)
	}
}
