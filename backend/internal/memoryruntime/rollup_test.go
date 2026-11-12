package memoryruntime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

type fakeWindowSummarizer struct {
	result WindowSummary
	err    error
	calls  int
	inputs []WindowSummaryInput
}

func (s *fakeWindowSummarizer) SummarizeWindow(_ context.Context, input WindowSummaryInput) (WindowSummary, error) {
	s.calls++
	s.inputs = append(s.inputs, input)
	return s.result, s.err
}

func TestRunSessionEndRollsUpL1AndL2(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	transcript := validTranscript("s1", "hash-a")
	source := &fakeSource{
		sessions: []SessionRef{{
			ID: "s1", WorkspacePath: "/workspace", State: "closed",
			LastActivityAt: now.Add(-time.Hour), CanonicalCount: 2,
		}},
		transcripts: map[string]Transcript{"s1": transcript},
	}
	extractor := &fakeExtractor{result: ExtractionResult{Candidates: []Candidate{{
		Kind: "decision", Text: "use append-only memory writes", Confidence: 0.9,
		SourceMessageIDs: []string{"msg-2"},
	}}}}
	runtime, store := newRuntimeForTest(t, "shadow", source, extractor)
	runtime.Config.TurnDigestEnabled = true
	runtime.Config.TurnDigestWindowSummaryEnabled = true
	runtime.Config.TurnDigestWindowTurns = 2
	runtime.Config.SummaryTokenBudget = 400
	summarizer := &fakeWindowSummarizer{result: WindowSummary{
		Text:  "Window summary: the runtime uses append-only memory writes.",
		Model: "fake-window-model",
		Usage: TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}}
	runtime.WindowSummarizer = summarizer

	for index, turnID := range []string{"t1", "t2"} {
		_, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
			SessionID: "s1", TurnID: turnID, Outcome: "success",
			UserMessageID: "msg-1", AssistantMessageID: "msg-2",
			UserText: "question " + turnID, AssistantText: "answer " + turnID,
			MessageSeqEnd: int64((index + 1) * 2),
		})
		require.NoError(t, err)
	}

	result, err := runtime.RunSessionEnd(ctx, "s1")
	require.NoError(t, err)
	require.Equal(t, 1, result.Processed)
	require.Equal(t, 1, result.RolledUp)
	require.Zero(t, result.RollupFailed)
	require.Equal(t, 1, summarizer.calls)
	require.Len(t, summarizer.inputs[0].Digests, 2)

	second, err := runtime.RunSessionEnd(ctx, "s1")
	require.NoError(t, err)
	require.Empty(t, second.Errors)
	require.Zero(t, second.RolledUp)
	require.Equal(t, 1, summarizer.calls, "repeated session-end must not re-summarize a done window")

	windows, err := store.ListTurnWindows(ctx, "s1", 10)
	require.NoError(t, err)
	require.Len(t, windows, 1)
	require.Equal(t, memorystore.TurnWindowDone, windows[0].Status)
	require.Equal(t, "Window summary: the runtime uses append-only memory writes.", windows[0].SummaryText)
	require.EqualValues(t, 1, windows[0].SummaryRevision)
	require.NotEmpty(t, windows[0].SourceHash)

	ready, err := store.ListTurnDigestsForRollup(ctx, "s1", 10)
	require.NoError(t, err)
	require.Empty(t, ready, "L1 completion must advance the L0 rollup watermark")

	status, err := store.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, status.TurnWindows["done"])
	require.Equal(t, 2, status.TurnDigests["done"])

	page, err := store.ListRecords(ctx, memorystore.RecordFilter{Status: memorystore.StatusCandidate, Limit: 50})
	require.NoError(t, err)
	var windowRecord memorystore.Record
	for _, record := range page.Items {
		if record.SourceKind == "turn_window" {
			windowRecord = record
			break
		}
	}
	require.NotEmpty(t, windowRecord.ID)
	require.Equal(t, memorystore.StatusCandidate, windowRecord.Status)
	require.Contains(t, strings.Join(windowRecord.SourceRefs, ","), "turn_window:")
	require.Contains(t, windowRecord.Text, "append-only memory writes")

	consolidator := NewConsolidator(store, runtime.Config)
	consolidator.Clock = runtime.Clock
	consolidated, err := consolidator.Run(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, consolidated.SummaryID)
	require.Contains(t, consolidated.SummaryText, "Window summary")

	pruned, err := store.PruneTurnDigests(ctx, time.Now().UTC().Add(24*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 2, pruned)
	windows, err = store.ListTurnWindows(ctx, "s1", 10)
	require.NoError(t, err)
	require.Len(t, windows, 1, "L0 retention must not delete L1")
}

