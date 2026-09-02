package chat

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSyncSessionReplica_CopiesCheckpointedMaster verifies that a master
// database with an empty WAL sidecar is copied to the replica and the
// replica's own WAL/SHM sidecars are removed.
func TestSyncSessionReplica_CopiesCheckpointedMaster(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "master.sqlite")
	dst := filepath.Join(dir, "replica.sqlite")

	// Create a real SQLite master with one row.
	master, err := OpenPersistentSessionStorage(PersistentSessionStorageConfig{
		Backend: SessionStorageBackendSQLite,
		Dir:     dir,
		Path:    src,
	})
	require.NoError(t, err)
	require.NoError(t, master.Save(context.Background(), &Session{
		ID:      "s1",
		UserID:  "u1",
		Metadata: SessionMetadata{Title: "hello"},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}))

	// Force a checkpoint so the WAL is empty (close fully checkpoints).
	require.NoError(t, master.(SessionStorageCloser).CloseStorage())

	require.NoError(t, SyncSessionReplica(context.Background(), src, dst))
	_, err = os.Stat(dst)
	require.NoError(t, err, "replica file must exist after sync")
	_, err = os.Stat(dst + "-wal")
	require.Error(t, err, "replica WAL sidecar must be removed")
	_, err = os.Stat(dst + "-shm")
	require.Error(t, err, "replica SHM sidecar must be removed")

	// Read back through a fresh store.
	replica, err := OpenPersistentSessionStorage(PersistentSessionStorageConfig{
		Backend: SessionStorageBackendSQLite,
		Dir:     dir,
		Path:    dst,
	})
	require.NoError(t, err)
	loaded, err := replica.Load(context.Background(), "s1")
	require.NoError(t, err)
	require.Equal(t, "hello", loaded.Metadata.Title)
}

// TestSyncSessionReplica_InvalidMasterRejected verifies that a master that is
// not a valid SQLite database is rejected and no replica file is created.
func TestSyncSessionReplica_InvalidMasterRejected(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "master.sqlite")
	dst := filepath.Join(dir, "replica.sqlite")

	// Non-SQLite bytes must fail the open verification.
	require.NoError(t, os.WriteFile(src, []byte("this is not a sqlite database"), 0o644))

	err := SyncSessionReplica(context.Background(), src, dst)
	require.Error(t, err, "non-empty WAL must be rejected")
	_, statErr := os.Stat(dst)
	require.Error(t, statErr, "replica must not be created from an invalid master")
}

// TestSwitchableSessionStorage_Swap verifies hot-swapping keeps the wrapper
// usable and the old backend is closed.
func TestSwitchableSessionStorage_Swap(t *testing.T) {
	dir := t.TempDir()
	makeStore := func(name string) SessionStorage {
		st, err := OpenPersistentSessionStorage(PersistentSessionStorageConfig{
			Backend: SessionStorageBackendSQLite,
			Dir:     dir,
			Path:    filepath.Join(dir, name),
		})
		require.NoError(t, err)
		return st
	}

	first := makeStore("a.sqlite")
	wrapper := NewSwitchableSessionStorage(first)

	ctx := context.Background()
	require.NoError(t, wrapper.Save(ctx, &Session{ID: "s1", UserID: "u1", Metadata: SessionMetadata{Title: "first"}, CreatedAt: time.Now(), UpdatedAt: time.Now()}))
	loaded, err := wrapper.Load(ctx, "s1")
	require.NoError(t, err)
	require.Equal(t, "first", loaded.Metadata.Title)

	// Swap to a fresh backend (e.g. a new replica sync).
	second := makeStore("b.sqlite")
	wrapper.(*switchableSessionStorage).Swap(second)

	// The new backend does not have s1 yet.
	_, err = wrapper.Load(ctx, "s1")
	require.Error(t, err, "swapped backend must not see old data")

	require.NoError(t, wrapper.Save(ctx, &Session{ID: "s2", UserID: "u1", Metadata: SessionMetadata{Title: "second"}, CreatedAt: time.Now(), UpdatedAt: time.Now()}))
	loaded2, err := wrapper.Load(ctx, "s2")
	require.NoError(t, err)
	require.Equal(t, "second", loaded2.Metadata.Title)
}
