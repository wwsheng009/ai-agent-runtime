package policy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemoryGrantStoreListAndRevoke(t *testing.T) {
	store := &MemoryGrantStore{}
	require.NoError(t, store.Remember(Grant{Tool: "write", Pattern: "docs/", Scope: "session"}))
	require.NoError(t, store.Remember(Grant{Tool: "write", Pattern: "", Scope: "session"}))
	require.NoError(t, store.Remember(Grant{Tool: "edit", Pattern: "foo.go", Scope: "session"}))
	require.Error(t, store.Remember(Grant{Tool: "shell"}))

	listed := store.List()
	require.Len(t, listed, 3)

	// Revoke tool-wide only when matchEmptyPattern.
	n := store.Revoke("write", "", true)
	require.Equal(t, 1, n)
	listed = store.List()
	require.Len(t, listed, 2)

	// Revoke all write grants.
	n = store.Revoke("write", "", false)
	require.Equal(t, 1, n)
	listed = store.List()
	require.Len(t, listed, 1)
	require.Equal(t, "edit", listed[0].Tool)

	n = store.Revoke("edit", "foo.go", false)
	require.Equal(t, 1, n)
	require.Nil(t, store.List())
}

func TestFileGrantStoreRememberListRevokeRoundTrip(t *testing.T) {
	root := t.TempDir()
	store, err := OpenProjectGrantStore(root)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, ".aicli", DefaultGrantsFileName), store.Path())

	require.NoError(t, store.Remember(Grant{Tool: "write", Pattern: "README.md"}))
	require.NoError(t, store.Remember(Grant{Tool: "edit", Pattern: ""}))
	require.ErrorIs(t, store.Remember(Grant{Tool: "bash"}), errDangerousGrant)

	// re-open and list
	store2, err := OpenProjectGrantStore(root)
	require.NoError(t, err)
	listed := store2.List()
	require.Len(t, listed, 2)
	require.Equal(t, "project", listed[0].Scope)

	grant, ok := store2.Find("write", map[string]interface{}{"file_path": "README.md"})
	require.True(t, ok)
	require.Equal(t, "write", grant.Tool)

	n := store2.Revoke("edit", "", true)
	require.Equal(t, 1, n)
	require.Len(t, store2.List(), 1)

	// file exists on disk
	data, err := os.ReadFile(store2.Path())
	require.NoError(t, err)
	require.Contains(t, string(data), `"write"`)
	require.NotContains(t, string(data), `"edit"`)
}

func TestFileGrantStoreDedupesIdenticalGrants(t *testing.T) {
	store, err := OpenProjectGrantStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Remember(Grant{Tool: "view", Pattern: "a"}))
	require.NoError(t, store.Remember(Grant{Tool: "view", Pattern: "a", Scope: "project"}))
	require.Len(t, store.List(), 1)
}
