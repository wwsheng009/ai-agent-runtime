package contextledger

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// testFingerprinter mirrors the production prefix semantics: the fingerprint
// of a message list changes when any element changes.
func testFingerprinter(messages []types.Message) string {
	var builder strings.Builder
	for _, message := range messages {
		builder.WriteString(message.Role)
		builder.WriteByte(':')
		builder.WriteString(message.Content)
		builder.WriteByte('\n')
	}
	return builder.String()
}

func messagesOf(contents ...string) []types.Message {
	messages := make([]types.Message, 0, len(contents))
	for index, content := range contents {
		role := "user"
		if index%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, types.Message{Role: role, Content: content})
	}
	return messages
}

func validObservation(messages []types.Message, promptTokens, messageEstimate int) Observation {
	return Observation{
		PromptTokens:    promptTokens,
		MessageEstimate: messageEstimate,
		Fingerprint:     testFingerprinter(messages),
		MessageCount:    len(messages),
		ToolFingerprint: "tools-v1",
		Epoch:           0,
		Provider:        "commandgo",
		Model:           "ling-3.1-flash",
		Source:          SourceProviderReported,
	}
}

func TestProjectWithoutAnchorFallsBackToEstimate(t *testing.T) {
	ledger := New(testFingerprinter)
	messages := messagesOf("hello")
	projection := ledger.Project(ProjectInput{
		Messages:        messages,
		MessageEstimate: 120,
		ToolTokens:     30,
		ToolFingerprint: "tools-v1",
	})
	if projection.Source != ProjectionEstimated {
		t.Fatalf("expected estimated source, got %q", projection.Source)
	}
	if projection.InvalidReason != "no_anchor" {
		t.Fatalf("expected no_anchor, got %q", projection.InvalidReason)
	}
	if projection.Tokens != 150 {
		t.Fatalf("expected estimate 150, got %d", projection.Tokens)
	}
}

// TestProjectAnchorCorrectsEstimatorUndercount pins the 2026-10-11 incident
// shape: the local estimate read far below the provider's count (the send gate
// estimated 223,594 while the provider counted 262,948 at the failure step),
// and the anchor must correct the absolute value.
func TestProjectAnchorCorrectsEstimatorUndercount(t *testing.T) {
	ledger := New(testFingerprinter)
	anchorMessages := messagesOf(strings.Repeat("code json tool output ", 100))
	obs := validObservation(anchorMessages, 261559, 170441)
	if !ledger.Observe(obs) {
		t.Fatal("observation must be accepted")
	}

	// The prompt grows by a small tool result; the estimator reads the growth
	// at its own scale and the anchor supplies the absolute calibration.
	now := append(append([]types.Message{}, anchorMessages...), types.Message{Role: "tool", Content: "result"})
	projection := ledger.Project(ProjectInput{
		Messages:        now,
		MessageEstimate: 170441 + 40,
		ToolTokens:      21463,
		ToolFingerprint: "tools-v1",
		Provider:        "commandgo",
		Model:           "ling-3.1-flash",
	})
	if projection.Source != ProjectionAnchored {
		t.Fatalf("expected anchored source, got %q (%s)", projection.Source, projection.InvalidReason)
	}
	if projection.Tokens != 261599 {
		t.Fatalf("expected 261559+40=261599, got %d", projection.Tokens)
	}
	if projection.AnchorTokens != 261559 || projection.DeltaTokens != 40 {
		t.Fatalf("unexpected anchor/delta: %+v", projection)
	}
}

func TestProjectNeverDropsBelowEstimateFloor(t *testing.T) {
	ledger := New(testFingerprinter)
	anchorMessages := messagesOf("a")
	ledger.Observe(validObservation(anchorMessages, 1000, 100))

	// Anchor + delta (1000 + 200 = 1200) sits below the full estimate floor
	// (300 + 2000 = 2300): the conservative floor must win.
	now := append(append([]types.Message{}, anchorMessages...), types.Message{Role: "tool", Content: "x"})
	projection := ledger.Project(ProjectInput{
		Messages:        now,
		MessageEstimate: 300,
		ToolTokens:      2000,
		ToolFingerprint: "tools-v1",
	})
	if projection.Tokens != 2300 {
		t.Fatalf("expected floor at estimate 2300, got %d", projection.Tokens)
	}

	// When anchor + delta exceeds the estimate floor, the anchored value is
	// the more accurate one and must win.
	projection = ledger.Project(ProjectInput{
		Messages:        now,
		MessageEstimate: 5000,
		ToolTokens:      100,
		ToolFingerprint: "tools-v1",
	})
	if projection.Tokens != 5900 {
		t.Fatalf("expected anchored 5900, got %d", projection.Tokens)
	}
}

func TestProjectClampsNegativeDelta(t *testing.T) {
	ledger := New(testFingerprinter)
	anchorMessages := messagesOf("a", "b")
	ledger.Observe(validObservation(anchorMessages, 500, 200))

	now := append(append([]types.Message{}, anchorMessages...), types.Message{Role: "tool", Content: "x"})
	projection := ledger.Project(ProjectInput{
		Messages:        now,
		MessageEstimate: 150, // estimate shrank; delta must clamp to 0
		ToolTokens:      0,
		ToolFingerprint: "tools-v1",
	})
	if projection.DeltaTokens != 0 || projection.Tokens != 500 {
		t.Fatalf("expected clamp to anchor 500, got %+v", projection)
	}
}

