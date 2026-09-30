package subagentbatch

import (
	"context"
	"path/filepath"
	"testing"
)

// stubAgentSessionResolver maps child session id -> terminal. Presence in the
// map means the durable control plane has a row (found=true); an absent id
// models "no durable row" (skipped by the settle predicate).
type stubAgentSessionResolver map[string]bool

func (s stubAgentSessionResolver) AgentSessionTerminal(_ context.Context, sessionID string) (bool, bool, error) {
	terminal, found := s[sessionID]
	return terminal, found, nil
}

func newAgentSessionTestStore(t *testing.T) BatchStore {
	t.Helper()
	store, err := NewSQLiteBatchStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "batches.db")})
	if err != nil {
		t.Fatalf("NewSQLiteBatchStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestTurnObligationsSettledWithAgentSessions pins the child-session half of the
// settle predicate: a parked turn that dispatched spawn_agent children may only
// clear once every listed child reached a terminal control-plane state, a child
// without a lifecycle row (still running) blocks the settle instead of being
// skipped, and an unwired resolver keeps the turn parked (never clear on a guess).
func TestTurnObligationsSettledWithAgentSessions(t *testing.T) {
	ctx := context.Background()
	store := newAgentSessionTestStore(t)
	seedSettledBatch(t, store, "batch-done", BatchCompleted)
	seedSettledBatch(t, store, "batch-running", BatchRunning)

	resolver := stubAgentSessionResolver{
		"child-active": false,
		"child-closed": true,
	}

	cases := []struct {
		name     string
		record   *TurnSuspension
		resolver AgentSessionObligationResolver
		want     bool
	}{
		{
			name: "running child keeps the turn parked",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{AgentSessionObligationID("child-active")},
			},
			resolver: resolver,
			want:     false,
		},
		{
			name: "terminal child settles the turn",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{AgentSessionObligationID("child-closed")},
			},
			resolver: resolver,
			want:     true,
		},
		{
			name: "unknown child id is not evidence",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{AgentSessionObligationID("child-missing")},
			},
			resolver: resolver,
			want:     false,
		},
		{
			name: "a child without a lifecycle row blocks the settle even when a sibling is terminal",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{
					AgentSessionObligationID("child-closed"),
					AgentSessionObligationID("child-missing"),
				},
			},
			resolver: resolver,
			want:     false,
		},
		{
			name: "unwired resolver keeps the turn parked",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{AgentSessionObligationID("child-closed")},
			},
			resolver: nil,
			want:     false,
		},
		{
			name: "batch terminal but child still running keeps the turn parked",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{
					"batch-done",
					AgentSessionObligationID("child-active"),
				},
				ResumeQueue: []string{"batch-done"},
			},
			resolver: resolver,
			want:     false,
		},
		{
			name: "batch terminal and child terminal settles the turn",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{
					"batch-done",
					AgentSessionObligationID("child-closed"),
				},
				ResumeQueue: []string{"batch-done"},
			},
			resolver: resolver,
			want:     true,
		},
		{
			name: "batch running keeps the turn parked even when the child is terminal",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{
					"batch-running",
					AgentSessionObligationID("child-closed"),
				},
				ResumeQueue: []string{"batch-running"},
			},
			resolver: resolver,
			want:     false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := TurnObligationsSettledWith(ctx, store, tc.record, tc.resolver)
			if err != nil {
				t.Fatalf("TurnObligationsSettledWith: %v", err)
			}
			if got != tc.want {
				t.Fatalf("TurnObligationsSettledWith = %v, want %v", got, tc.want)
			}
		})
	}

	// The batch-only entry point must stay conservative for new-style records.
	agentOnly := cases[1].record
	if settled, err := TurnObligationsSettled(ctx, store, agentOnly); err != nil || settled {
		t.Fatalf("TurnObligationsSettled(agent-only, nil resolver) = (%v, %v), want (false, nil)", settled, err)
	}
}

