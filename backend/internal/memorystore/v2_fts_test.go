package memorystore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func requireMemoryFTS(t *testing.T, store *Store) {
	t.Helper()
	if !store.ftsEnabled {
		t.Skipf("FTS5 unavailable in this build: %v", store.ftsErr)
	}
}

func TestMemoryFTSExactIdentifierAndCJKRSearch(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	manual, err := store.Append(AppendNoteOptions{
		Text: "The E_INVALID_ARGUMENT path is internal/memorystore/v2_fts.go and version v1.2.3",
		Tags: []string{"retrieval"},
	})
	require.NoError(t, err)
	automatic := seedActiveDerivedMemory(t, store,
		"记忆引擎支持中文检索和 NewStore 函数", "fact")

	requireMemoryFTS(t, store)

	for _, query := range []string{
		"E_INVALID_ARGUMENT",
		"internal/memorystore/v2_fts.go",
		"v1.2.3",
	} {
		hits, searchErr := store.Search(SearchOptions{Query: query, Limit: 10})
		require.NoError(t, searchErr, "query %q", query)
		require.NotEmpty(t, hits, "query %q should hit the manual record", query)
		require.Equal(t, manual.ID, hits[0].Note.ID, "query %q", query)
	}

	hits, err := store.Search(SearchOptions{Query: "记忆引擎", Limit: 10})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	require.Equal(t, automatic.ID, hits[0].Note.ID)

	hits, err = store.Search(SearchOptions{Query: "NewStore", Limit: 10})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	require.Equal(t, automatic.ID, hits[0].Note.ID)

	hits, err = store.Search(SearchOptions{
		Query: "记忆引擎", SourceKind: "session_extract", Tags: []string{"retrieval"},
		Limit: 10,
	})
	require.NoError(t, err)
	require.Empty(t, hits, "tag/source filters must still apply to FTS hits")

	hits, err = store.Search(SearchOptions{
		Query: "记忆引擎", SourceKind: "session_extract", Limit: 10,
	})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	require.Equal(t, automatic.ID, hits[0].Note.ID)

	page, err := store.ListRecords(context.Background(), RecordFilter{
		Query: "internal/memorystore/v2_fts.go", Status: StatusActive, Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, 1, page.Count)
	require.Equal(t, manual.ID, page.Items[0].ID)
	page, err = store.ListRecords(context.Background(), RecordFilter{
		Query: "记忆", Status: StatusActive, Limit: 10,
	})
	require.NoError(t, err)
	require.NotEmpty(t, page.Items)

	status, err := store.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, "fts5", status.SearchBackend)
}

func TestMemoryFTSStaysSynchronizedAcrossUpdateDeleteAndReopen(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Root: root, Mode: "on", ProjectKey: "fts-sync-project"}
	store, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	note, err := store.Append(AppendNoteOptions{
		Text: "before-update marker E_OLD",
		Tags: []string{"sync"},
	})
	require.NoError(t, err)
	requireMemoryFTS(t, store)

	hits, err := store.Search(SearchOptions{Query: "E_OLD", Limit: 10})
	require.NoError(t, err)
	require.Len(t, hits, 1)

	newText := "after-update marker E_NEW"
	_, err = store.UpdateRecord(context.Background(), note.ID, RecordPatch{Text: &newText})
	require.NoError(t, err)

	hits, err = store.Search(SearchOptions{Query: "E_OLD", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, hits)
	hits, err = store.Search(SearchOptions{Query: "E_NEW", Limit: 10})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, note.ID, hits[0].Note.ID)

	require.NoError(t, store.Close())
	reopened, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	hits, err = reopened.Search(SearchOptions{Query: "E_NEW", Limit: 10})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	requireMemoryFTS(t, reopened)

	require.NoError(t, reopened.DeleteRecord(context.Background(), note.ID))
	hits, err = reopened.Search(SearchOptions{Query: "E_NEW", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, hits, "deleted records must not be returned by FTS search")
}

func TestMemoryFTSFallsBackForShortQueries(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	note, err := store.Append(AppendNoteOptions{Text: "短词检索 记忆"})
	require.NoError(t, err)
	hits, err := store.Search(SearchOptions{Query: "记忆", Limit: 10})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	require.Equal(t, note.ID, hits[0].Note.ID)
}