func TestProjectInvalidationMatrix(t *testing.T) {
	base := messagesOf("one", "two")
	cases := []struct {
		name   string
		mutate func(obs *Observation, in *ProjectInput)
		reason string
	}{
		{
			name:   "provider changed",
			mutate: func(_ *Observation, in *ProjectInput) { in.Provider = "other" },
			reason: "provider_changed",
		},
		{
			name:   "model changed",
			mutate: func(_ *Observation, in *ProjectInput) { in.Model = "other" },
			reason: "model_changed",
		},
		{
			name:   "epoch changed",
			mutate: func(_ *Observation, in *ProjectInput) { in.Epoch = 3 },
			reason: "epoch_changed",
		},
		{
			name:   "tool surface changed",
			mutate: func(_ *Observation, in *ProjectInput) { in.ToolFingerprint = "tools-v2" },
			reason: "tool_surface_changed",
		},
		{
			name: "history shrank",
			mutate: func(_ *Observation, in *ProjectInput) {
				in.Messages = in.Messages[:1]
			},
			reason: "history_shrank",
		},
		{
			name: "prefix rewritten",
			mutate: func(_ *Observation, in *ProjectInput) {
				in.Messages = messagesOf("REWRITTEN", "two", "three")
			},
			reason: "prefix_changed",
		},
		{
			name: "anchor anomaly",
			mutate: func(obs *Observation, in *ProjectInput) {
				obs.PromptTokens = 100000
				in.MessageEstimate = 100
				in.ToolTokens = 0
			},
			reason: "anchor_anomaly",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ledger := New(testFingerprinter)
			obs := validObservation(base, 5000, 400)
			ledger.Observe(obs)
			in := ProjectInput{
				Messages:        append(append([]types.Message{}, base...), types.Message{Role: "tool", Content: "x"}),
				MessageEstimate: 420,
				ToolTokens:      50,
				ToolFingerprint: "tools-v1",
				Provider:        "commandgo",
				Model:           "ling-3.1-flash",
			}
			testCase.mutate(&obs, &in)
			// Re-apply the (possibly mutated) observation; mutation of obs
			// alone must also be visible.
			ledger.Observe(obs)

			projection := ledger.Project(in)
			if projection.Source != ProjectionEstimated {
				t.Fatalf("expected invalidation, got anchored projection %+v", projection)
			}
			if projection.InvalidReason != testCase.reason {
				t.Fatalf("expected reason %q, got %q", testCase.reason, projection.InvalidReason)
			}
			if projection.Tokens != in.MessageEstimate+in.ToolTokens {
				t.Fatalf("expected estimate fallback %d, got %d", in.MessageEstimate+in.ToolTokens, projection.Tokens)
			}
		})
	}
}

func TestObserveRejectsNonProviderSourcesAndMissingIdentity(t *testing.T) {
	ledger := New(testFingerprinter)
	messages := messagesOf("hello")

	if ledger.Observe(Observation{
		PromptTokens: 100, MessageEstimate: 10, Fingerprint: testFingerprinter(messages),
		MessageCount: 1, Source: "local_estimate",
	}) {
		t.Fatal("local_estimate must not become an anchor")
	}
	if ledger.Observe(Observation{Source: SourceProviderReported, MessageCount: 1, Fingerprint: "fp"}) {
		t.Fatal("zero prompt tokens must be rejected")
	}
	if ledger.Observe(Observation{Source: SourceProviderReported, PromptTokens: 100}) {
		t.Fatal("missing prompt identity must be rejected")
	}
	if _, ok := ledger.Snapshot(); ok {
		t.Fatal("ledger must remain empty after rejected observations")
	}
}

// TestProjectSkipsToolCheckWhenSurfaceUnknown documents the pre-turn case:
// before the turn's tool surface is frozen the caller cannot identify it, so
// the tool check is skipped while prefix/epoch identity still validates.
func TestProjectSkipsToolCheckWhenSurfaceUnknown(t *testing.T) {
	ledger := New(testFingerprinter)
	base := messagesOf("one", "two")
	ledger.Observe(validObservation(base, 5000, 400))

	projection := ledger.Project(ProjectInput{
		Messages:        append(append([]types.Message{}, base...), types.Message{Role: "tool", Content: "x"}),
		MessageEstimate: 420,
		ToolTokens:      50,
		ToolFingerprint: "",
		Provider:        "commandgo",
		Model:           "ling-3.1-flash",
	})
	if projection.Source != ProjectionAnchored {
		t.Fatalf("empty tool fingerprint must skip the tool check, got %+v", projection)
	}
}

func TestProjectNilLedgerAndNilFingerprinterFallBack(t *testing.T) {
	var nilLedger *Ledger
	projection := nilLedger.Project(ProjectInput{MessageEstimate: 42, ToolTokens: 8})
	if projection.Source != ProjectionEstimated || projection.Tokens != 50 {
		t.Fatalf("nil ledger must estimate, got %+v", projection)
	}

	ledger := New(nil)
	messages := messagesOf("hello")
	ledger.Observe(validObservation(messages, 900, 100))
	projection = ledger.Project(ProjectInput{
		Messages:        messages,
		MessageEstimate: 100,
		ToolTokens:      0,
		ToolFingerprint: "tools-v1",
	})
	if projection.Source != ProjectionEstimated || projection.InvalidReason != "fingerprint_unavailable" {
		t.Fatalf("nil fingerprinter must fall back, got %+v", projection)
	}
}
