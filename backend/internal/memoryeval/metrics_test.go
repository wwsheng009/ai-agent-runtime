package memoryeval

import "testing"

func TestRecallAndMRR(t *testing.T) {
	ranked := []string{"a", "b", "c"}
	expected := []string{"c", "missing"}
	if got := RecallAtK(ranked, expected, 1); got != 0 {
		t.Fatalf("RecallAt1 = %v", got)
	}
	if got := RecallAtK(ranked, expected, 3); got != 0.5 {
		t.Fatalf("RecallAt3 = %v", got)
	}
	if got := ReciprocalRank(ranked, expected); got != 1.0/3.0 {
		t.Fatalf("MRR = %v", got)
	}
}

func TestPercentile(t *testing.T) {
	values := []float64{10, 20, 30, 40}
	if got := Percentile(values, 0.5); got != 20 {
		t.Fatalf("p50 = %v", got)
	}
	if got := Percentile(values, 0.95); got != 40 {
		t.Fatalf("p95 = %v", got)
	}
}
