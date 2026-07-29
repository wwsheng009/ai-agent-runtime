package render

// SpacingPolicy describes the vertical rhythm between adjacent blocks.
//
// It is pure data. The layout stage materializes it into BlockSpacer blocks so
// backends stay unaware of block semantics: every backend already knows how to
// emit a line without spans as a blank row.
type SpacingPolicy struct {
	// Default is the number of blank lines inserted between two blocks.
	Default int
	// SameKind overrides Default when both neighbors share a kind, which keeps
	// tight lists, quote continuations and status stacks dense.
	SameKind map[BlockKind]int
	// Between overrides Default (and SameKind) for one specific ordered pair.
	Between map[BlockPair]int
}

// BlockPair identifies an ordered adjacency of two block kinds.
type BlockPair struct {
	Prev BlockKind
	Next BlockKind
}

// DefaultSpacingPolicy separates prose-level blocks with a single blank line
// while keeping list items and quote continuations tight.
func DefaultSpacingPolicy() SpacingPolicy {
	return SpacingPolicy{
		Default: 1,
		SameKind: map[BlockKind]int{
			BlockList:   0,
			BlockQuote:  0,
			BlockStatus: 0,
			BlockCustom: 0,
		},
	}
}

// CompactSpacingPolicy keeps blocks adjacent (legacy dense output).
func CompactSpacingPolicy() SpacingPolicy {
	return SpacingPolicy{}
}

// Gap returns the number of blank lines to insert between two block kinds.
func (p SpacingPolicy) Gap(prev, next BlockKind) int {
	if gap, ok := p.Between[BlockPair{Prev: prev, Next: next}]; ok {
		return normalizeGap(gap)
	}
	if prev == next {
		if gap, ok := p.SameKind[prev]; ok {
			return normalizeGap(gap)
		}
	}
	return normalizeGap(p.Default)
}

func normalizeGap(gap int) int {
	if gap < 0 {
		return 0
	}
	return gap
}

// SpacerBlock builds vertical whitespace made of n blank lines.
func SpacerBlock(n int) Block {
	if n < 1 {
		n = 1
	}
	return Block{Kind: BlockSpacer, Lines: make([]Line, n)}
}

// ApplyBlockSpacing returns a copy of doc with BlockSpacer blocks inserted
// between content blocks according to policy.
//
// The pass is idempotent and order-preserving: pre-existing spacers are layout
// artifacts and get recomputed, empty blocks are dropped, and no spacer is
// emitted before the first or after the last content block.
func ApplyBlockSpacing(doc Document, policy SpacingPolicy) Document {
	content := make([]Block, 0, len(doc.Blocks))
	for _, block := range doc.Blocks {
		if block.Kind == BlockSpacer || len(block.Lines) == 0 {
			continue
		}
		content = append(content, block)
	}
	if len(content) == 0 {
		return Document{}
	}
	out := make([]Block, 0, len(content)*2)
	for i, block := range content {
		if i > 0 {
			if gap := policy.Gap(content[i-1].Kind, block.Kind); gap > 0 {
				out = append(out, SpacerBlock(gap))
			}
		}
		out = append(out, block)
	}
	return Document{Blocks: out}
}
