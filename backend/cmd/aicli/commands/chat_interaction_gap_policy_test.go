package commands

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
)

// gapName renders a blockGap for readable failure messages.
func gapName(g blockGap) string {
	switch g {
	case gapNone:
		return "gapNone"
	case gapBlank:
		return "gapBlank"
	default:
		return "gap(?)"
	}
}

// TestGapPolicyTruthTable pins the cell-boundary spacing rule (unified plan
// §7.3): gapBeforeBlockLocked delegates to boundary.ResolveGap, which decides
// 0/1 from the previous committed block's metadata and the next block's
// metadata. Prompt visibility and old async-chain booleans no longer
// participate: viewport-only Running rows are not history cells, while every
// retained event owns its own boundary.
func TestGapPolicyTruthTable(t *testing.T) {
	assistant := boundary.CellMeta{ID: "assistant-1", Kind: boundary.KindAssistant, TopLevel: true}
	user := boundary.CellMeta{ID: "user-1", Kind: boundary.KindUser, TopLevel: true}
	cases := []struct {
		name string
		prev boundary.CellMeta
		next boundary.CellMeta
		want blockGap
	}{
		{name: "first cell stays dense", prev: boundary.CellMeta{}, next: user, want: gapNone},
		{name: "same id stays dense", prev: assistant, next: assistant, want: gapNone},
		{
			name: "same tool chain stays dense",
			prev: boundary.CellMeta{ID: "tool-1", Kind: boundary.KindTool, TopLevel: true, ChainKey: "chain"},
			next: boundary.CellMeta{ID: "tool-2", Kind: boundary.KindTool, TopLevel: true, ChainKey: "chain"},
			want: gapNone,
		},
		{name: "assistant->user", prev: assistant, next: user, want: gapBlank},
		{name: "user->assistant", prev: user, next: assistant, want: gapBlank},
		{
			name: "committed->command",
			prev: assistant,
			next: boundary.CellMeta{ID: "command-1", Kind: boundary.KindCommand, TopLevel: true},
			want: gapBlank,
		},
		{
			name: "committed->system",
			prev: assistant,
			next: boundary.CellMeta{ID: "error-1", Kind: boundary.KindSystem, TopLevel: true},
			want: gapBlank,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &chatInteractionCoordinator{lastBlockMeta: tc.prev}
			if got := c.gapBeforeBlockLocked(tc.next); got != tc.want {
				t.Fatalf("gapBeforeBlockLocked(%q after %q)=%s want %s",
					tc.next.ID, tc.prev.ID, gapName(got), gapName(tc.want))
			}
		})
	}
}

// TestGapPolicyPrewrittenGapConsumed pins the prompt-pre-written gap handoff:
// once the gap is materialized ahead of a prompt repaint (writePromptGapLocked
// -> gapPreWritten), the next committed block must not write it again.
func TestGapPolicyPrewrittenGapConsumed(t *testing.T) {
	c := &chatInteractionCoordinator{
		lastBlockMeta: boundary.CellMeta{ID: "assistant-1", Kind: boundary.KindAssistant, TopLevel: true},
		gapPreWritten: true,
	}
	if got := c.gapBeforeBlockLocked(boundary.CellMeta{ID: "user-1", Kind: boundary.KindUser, TopLevel: true}); got != gapNone {
		t.Fatalf("prewritten gap must be consumed, got %s", gapName(got))
	}
}

// TestReasoningSupplementSpacing pins the reasoning-supplement boundary: an
// independent supplement after a committed assistant block gets one gap; with
// no previous block it stays dense.
func TestReasoningSupplementSpacing(t *testing.T) {
	supplement := boundary.CellMeta{ID: "supplement-1", Kind: boundary.KindAssistant, TopLevel: true}

	c := &chatInteractionCoordinator{}
	if got := c.gapBeforeBlockLocked(supplement); got != gapNone {
		t.Fatalf("first-cell supplement=%s want gapNone", gapName(got))
	}

	c.lastBlockMeta = boundary.CellMeta{ID: "assistant-1", Kind: boundary.KindAssistant, TopLevel: true}
	if got := c.gapBeforeBlockLocked(supplement); got != gapBlank {
		t.Fatalf("supplement after assistant=%s want gapBlank", gapName(got))
	}
}

