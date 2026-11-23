//go:build win7compat

package memorystore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// win7compat is pinned to the older ncruces Wasm driver, which does not ship
// the optional ext/fts5 package. Its embedded SQLite build may still provide
// FTS5 natively, and hybrid/profile builds may not. The store must work in
// either case: use FTS5 when it is present, otherwise keep the LIKE fallback.
func TestMemoryFTSWin7CompatHandlesDriverCapability(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	note, err := store.Append(AppendNoteOptions{Text: "win7compat fallback marker"})
	require.NoError(t, err)

	hits, err := store.Search(SearchOptions{Query: "fallback marker", Limit: 10})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	require.Equal(t, note.ID, hits[0].Note.ID)

	status, err := store.Status(context.Background())
	require.NoError(t, err)
	if store.ftsEnabled {
		require.NoError(t, store.ftsErr)
		require.Equal(t, "fts5", status.SearchBackend)
		return
	}
	require.Error(t, store.ftsErr)
	require.Equal(t, "like_fallback", status.SearchBackend)
}