func TestRunSessionEndKeepsL0WhenWindowSummaryFails(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{
		sessions: []SessionRef{{
			ID: "s1", WorkspacePath: "/workspace", State: "closed",
			LastActivityAt: now.Add(-time.Hour), CanonicalCount: 2,
		}},
		transcripts: map[string]Transcript{"s1": validTranscript("s1", "hash-a")},
	}
	extractor := &fakeExtractor{}
	runtime, store := newRuntimeForTest(t, "shadow", source, extractor)
	runtime.Config.TurnDigestEnabled = true
	runtime.Config.TurnDigestWindowSummaryEnabled = true
	runtime.Config.TurnDigestWindowTurns = 2
	runtime.WindowSummarizer = &fakeWindowSummarizer{err: errors.New("summary provider unavailable")}

	for index, turnID := range []string{"t1", "t2"} {
		_, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
			SessionID: "s1", TurnID: turnID, Outcome: "success",
			AssistantText: "answer", MessageSeqEnd: int64((index + 1) * 2),
		})
		require.NoError(t, err)
	}

	result, err := runtime.RunSessionEnd(ctx, "s1")
	require.NoError(t, err)
	require.Equal(t, 1, result.RollupFailed)
	require.Zero(t, result.RolledUp)

	windows, err := store.ListTurnWindows(ctx, "s1", 10)
	require.NoError(t, err)
	require.Len(t, windows, 1)
	require.Equal(t, memorystore.TurnWindowFailed, windows[0].Status)
	require.Contains(t, windows[0].LastError, "summary provider unavailable")

	ready, err := store.ListTurnDigestsForRollup(ctx, "s1", 10)
	require.NoError(t, err)
	require.Len(t, ready, 2)

	pruned, err := store.PruneTurnDigests(ctx, time.Now().UTC().Add(24*time.Hour))
	require.NoError(t, err)
	require.Zero(t, pruned, "failed L1 must keep L0")
}

func TestRunSessionEndSkipsRollupWhenWindowSummaryDisabled(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{
		sessions: []SessionRef{{
			ID: "s1", WorkspacePath: "/workspace", State: "closed",
			LastActivityAt: now.Add(-time.Hour), CanonicalCount: 2,
		}},
		transcripts: map[string]Transcript{"s1": validTranscript("s1", "hash-a")},
	}
	extractor := &fakeExtractor{}
	runtime, store := newRuntimeForTest(t, "shadow", source, extractor)
	runtime.Config.TurnDigestEnabled = true
	runtime.Config.TurnDigestWindowSummaryEnabled = false
	summarizer := &fakeWindowSummarizer{result: WindowSummary{Text: "must not run"}}
	runtime.WindowSummarizer = summarizer

	digests := make([]memorystore.TurnDigest, 0, 2)
	for index, turnID := range []string{"t1", "t2"} {
		digest, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
			SessionID: "s1", TurnID: turnID, Outcome: "success",
			AssistantText: "answer", MessageSeqEnd: int64((index + 1) * 2),
		})
		require.NoError(t, err)
		digests = append(digests, digest)
	}

	result, err := runtime.RunSessionEnd(ctx, "s1")
	require.NoError(t, err)
	require.Zero(t, result.RolledUp)
	require.Zero(t, summarizer.calls)

	windows, err := store.ListTurnWindows(ctx, "s1", 10)
	require.NoError(t, err)
	require.Empty(t, windows)

	for _, digest := range digests {
		same, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
			SessionID: "s1", TurnID: digest.TurnID, Outcome: "success",
			AssistantText: "answer", MessageSeqEnd: digest.MessageSeqEnd,
		})
		require.NoError(t, err)
		require.Equal(t, memorystore.TurnDigestDone, same.Status)
		require.Equal(t, memorystore.TurnDigestRollupSkipped, same.RollupStatus)
	}

	pruned, err := store.PruneTurnDigests(ctx, time.Now().UTC().Add(24*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 2, pruned)
}

