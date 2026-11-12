package memoryruntime

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestValidateCandidatesHonorsRedactionPolicy(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	extraction := ExtractionResult{Candidates: []Candidate{{
		Kind: "fact", Text: "api_key=supersecretvalue123", Confidence: 0.9,
		SourceMessageIDs: []string{"msg-2"},
	}}}

	runtime, _ := newRuntimeForTest(t, "shadow", &fakeSource{}, &fakeExtractor{})
	runtime.Config.RedactionConfigured = true
	runtime.Config.RedactionEnabled = true
	runtime.Config.RedactionDropOnSecret = true
	candidates, reasons := runtime.validateCandidates(extraction, validTranscript("s1", "hash-a"), now)
	require.Empty(t, candidates)
	require.Contains(t, strings.Join(reasons, ","), "secret:")

	runtime.Config.RedactionDropOnSecret = false
	candidates, reasons = runtime.validateCandidates(extraction, validTranscript("s1", "hash-a"), now)
	require.Empty(t, reasons)
	require.Len(t, candidates, 1)
	require.Contains(t, candidates[0].Text, "REDACTED")
	require.NotContains(t, candidates[0].Text, "supersecretvalue123")
}

func TestValidateCandidatesUsesConfiguredClassAndTTL(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	runtime, _ := newRuntimeForTest(t, "shadow", &fakeSource{}, &fakeExtractor{})
	runtime.Config.RetentionClasses = memorystore.RetentionClasses{
		Permanent: []string{"fact"},
		Ephemeral: []string{"workflow"},
	}
	runtime.Config.FreshnessPerKind = map[string]time.Duration{
		"fact":     7 * 24 * time.Hour,
		"workflow": 2 * 24 * time.Hour,
	}
	runtime.Config.FreshnessDefaultTTL = 30 * 24 * time.Hour

	candidates, reasons := runtime.validateCandidates(ExtractionResult{Candidates: []Candidate{
		{Kind: "fact", Text: "configured permanent fact", Confidence: 0.9, SourceMessageIDs: []string{"msg-2"}},
		{Kind: "workflow", Text: "configured ephemeral workflow", Confidence: 0.9, SourceMessageIDs: []string{"msg-2"}},
	}}, validTranscript("s1", "hash-a"), now)
	require.Empty(t, reasons)
	require.Len(t, candidates, 2)

	byKind := make(map[string]memorystore.Candidate, len(candidates))
	for _, candidate := range candidates {
		byKind[candidate.Kind] = candidate
	}
	require.Equal(t, "permanent", byKind["fact"].RetentionClass)
	require.Equal(t, "ephemeral", byKind["workflow"].RetentionClass)
	require.WithinDuration(t, now.Add(7*24*time.Hour), byKind["fact"].RevalidateAfter, time.Second)
	require.WithinDuration(t, now.Add(2*24*time.Hour), byKind["workflow"].RevalidateAfter, time.Second)

	runtime.Config.FreshnessDisabled = true
	candidates, reasons = runtime.validateCandidates(ExtractionResult{Candidates: []Candidate{
		{Kind: "fact", Text: "disabled freshness", Confidence: 0.9, SourceMessageIDs: []string{"msg-2"}},
	}}, validTranscript("s1", "hash-a"), now)
	require.Empty(t, reasons)
	require.Len(t, candidates, 1)
	require.True(t, candidates[0].RevalidateAfter.IsZero())
}
