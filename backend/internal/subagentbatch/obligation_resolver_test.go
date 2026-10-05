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

// stubTeamResolver maps team id -> terminal. Presence in the map means the
// durable control plane has a team row (found=true); an absent id models "no
// durable row" (kept strict: never evidence of completion).
type stubTeamResolver map[string]bool

func (s stubTeamResolver) TeamTerminal(_ context.Context, teamID string) (bool, bool, error) {
	terminal, found := s[teamID]
	return terminal, found, nil
}

// TestTeamObligationEncoding pins the team: obligation encoding plus the rule
// that batch readers never mistake a team/child id for a batch id.
func TestTeamObligationEncoding(t *testing.T) {
	if got := TeamObligationID("t-1"); got != "team:t-1" {
		t.Fatalf("TeamObligationID = %q, want team:t-1", got)
	}
	if !IsTeamObligation(TeamObligationID("t-1")) || IsTeamObligation(AgentSessionObligationID("c-1")) || IsTeamObligation("batch-1") {
		t.Fatal("IsTeamObligation must match only team: prefixed ids")
	}
	if got := TeamIDFromObligation(TeamObligationID(" t-1 ")); got != "t-1" {
		t.Fatalf("TeamIDFromObligation = %q, want t-1", got)
	}
	if got := TeamIDFromObligation("batch-1"); got != "" {
		t.Fatalf("TeamIDFromObligation(batch) = %q, want empty", got)
	}

	record := &TurnSuspension{ObligationIDs: []string{
		"batch-1",
		AgentSessionObligationID("c-1"),
		TeamObligationID("t-1"),
		TeamObligationID("t-2"),
	}}
	if got := record.ObligationTeamIDs(); len(got) != 2 || got[0] != "t-1" || got[1] != "t-2" {
		t.Fatalf("ObligationTeamIDs = %v, want [t-1 t-2]", got)
	}

	// Fallback batch resolution must skip both prefixed kinds.
	fallback := &TurnSuspension{ObligationIDs: []string{AgentSessionObligationID("c-1"), TeamObligationID("t-1")}}
	if got := fallback.ObligationBatchIDs(); len(got) != 0 {
		t.Fatalf("ObligationBatchIDs(prefixed-only) = %v, want empty", got)
	}
	plain := &TurnSuspension{ObligationIDs: []string{TeamObligationID("t-1"), "batch-9"}}
	if got := plain.ObligationBatchIDs(); len(got) != 1 || got[0] != "batch-9" {
		t.Fatalf("ObligationBatchIDs = %v, want [batch-9]", got)
	}
}

// TestTurnObligationsSettledWithTeams pins the team half of the settle
// predicate: a parked turn on one team run may only clear once the team row is
// durable and terminal; a missing row or unwired resolver keeps it parked.
func TestTurnObligationsSettledWithTeams(t *testing.T) {
	ctx := context.Background()
	store := newAgentSessionTestStore(t)
	seedSettledBatch(t, store, "batch-done", BatchCompleted)
	seedSettledBatch(t, store, "batch-running", BatchRunning)

	agent := stubAgentSessionResolver{
		"child-closed": true,
		"child-active": false,
	}
	teams := stubTeamResolver{
		"team-done":    true,
		"team-running": false,
	}

	cases := []struct {
		name     string
		record   *TurnSuspension
		resolver AgentSessionObligationResolver
		teams    TeamObligationResolver
		want     bool
	}{
		{
			name: "terminal team settles the turn",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{TeamObligationID("team-done")},
			},
			resolver: agent, teams: teams, want: true,
		},
		{
			name: "running team keeps the turn parked",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{TeamObligationID("team-running")},
			},
			resolver: agent, teams: teams, want: false,
		},
		{
			name: "missing team row is not evidence",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{TeamObligationID("team-missing")},
			},
			resolver: agent, teams: teams, want: false,
		},
		{
			name: "unwired team resolver keeps the turn parked",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{TeamObligationID("team-done")},
			},
			resolver: agent, teams: nil, want: false,
		},
		{
			name: "mixed batch+child+team all terminal settles",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{
					"batch-done",
					AgentSessionObligationID("child-closed"),
					TeamObligationID("team-done"),
				},
				ResumeQueue: []string{"batch-done"},
			},
			resolver: agent, teams: teams, want: true,
		},
		{
			name: "mixed with a running team keeps the turn parked",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{
					"batch-done",
					AgentSessionObligationID("child-closed"),
					TeamObligationID("team-running"),
				},
				ResumeQueue: []string{"batch-done"},
			},
			resolver: agent, teams: teams, want: false,
		},
		{
			name: "mixed with a running batch keeps the turn parked even when team is terminal",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{
					"batch-running",
					TeamObligationID("team-done"),
				},
				ResumeQueue: []string{"batch-running"},
			},
			resolver: agent, teams: teams, want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := TurnObligationsSettledWithTeams(ctx, store, tc.record, tc.resolver, tc.teams)
			if err != nil {
				t.Fatalf("TurnObligationsSettledWithTeams: %v", err)
			}
			if got != tc.want {
				t.Fatalf("TurnObligationsSettledWithTeams = %v, want %v", got, tc.want)
			}
		})
	}

	// The legacy entry points must stay conservative on team obligations:
	// clearing a parked turn on a team id with no team resolver is a guess.
	teamOnly := cases[0].record
	if settled, err := TurnObligationsSettledWith(ctx, store, teamOnly, agent); err != nil || settled {
		t.Fatalf("TurnObligationsSettledWith(team-only, nil team resolver) = (%v, %v), want (false, nil)", settled, err)
	}
}

