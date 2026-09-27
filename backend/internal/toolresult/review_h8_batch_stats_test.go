package toolresult

import "testing"

// TestExtractBatchStatsDoesNotInventSuccessesForAllFailed pins finding H8: a
// batch that reports its true failure total (with the printed rows bounded via
// failures_omitted) must not be turned into "requested - failed" successes by
// the inference below.
func TestExtractBatchStatsDoesNotInventSuccessesForAllFailed(t *testing.T) {
	metadata := map[string]interface{}{
		"batch":                true,
		"request_count":        60,
		"failed_count":         60,
		"failures_omitted":     40,
		"failed_items_omitted": 40,
	}
	stats := ExtractBatchStats(metadata)
	if stats.Requested != 60 || stats.Failed != 60 {
		t.Fatalf("expected requested=60 failed=60, got %+v", stats)
	}
	if stats.Succeeded != 0 {
		t.Fatalf("an all-failed batch must not infer successes, got %+v", stats)
	}
	if stats.Partial {
		t.Fatalf("an all-failed batch is not partial, got %+v", stats)
	}
}

// TestExtractBatchStatsKeepsPartialAccounting: with explicit success counts and
// a bounded failure list, omitted rows still add to Failed without inventing or
// losing successes.
func TestExtractBatchStatsKeepsPartialAccounting(t *testing.T) {
	metadata := map[string]interface{}{
		"batch":            true,
		"request_count":    100,
		"failed_count":     60,
		"failures_omitted": 40,
		"succeeded_count":  40,
		"partial_failure":  true,
	}
	stats := ExtractBatchStats(metadata)
	if stats.Failed != 60 || stats.Succeeded != 40 || stats.Requested != 100 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if !stats.Partial {
		t.Fatalf("expected partial, got %+v", stats)
	}
}