// TestTurnObligationsAllTerminal pins the strict sibling the settlement wake
// uses: unlike the settle predicate, an id without a durable row blocks the
// verdict — a running spawn_agent child has no lifecycle row yet, so a skipped
// id here would resume the parent while its cohort is still working.
func TestTurnObligationsAllTerminal(t *testing.T) {
	ctx := context.Background()
	store := newAgentSessionTestStore(t)
	seedSettledBatch(t, store, "batch-done", BatchCompleted)
	seedSettledBatch(t, store, "batch-running", BatchRunning)

	resolver := stubAgentSessionResolver{
		"child-active": false,
		"child-closed": true,
	}
	cases := []struct {
		name     string
		record   *TurnSuspension
		resolver AgentSessionObligationResolver
		want     bool
	}{
		{
			name: "every listed child terminal",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{AgentSessionObligationID("child-closed")},
			},
			resolver: resolver,
			want:     true,
		},
		{
			name: "a child without a durable row blocks the verdict",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{
					AgentSessionObligationID("child-closed"),
					AgentSessionObligationID("child-missing"),
				},
			},
			resolver: resolver,
			want:     false,
		},
		{
			name: "a running child blocks the verdict",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{AgentSessionObligationID("child-active")},
			},
			resolver: resolver,
			want:     false,
		},
		{
			name: "terminal batch plus terminal child",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{
					"batch-done",
					AgentSessionObligationID("child-closed"),
				},
			},
			resolver: resolver,
			want:     true,
		},
		{
			name: "running batch blocks the verdict",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{
					"batch-running",
					AgentSessionObligationID("child-closed"),
				},
			},
			resolver: resolver,
			want:     false,
		},
		{
			name: "unwired resolver blocks the verdict",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{AgentSessionObligationID("child-closed")},
			},
			resolver: nil,
			want:     false,
		},
		{
			name:     "record without obligations never wakes",
			record:   &TurnSuspension{TurnID: "t1", SessionID: "s1"},
			resolver: resolver,
			want:     false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := TurnObligationsAllTerminal(ctx, store, tc.record, tc.resolver)
			if err != nil {
				t.Fatalf("TurnObligationsAllTerminal: %v", err)
			}
			if got != tc.want {
				t.Fatalf("TurnObligationsAllTerminal = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBuildWaitLedgerWithAgentSessionRows pins the ledger view: batch rows keep
// their shape, child-session rows are attributed to their own subject kind, and
// an unknown child id (or an unwired resolver) is reported as missing and
// non-terminal so wait guidance keeps the parent from finalizing.
func TestBuildWaitLedgerWithAgentSessionRows(t *testing.T) {
	ctx := context.Background()
	store := newAgentSessionTestStore(t)
	seedSettledBatch(t, store, "batch-running", BatchRunning)

	record := &TurnSuspension{
		TurnID: "t1", SessionID: "s1",
		ObligationIDs: []string{
			"batch-running",
			AgentSessionObligationID("child-active"),
			AgentSessionObligationID("child-missing"),
		},
		ResumeQueue: []string{"batch-running"},
	}
	resolver := stubAgentSessionResolver{"child-active": false}

	rows, err := BuildWaitLedgerWith(ctx, store, record, resolver)
	if err != nil {
		t.Fatalf("BuildWaitLedgerWith: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (%+v)", len(rows), rows)
	}
	if rows[0].SubjectKind != "batch" || rows[0].Terminal {
		t.Fatalf("batch row = %+v, want non-terminal batch row", rows[0])
	}
	if rows[1].ObligationID != AgentSessionObligationID("child-active") ||
		rows[1].SubjectKind != "agent_session" || rows[1].SubjectID != "child-active" ||
		rows[1].State != "active" || rows[1].Terminal {
		t.Fatalf("active child row = %+v", rows[1])
	}
	if rows[2].State != WaitLedgerStateMissing || rows[2].Terminal {
		t.Fatalf("missing child row = %+v, want missing/non-terminal", rows[2])
	}

	// Without a resolver every child-session row stays missing/pending.
	legacy, err := BuildWaitLedger(ctx, store, record)
	if err != nil {
		t.Fatalf("BuildWaitLedger: %v", err)
	}
	if len(legacy) != 3 {
		t.Fatalf("legacy rows = %d, want 3", len(legacy))
	}
	for _, row := range legacy[1:] {
		if row.State != WaitLedgerStateMissing || row.Terminal {
			t.Fatalf("legacy child row = %+v, want missing/non-terminal", row)
		}
	}
}
