package agentcontrol

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Retention query plan + scale evidence (plan §P2-9 registry retention).
//
// The purge is the only housekeeping that walks terminal registry rows on every
// reconcile pass. Its predicate must stay usable by the index retention keeps
// for exactly this job (idx_agent_control_agents_closed_at): comparing the
// column through datetime() hides the aged order from the planner, so the range
// can still be index-served while the batch sorts its hits through a temp
// b-tree — the sort the index was added to avoid. These tests pin the
// index-usable shape of both statements (the named index behind a SEARCH, no
// temp b-tree) and measure one batch at the "long-lived deployment" size the
// design assumes (~10k terminal rows).

// purgeStatementPlan renders the plan SQLite would run for one purge statement.
func purgeStatementPlan(t *testing.T, store *SQLiteGlobalAgentRegistryStore, statement string, args ...interface{}) string {
	t.Helper()
	require.NoError(t, store.ensure(), "the schema must exist before a statement can be explained")
	rows, err := store.db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+statement, args...)
	require.NoError(t, err)
	defer rows.Close()
	details := make([]string, 0, 4)
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		details = append(details, detail)
	}
	require.NoError(t, rows.Err())
	return strings.Join(details, " | ")
}

// seedAgedTerminalRows fills the fixture with raw SQL: these tests care about
// row counts and ages, not about the identity rules UpsertAgentControlAgent
// enforces (which would append a wake event per row and dominate the fixture).
func seedAgedTerminalRows(t testing.TB, store *SQLiteGlobalAgentRegistryStore, from, rows int, aged time.Time) {
	t.Helper()
	require.NoError(t, store.ensure(), "the fixture writes rows through the store's own connection")
	tx, err := store.db.Begin()
	require.NoError(t, err)
	agentStmt, err := tx.Prepare(`
		INSERT INTO agent_control_agents (
			agent_id, root_session_id, agent_path, depth, status, created_at, updated_at, closed_at
		) VALUES (?, ?, ?, 1, ?, ?, ?, ?)`)
	require.NoError(t, err)
	defer func() { _ = agentStmt.Close() }()
	wakeStmt, err := tx.Prepare(`
		INSERT INTO agent_control_agent_wake_events (
			agent_id, root_session_id, agent_path, depth, status, event_kind, created_at
		) VALUES (?, ?, ?, 1, ?, 'upsert', ?)`)
	require.NoError(t, err)
	defer func() { _ = wakeStmt.Close() }()
	stamp := formatAgentTime(aged)
	for i := from; i < from+rows; i++ {
		agentID := agentIDForPurgeFixture(i)
		path := "/root/" + agentID
		_, err = agentStmt.Exec(agentID, "purge-root", path, AgentStatusClosed, stamp, stamp, stamp)
		require.NoError(t, err)
		_, err = wakeStmt.Exec(agentID, "purge-root", path, AgentStatusClosed, stamp)
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
}

func agentIDForPurgeFixture(index int) string {
	return fmt.Sprintf("purge-agent-%010d", index)
}

func TestPurgeStatementsStayIndexUsable(t *testing.T) {
	store := newTestGlobalAgentRegistryStore(t)
	aged := time.Now().UTC().Add(-DefaultTerminalRetention - time.Hour)
	seedAgedTerminalRows(t, store, 0, 2000, aged)
	_, err := store.db.ExecContext(context.Background(), "ANALYZE")
	require.NoError(t, err)
	cutoff := formatAgentTime(time.Now().UTC().Add(-DefaultTerminalRetention))

	// The cutoff is bound twice on purpose: once for the sargable range and once
	// for the datetime() age re-check the statement keeps as its authority.
	rowPlan := purgeStatementPlan(t, store, purgeTerminalAgentsSQL, cutoff, cutoff, terminalPurgeBatch)
	require.Contains(t, rowPlan, "SEARCH agent_control_agents USING",
		"retention rows must be deleted through an index range, got plan: %s", rowPlan)
	require.Contains(t, rowPlan, "idx_agent_control_agents_closed_at",
		"the aged row order must come from the retention index, not another access path, got plan: %s", rowPlan)
	require.NotContains(t, rowPlan, "TEMP B-TREE",
		"closed_at ASC, id ASC must come from the index instead of a sort, got plan: %s", rowPlan)

	wakePlan := purgeStatementPlan(t, store, purgeAgentWakeEventsSQL, cutoff, cutoff, terminalPurgeBatch)
	require.Contains(t, wakePlan, "SEARCH agent_control_agent_wake_events USING",
		"retention wake events must be deleted through an index range, got plan: %s", wakePlan)
	require.Contains(t, wakePlan, "idx_agent_control_agent_wake_created_at",
		"the aged event order must come from the retention index, not another access path, got plan: %s", wakePlan)
	require.NotContains(t, wakePlan, "TEMP B-TREE",
		"created_at ASC, id ASC must come from the index instead of a sort, got plan: %s", wakePlan)
	t.Logf("rows plan: %s", rowPlan)
	t.Logf("wake plan: %s", wakePlan)
}

// BenchmarkPurgeTerminalAgentBatch10k measures one bounded purge batch (512
// rows) on a registry holding ~10k aged terminal rows. The fixture is refilled
// outside the timed section, so every iteration measures the statement against a
// full table instead of draining it; Go benchmarks assert nothing, so this is
// the recorded evidence behind the "single pass stays bounded at ten thousand
// rows" claim rather than a regression gate.
func BenchmarkPurgeTerminalAgentBatch10k(b *testing.B) {
	store := newBenchmarkAgentRegistryStore(b)
	ctx := context.Background()
	aged := time.Now().UTC().Add(-DefaultTerminalRetention - time.Hour)
	seedAgedTerminalRows(b, store, 0, 10000, aged)
	if _, err := store.db.ExecContext(ctx, "ANALYZE"); err != nil {
		b.Fatalf("analyze agent registry: %v", err)
	}
	cutoff := time.Now().UTC().Add(-DefaultTerminalRetention)
	next := 10000
	purged := int64(0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		seedAgedTerminalRows(b, store, next, terminalPurgeBatch, aged)
		next += terminalPurgeBatch
		b.StartTimer()
		count, err := store.PurgeAgentControlTerminalAgents(ctx, cutoff, terminalPurgeBatch)
		if err != nil {
			b.Fatalf("purge terminal agents: %v", err)
		}
		purged += count
	}
	b.StopTimer()
	if b.N > 0 {
		b.ReportMetric(float64(purged)/float64(b.N), "rows/batch")
	}
}

func newBenchmarkAgentRegistryStore(b *testing.B) *SQLiteGlobalAgentRegistryStore {
	b.Helper()
	store, err := NewSQLiteGlobalAgentRegistryStore(&GlobalAgentStoreConfig{
		Path: filepath.Join(b.TempDir(), "agent-registry-bench.db"),
	})
	if err != nil {
		b.Fatalf("open agent registry store: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })
	return store
}

// TestPurgeLeavesUndatableTimestampsAlone pins the half of the predicate the
// ordering rewrite must not weaken: an unparsable closed_at is kept even when it
// sorts below the cutoff, because the datetime() re-check is the authority on
// age. Without it, the newly sargable range would hand a hand-edited or legacy
// row (and its wake event) to the purge just for looking old as a string.
func TestPurgeLeavesUndatableTimestampsAlone(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)
	require.NoError(t, store.ensure())
	now := time.Now().UTC()
	aged := now.Add(-DefaultTerminalRetention - time.Hour)
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO agent_control_agents (
			agent_id, root_session_id, agent_path, depth, status, created_at, updated_at, closed_at
		) VALUES (?, ?, ?, 1, ?, ?, ?, ?)
	`, "undatable-agent", "purge-root", "/root/undatable", AgentStatusClosed,
		formatAgentTime(aged), formatAgentTime(now), "0000-00-00T00:00:00Z")
	require.NoError(t, err)
	// The same shape in the wake log: the conservative half of the predicate has
	// to hold on both statements, or a legacy row keeps its row and loses its
	// event.
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO agent_control_agent_wake_events (
			agent_id, root_session_id, agent_path, depth, status, event_kind, created_at
		) VALUES (?, ?, ?, 1, ?, 'upsert', ?)
	`, "undatable-agent", "purge-root", "/root/undatable", AgentStatusClosed, "0000-00-00T00:00:00Z")
	require.NoError(t, err)
	seedAgedTerminalRows(t, store, 0, 1, aged)

	outcome, err := PurgeTerminalAgentRecords(ctx, store, TerminalPurgePolicy{
		Retention: DefaultTerminalRetention,
		Now:       now,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), outcome.Rows, "only the aged row with a parsable timestamp may go")
	require.Equal(t, int64(1), outcome.WakeEvents, "only the aged event with a parsable timestamp may go")

	records, err := store.ListAgentControlAgents(ctx, AgentFilter{IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "undatable-agent", records[0].AgentID)

	var wakeAgentID string
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT agent_id FROM agent_control_agent_wake_events`).Scan(&wakeAgentID))
	require.Equal(t, "undatable-agent", wakeAgentID,
		"the undatable event must outlive the pass that removed every datable one")
}
