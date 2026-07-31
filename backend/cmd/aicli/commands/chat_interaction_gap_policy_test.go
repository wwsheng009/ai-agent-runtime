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

// TestGapPolicyTruthTable pins the cell-boundary spacing rule. Prompt visibility
// and old async-chain state no longer participate: viewport-only Running rows
// are not history cells, while every retained event owns its own boundary.
func TestGapPolicyTruthTable(t *testing.T) {
	cases := []struct {
		completeBlockOutput bool
		want                blockGap
	}{
		{false, gapNone},
		{true, gapBlank},
	}
	for _, tc := range cases {
		c := &chatInteractionCoordinator{completeBlockOutput: tc.completeBlockOutput}

		got := c.gapBeforeBlockLocked()
		if got != tc.want {
			t.Fatalf("gapBeforeBlockLocked(cbo=%t)=%s want %s",
				tc.completeBlockOutput, gapName(got), gapName(tc.want))
		}

		// Top-level message boundary shares the core verbatim.
		if top := c.gapForTopLevelMessage(); top != tc.want {
			t.Fatalf("gapForTopLevelMessage(cbo=%t)=%s want %s",
				tc.completeBlockOutput, gapName(top), gapName(tc.want))
		}

		if event := c.gapForEventBlock(); event != tc.want {
			t.Fatalf("gapForEventBlock(cbo=%t)=%s want %s",
				tc.completeBlockOutput, gapName(event), gapName(tc.want))
		}
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
	if got := c.gapBeforeBlockLocked(); got != gapNone {
		t.Fatalf("nil gapBeforeBlockLocked=%s want gapNone", gapName(got))
	}
	if got := c.gapForEventBlock(); got != gapNone {
		t.Fatalf("nil gapForEventBlock=%s want gapNone", gapName(got))
	}
}
