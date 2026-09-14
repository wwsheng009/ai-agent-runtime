package agentcontrol

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Registry retention (G2): terminal rows and the wake-event log had no pruning
// path, so a long-lived deployment grew forever. These tests pin the semantics
// the two hosts rely on: 0 → default window, negative → keep forever, only
// already-terminal rows are deletable, and one pass stays bounded.
func TestNormalizeTerminalRetentionSemantics(t *testing.T) {
	require.Equal(t, DefaultTerminalRetention, NormalizeTerminalRetention(0), "unset uses the shared default")
	require.Equal(t, time.Duration(0), NormalizeTerminalRetention(-time.Hour), "negative keeps rows forever")
	require.Equal(t, 7*24*time.Hour, NormalizeTerminalRetention(7*24*time.Hour))
}

func TestParseTerminalRetentionAcceptsOffAndDurations(t *testing.T) {
	window, ok := ParseTerminalRetention("off")
	require.True(t, ok)
	require.Equal(t, time.Duration(0), NormalizeTerminalRetention(window))

	window, ok = ParseTerminalRetention("none")
	require.True(t, ok)
	require.Equal(t, time.Duration(0), NormalizeTerminalRetention(window))

	window, ok = ParseTerminalRetention("12h")
	require.True(t, ok)
	require.Equal(t, 12*time.Hour, NormalizeTerminalRetention(window))

	window, ok = ParseTerminalRetention("default")
	require.True(t, ok)
	require.Equal(t, DefaultTerminalRetention, NormalizeTerminalRetention(window))

	_, ok = ParseTerminalRetention("not-a-duration")
	require.False(t, ok, "a malformed override must fall back to the next precedence level")

	_, ok = ParseTerminalRetention("")
	require.False(t, ok)
}

// A disabled window (or a store that cannot prune) must be a silent no-op: both
// hosts wire the hook unconditionally, so "nothing configured" cannot be an
// error and cannot reach the store.
func TestPurgeTerminalAgentRecordsNoOpsWhenDisabledOrUnsupported(t *testing.T) {
	ctx := context.Background()

	outcome, err := PurgeTerminalAgentRecords(ctx, nil, TerminalPurgePolicy{Retention: DefaultTerminalRetention})
	require.NoError(t, err)
	require.False(t, outcome.Supported)
	require.Zero(t, outcome.Rows)

	store := newTestGlobalAgentRegistryStore(t)
	_, err = store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "aged-agent",
		RootSessionID: "root-1",
		AgentPath:     "/root/aged",
		SessionID:     "sess-aged",
		AgentType:     AgentTypeChild,
		Status:        AgentStatusClosed,
	})
	require.NoError(t, err)

	outcome, err = PurgeTerminalAgentRecords(ctx, store, TerminalPurgePolicy{Retention: 0})
	require.NoError(t, err)
	require.False(t, outcome.Supported, "a disabled window reports unsupported instead of deleting")

	records, err := store.ListAgentControlAgents(ctx, AgentFilter{IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, records, 1)
}

