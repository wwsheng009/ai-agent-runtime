package chat

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenPersistentSessionStorage_SQLiteIsLazyUntilFirstUse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session_history.sqlite")
	cfg := DefaultPersistentSessionStorageConfig(dir)
	cfg.Path = path
	cfg.ImportLegacyJSON = false

	store, err := OpenPersistentSessionStorage(cfg)
	require.NoError(t, err)
	lazy, ok := store.(*lazySessionStorage)
	require.True(t, ok)
	require.False(t, lazy.Opened())
	require.Equal(t, path, lazy.Path())
	require.Equal(t, dir, lazy.Dir())

	// Background housekeeping must not force the first open.
	removed, err := lazy.Cleanup(context.Background(), time.Now())
	require.NoError(t, err)
	require.Zero(t, removed)
	require.False(t, lazy.Opened())
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err), "lazy open must not create the sqlite file early")

	session := NewSession("lazy-user")
	require.NoError(t, store.Save(context.Background(), session))
	require.True(t, lazy.Opened())
	_, err = os.Stat(path)
	require.NoError(t, err)

	loaded, err := store.Load(context.Background(), session.ID)
	require.NoError(t, err)
	require.Equal(t, session.ID, loaded.ID)
	require.NoError(t, store.(SessionStorageCloser).CloseStorage())
}

func TestOpenEagerPersistentSessionStorage_OpensImmediately(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session_history.sqlite")
	cfg := DefaultPersistentSessionStorageConfig(dir)
	cfg.Path = path
	cfg.ImportLegacyJSON = false

	store, err := OpenEagerPersistentSessionStorage(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.(SessionStorageCloser).CloseStorage()) })
	_, ok := store.(*SQLiteSessionStorage)
	require.True(t, ok)
	_, err = os.Stat(path)
	require.NoError(t, err)
}

func TestLazySessionStorage_MaintenanceOpensExistingFileAfterRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session_history.sqlite")
	cfg := DefaultPersistentSessionStorageConfig(dir)
	cfg.Path = path
	cfg.ImportLegacyJSON = false

	// First process writes durable history.
	first, err := OpenPersistentSessionStorage(cfg)
	require.NoError(t, err)
	session := NewSession("lazy-user")
	session.UpdatedAt = time.Now().UTC().Add(-2 * time.Hour)
	require.NoError(t, first.Save(context.Background(), session))
	require.NoError(t, first.(SessionStorageCloser).CloseStorage())

	// Restarted process must still maintain existing history without requiring
	// an intervening Save/Load first.
	second, err := OpenPersistentSessionStorage(cfg)
	require.NoError(t, err)
	lazy, ok := second.(*lazySessionStorage)
	require.True(t, ok)
	require.False(t, lazy.Opened())
	require.True(t, lazy.durableFileExists())

	removed, err := second.Cleanup(context.Background(), time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	require.GreaterOrEqual(t, removed, 0)
	require.True(t, lazy.Opened(), "existing history must open for maintenance after restart")

	loaded, err := second.Load(context.Background(), session.ID)
	// Cleanup may or may not remove the session depending on TTL rules; either
	// way the open path must succeed without inventing a new empty DB.
	require.NoError(t, err)
	if loaded != nil {
		require.Equal(t, session.ID, loaded.ID)
	}
	require.NoError(t, second.(SessionStorageCloser).CloseStorage())
}

func TestLazySessionStorage_ListAllDelegatesToDurableBackend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session_history.sqlite")
	cfg := DefaultPersistentSessionStorageConfig(dir)
	cfg.Path = path
	cfg.ImportLegacyJSON = false

	store, err := OpenPersistentSessionStorage(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.(SessionStorageCloser).CloseStorage()) })

	// Lazy wrapper must expose the optional all-lister capability so user
	// discovery (ListSessionUsers) can see every persisted user's sessions.
	allLister, ok := store.(SessionStorageAllLister)
	require.True(t, ok, "lazy session storage must implement SessionStorageAllLister")

	first := NewSession("thinkbook14\\wangweisheng")
	require.NoError(t, store.Save(context.Background(), first))
	second := NewSession("anonymous")
	require.NoError(t, store.Save(context.Background(), second))

	all, err := allLister.ListAll(context.Background(), 0, 0)
	require.NoError(t, err)
	require.Len(t, all, 2)

	byID := make(map[string]string, len(all))
	for _, session := range all {
		byID[session.ID] = session.UserID
	}
	require.Equal(t, "thinkbook14\\wangweisheng", byID[first.ID])
	require.Equal(t, "anonymous", byID[second.ID])
}

// TestLazySessionStorage_OpenLockFailureRecoversAfterRelease proves that a
// failed first open (a concurrent process holding the sqlite write lock) is
// not cached permanently: once the lock is released, the next operation
// reopens the storage and succeeds instead of wedging the process until
// restart. Regression test for "Runtime stream failed. failed to load
// session: ... database is locked" persisting across retries.
func TestLazySessionStorage_OpenLockFailureRecoversAfterRelease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session_history.sqlite")
	cfg := DefaultPersistentSessionStorageConfig(dir)
	cfg.Path = path
	cfg.ImportLegacyJSON = false
	cfg.BusyTimeout = 100 * time.Millisecond

	// Seed a migrated database first (without a lock the first open would
	// just create it and never see the lock).
	seed, err := NewSQLiteSessionStorage(cfg)
	require.NoError(t, err)
	require.NoError(t, seed.CloseStorage())

	// Hold a write lock on a rollback-journal database so new readers (the
	// PRAGMA user_version probe during open) hit SQLITE_BUSY while the lock
	// is held, mimicking a concurrent aicli/runtime-server process.
	locker, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	_, err = locker.Exec("PRAGMA journal_mode=DELETE")
	require.NoError(t, err)
	_, err = locker.Exec("BEGIN EXCLUSIVE")
	require.NoError(t, err)
	defer func() {
		_, _ = locker.ExecContext(context.Background(), "COMMIT")
		_ = locker.Close()
	}()

	store, err := OpenPersistentSessionStorage(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.(SessionStorageCloser).CloseStorage() })

	// First operation while the lock is held must fail with a lock error.
	_, err = store.Load(context.Background(), "locked-session")
	require.Error(t, err)
	require.Contains(t, err.Error(), "database is locked")

	// Release the lock; the next operation must recover on its own.
	_, err = locker.ExecContext(context.Background(), "COMMIT")
	require.NoError(t, err)
	_ = locker.Close()

	// Save never depends on the session existing, so it cleanly proves the
	// storage reopened instead of re-serving the cached open failure.
	session := NewSession("locked-session-recovered")
	require.NoError(t, store.Save(context.Background(), session))
	loaded, err := store.Load(context.Background(), session.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Equal(t, session.ID, loaded.ID)
}
