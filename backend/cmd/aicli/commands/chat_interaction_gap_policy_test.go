package commands

import "testing"

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

// TestGapPolicyTruthTable pins the single spacing policy shared by the top-level
// and async block boundaries. Spacing regressions (missing/duplicate separator
// blanks, tool-chain denseness) are exactly the class of bug this locks down, so
// the whole truth table is enumerated rather than sampled.
func TestGapPolicyTruthTable(t *testing.T) {
	cases := []struct {
		promptWasVisible    bool
		promptAfterBlockGap bool
		completeBlockOutput bool
		want                blockGap
	}{
		{false, false, false, gapNone},
		{false, false, true, gapBlank},
		{false, true, false, gapBlank},
		{false, true, true, gapBlank},
		{true, false, false, gapNone},
		{true, false, true, gapNone},
		{true, true, false, gapBlank},
		{true, true, true, gapBlank},
	}
	for _, tc := range cases {
		c := &chatInteractionCoordinator{completeBlockOutput: tc.completeBlockOutput}

		got := c.gapBeforeBlockLocked(tc.promptWasVisible, tc.promptAfterBlockGap)
		if got != tc.want {
			t.Fatalf("gapBeforeBlockLocked(pv=%t,pab=%t,cbo=%t)=%s want %s",
				tc.promptWasVisible, tc.promptAfterBlockGap, tc.completeBlockOutput, gapName(got), gapName(tc.want))
		}

		// Top-level message boundary shares the core verbatim.
		if top := c.gapForTopLevelMessage(tc.promptWasVisible, tc.promptAfterBlockGap); top != tc.want {
			t.Fatalf("gapForTopLevelMessage(pv=%t,pab=%t,cbo=%t)=%s want %s",
				tc.promptWasVisible, tc.promptAfterBlockGap, tc.completeBlockOutput, gapName(top), gapName(tc.want))
		}

		// First async line (no prior async) matches the core gap policy.
		if async := c.gapForAsyncLine(tc.promptWasVisible, tc.promptAfterBlockGap); async != tc.want {
			t.Fatalf("gapForAsyncLine(pv=%t,pab=%t,cbo=%t,first)=%s want %s",
				tc.promptWasVisible, tc.promptAfterBlockGap, tc.completeBlockOutput, gapName(async), gapName(tc.want))
		}
		// Continuation async (tool chain) stays dense via lastCompletedAsyncLine.
		c.lastCompletedAsyncLine = true
		if chain := c.gapForAsyncLine(tc.promptWasVisible, tc.promptAfterBlockGap); chain != gapNone {
			t.Fatalf("gapForAsyncLine(pv=%t,pab=%t,cbo=%t,chain)=%s want gapNone",
				tc.promptWasVisible, tc.promptAfterBlockGap, tc.completeBlockOutput, gapName(chain))
		}
		c.lastCompletedAsyncLine = false
	}
}

// TestGapIfPriorComplete pins the reasoning-supplement spacing helper.
func TestGapIfPriorComplete(t *testing.T) {
	if got := (&chatInteractionCoordinator{completeBlockOutput: true}).gapIfPriorComplete(); got != gapBlank {
		t.Fatalf("gapIfPriorComplete(cbo=true)=%s want gapBlank", gapName(got))
	}
	if got := (&chatInteractionCoordinator{completeBlockOutput: false}).gapIfPriorComplete(); got != gapNone {
		t.Fatalf("gapIfPriorComplete(cbo=false)=%s want gapNone", gapName(got))
	}
}

// TestGapPolicyNilReceiverSafe guards the nil-coordinator fast paths the render
// helpers rely on.
func TestGapPolicyNilReceiverSafe(t *testing.T) {
	var c *chatInteractionCoordinator
	if got := c.gapBeforeBlockLocked(false, false); got != gapNone {
		t.Fatalf("nil gapBeforeBlockLocked=%s want gapNone", gapName(got))
	}
	if got := c.gapForAsyncLine(false, false); got != gapNone {
		t.Fatalf("nil gapForAsyncLine=%s want gapNone", gapName(got))
	}
}
