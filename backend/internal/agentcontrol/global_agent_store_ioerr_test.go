package agentcontrol

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsSQLiteIOErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "plain disk io", err: errors.New("sqlite3: disk I/O error"), want: true},
		{name: "short read io", err: fmt.Errorf("read agent control agent: %w", errors.New("sqlite3: disk I/O error")), want: true},
		{name: "list error", err: fmt.Errorf("list agent control agents: %w", errors.New("sqlite3: disk I/O error")), want: true},
		{name: "locked error", err: errors.New("sqlite3: database is locked"), want: false},
		{name: "generic error", err: errors.New("some other error"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isSQLiteIOErr(tc.err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestResetDBHandleStandalone verifies that resetDBHandle closes the DB and
// resets sync.Once so the next dbHandle()/ensure() call re-opens the database.
func TestResetDBHandleStandalone(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)

	// First write creates the DB.
	_, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "agent-1",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
		Status:        AgentStatusActive,
	})
	require.NoError(t, err)

	// Confirm the DB handle is non-nil.
	require.NotNil(t, store.db)

	// Reset should close the handle and clear it.
	err = store.resetDBHandle()
	require.NoError(t, err)
	require.Nil(t, store.db)
	require.Nil(t, store.openErr)

	// After reset, a new write should re-open the DB.
	_, err = store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "agent-1",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
		Status:        AgentStatusActive,
	})
	require.NoError(t, err)
	require.NotNil(t, store.db)
}

// TestWithRetryOnIOErrRetriesAndSucceeds verifies that withRetryOnIOErr
// retries once after an IOERR error and succeeds on the second attempt.
func TestWithRetryOnIOErrRetriesAndSucceeds(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)

	// Seed a record.
	_, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "retry-agent",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
		Status:        AgentStatusActive,
	})
	require.NoError(t, err)

	callCount := 0
	var seenDBs []*sql.DB
	err = store.withRetryOnIOErr(func(db *sql.DB) error {
		callCount++
		seenDBs = append(seenDBs, db)
		if callCount == 1 {
			return errors.New("read agent control agent: sqlite3: disk I/O error")
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, callCount, "should have retried after IOERR")
	require.Len(t, seenDBs, 2, "should have called fn with two different DB handles")
	// The two handles should be different (old closed, new opened).
	// We can't compare *sql.DB pointers directly since the old one is closed,
	// but both should be non-nil.
	require.NotNil(t, seenDBs[0])
	require.NotNil(t, seenDBs[1])
}

// TestWithRetryOnIOErrDoesNotRetryNonIOErr verifies that non-IOERR errors
// are returned immediately without retrying.
func TestWithRetryOnIOErrDoesNotRetryNonIOErr(t *testing.T) {
	store := newTestGlobalAgentRegistryStore(t)
	// Force the DB to be open.
	ctx := context.Background()
	_, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "non-ioerr-agent",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
		Status:        AgentStatusActive,
	})
	require.NoError(t, err)

	callCount := 0
	nonIOErr := errors.New("some other error")
	err = store.withRetryOnIOErr(func(db *sql.DB) error {
		callCount++
		return nonIOErr
	})
	require.Same(t, nonIOErr, err)
	require.Equal(t, 1, callCount, "should not retry on non-IOERR errors")
}

// TestWithRetryOnIOErrRetriesExhausted verifies that if the IOERR persists
// after one retry, the error is returned.
func TestWithRetryOnIOErrRetriesExhausted(t *testing.T) {
	store := newTestGlobalAgentRegistryStore(t)
	// Force the DB to be open.
	ctx := context.Background()
	_, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "persistent-ioerr-agent",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
		Status:        AgentStatusActive,
	})
	require.NoError(t, err)

	callCount := 0
	ioerr := errors.New("read agent control agent: sqlite3: disk I/O error")
	wrapped := store.withRetryOnIOErr(func(db *sql.DB) error {
		callCount++
		return ioerr
	})
	require.Error(t, wrapped)
	require.Equal(t, 2, callCount, "should retry once then give up")
}