// TestGapPolicyNilReceiverSafe guards the nil-coordinator fast paths the render
// helpers rely on.
func TestGapPolicyNilReceiverSafe(t *testing.T) {
	var c *chatInteractionCoordinator
	if got := c.gapBeforeBlockLocked(boundary.CellMeta{ID: "x"}); got != gapNone {
		t.Fatalf("nil gapBeforeBlockLocked=%s want gapNone", gapName(got))
	}
	c.markBlockCommittedLocked(boundary.CellMeta{ID: "x"}) // must not panic
}

// TestGapPolicyInterruptedStreamCompensation pins the stream-interrupt
// compensation (legacy completeBlockOutput=false semantics): after streaming
// deltas have painted the block — or a terminator blank closed its last
// mid-line — the next independent block inserted via beginMessageLocked (e.g.
// tool_result) must NOT repeat the gap. A finalize commit
// (markBlockCommittedLocked) clears the marker and restores rule-table
// decisions, so the block after the interrupt gets a fresh gap again.
func TestGapPolicyInterruptedStreamCompensation(t *testing.T) {
	user := boundary.CellMeta{ID: "user-1", Kind: boundary.KindUser, TopLevel: true}
	tool := boundary.CellMeta{ID: "tool-1", Kind: boundary.KindTool, TopLevel: true}

	c := &chatInteractionCoordinator{lastBlockMeta: user}
	// 等价于 writeIndentedStreamingDeltaLocked / renderBufferedAssistantStreamLocked
	// 增量写出后的状态：增量行已提供块间视觉分隔。
	c.gapPreWritten = true
	if got := c.gapBeforeBlockLocked(tool); got != gapNone {
		t.Fatalf("interrupt block after stream deltas=%s want gapNone (delta already separates)", gapName(got))
	}
	// 打断块提交消费标记。
	c.markBlockCommittedLocked(tool)
	if c.gapPreWritten {
		t.Fatal("markBlockCommittedLocked must clear gapPreWritten")
	}
	// 规则表决策恢复：打断后的下一个独立块重新获得 gap。
	next := boundary.CellMeta{ID: "supplement-1", Kind: boundary.KindAssistant, TopLevel: true}
	if got := c.gapBeforeBlockLocked(next); got != gapBlank {
		t.Fatalf("block after committed interrupt=%s want gapBlank", gapName(got))
	}
}

// TestGapPolicyStreamIDLifecycle pins the stream-boundary identity lifecycle:
// chunks inside one stream share one ID (ResolveGap same-ID -> dense), while
// resetStreamLocked / resetBlockBoundaryLocked must allocate a fresh ID for
// the next turn — otherwise two consecutive assistant streams would share a
// boundary ID and the gap between turns would be lost (INV-GAP-03).
func TestGapPolicyStreamIDLifecycle(t *testing.T) {
	c := &chatInteractionCoordinator{}
	first := c.streamBoundaryMetaLocked()
	if first.ID == "" {
		t.Fatal("stream meta must allocate an ID")
	}
	if again := c.streamBoundaryMetaLocked(); again.ID != first.ID {
		t.Fatalf("chunks inside one stream must stay dense: %q != %q", again.ID, first.ID)
	}
	// Turn boundary: the stream ends; the next turn is a different stream.
	c.resetStreamLocked()
	second := c.streamBoundaryMetaLocked()
	if second.ID == first.ID {
		t.Fatalf("resetStreamLocked must allocate a fresh stream ID, got %q", second.ID)
	}
	// Run reset / shutdown path clears it as well.
	c.resetBlockBoundaryLocked()
	third := c.streamBoundaryMetaLocked()
	if third.ID == second.ID {
		t.Fatalf("resetBlockBoundaryLocked must allocate a fresh stream ID, got %q", third.ID)
	}
	// A stream right after a reset is the first block: no leading gap.
	if got := c.gapBeforeBlockLocked(third); got != gapNone {
		t.Fatalf("first stream block after reset=%s want gapNone", gapName(got))
	}
}
