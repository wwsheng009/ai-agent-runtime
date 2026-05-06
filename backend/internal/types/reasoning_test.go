package types

import "testing"

func TestReasoningBlock_RawDisplayTextPreservesWhitespace(t *testing.T) {
	block := &ReasoningBlock{
		Summary:    " user",
		Streamable: true,
	}

	if got := block.RawDisplayText(); got != " user" {
		t.Fatalf("expected raw display text to preserve whitespace, got %q", got)
	}
	if got := block.DisplayText(); got != "user" {
		t.Fatalf("expected trimmed display text for final rendering, got %q", got)
	}
}

func TestReasoningBlock_RoundTripPreservesStreamingWhitespace(t *testing.T) {
	block := &ReasoningBlock{
		Provider:   "nvidia",
		Format:     "stream_delta",
		Summary:    " user",
		Streamable: true,
		Visibility: ReasoningVisibilitySummary,
	}

	decoded := ReasoningBlockFromMap(block.ToMap())
	if decoded == nil {
		t.Fatal("expected decoded reasoning block")
	}
	if got := decoded.RawDisplayText(); got != " user" {
		t.Fatalf("expected raw display text after round trip, got %q", got)
	}
}

func TestReasoningBlock_OpaqueVisibilitySuppressesDisplayText(t *testing.T) {
	block := &ReasoningBlock{
		Summary:    "internal reasoning",
		Visibility: ReasoningVisibilityOpaque,
	}

	if got := block.RawDisplayText(); got != "" {
		t.Fatalf("expected opaque raw display text to be suppressed, got %q", got)
	}
	if got := block.DisplayText(); got != "" {
		t.Fatalf("expected opaque display text to be suppressed, got %q", got)
	}
}
