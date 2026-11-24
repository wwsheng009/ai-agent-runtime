package memorystore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSearchRecordsUsesUnifiedFTSAndMatchesListRecords(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	manual, err := store.Append(AppendNoteOptions{
		Text: "search unification keeps E_INVALID_ARGUMENT visible from every surface",
		Tags: []string{"retrieval"},
	})
	require.NoError(t, err)
	automatic := seedActiveDerivedMemory(t, store, "统一检索需要覆盖中文查询与 NewStore", "fact")

	requireMemoryFTS(t, store)

	result, err := store.SearchRecords(context.Background(), RecordSearchQuery{
		Text: "E_INVALID_ARGUMENT", Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, "fts5", result.Backend)
	require.NotEmpty(t, result.Items)
	require.Equal(t, manual.ID, result.Items[0].Record.ID)
	require.Equal(t, "fts5", result.Items[0].MatchSource)

	page, err := store.ListRecords(context.Background(), RecordFilter{
		Query: "E_INVALID_ARGUMENT", Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, recordIDs(page.Items), recordIDsFromSearch(result))

	result, err = store.SearchRecords(context.Background(), RecordSearchQuery{
		Text: "NewStore", Status: StatusActive, SourceKind: "session_extract", Limit: 10,
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Items)
	require.Equal(t, automatic.ID, result.Items[0].Record.ID)
	require.Equal(t, recordIDsFromSearch(result), []string{automatic.ID})

	result, err = store.SearchRecords(context.Background(), RecordSearchQuery{
		Text: "E_INVALID_ARGUMENT", SourceKind: "session_extract", Limit: 10,
	})
	require.NoError(t, err)
	require.Empty(t, result.Items, "source filters must still apply to unified search")
}

func TestSearchRecordsShortQueryFallsBackToLike(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	note, err := store.Append(AppendNoteOptions{Text: "ab short token remains searchable"})
	require.NoError(t, err)

	result, err := store.SearchRecords(context.Background(), RecordSearchQuery{Text: "ab", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, "like_fallback", result.Backend)
	require.NotEmpty(t, result.Items)
	require.Equal(t, note.ID, result.Items[0].Record.ID)
	require.Equal(t, "like_fallback", result.Items[0].MatchSource)
}

func TestSearchRecordsAppliesMultiValueFiltersOnFTS(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.Append(AppendNoteOptions{
		Text: "filtered manual decision alpha beta", Kind: "decision",
		Tags: []string{"alpha", "beta"},
	})
	require.NoError(t, err)
	fact, err := store.InsertCandidate(context.Background(), Candidate{
		SessionID: "filter-session", Kind: "fact", Text: "filtered automatic fact alpha beta",
		Tags: []string{"alpha", "beta"}, Confidence: 0.9,
		SourceHash: "filter-fact", SourceRevision: 1,
	})
	require.NoError(t, err)
	active := StatusActive
	fact, err = store.UpdateRecord(context.Background(), fact.ID, RecordPatch{Status: &active})
	require.NoError(t, err)
	decision, err := store.InsertCandidate(context.Background(), Candidate{
		SessionID: "filter-session", Kind: "decision", Text: "filtered automatic decision alpha",
		Tags: []string{"alpha"}, Confidence: 0.9,
		SourceHash: "filter-decision", SourceRevision: 1,
	})
	require.NoError(t, err)
	decision, err = store.UpdateRecord(context.Background(), decision.ID, RecordPatch{Status: &active})
	require.NoError(t, err)

	requireMemoryFTS(t, store)
	result, err := store.SearchRecords(context.Background(), RecordSearchQuery{
		Text: "filtered", Status: StatusActive, Kinds: []string{"fact"},
		Tags: []string{"beta"}, Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, "fts5", result.Backend)
	require.Equal(t, []string{fact.ID}, recordIDsFromSearch(result))
	require.NotContains(t, recordIDsFromSearch(result), decision.ID)
}

func TestSearchRecordsSupportsLegacyJSONLMode(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "off"})
	require.NoError(t, err)

	note, err := store.Append(AppendNoteOptions{Text: "legacy worktree note"})
	require.NoError(t, err)

	result, err := store.SearchRecords(context.Background(), RecordSearchQuery{Text: "worktree", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, "legacy_jsonl", result.Backend)
	require.NotEmpty(t, result.Items)
	require.Equal(t, note.ID, result.Items[0].Record.ID)
}

func TestSearchRecordsReportsFallbackWhenFTSHasNoMatch(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.Append(AppendNoteOptions{Text: "alpha beta gamma"})
	require.NoError(t, err)
	requireMemoryFTS(t, store)

	result, err := store.SearchRecords(context.Background(), RecordSearchQuery{Text: "zzzz-no-match", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, result.Items)
	require.Equal(t, "like_fallback", result.Backend,
		"an FTS miss that falls through to LIKE must not be labelled fts5")
}

func TestSearchRecordsReportsRecentForEmptyQuery(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	note, err := store.Append(AppendNoteOptions{Text: "recent memory marker"})
	require.NoError(t, err)

	result, err := store.SearchRecords(context.Background(), RecordSearchQuery{Limit: 10})
	require.NoError(t, err)
	require.NotEmpty(t, result.Items)
	require.Equal(t, note.ID, result.Items[0].Record.ID)
	require.Equal(t, "recent", result.Backend)
}

func recordIDs(records []Record) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids
}

func recordIDsFromSearch(page RecordSearchPage) []string {
	ids := make([]string, 0, len(page.Items))
	for _, hit := range page.Items {
		ids = append(ids, hit.Record.ID)
	}
	return ids
}
