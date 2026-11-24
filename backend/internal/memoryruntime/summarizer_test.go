package memoryruntime

import "testing"

func TestSummaryModelMaxTokensLeavesReasoningHeadroom(t *testing.T) {
	tests := []struct {
		name   string
		budget int
		want   int
	}{
		{name: "zero budget", budget: 0, want: 2048},
		{name: "small summary budget", budget: 400, want: 2048},
		{name: "live acceptance budget", budget: 1100, want: 2048},
		{name: "large summary budget", budget: 5000, want: 4096},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summaryModelMaxTokens(tt.budget); got != tt.want {
				t.Fatalf("summaryModelMaxTokens(%d) = %d, want %d", tt.budget, got, tt.want)
			}
		})
	}
}
