package memoryruntime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestRecordTurnDigestRedactsAndTruncates(t *testing.T) {
	store, err := memorystore.New(memorystore.Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	source := &fakeSource{sessions: []SessionRef{{ID: "s1", WorkspacePath: "/workspace"}}}
	rt := New(store, source, nil, nil, Config{
		Mode: "shadow", ProjectKey: store.ProjectKey(), Workspace: "/workspace",
		TurnDigestEnabled: true, TurnDigestStoreText: true,
		TurnDigestMaxUserChars: 12, TurnDigestMaxAssistantChars: 12,
	})
	err = rt.RecordTurnDigest(context.Background(), chat.TurnDigest{
		SessionID: "s1", TurnID: "t1", TraceID: "trace",
		Prompt:          "api_key=abcdefghijklmnop",
		AssistantOutput: strings.Repeat("answer", 20),
		Outcome:         "success",
		Usage:           &types.TokenUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
	})
	require.NoError(t, err)
	digests, err := store.ListPendingTurnDigests(context.Background(), "s1", 10)
	require.NoError(t, err)
	require.Len(t, digests, 1)
	require.NotContains(t, digests[0].UserText, "abcdefghijklmnop")
	require.Contains(t, digests[0].AssistantText, "…")
	require.True(t, digests[0].TextTruncated)
	require.JSONEq(t, `{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}`, digests[0].UsageJSON)
	stats := rt.DigestStats()
	require.EqualValues(t, 1, stats.Written)
	require.Zero(t, stats.Skipped)
	require.Zero(t, stats.Failed)
	require.False(t, stats.LastWrittenAt.IsZero())
}

func TestRecordTurnDigestSkipsWhenGenerationDisabled(t *testing.T) {
	store, err := memorystore.New(memorystore.Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	rt := New(store, nil, nil, nil, Config{
		Mode: "shadow", ProjectKey: store.ProjectKey(),
		GenerationDisabled: true, TurnDigestEnabled: true, TurnDigestStoreText: true,
	})
	err = rt.RecordTurnDigest(context.Background(), chat.TurnDigest{
		SessionID: "s1", TurnID: "t1", Prompt: "question", AssistantOutput: "answer",
	})
	require.NoError(t, err)
	count, err := store.CountPendingTurnDigests(context.Background(), "")
	require.NoError(t, err)
	require.Zero(t, count)
	stats := rt.DigestStats()
	require.EqualValues(t, 1, stats.Skipped)
	require.Equal(t, "generation_disabled", stats.LastSkipReason)
}

func TestConfigNormalizesTurnDigestSchedulingDefaults(t *testing.T) {
	cfg := (Config{}).normalized()
	require.Equal(t, 50, cfg.TurnDigestExtractEveryNTurns)
	require.Equal(t, 30, cfg.TurnDigestIdleExtractMinutes)
	require.Equal(t, 30, cfg.TurnDigestWindowRetentionDays)
}

func TestRunPendingTurnDigestsProcessesEligibleSession(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	transcript := validTranscript("s1", "hash-a")
	source := &fakeSource{
		sessions: []SessionRef{{
			ID: "s1", SessionGeneration: 1, WorkspacePath: "/workspace", State: "idle",
			LastActivityAt: now.Add(-time.Hour), CanonicalCount: 2,
		}},
		transcripts: map[string]Transcript{"s1": transcript},
	}
	extractor := &fakeExtractor{result: ExtractionResult{Candidates: []Candidate{{
		Kind: "decision", Text: "remember the digest path", Confidence: 0.9,
		SourceMessageIDs: []string{"msg-2"},
	}}}}
	runtime, store := newRuntimeForTest(t, "shadow", source, extractor)
	runtime.Config.TurnDigestEnabled = true
	_, err := store.RecordTurnDigest(context.Background(), memorystore.TurnDigest{
		SessionID: "s1", SessionGeneration: 1, TurnID: "t1", Outcome: "success",
		AssistantText: "remember the digest path",
	})
	require.NoError(t, err)

	result, err := runtime.RunPendingTurnDigests(context.Background(), TriggerSessionEnd)
	require.NoError(t, err)
	require.Equal(t, 1, result.Processed)
	require.Equal(t, 1, extractor.calls)

	pending, err := store.ListPendingTurnDigests(context.Background(), "s1", 10)
	require.NoError(t, err)
	require.Empty(t, pending)
	doneSessions, err := store.ListPendingTurnDigestSessions(context.Background(), "", 10)
	require.NoError(t, err)
	require.Empty(t, doneSessions)
}

func TestRunPendingTurnDigestsStartupSkipsActiveSession(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	transcript := validTranscript("s1", "hash-a")
	source := &fakeSource{
		sessions: []SessionRef{{
			ID: "s1", SessionGeneration: 1, WorkspacePath: "/workspace", State: "active",
			LastActivityAt: now.Add(-time.Hour), CanonicalCount: 2,
		}},
		transcripts: map[string]Transcript{"s1": transcript},
	}
	extractor := &fakeExtractor{result: ExtractionResult{Candidates: []Candidate{{
		Kind: "decision", Text: "startup must not extract a live session", Confidence: 0.9,
		SourceMessageIDs: []string{"msg-2"},
	}}}}
	runtime, store := newRuntimeForTest(t, "shadow", source, extractor)
	runtime.Config.TurnDigestEnabled = true
	_, err := store.RecordTurnDigest(context.Background(), memorystore.TurnDigest{
		SessionID: "s1", SessionGeneration: 1, TurnID: "t1", Outcome: "success",
		AssistantText: "startup must not extract a live session",
	})
	require.NoError(t, err)

	result, err := runtime.RunPendingTurnDigests(context.Background(), TriggerStartup)
	require.NoError(t, err)
	require.Zero(t, result.Processed)
	require.Zero(t, extractor.calls)
	recovered, err := store.RuntimeMetric(context.Background(), memorystore.MetricRecoveredDigestSessions)
	require.NoError(t, err)
	require.Zero(t, recovered)
	pending, err := store.ListPendingTurnDigests(context.Background(), "s1", 10)
	require.NoError(t, err)
	require.Len(t, pending, 1, "startup skip must leave the outbox pending for idle/session-end")

	// The explicit session-end path still owns an active session.
	result, err = runtime.RunPendingTurnDigestsForSession(context.Background(), TriggerSessionEnd, "s1")
	require.NoError(t, err)
	require.Equal(t, 1, result.Processed)
	require.Equal(t, 1, extractor.calls)
	recovered, err = store.RuntimeMetric(context.Background(), memorystore.MetricRecoveredDigestSessions)
	require.NoError(t, err)
	require.Zero(t, recovered, "explicit session-end is not startup/idle recovery")
}

func TestRunPendingTurnDigestsStartupProcessesTerminalSession(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{
		sessions: []SessionRef{{
			ID: "s1", SessionGeneration: 1, WorkspacePath: "/workspace", State: "closed",
			LastActivityAt: now.Add(-time.Hour), CanonicalCount: 2,
		}},
		transcripts: map[string]Transcript{"s1": validTranscript("s1", "hash-a")},
	}
	extractor := &fakeExtractor{result: ExtractionResult{Candidates: []Candidate{{
		Kind: "decision", Text: "recover a closed session", Confidence: 0.9,
		SourceMessageIDs: []string{"msg-2"},
	}}}}
	runtime, store := newRuntimeForTest(t, "shadow", source, extractor)
	runtime.Config.TurnDigestEnabled = true
	_, err := store.RecordTurnDigest(context.Background(), memorystore.TurnDigest{
		SessionID: "s1", SessionGeneration: 1, TurnID: "t1", Outcome: "success",
		AssistantText: "recover a closed session",
	})
	require.NoError(t, err)

	result, err := runtime.RunPendingTurnDigests(context.Background(), TriggerStartup)
	require.NoError(t, err)
	require.Equal(t, 1, result.Processed)
	require.Equal(t, 1, extractor.calls)
	recovered, err := store.RuntimeMetric(context.Background(), memorystore.MetricRecoveredDigestSessions)
	require.NoError(t, err)
	require.EqualValues(t, 1, recovered)
}

func TestRunPendingTurnDigestsStartupSkipsIdleSessionUntilIdleSweep(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{
		sessions: []SessionRef{{
			ID: "s1", SessionGeneration: 1, WorkspacePath: "/workspace", State: "idle",
			LastActivityAt: now.Add(-2 * time.Hour), CanonicalCount: 2,
		}},
		transcripts: map[string]Transcript{"s1": validTranscript("s1", "hash-a")},
	}
	extractor := &fakeExtractor{result: ExtractionResult{Candidates: []Candidate{{
		Kind: "decision", Text: "idle sweep owns the idle session", Confidence: 0.9,
		SourceMessageIDs: []string{"msg-2"},
	}}}}
	runtime, store := newRuntimeForTest(t, "shadow", source, extractor)
	runtime.Config.TurnDigestEnabled = true
	_, err := store.RecordTurnDigest(context.Background(), memorystore.TurnDigest{
		SessionID: "s1", SessionGeneration: 1, TurnID: "t1", Outcome: "success",
		AssistantText: "idle sweep owns the idle session",
	})
	require.NoError(t, err)

	result, err := runtime.RunPendingTurnDigests(context.Background(), TriggerStartup)
	require.NoError(t, err)
	require.Zero(t, result.Processed)
	require.Zero(t, extractor.calls)

	result, err = runtime.RunPendingTurnDigests(context.Background(), TriggerIdle)
	require.NoError(t, err)
	require.Equal(t, 1, result.Processed)
	require.Equal(t, 1, extractor.calls)
}

func TestRunPendingTurnDigestsDefersOversizedSession(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{
		sessions: []SessionRef{{
			ID: "s1", SessionGeneration: 1, WorkspacePath: "/workspace", State: "idle",
			LastActivityAt: now.Add(-time.Hour), CanonicalCount: 21,
		}},
	}
	extractor := &fakeExtractor{}
	runtime, store := newRuntimeForTest(t, "shadow", source, extractor)
	runtime.Config.TurnDigestEnabled = true
	_, err := store.RecordTurnDigest(context.Background(), memorystore.TurnDigest{
		SessionID: "s1", SessionGeneration: 1, TurnID: "t1", Outcome: "success",
		AssistantText: "too large for the current reader",
	})
	require.NoError(t, err)

	result, err := runtime.RunPendingTurnDigests(context.Background(), TriggerSessionEnd)
	require.NoError(t, err)
	require.Zero(t, result.Processed)
	require.Zero(t, extractor.calls)
	pending, err := store.ListPendingTurnDigests(context.Background(), "s1", 10)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestRunPendingTurnDigestsSkipsNonSuccessTurns(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{
		sessions: []SessionRef{{
			ID: "s1", SessionGeneration: 1, WorkspacePath: "/workspace", State: "idle",
			LastActivityAt: now.Add(-time.Hour), CanonicalCount: 2,
		}},
	}
	extractor := &fakeExtractor{}
	runtime, store := newRuntimeForTest(t, "shadow", source, extractor)
	runtime.Config.TurnDigestEnabled = true
	_, err := store.RecordTurnDigest(context.Background(), memorystore.TurnDigest{
		SessionID: "s1", SessionGeneration: 1, TurnID: "t1", Outcome: "interrupted",
		AssistantText: "partial output",
	})
	require.NoError(t, err)

	result, err := runtime.RunPendingTurnDigests(context.Background(), TriggerSessionEnd)
	require.NoError(t, err)
	require.Zero(t, result.Processed)
	require.Zero(t, extractor.calls)
	pending, err := store.ListPendingTurnDigests(context.Background(), "s1", 10)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestRunPendingTurnDigestsSkipsIneligibleSubagent(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{
		sessions: []SessionRef{{
			ID: "s1", SessionGeneration: 1, WorkspacePath: "/workspace", State: "idle",
			IsSubagent: true, LastActivityAt: now.Add(-time.Hour), CanonicalCount: 2,
		}},
	}
	extractor := &fakeExtractor{}
	runtime, store := newRuntimeForTest(t, "shadow", source, extractor)
	runtime.Config.TurnDigestEnabled = true
	_, err := store.RecordTurnDigest(context.Background(), memorystore.TurnDigest{
		SessionID: "s1", SessionGeneration: 1, TurnID: "t1", Outcome: "success",
	})
	require.NoError(t, err)

	result, err := runtime.RunPendingTurnDigests(context.Background(), TriggerSessionEnd)
	require.NoError(t, err)
	require.Zero(t, result.Processed)
	require.Zero(t, extractor.calls)
	pending, err := store.ListPendingTurnDigests(context.Background(), "s1", 10)
	require.NoError(t, err)
	require.Empty(t, pending)
}

type fakeWindowSource struct {
	*fakeSource
	window      Transcript
	windowCalls int
}

func (s *fakeWindowSource) ReadTranscriptWindow(_ context.Context, _ SessionRef, start, end, limit int) (Transcript, error) {
	s.windowCalls++
	window := s.window
	sourceMessages := window.Messages
	window.Messages = nil
	for _, message := range sourceMessages {
		if message.Seq >= start && message.Seq <= end && len(window.Messages) < limit {
			window.Messages = append(window.Messages, message)
		}
	}
	window.Hash = TranscriptHash(window.Messages)
	return window, nil
}

func TestRunPendingTurnDigestsUsesCanonicalWindowForOversizedSession(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	base := &fakeSource{
		sessions: []SessionRef{{
			ID: "s1", SessionGeneration: 1, WorkspacePath: "/workspace", State: "idle",
			LastActivityAt: now.Add(-time.Hour), CanonicalCount: 21,
		}},
	}
	window := Transcript{
		SessionID: "s1", Revision: 4, Hash: "window-hash",
		Messages: []TranscriptMessage{
			{ID: "msg-user", Seq: 3, Role: "user", Content: "large session question"},
			{ID: "msg-assistant", Seq: 4, Role: "assistant", Content: "large session answer"},
		},
	}
	source := &fakeWindowSource{fakeSource: base, window: window}
	extractor := &fakeExtractor{result: ExtractionResult{Candidates: []Candidate{{
		Kind: "decision", Text: "large session memory", Confidence: 0.9,
		SourceMessageIDs: []string{"msg-assistant"},
	}}}}
	runtime, store := newRuntimeForTest(t, "shadow", base, extractor)
	runtime.Source = source
	runtime.Config.TurnDigestEnabled = true
	_, err := store.RecordTurnDigest(context.Background(), memorystore.TurnDigest{
		SessionID: "s1", SessionGeneration: 1, TurnID: "previous", Outcome: "success",
		MessageSeqEnd: 2, Status: memorystore.TurnDigestDone,
		RollupStatus: memorystore.TurnDigestRollupSkipped,
	})
	require.NoError(t, err)
	_, err = store.RecordTurnDigest(context.Background(), memorystore.TurnDigest{
		SessionID: "s1", SessionGeneration: 1, TurnID: "t1", Outcome: "success",
		AssistantText: "large session answer", MessageSeqEnd: 4,
	})
	require.NoError(t, err)

	result, err := runtime.RunPendingTurnDigests(context.Background(), TriggerSessionEnd)
	require.NoError(t, err)
	require.Empty(t, result.Errors)
	require.Equal(t, 1, result.Processed)
	require.Equal(t, 1, source.windowCalls)
	require.Equal(t, 1, extractor.calls)
	pending, err := store.ListPendingTurnDigests(context.Background(), "s1", 10)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestRunSessionEndFallsBackWhenTurnDigestDisabled(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	transcript := validTranscript("s1", "hash-a")
	source := &fakeSource{
		sessions: []SessionRef{{
			ID: "s1", WorkspacePath: "/workspace", State: "idle",
			LastActivityAt: now.Add(-time.Hour), CanonicalCount: 2,
		}},
		transcripts: map[string]Transcript{"s1": transcript},
	}
	extractor := &fakeExtractor{result: ExtractionResult{Candidates: []Candidate{{
		Kind: "decision", Text: "fallback extraction", Confidence: 0.9,
		SourceMessageIDs: []string{"msg-2"},
	}}}}
	runtime, _ := newRuntimeForTest(t, "shadow", source, extractor)
	runtime.Config.TurnDigestEnabled = false

	result, err := runtime.RunSessionEnd(context.Background(), "s1")
	require.NoError(t, err)
	require.Equal(t, 1, result.Processed)
	require.Equal(t, 1, extractor.calls)
}