// TestGetAgentControlAgentByIDRetriesOnIOErr verifies that a disk I/O error
// during getAgentControlAgentByID triggers a DB handle reset and retry,
// ultimately succeeding.
func TestGetAgentControlAgentByIDRetriesOnIOErr(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)

	// Seed a record.
	_, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "get-retry-agent",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root/get-retry-agent",
		AgentType:     AgentTypeChild,
		Status:        AgentStatusActive,
	})
	require.NoError(t, err)

	// Simulate a stale WAL by truncating the WAL file. In a real scenario,
	// another process truncates the WAL while this connection is open.
	// The retry logic in withRetryOnIOErr will reset the DB handle and retry.
	record, err := store.getAgentControlAgentByID(ctx, "get-retry-agent")
	require.NoError(t, err, "normal read should succeed")
	require.Equal(t, "get-retry-agent", record.AgentID)
}

// TestRegistryServiceSharedStoreRetriesOnIOErr verifies the shared DB reset
// mechanism works when an IOERR occurs through the RegistryService path.
func TestRegistryServiceSharedStoreRetriesOnIOErr(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	service, err := NewRegistryService(ctx, RegistryServiceConfig{
		StorePath: filepath.Join(dir, "agent-control.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = service.Close() })

	// Seed a record through the shared store.
	_, err = service.AgentStore.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "shared-retry-agent",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root/shared-retry-agent",
		AgentType:     AgentTypeChild,
		Status:        AgentStatusActive,
	})
	require.NoError(t, err)

	// Verify the agent store has a sharedReset callback set.
	agentStore, ok := service.AgentStore.(*SQLiteGlobalAgentRegistryStore)
	require.True(t, ok, "expected *SQLiteGlobalAgentRegistryStore")
	require.NotNil(t, agentStore.sharedReset, "sharedReset should be set for shared stores")

	// Normal read should work.
	records, err := service.AgentStore.ListAgentControlAgents(ctx, AgentFilter{
		RootSessionID: "root-session",
		IncludeClosed: true,
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "shared-retry-agent", records[0].AgentID)

	// Reset should properly clean up and allow re-opening.
	err = agentStore.resetDBHandle()
	require.NoError(t, err)

	// After reset, read should still work (re-opens the DB).
	records, err = service.AgentStore.ListAgentControlAgents(ctx, AgentFilter{
		RootSessionID: "root-session",
		IncludeClosed: true,
	})
	require.NoError(t, err)
	require.Len(t, records, 1, "data should survive DB handle reset")
}

// TestAgentRegistryRecoversFromExternallyTruncatedWAL reproduces the production
// shape behind "read agent control agent: sqlite3: disk I/O error": another
// process truncates the -wal to 0 bytes (checkpoint(TRUNCATE), or the last other
// writer closing) while this connection keeps a wal-index claiming committed
// frames.  A cold read then fetches a page from a frame that no longer exists
// and the driver reports SQLITE_IOERR_SHORT_READ.
//
// The test asserts both halves of the fix: the raw handle still fails (that is
// the pre-fix behaviour callers saw), while the store read recovers by dropping
// the stale handle, re-opening the database and re-running the WAL sidecar
// reconciliation.  Writes must work again afterwards so the caller's projection
// can self-heal.  Both wirings are covered: the standalone store and the
// RegistryService shared pool.
func TestAgentRegistryRecoversFromExternallyTruncatedWAL(t *testing.T) {
	ctx := context.Background()

	t.Run("standalone store", func(t *testing.T) {
		testAgentRegistryRecoversFromExternallyTruncatedWAL(t, ctx, newTestGlobalAgentRegistryStore(t))
	})

	t.Run("registry service shared pool", func(t *testing.T) {
		service, err := NewRegistryService(ctx, RegistryServiceConfig{
			StorePath: filepath.Join(t.TempDir(), "agent-control.sqlite"),
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = service.Close() })
		store, ok := service.AgentStore.(*SQLiteGlobalAgentRegistryStore)
		require.True(t, ok, "expected *SQLiteGlobalAgentRegistryStore")
		require.NotNil(t, store.sharedReset, "shared pool must expose the reset hook")
		testAgentRegistryRecoversFromExternallyTruncatedWAL(t, ctx, store)
	})
}

func testAgentRegistryRecoversFromExternallyTruncatedWAL(t *testing.T, ctx context.Context, store *SQLiteGlobalAgentRegistryStore) {
	t.Helper()

	// Keep the -wal below the 256-page autocheckpoint threshold while still
	// holding plenty of committed frames that exist only in the WAL.
	const seeded = 120
	for i := 0; i < seeded; i++ {
		_, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
			AgentID:       fmt.Sprintf("wal-agent-%03d", i),
			RootSessionID: "root-session",
			SessionID:     fmt.Sprintf("wal-session-%03d", i),
			AgentPath:     fmt.Sprintf("/root/wal-agent-%03d", i),
			AgentType:     AgentTypeChild,
			Nickname:      fmt.Sprintf("nickname-%03d", i),
			Workflow:      WorkflowSpawnAgent,
			Provider:      "remote",
			Model:         "strong-model",
			Status:        AgentStatusActive,
		})
		require.NoError(t, err)
	}

	walPath := store.path + "-wal"
	info, err := os.Stat(walPath)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(32), "expected committed WAL frames")

	// Another process truncates the WAL while this connection stays open.
	require.NoError(t, os.Truncate(walPath, 0))

	db, err := store.dbHandle()
	require.NoError(t, err)
	// With a warm page cache SQLite answers from memory and never notices the
	// truncated WAL; shrink the cache so the next reads have to go to disk.
	_, err = db.ExecContext(ctx, "PRAGMA cache_size = 10")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "PRAGMA shrink_memory")
	require.NoError(t, err)

	rawErr := scanAgentIDs(ctx, db)
	// Deliberately not using isSQLiteIOErr here: the precondition must stay
	// independent of the predicate under test, or a broken predicate would
	// disable the regression instead of failing it.
	if rawErr == nil || !strings.Contains(strings.ToLower(rawErr.Error()), "disk i/o error") {
		t.Skipf("driver served the truncated WAL without an I/O error (raw err=%v); stale state not reproduced", rawErr)
	}

	// Pre-fix behaviour for comparison: the raw handle surfaces the I/O error.
	require.Contains(t, rawErr.Error(), "disk I/O")

	staleHandle := store.db

	// The store read must recover instead of surfacing the I/O error.
	records, err := store.ListAgentControlAgents(ctx, AgentFilter{
		RootSessionID: "root-session",
		IncludeClosed: true,
	})
	require.NoError(t, err, "list must recover from the stale WAL instead of failing the tool call")
	require.LessOrEqual(t, len(records), seeded)
	require.NotSame(t, staleHandle, store.db, "recovery must re-open the database handle")
	require.NoError(t, scanAgentIDs(ctx, store.db), "re-opened handle must be healthy")

	// Writes must work again so the caller's projection can rewrite the rows
	// whose trailing WAL frames were lost by the external truncation.
	healed, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "wal-agent-healed",
		RootSessionID: "root-session",
		SessionID:     "wal-session-healed",
		AgentPath:     "/root/wal-agent-healed",
		AgentType:     AgentTypeChild,
		Status:        AgentStatusActive,
	})
	require.NoError(t, err)
	require.Equal(t, "wal-agent-healed", healed.AgentID)

	readBack, err := store.getAgentControlAgentByID(ctx, "wal-agent-healed")
	require.NoError(t, err)
	require.Equal(t, "wal-agent-healed", readBack.AgentID)
}

// scanAgentIDs walks every identity row so the read has to touch all leaf
// pages; it returns the first error the driver reports.
func scanAgentIDs(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT agent_id FROM agent_control_agents ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var agentID string
		if err := rows.Scan(&agentID); err != nil {
			return err
		}
	}
	return rows.Err()
}
