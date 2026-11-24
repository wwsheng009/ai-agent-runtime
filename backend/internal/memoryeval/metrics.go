package memoryeval

import "math"

// RecallAtK is the fraction of expected records found in the first K ranks.
func RecallAtK(ranked, expected []string, k int) float64 {
	if len(expected) == 0 || k <= 0 {
		return 0
	}
	limit := k
	if limit > len(ranked) {
		limit = len(ranked)
	}
	found := make(map[string]struct{}, limit)
	for _, id := range ranked[:limit] {
		found[id] = struct{}{}
	}
	hits := 0
	for _, id := range expected {
		if _, ok := found[id]; ok {
			hits++
		}
	}
	return float64(hits) / float64(len(expected))
}

// ReciprocalRank returns 1/rank for the first expected result, or zero.
func ReciprocalRank(ranked, expected []string) float64 {
	wanted := make(map[string]struct{}, len(expected))
	for _, id := range expected {
		wanted[id] = struct{}{}
	}
	for index, id := range ranked {
		if _, ok := wanted[id]; ok {
			return 1 / float64(index+1)
		}
	}
	return 0
}

func Mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

func Percentile(values []float64, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if percentile <= 0 {
		return 0
	}
	if percentile >= 1 {
		percentile = 1
	}
	sorted := append([]float64(nil), values...)
	sortFloat64s(sorted)
	index := int(math.Ceil(percentile*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func sortFloat64s(values []float64) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
