package ui

import "testing"

func TestTruncateFixedStatusLineFitsWidth(t *testing.T) {
	if got := truncateFixedStatusLine("Ready | model mimo", 80); got != "Ready | model mimo" {
		t.Fatalf("expected status line to remain unchanged, got %q", got)
	}
}

func TestTruncateFixedStatusLineAddsAsciiEllipsis(t *testing.T) {
	got := truncateFixedStatusLine("Ready | model mimo | provider anthropic", 16)
	if got != "Ready | model..." {
		t.Fatalf("unexpected truncated status line: %q", got)
	}
	if DisplayWidth(got) > 16 {
		t.Fatalf("expected truncated status line to fit width, got width=%d text=%q", DisplayWidth(got), got)
	}
}
