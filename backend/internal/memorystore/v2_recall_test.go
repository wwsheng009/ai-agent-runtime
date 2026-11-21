package memorystore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTokenizeAddsHanBigrams(t *testing.T) {
	tokens := tokenize("记忆摘要逻辑")
	for _, want := range []string{"记忆摘要逻辑", "记忆", "忆摘", "摘要", "要逻", "逻辑"} {
		require.Contains(t, tokens, want)
	}
	mixed := tokenize("API超时")
	require.Contains(t, mixed, "api")
	require.Contains(t, mixed, "超时")

	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	record := Record{
		Kind: "decision", Text: "记忆摘要逻辑需要保留来源", NormalizedText: "记忆摘要逻辑需要保留来源",
		Confidence: 0.9, UpdatedAt: now,
	}
	require.Greater(t, scoreRecordForRecall(record, tokenize("摘要逻辑"), "摘要逻辑", now), 0.0)
}

func TestScoreRecordForRecallGatesNonMatchesAndBoostsCorrection(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	base := Record{
		Text: "API timeout is 30 seconds", NormalizedText: "API timeout is 30 seconds",
		Confidence: 0.8, UpdatedAt: now,
	}
	fact := base
	fact.Kind = "fact"
	correction := base
	correction.Kind = "correction"
	query := "API timeout"
	tokens := tokenize(query)

	factScore := scoreRecordForRecall(fact, tokens, query, now)
	correctionScore := scoreRecordForRecall(correction, tokens, query, now)
	require.Greater(t, correctionScore, factScore)

	unrelated := base
	unrelated.Text = "unrelated deployment note"
	unrelated.NormalizedText = unrelated.Text
	require.Zero(t, scoreRecordForRecall(unrelated, tokens, query, now))
}

func TestSelectForInjectV2DropsNearDuplicateRecallRows(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{
		ProjectRoot: t.TempDir(), Mode: "on", InjectionMode: "notes",
		PinnedBudgetRatio: 0.2,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	activate := func(text, hash string) Record {
		t.Helper()
		record, insertErr := store.InsertCandidate(ctx, Candidate{
			SessionID: "s1", Kind: "decision", Text: text, Confidence: 0.9,
			SourceHash: hash, SourceRevision: 1, RetentionClass: "permanent",
		})
		require.NoError(t, insertErr)
		record, insertErr = store.PromoteCandidate(ctx, record.ID)
		require.NoError(t, insertErr)
		return record
	}
	first := activate("记忆摘要逻辑需要保留来源。", "hash-recall-1")
	duplicate := activate("记忆摘要逻辑需要保留来源", "hash-recall-2")
	unrelated := activate("发布流程需要双人复核", "hash-recall-3")

	selection, err := store.SelectForInjectV2(InjectOptions{
		Query: "摘要逻辑", Limit: 5, TokenBudget: 600, SkipTrace: true,
	})
	require.NoError(t, err)
	require.Len(t, selection.Recall, 1)
	selectedID := selection.Recall[0].ID
	require.Contains(t, []string{first.ID, duplicate.ID}, selectedID)

	dropped := make(map[string]string, len(selection.Dropped))
	for _, item := range selection.Dropped {
		dropped[item.ID] = item.Reason
	}
	droppedDuplicate := duplicate.ID
	if selectedID == duplicate.ID {
		droppedDuplicate = first.ID
	}
	require.Equal(t, "duplicate", dropped[droppedDuplicate])
	require.NotContains(t, dropped, unrelated.ID, "non-matching rows are filtered before budget diagnostics")
}

func TestSelectForInjectV2DiversifiesRecallKinds(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{
		ProjectRoot: t.TempDir(), Mode: "on", InjectionMode: "notes",
		PinnedBudgetRatio: 0.2,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	activate := func(kind, text, hash string) Record {
		t.Helper()
		record, insertErr := store.InsertCandidate(ctx, Candidate{
			SessionID: "s1", Kind: kind, Text: text, Confidence: 0.9,
			SourceHash: hash, SourceRevision: 1,
		})
		require.NoError(t, insertErr)
		record, insertErr = store.PromoteCandidate(ctx, record.ID)
		require.NoError(t, insertErr)
		return record
	}
	for i := 0; i < 3; i++ {
		activate("decision", "shared topic decision "+string(rune('a'+i)), "hash-kind-decision-"+string(rune('a'+i)))
	}
	fact := activate("fact", "shared topic fact", "hash-kind-fact")

	selection, err := store.SelectForInjectV2(InjectOptions{
		Query: "shared topic", Limit: 2, TokenBudget: 600, SkipTrace: true,
	})
	require.NoError(t, err)
	require.Len(t, selection.Recall, 2)
	ids := []string{selection.Recall[0].ID, selection.Recall[1].ID}
	require.Contains(t, ids, fact.ID, "kind diversity should surface a fact before a second same-kind decision")
}

func TestSelectForInjectV2NoMatchKeepsEmptySelection(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{
		ProjectRoot: t.TempDir(), Mode: "on", InjectionMode: "notes",
		PinnedBudgetRatio: 0.2,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	record, err := store.InsertCandidate(ctx, Candidate{
		SessionID: "s1", Kind: "fact", Text: "unrelated deployment note",
		Confidence: 0.9, SourceHash: "hash-no-match", SourceRevision: 1,
	})
	require.NoError(t, err)
	_, err = store.PromoteCandidate(ctx, record.ID)
	require.NoError(t, err)

	selection, err := store.SelectForInjectV2(InjectOptions{
		Query: "zzzz-unmatched-token", Limit: 5, TokenBudget: 600, SkipTrace: true,
	})
	require.NoError(t, err)
	require.Equal(t, 1, selection.CandidateCount)
	require.Empty(t, selection.Recall)
	require.Empty(t, selection.Dropped)
}

func TestSelectForInjectV2HydratesSelectedRecordAndPreservesFittedText(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{
		ProjectRoot: t.TempDir(), Mode: "on", InjectionMode: "notes",
		PinnedBudgetRatio: 0.2,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	original := "budgetprobe " + strings.Repeat("detail ", 80)
	record, err := store.InsertCandidate(ctx, Candidate{
		SessionID: "s1", Kind: "fact", Text: original, Confidence: 0.9,
		SourceHash: "hash-hydration", SourceRevision: 1,
		SourceRefs: []string{"source:hydration"}, Metadata: map[string]interface{}{"origin": "recall-test"},
	})
	require.NoError(t, err)
	_, err = store.PromoteCandidate(ctx, record.ID)
	require.NoError(t, err)

	selection, err := store.SelectForInjectV2(InjectOptions{
		Query: "budgetprobe", Limit: 1, TokenBudget: 50, SkipTrace: true,
	})
	require.NoError(t, err)
	require.Len(t, selection.Recall, 1)
	selected := selection.Recall[0]
	require.Equal(t, "recall-test", selected.Metadata["origin"],
		"selected records must be hydrated from the authoritative row")
	require.Contains(t, selected.SourceRefs, "source:hydration")
	require.Less(t, len(selected.Text), len(original))
	require.True(t, strings.HasSuffix(selected.Text, "…"),
		"hydration must not undo the token-budget truncation")
}
