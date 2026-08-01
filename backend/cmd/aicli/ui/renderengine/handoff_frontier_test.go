package renderengine

import "testing"

func TestHandoffFrontierAdvancesMonotonicallyAndRebasesTrim(t *testing.T) {
	var frontier HandoffFrontier
	if !frontier.AdvanceTo(3, 5) {
		t.Fatal("initial handoff advance failed")
	}
	if frontier.AdvanceTo(2, 5) {
		t.Fatal("frontier moved backward")
	}
	frontier.TrimPrefix(2, 3)
	if got := frontier.Value(); got != 1 {
		t.Fatalf("frontier after trim = %d, want 1", got)
	}
	frontier.Clamp(0)
	if got := frontier.Value(); got != 0 {
		t.Fatalf("frontier after empty replacement = %d, want 0", got)
	}
}

func TestHandoffFrontierClampsMalformedInputs(t *testing.T) {
	var frontier HandoffFrontier
	frontier.AdvanceTo(20, 4)
	if got := frontier.Value(); got != 4 {
		t.Fatalf("frontier = %d, want clamped 4", got)
	}
	frontier.TrimPrefix(99, 3)
	if got := frontier.Value(); got != 0 {
		t.Fatalf("frontier after oversized trim = %d, want 0", got)
	}
}