// The core rule: age decides, not status alone. A live row and an in-window
// terminal row survive; the out-of-window terminal row and its wake event go.
func TestPurgeTerminalAgentRecordsPrunesOnlyAgedTerminalRows(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)
	now := time.Now().UTC()
	aged := now.Add(-45 * 24 * time.Hour)
	fresh := now.Add(-2 * time.Hour)

	_, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "live-agent",
		RootSessionID: "root-1",
		AgentPath:     "/root/live",
		SessionID:     "sess-live",
		AgentType:     AgentTypeChild,
		Status:        AgentStatusActive,
	})
	require.NoError(t, err)
	_, err = store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "aged-agent",
		RootSessionID: "root-1",
		AgentPath:     "/root/aged",
		SessionID:     "sess-aged",
		AgentType:     AgentTypeChild,
		Status:        AgentStatusClosed,
		ClosedAt:      &aged,
	})
	require.NoError(t, err)
	_, err = store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "fresh-agent",
		RootSessionID: "root-1",
		AgentPath:     "/root/fresh",
		SessionID:     "sess-fresh",
		AgentType:     AgentTypeChild,
		Status:        AgentStatusStale,
		ClosedAt:      &fresh,
	})
	require.NoError(t, err)

	// The wake-event log is append-only, so the test backdates two of its rows
	// directly: retention must judge them by created_at, exactly like the rows.
	insertWake := func(createdAt time.Time) {
		t.Helper()
		_, execErr := store.db.ExecContext(ctx, `
			INSERT INTO agent_control_agent_wake_events (agent_id, root_session_id, agent_path, event_kind, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, "aged-agent", "root-1", "/root/aged", "closed", formatAgentTime(createdAt))
		require.NoError(t, execErr)
	}
	insertWake(aged)
	insertWake(fresh)

	outcome, err := PurgeTerminalAgentRecords(ctx, store, TerminalPurgePolicy{
		Now:       now,
		Retention: DefaultTerminalRetention,
	})
	require.NoError(t, err)
	require.True(t, outcome.Supported)
	require.EqualValues(t, 1, outcome.Rows)
	require.EqualValues(t, 1, outcome.WakeEvents)

	records, err := store.ListAgentControlAgents(ctx, AgentFilter{IncludeClosed: true})
	require.NoError(t, err)
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.AgentID)
	}
	require.Contains(t, ids, "live-agent", "retention must never touch a row that still holds quota")
	require.Contains(t, ids, "fresh-agent", "an in-window terminal row is still diagnostics")
	require.NotContains(t, ids, "aged-agent")

	// Upserts append their own (in-window) wake events, so the invariant to pin
	// is "nothing older than the window survives", not a raw row count.
	var agedWakeCount, wakeCount int
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM agent_control_agent_wake_events
		WHERE datetime(created_at) < datetime(?)
	`, formatAgentTime(now.Add(-DefaultTerminalRetention))).Scan(&agedWakeCount))
	require.Zero(t, agedWakeCount, "no wake event may outlive the retention window")
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_control_agent_wake_events`).Scan(&wakeCount))
	require.Greater(t, wakeCount, 0, "in-window wake events stay available to watchers")
}

// One pass is bounded: batching keeps a neglected registry from holding a huge
// write transaction, and repeated passes still converge it completely.
func TestPurgeTerminalAgentRecordsConvergesInBatches(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)
	now := time.Now().UTC()
	aged := now.Add(-45 * 24 * time.Hour)
	for _, id := range []string{"aged-1", "aged-2", "aged-3", "aged-4", "aged-5"} {
		_, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
			AgentID:       id,
			RootSessionID: "root-1",
			AgentPath:     "/root/" + id,
			SessionID:     "sess-" + id,
			AgentType:     AgentTypeChild,
			Status:        AgentStatusClosed,
			ClosedAt:      &aged,
		})
		require.NoError(t, err)
	}

	outcome, err := PurgeTerminalAgentRecords(ctx, store, TerminalPurgePolicy{
		Now:        now,
		Retention:  DefaultTerminalRetention,
		BatchLimit: 2,
	})
	require.NoError(t, err)
	require.EqualValues(t, 5, outcome.Rows)
	require.Equal(t, 3, outcome.Batches)

	records, err := store.ListAgentControlAgents(ctx, AgentFilter{IncludeClosed: true})
	require.NoError(t, err)
	require.Empty(t, records)
}

func TestPurgeTerminalAgentRecordsHonorsMaxBatches(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)
	now := time.Now().UTC()
	aged := now.Add(-45 * 24 * time.Hour)
	for _, id := range []string{"aged-1", "aged-2", "aged-3", "aged-4", "aged-5"} {
		_, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
			AgentID:       id,
			RootSessionID: "root-1",
			AgentPath:     "/root/" + id,
			SessionID:     "sess-" + id,
			AgentType:     AgentTypeChild,
			Status:        AgentStatusClosed,
			ClosedAt:      &aged,
		})
		require.NoError(t, err)
	}

	outcome, err := PurgeTerminalAgentRecords(ctx, store, TerminalPurgePolicy{
		Now:        now,
		Retention:  DefaultTerminalRetention,
		BatchLimit: 1,
		MaxBatches: 2,
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, outcome.Rows)
	require.Equal(t, 2, outcome.Batches)

	records, err := store.ListAgentControlAgents(ctx, AgentFilter{IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, records, 3, "the remainder waits for the next cadence instead of blocking this pass")
}

// The pass folds retention into the same report the hosts render, and a purge
// failure must not blank an audit result the operator is looking at.
func TestReconcilerFoldsPurgeIntoReport(t *testing.T) {
	store := newTestGlobalAgentRegistryStore(t)
	purgeCalls := 0
	reconciler := &Reconciler{
		Store:  store,
		Lookup: sessionLookupFromSnapshot(nil),
		Purge: func(context.Context, time.Time) (TerminalPurgeOutcome, error) {
			purgeCalls++
			return TerminalPurgeOutcome{Supported: true, Rows: 3, WakeEvents: 4}, nil
		},
	}

	report, err := reconciler.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, purgeCalls)
	require.EqualValues(t, 3, report.PurgedRows)
	require.EqualValues(t, 4, report.PurgedWakeEvents)
	require.Empty(t, report.PurgeError)

	summary := reconciler.ReconcileSummary()
	require.True(t, strings.Contains(summary, "purged_rows=3"), summary)
	require.True(t, strings.Contains(summary, "purged_wake_events=4"), summary)
}

func TestReconcilerRecordsPurgeFailureWithoutBlankingTheReport(t *testing.T) {
	store := newTestGlobalAgentRegistryStore(t)
	reconciler := &Reconciler{
		Store:  store,
		Lookup: sessionLookupFromSnapshot(nil),
		Purge: func(context.Context, time.Time) (TerminalPurgeOutcome, error) {
			return TerminalPurgeOutcome{}, errors.New("disk full")
		},
	}

	report, err := reconciler.RunOnce(context.Background())
	require.NoError(t, err, "housekeeping failures must not fail the audit pass")
	require.Contains(t, report.PurgeError, "disk full")
	require.Contains(t, reconciler.ReconcileSummary(), "purge_error=disk full")
}