func TestRunSessionEndFinalizesPartialWindow(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{
		sessions: []SessionRef{{
			ID: "s1", WorkspacePath: "/workspace", State: "closed",
			LastActivityAt: now.Add(-time.Hour), CanonicalCount: 2,
		}},
		transcripts: map[string]Transcript{"s1": validTranscript("s1", "hash-a")},
	}
	runtime, store := newRuntimeForTest(t, "shadow", source, &fakeExtractor{})
	runtime.Config.TurnDigestEnabled = true
	runtime.Config.TurnDigestWindowSummaryEnabled = true
	runtime.Config.TurnDigestWindowTurns = 2
	summarizer := &fakeWindowSummarizer{result: WindowSummary{Text: "partial window summary"}}
	runtime.WindowSummarizer = summarizer

	for index, turnID := range []string{"t1", "t2", "t3"} {
		_, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
			SessionID: "s1", TurnID: turnID, Outcome: "success",
			AssistantText: "answer " + strings.Repeat("x", index+1),
			MessageSeqEnd: int64((index + 1) * 2),
		})
		require.NoError(t, err)
	}

	result, err := runtime.RunSessionEnd(ctx, "s1")
	require.NoError(t, err)
	require.Empty(t, result.Errors)
	require.Equal(t, 2, result.RolledUp)
	require.Equal(t, 2, summarizer.calls)

	windows, err := store.ListTurnWindows(ctx, "s1", 10)
	require.NoError(t, err)
	require.Len(t, windows, 2)
	require.Equal(t, 1, windows[0].TurnCount)
	require.Equal(t, 2, windows[1].TurnCount)
	for _, window := range windows {
		require.Equal(t, memorystore.TurnWindowDone, window.Status)
	}
}

func TestRollupWaitsForBacklogBeforePartialWindow(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	base := &fakeSource{
		sessions: []SessionRef{{
			ID: "s1", WorkspacePath: "/workspace", State: "closed",
			LastActivityAt: now.Add(-time.Hour), CanonicalCount: 21,
		}},
	}
	window := Transcript{
		SessionID: "s1", Revision: 2, Hash: "window-hash",
		Messages: []TranscriptMessage{
			{ID: "msg-user", Seq: 1, Role: "user", Content: "question"},
			{ID: "msg-assistant", Seq: 2, Role: "assistant", Content: "answer"},
		},
	}
	source := &fakeWindowSource{fakeSource: base, window: window}
	runtime, store := newRuntimeForTest(t, "shadow", base, &fakeExtractor{})
	runtime.Source = source
	runtime.Config.TurnDigestEnabled = true
	runtime.Config.TurnDigestWindowSummaryEnabled = true
	runtime.Config.TurnDigestWindowTurns = 2
	runtime.Config.TurnDigestMaxTurnsPerBatch = 1
	summarizer := &fakeWindowSummarizer{result: WindowSummary{Text: "window summary"}}
	runtime.WindowSummarizer = summarizer

	for index, turnID := range []string{"t1", "t2", "t3"} {
		_, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
			SessionID: "s1", TurnID: turnID, Outcome: "success",
			AssistantText: "answer", MessageSeqEnd: int64((index + 1) * 2),
		})
		require.NoError(t, err)
	}

	for run := 1; run <= 3; run++ {
		result, err := runtime.RunPendingTurnDigests(ctx, TriggerSessionEnd)
		require.NoError(t, err)
		require.Empty(t, result.Errors)
		windows, err := store.ListTurnWindows(ctx, "s1", 10)
		require.NoError(t, err)
		switch run {
		case 1:
			require.Empty(t, windows)
			require.Zero(t, summarizer.calls)
		case 2:
			require.Len(t, windows, 1)
			require.Equal(t, 2, windows[0].TurnCount, "full windows may roll up while backlog drains")
			require.Equal(t, 1, summarizer.calls)
		case 3:
			require.Len(t, windows, 2)
			require.Equal(t, 1, windows[0].TurnCount)
			require.Equal(t, 2, windows[1].TurnCount)
			require.Equal(t, 2, summarizer.calls, "the partial tail is only summarized after backlog drains")
		}
	}
}

