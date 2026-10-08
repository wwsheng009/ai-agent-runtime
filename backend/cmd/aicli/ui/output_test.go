package ui

import (
	"testing"
)

func TestTruncateVisibleUsesCellWidth(t *testing.T) {
	got := TruncateVisible("你好世界", 5, "..")
	if DisplayWidth(got) > 5 {
		t.Fatalf("visible width %d > 5 for %q", DisplayWidth(got), got)
	}
}