// TestTurnObligationsAllTerminalWithTeams pins the strict sibling used by the
// settlement wake: a missing or non-terminal team row always blocks the resume.
func TestTurnObligationsAllTerminalWithTeams(t *testing.T) {
	ctx := context.Background()
	store := newAgentSessionTestStore(t)
	seedSettledBatch(t, store, "batch-done", BatchCompleted)

	agent := stubAgentSessionResolver{"child-closed": true}
	teams := stubTeamResolver{"team-done": true, "team-running": false}

	cases := []struct {
		name     string
		record   *TurnSuspension
		resolver AgentSessionObligationResolver
		teams    TeamObligationResolver
		want     bool
	}{
		{
			name: "terminal team resumes",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{TeamObligationID("team-done")},
			},
			resolver: agent, teams: teams, want: true,
		},
		{
			name: "missing team row blocks the resume",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{TeamObligationID("team-missing")},
			},
			resolver: agent, teams: teams, want: false,
		},
		{
			name: "running team blocks the resume",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{TeamObligationID("team-running")},
			},
			resolver: agent, teams: teams, want: false,
		},
		{
			name: "mixed all terminal resumes",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{
					"batch-done",
					AgentSessionObligationID("child-closed"),
					TeamObligationID("team-done"),
				},
				ResumeQueue: []string{"batch-done"},
			},
			resolver: agent, teams: teams, want: true,
		},
		{
			name: "unwired team resolver blocks the resume",
			record: &TurnSuspension{
				TurnID: "t1", SessionID: "s1",
				ObligationIDs: []string{TeamObligationID("team-done")},
			},
			resolver: agent, teams: nil, want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := TurnObligationsAllTerminalWithTeams(ctx, store, tc.record, tc.resolver, tc.teams)
			if err != nil {
				t.Fatalf("TurnObligationsAllTerminalWithTeams: %v", err)
			}
			if got != tc.want {
				t.Fatalf("TurnObligationsAllTerminalWithTeams = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCountObligationProgressStrictness pins the feedback-sweep counter: missing
// rows and unwired resolvers count as pending, so a sweep can only ever
// under-report progress — it must never claim more finished than reality.
func TestCountObligationProgressStrictness(t *testing.T) {
	ctx := context.Background()
	store := newAgentSessionTestStore(t)
	seedSettledBatch(t, store, "batch-done", BatchCompleted)
	seedSettledBatch(t, store, "batch-running", BatchRunning)

	agent := stubAgentSessionResolver{"child-closed": true, "child-running": false}
	teams := stubTeamResolver{"team-done": true, "team-running": false}

	record := &TurnSuspension{ObligationIDs: []string{
		"batch-done", "batch-running",
		AgentSessionObligationID("child-closed"), AgentSessionObligationID("child-running"),
		TeamObligationID("team-done"), TeamObligationID("team-running"),
	}, ResumeQueue: []string{"batch-done", "batch-running"}}
	progress, err := CountObligationProgress(ctx, store, record, agent, teams)
	if err != nil {
		t.Fatalf("CountObligationProgress: %v", err)
	}
	if progress.Total != 6 || progress.Terminal != 3 {
		t.Fatalf("progress = %+v, want total=6 terminal=3", progress)
	}
	if progress.AllTerminal() {
		t.Fatal("a mixed cohort with running obligations must not read as all-terminal")
	}

	missing := &TurnSuspension{ObligationIDs: []string{
		"batch-missing", AgentSessionObligationID("child-missing"), TeamObligationID("team-missing"),
	}}
	progress, err = CountObligationProgress(ctx, store, missing, agent, teams)
	if err != nil {
		t.Fatalf("CountObligationProgress(missing): %v", err)
	}
	if progress.Total != 3 || progress.Terminal != 0 {
		t.Fatalf("missing rows must count as pending, got %+v", progress)
	}

	unwired := &TurnSuspension{ObligationIDs: []string{
		AgentSessionObligationID("child-closed"), TeamObligationID("team-done"),
	}}
	progress, err = CountObligationProgress(ctx, store, unwired, nil, nil)
	if err != nil {
		t.Fatalf("CountObligationProgress(unwired): %v", err)
	}
	if progress.Total != 2 || progress.Terminal != 0 {
		t.Fatalf("unwired resolvers must count as pending, got %+v", progress)
	}

	all := &TurnSuspension{ObligationIDs: []string{
		"batch-done", AgentSessionObligationID("child-closed"), TeamObligationID("team-done"),
	}}
	progress, err = CountObligationProgress(ctx, store, all, agent, teams)
	if err != nil {
		t.Fatalf("CountObligationProgress(all): %v", err)
	}
	if progress.Total != 3 || progress.Terminal != 3 || !progress.AllTerminal() {
		t.Fatalf("all-terminal cohort mis-read: %+v", progress)
	}
}