func TestRunSessionEndDrainsWindowBacklog(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	base := &fakeSource{
		sessions: []SessionRef{{
			ID: "s1", WorkspacePath: "/workspace", State: "closed",
			LastActivityAt: now.Add(-time.Hour), CanonicalCount: 21,
		}},
	}
	window := Transcript{
		SessionID: "s1", Revision: 2, Hash: "window-hash",
		Messages: []TranscriptMessage{
			{ID: "msg-user", Seq: 1, Role: "user", Content: "question"},
			{ID: "msg-assistant", Seq: 2, Role: "assistant", Content: "answer"},
		},
	}
	source := &fakeWindowSource{fakeSource: base, window: window}
	extractor := &fakeExtractor{}
	runtime, store := newRuntimeForTest(t, "shadow", base, extractor)
	runtime.Source = source
	runtime.Config.TurnDigestEnabled = true
	runtime.Config.TurnDigestWindowSummaryEnabled = true
	runtime.Config.TurnDigestWindowTurns = 2
	runtime.Config.TurnDigestMaxTurnsPerBatch = 1
	runtime.Config.TurnDigestMaxDigestsPerRun = 10
	summarizer := &fakeWindowSummarizer{result: WindowSummary{Text: "window summary"}}
	runtime.WindowSummarizer = summarizer

	for index, turnID := range []string{"t1", "t2", "t3"} {
		_, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
			SessionID: "s1", TurnID: turnID, Outcome: "success",
			AssistantText: "answer", MessageSeqEnd: int64((index + 1) * 2),
		})
		require.NoError(t, err)
	}

	result, err := runtime.RunSessionEnd(ctx, "s1")
	require.NoError(t, err)
	require.Empty(t, result.Errors)
	require.Equal(t, 3, result.Processed)
	require.Equal(t, 3, source.windowCalls)
	require.Equal(t, 3, extractor.calls)
	require.Equal(t, 2, result.RolledUp)
	require.Equal(t, 2, summarizer.calls)

	pending, err := store.ListPendingTurnDigests(ctx, "s1", 10)
	require.NoError(t, err)
	require.Empty(t, pending)
	windows, err := store.ListTurnWindows(ctx, "s1", 10)
	require.NoError(t, err)
	require.Len(t, windows, 2)
}

func TestIdleSweepWaitsForExtractEveryNTurns(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	transcript := validTranscript("s1", "hash-a")
	source := &fakeSource{
		sessions: []SessionRef{{
			ID: "s1", WorkspacePath: "/workspace", State: "active",
			LastActivityAt: now.Add(-time.Minute), CanonicalCount: 2,
		}},
		transcripts: map[string]Transcript{"s1": transcript},
	}
	extractor := &fakeExtractor{}
	runtime, store := newRuntimeForTest(t, "shadow", source, extractor)
	runtime.Config.TurnDigestEnabled = true
	runtime.Config.TurnDigestExtractEveryNTurns = 3

	for _, turnID := range []string{"t1", "t2"} {
		_, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
			SessionID: "s1", TurnID: turnID, Outcome: "success",
			AssistantText: "answer", MessageSeqEnd: 2,
		})
		require.NoError(t, err)
	}
	result, err := runtime.RunPendingTurnDigests(ctx, TriggerIdle)
	require.NoError(t, err)
	require.Zero(t, result.Processed)
	require.Zero(t, extractor.calls, "active session below extractEveryNTurns and minIdle must wait")
	pending, err := store.ListPendingTurnDigests(ctx, "s1", 10)
	require.NoError(t, err)
	require.Len(t, pending, 2)

	_, err = store.RecordTurnDigest(ctx, memorystore.TurnDigest{
		SessionID: "s1", TurnID: "t3", Outcome: "success",
		AssistantText: "answer", MessageSeqEnd: 2,
	})
	require.NoError(t, err)
	result, err = runtime.RunPendingTurnDigests(ctx, TriggerIdle)
	require.NoError(t, err)
	require.Equal(t, 1, result.Processed)
	require.Equal(t, 1, extractor.calls)
	pending, err = store.ListPendingTurnDigests(ctx, "s1", 10)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestPruneTurnWindowsUsesConfiguredRetention(t *testing.T) {
	ctx := context.Background()
	runtime, store := newRuntimeForTest(t, "shadow", &fakeSource{}, &fakeExtractor{})
	runtime.Config.TurnDigestEnabled = true
	runtime.Config.TurnDigestWindowRetentionDays = 1
	window, _, err := store.EnsureTurnWindow(ctx, memorystore.TurnWindow{
		ProjectKey: store.ProjectKey(), SessionID: "s1", WindowIndex: 1,
		StartDigestSeq: 1, EndDigestSeq: 1, TurnCount: 1,
		SourceHash: "old-window", Status: memorystore.TurnWindowPending,
		CreatedAt: time.Now().UTC().Add(-48 * time.Hour),
	})
	require.NoError(t, err)
	_, err = store.CompleteTurnWindow(ctx, window.ID, memorystore.TurnWindowSummary{Text: "old summary"})
	require.NoError(t, err)

	deleted, err := runtime.PruneTurnWindows(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	_, err = store.GetTurnWindow(ctx, window.ID)
	require.Error(t, err)
}
