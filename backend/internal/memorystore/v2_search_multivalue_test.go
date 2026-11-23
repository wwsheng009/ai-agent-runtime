package memorystore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSearchRecordsMultiValueFilters(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	active, err := store.Append(AppendNoteOptions{
		Text: "active reference with alpha beta tags",
		Tags: []string{"alpha", "beta"},
	})
	require.NoError(t, err)

	candidate, err := store.InsertCandidate(context.Background(), Candidate{
		SessionID: "multi-value", Kind: "fact", Text: "candidate fact with beta gamma tags",
		Tags: []string{"beta", "gamma"}, Confidence: 0.9,
		SourceHash: "multi-value", SourceRevision: 1,
	})
	require.NoError(t, err)
	require.Equal(t, StatusCandidate, candidate.Status)

	ids := func(page RecordSearchPage) map[string]struct{} {
		out := make(map[string]struct{}, len(page.Items))
		for _, hit := range page.Items {
			out[hit.Record.ID] = struct{}{}
		}
		return out
	}

	result, err := store.SearchRecords(context.Background(), RecordSearchQuery{
		Statuses: []Status{StatusActive, StatusCandidate}, Limit: 10,
	})
	require.NoError(t, err)
	got := ids(result)
	require.Contains(t, got, active.ID)
	require.Contains(t, got, candidate.ID)

	result, err = store.SearchRecords(context.Background(), RecordSearchQuery{
		Kinds: []string{"reference", "fact"}, Statuses: []Status{StatusActive, StatusCandidate}, Limit: 10,
	})
	require.NoError(t, err)
	got = ids(result)
	require.Contains(t, got, active.ID)
	require.Contains(t, got, candidate.ID)

	result, err = store.SearchRecords(context.Background(), RecordSearchQuery{
		Status: StatusCandidate, Kind: "fact", Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, map[string]struct{}{candidate.ID: {}}, ids(result))

	result, err = store.SearchRecords(context.Background(), RecordSearchQuery{
		Tags: []string{"alpha", "beta"}, Statuses: []Status{StatusActive, StatusCandidate}, Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, map[string]struct{}{active.ID: {}}, ids(result),
		"multi-tag filters must require all tags, not JSON substring matches")

	result, err = store.SearchRecords(context.Background(), RecordSearchQuery{
		Tags: []string{"beta"}, Statuses: []Status{StatusActive, StatusCandidate}, Limit: 10,
	})
	require.NoError(t, err)
	got = ids(result)
	require.Contains(t, got, active.ID)
	require.Contains(t, got, candidate.ID)

	result, err = store.SearchRecords(context.Background(), RecordSearchQuery{
		Tags: []string{"alpha", "gamma"}, Statuses: []Status{StatusActive, StatusCandidate}, Limit: 10,
	})
	require.NoError(t, err)
	require.Empty(t, result.Items)
}

func TestSearchRecordsSingleValueFiltersStillWork(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	note, err := store.Append(AppendNoteOptions{Text: "single filter marker", Tags: []string{"alpha"}})
	require.NoError(t, err)

	result, err := store.SearchRecords(context.Background(), RecordSearchQuery{
		Status: StatusActive, Kind: "reference", Tag: "alpha", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	require.Equal(t, note.ID, result.Items[0].Record.ID)
}
