package types

import "testing"

func TestTokenUsageAddTracksUsageSourceQuality(t *testing.T) {
	total := &TokenUsage{}
	total.Add(&TokenUsage{TotalTokens: 10, UsageSource: "provider_reported"})
	if total.UsageSource != "provider_reported" {
		t.Fatalf("expected provider source, got %q", total.UsageSource)
	}
	total.Add(&TokenUsage{TotalTokens: 5, UsageSource: "local_estimate"})
	if total.UsageSource != "mixed" {
		t.Fatalf("expected mixed source, got %q", total.UsageSource)
	}
	if clone := total.Clone(); clone == nil || clone.UsageSource != "mixed" {
		t.Fatalf("clone lost usage source: %#v", clone)
	}
}
