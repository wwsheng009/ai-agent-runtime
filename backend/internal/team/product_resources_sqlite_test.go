package team

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLiteTeamDefinitionCRUDIsOwnerScopedAndVersioned(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	definition, err := store.CreateTeamDefinition(ctx, TeamDefinitionCreateRequest{
		OwnerID:     "owner-a",
		WorkspaceID: "workspace-a",
		Name:        "Research team",
		Description: "Reusable research setup",
		Config:      json.RawMessage(`{"members":[{"role":"researcher"}]}`),
	})
	require.NoError(t, err)
	require.NotNil(t, definition)
	assert.Equal(t, int64(1), definition.Version)
	assert.JSONEq(t, `{"members":[{"role":"researcher"}]}`, string(definition.Config))

	otherOwner, err := store.GetTeamDefinition(ctx, "owner-b", definition.ID)
	require.NoError(t, err)
	assert.Nil(t, otherOwner)

	ownerList, err := store.ListTeamDefinitions(ctx, "owner-a", "workspace-a", 10)
	require.NoError(t, err)
	require.Len(t, ownerList, 1)
	assert.Equal(t, definition.ID, ownerList[0].ID)

	otherOwnerList, err := store.ListTeamDefinitions(ctx, "owner-b", "workspace-a", 10)
	require.NoError(t, err)
	assert.Empty(t, otherOwnerList)

	updated, err := store.UpdateTeamDefinition(ctx, TeamDefinitionUpdateRequest{
		ID:              definition.ID,
		OwnerID:         "owner-a",
		ExpectedVersion: 1,
		Name:            "Research team v2",
		Description:     "Updated reusable setup",
		Config:          json.RawMessage(`{"members":[{"role":"researcher"},{"role":"reviewer"}]}`),
	})
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, int64(2), updated.Version)
	assert.Equal(t, "Research team v2", updated.Name)

	_, err = store.UpdateTeamDefinition(ctx, TeamDefinitionUpdateRequest{
		ID:              definition.ID,
		OwnerID:         "owner-a",
		ExpectedVersion: 1,
		Name:            "Stale update",
		Config:          json.RawMessage(`{"members":[]}`),
	})
	require.ErrorIs(t, err, ErrTeamDefinitionVersionConflict)
}

func TestSQLiteTeamRunDraftSnapshotIsImmutableAcrossDefinitionUpdateAndReopen(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "team-product-resources.db")
	store, err := NewSQLiteStore(&StoreConfig{Path: dbPath})
	require.NoError(t, err)

	definition, err := store.CreateTeamDefinition(ctx, TeamDefinitionCreateRequest{
		OwnerID:     "owner-a",
		WorkspaceID: "workspace-a",
		Name:        "Builder",
		Config:      json.RawMessage(`{"roles":["planner","builder"]}`),
	})
	require.NoError(t, err)

	request := TeamRunCreateRequest{
		OwnerID:                   "owner-a",
		WorkspaceID:               "workspace-a",
		DefinitionID:              definition.ID,
		ExpectedDefinitionVersion: definition.Version,
		Goal:                      "Build a tested feature",
		IdempotencyKey:            "create-run-001",
		RequestID:                 "request-001",
	}
	run, err := store.CreateTeamRunDraft(ctx, request)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, TeamRunStatusDraft, run.Status)
	assert.Equal(t, TeamRunPhaseDraft, run.Phase)
	assert.Equal(t, definition.ID, run.DefinitionID)
	require.NotNil(t, run.DefinitionVersion)
	assert.Equal(t, definition.Version, *run.DefinitionVersion)
	assert.Equal(t, digestTeamResource(run.Snapshot), run.SnapshotChecksum)

	replayed, err := store.CreateTeamRunDraft(ctx, request)
	require.NoError(t, err)
	require.NotNil(t, replayed)
	assert.Equal(t, run.ID, replayed.ID)

	replayedWithNewRequestID := request
	replayedWithNewRequestID.RequestID = "request-retry"
	replayedAfterNetworkRetry, err := store.CreateTeamRunDraft(ctx, replayedWithNewRequestID)
	require.NoError(t, err)
	assert.Equal(t, run.ID, replayedAfterNetworkRetry.ID, "request correlation IDs must not change idempotency")

	conflictingRequest := request
	conflictingRequest.Goal = "Different goal"
	_, err = store.CreateTeamRunDraft(ctx, conflictingRequest)
	require.ErrorIs(t, err, ErrTeamRunIdempotencyConflict)

	updated, err := store.UpdateTeamDefinition(ctx, TeamDefinitionUpdateRequest{
		ID:              definition.ID,
		OwnerID:         "owner-a",
		ExpectedVersion: definition.Version,
		Name:            definition.Name,
		Config:          json.RawMessage(`{"roles":["planner","builder","reviewer"]}`),
	})
	require.NoError(t, err)
	assert.Equal(t, definition.Version+1, updated.Version)

	require.NoError(t, store.Close())
	reopened, err := NewSQLiteStore(&StoreConfig{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })

	persistedDefinition, err := reopened.GetTeamDefinition(ctx, "owner-a", definition.ID)
	require.NoError(t, err)
	require.NotNil(t, persistedDefinition)
	assert.Equal(t, definition.Version+1, persistedDefinition.Version)
	assert.JSONEq(t, `{"roles":["planner","builder","reviewer"]}`, string(persistedDefinition.Config))

	persistedRun, err := reopened.GetTeamRun(ctx, "owner-a", run.ID)
	require.NoError(t, err)
	require.NotNil(t, persistedRun)
	assert.Equal(t, run.SnapshotChecksum, persistedRun.SnapshotChecksum)
	assert.JSONEq(t, string(run.Snapshot), string(persistedRun.Snapshot))

	replayedAfterReopen, err := reopened.CreateTeamRunDraft(ctx, request)
	require.NoError(t, err)
	require.NotNil(t, replayedAfterReopen)
	assert.Equal(t, run.ID, replayedAfterReopen.ID)
	assert.Equal(t, run.SnapshotChecksum, replayedAfterReopen.SnapshotChecksum)

	var snapshot teamRunSnapshotEnvelope
	require.NoError(t, json.Unmarshal(persistedRun.Snapshot, &snapshot))
	assert.JSONEq(t, `{"roles":["planner","builder"]}`, string(snapshot.Config))

	inaccessibleRun, err := reopened.GetTeamRun(ctx, "owner-b", run.ID)
	require.NoError(t, err)
	assert.Nil(t, inaccessibleRun)

	ownerRuns, err := reopened.ListTeamRuns(ctx, "owner-a", "workspace-a", 10)
	require.NoError(t, err)
	require.Len(t, ownerRuns, 1)
	assert.Equal(t, run.ID, ownerRuns[0].ID)

	otherOwnerRuns, err := reopened.ListTeamRuns(ctx, "owner-b", "workspace-a", 10)
	require.NoError(t, err)
	assert.Empty(t, otherOwnerRuns)
}

func TestSQLiteTeamRunIdempotencyKeyIsScopedToOwner(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	request := TeamRunCreateRequest{
		OwnerID:        "owner-a",
		WorkspaceID:    "workspace-a",
		Goal:           "Run once",
		Config:         json.RawMessage(`{"mode":"quick"}`),
		IdempotencyKey: "shared-key",
	}
	ownerARun, err := store.CreateTeamRunDraft(ctx, request)
	require.NoError(t, err)

	request.OwnerID = "owner-b"
	ownerBRun, err := store.CreateTeamRunDraft(ctx, request)
	require.NoError(t, err)
	require.NotEqual(t, ownerARun.ID, ownerBRun.ID)

	replayed, err := store.CreateTeamRunDraft(ctx, request)
	require.NoError(t, err)
	assert.Equal(t, ownerBRun.ID, replayed.ID)
}

func TestSQLiteTeamRunConcurrentDraftReplayCreatesSingleRun(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	const requestCount = 12
	request := TeamRunCreateRequest{
		OwnerID:        "owner-a",
		WorkspaceID:    "workspace-a",
		Goal:           "Concurrent idempotent draft",
		Config:         json.RawMessage(`{"mode":"guided"}`),
		IdempotencyKey: "concurrent-create",
	}

	type result struct {
		run *TeamRun
		err error
	}
	results := make(chan result, requestCount)
	var wg sync.WaitGroup
	for range requestCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run, err := store.CreateTeamRunDraft(ctx, request)
			results <- result{run: run, err: err}
		}()
	}
	wg.Wait()
	close(results)

	var runID string
	for result := range results {
		require.NoError(t, result.err)
		require.NotNil(t, result.run)
		if runID == "" {
			runID = result.run.ID
		}
		assert.Equal(t, runID, result.run.ID)
	}
	runs, err := store.ListTeamRuns(ctx, "owner-a", "workspace-a", 20)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, runID, runs[0].ID)
}

func TestSQLiteTeamRunConcurrentDraftReplayAcrossStoreInstancesCreatesSingleRun(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "team-product-multi-instance.db")
	bootstrap, err := NewSQLiteStore(&StoreConfig{Path: dbPath})
	require.NoError(t, err)
	_, err = bootstrap.CreateTeamDefinition(ctx, TeamDefinitionCreateRequest{
		OwnerID:     "owner-a",
		WorkspaceID: "workspace-a",
		Name:        "Bootstrap",
		Config:      json.RawMessage(`{"mode":"shared-db"}`),
	})
	require.NoError(t, err)
	require.NoError(t, bootstrap.Close())

	const instanceCount = 8
	stores := make([]*SQLiteStore, 0, instanceCount)
	for range instanceCount {
		store, err := NewSQLiteStore(&StoreConfig{Path: dbPath})
		require.NoError(t, err)
		stores = append(stores, store)
		t.Cleanup(func() { _ = store.Close() })
	}
	// Open and migrate each independent store handle before racing writes. This
	// keeps the test focused on cross-instance transaction/idempotency behavior
	// rather than first-open migration coordination.
	for _, store := range stores {
		_, err := store.ListTeamRuns(ctx, "owner-a", "workspace-a", 10)
		require.NoError(t, err)
	}

	request := TeamRunCreateRequest{
		OwnerID:        "owner-a",
		WorkspaceID:    "workspace-a",
		Goal:           "Concurrent multi-instance draft",
		Config:         json.RawMessage(`{"mode":"shared-db"}`),
		IdempotencyKey: "multi-instance-create",
	}
	type result struct {
		run *TeamRun
		err error
	}
	results := make(chan result, instanceCount)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, store := range stores {
		wg.Add(1)
		go func(store *SQLiteStore) {
			defer wg.Done()
			<-start
			run, err := store.CreateTeamRunDraft(ctx, request)
			results <- result{run: run, err: err}
		}(store)
	}
	close(start)
	wg.Wait()
	close(results)

	var runID string
	for result := range results {
		require.NoError(t, result.err)
		require.NotNil(t, result.run)
		if runID == "" {
			runID = result.run.ID
		}
		assert.Equal(t, runID, result.run.ID)
	}
	runs, err := stores[0].ListTeamRuns(ctx, "owner-a", "workspace-a", 20)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, runID, runs[0].ID)
}

func TestSQLiteTeamRunDetectsSnapshotChecksumCorruption(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	run, err := store.CreateTeamRunDraft(ctx, TeamRunCreateRequest{
		OwnerID:        "owner-a",
		WorkspaceID:    "workspace-a",
		Goal:           "Detect corruption",
		Config:         json.RawMessage(`{"mode":"advanced"}`),
		IdempotencyKey: "corruption-check",
	})
	require.NoError(t, err)

	_, err = store.db.ExecContext(ctx, `
		UPDATE team_runs SET config_snapshot_json = ?
		WHERE owner_id = ? AND id = ?
	`, `{"tampered":true}`, "owner-a", run.ID)
	require.NoError(t, err)

	_, err = store.GetTeamRun(ctx, "owner-a", run.ID)
	require.ErrorIs(t, err, ErrTeamRunSnapshotCorrupt)
}

func TestSQLiteTeamRunRejectsMalformedSnapshotEvenWithUpdatedChecksum(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	run, err := store.CreateTeamRunDraft(ctx, TeamRunCreateRequest{
		OwnerID:        "owner-a",
		WorkspaceID:    "workspace-a",
		Goal:           "Reject malformed snapshot",
		Config:         json.RawMessage(`{"mode":"advanced"}`),
		IdempotencyKey: "malformed-snapshot",
	})
	require.NoError(t, err)

	// Keep the outer JSON valid and recompute its checksum, but alter immutable
	// metadata so checksum validity alone cannot make the row trustworthy.
	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(run.Snapshot, &envelope))
	envelope["goal"] = json.RawMessage(`"some other goal"`)
	tampered, err := json.Marshal(envelope)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `
		UPDATE team_runs SET config_snapshot_json = ?, snapshot_checksum = ?
		WHERE owner_id = ? AND id = ?
	`, string(tampered), digestTeamResource(tampered), "owner-a", run.ID)
	require.NoError(t, err)

	_, err = store.GetTeamRun(ctx, "owner-a", run.ID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTeamRunSnapshotCorrupt))
}

func TestSQLiteTeamRunStartProjectsLegacyRuntimeAtomicallyAndReplays(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	run, err := store.CreateTeamRunDraft(ctx, TeamRunCreateRequest{
		OwnerID:     "owner-a",
		WorkspaceID: "workspace-a",
		Goal:        "execute a real team run",
		Config: json.RawMessage(`{
			"strategy":"guided",
			"lead_session_id":"lead-session",
			"members":[
				{"id":"planner","role":"planner","session_id":"session-planner","state":"idle"},
				{"id":"builder","role":"builder","session_id":"session-builder","state":"idle"}
			],
			"tasks":[
				{"id":"plan","title":"Plan","goal":"create a plan","assignee":"planner"},
				{"id":"build","title":"Build","goal":"build the feature","assignee":"builder"}
			],
			"dependencies":[{"task":"build","depends_on":"plan"}]
		}`),
		IdempotencyKey: "create-startable-run",
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), run.Version)

	started, err := store.StartTeamRun(ctx, TeamRunStartRequest{
		OwnerID:         "owner-a",
		WorkspaceID:     "workspace-a",
		RunID:           run.ID,
		ExpectedVersion: run.Version,
		IdempotencyKey:  "start-run-1",
		RequestID:       "start-request-1",
	})
	require.NoError(t, err)
	require.NotNil(t, started)
	require.Equal(t, TeamRunStatusActive, started.Status)
	require.Equal(t, TeamRunPhaseRunning, started.Phase)
	require.Equal(t, TeamRunCompatibilityLegacyTeamView, started.CompatibilityMode)
	require.Equal(t, int64(2), started.Version)
	require.NotEmpty(t, started.LegacyTeamID)
	require.Greater(t, started.EventSeq, int64(0))
	require.NotNil(t, started.StartedAt)

	legacyTeam, err := store.GetTeam(ctx, started.LegacyTeamID)
	require.NoError(t, err)
	require.NotNil(t, legacyTeam)
	require.Equal(t, TeamStatusActive, legacyTeam.Status)
	require.Equal(t, "workspace-a", legacyTeam.WorkspaceID)
	require.Equal(t, "lead-session", legacyTeam.LeadSessionID)

	mates, err := store.ListTeammates(ctx, started.LegacyTeamID)
	require.NoError(t, err)
	require.Len(t, mates, 2)
	tasks, err := store.ListTasks(ctx, TaskFilter{TeamID: started.LegacyTeamID})
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	var deps []TaskDependency
	for _, task := range tasks {
		candidate, dependencyErr := store.ListTaskDependencyRecords(ctx, TaskDependencyFilter{TaskID: task.ID})
		require.NoError(t, dependencyErr)
		if len(candidate) > 0 {
			deps = candidate
			break
		}
	}
	require.Len(t, deps, 1)

	events, err := store.ListTeamEvents(ctx, TeamEventFilter{TeamID: started.LegacyTeamID})
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, TaskDependencyCreatedEvent, events[0].Type)
	require.Equal(t, "team.run.started", events[1].Type)
	require.Equal(t, started.EventSeq, events[1].Seq)

	replayed, err := store.StartTeamRun(ctx, TeamRunStartRequest{
		OwnerID:         "owner-a",
		WorkspaceID:     "workspace-a",
		RunID:           run.ID,
		ExpectedVersion: 1, // replay must not be rejected by the now-stale version
		IdempotencyKey:  "start-run-1",
		RequestID:       "retry-with-new-request-id",
	})
	require.NoError(t, err)
	require.Equal(t, started.ID, replayed.ID)
	require.Equal(t, started.Version, replayed.Version)
	require.Equal(t, started.LegacyTeamID, replayed.LegacyTeamID)

	teams, err := store.ListTeams(ctx, TeamFilter{WorkspaceID: "workspace-a"})
	require.NoError(t, err)
	require.Len(t, teams, 1, "idempotent Start must not create a second legacy Team")

	_, err = store.StartTeamRun(ctx, TeamRunStartRequest{
		OwnerID:         "owner-a",
		WorkspaceID:     "workspace-a",
		RunID:           run.ID,
		ExpectedVersion: 1,
		IdempotencyKey:  "start-run-1",
		RequestID:       "different-payload",
	})
	require.NoError(t, err, "request correlation id is intentionally excluded from the Start digest")

	_, err = store.StartTeamRun(ctx, TeamRunStartRequest{
		OwnerID:         "owner-a",
		WorkspaceID:     "workspace-a",
		RunID:           run.ID,
		ExpectedVersion: 2,
		IdempotencyKey:  "different-start-key",
	})
	require.ErrorIs(t, err, ErrTeamRunInvalidTransition)
}

func TestSQLiteTeamRunStartRejectsPreflightBlockerWithoutProjection(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	run, err := store.CreateTeamRunDraft(ctx, TeamRunCreateRequest{
		OwnerID:     "owner-a",
		WorkspaceID: "workspace-a",
		Goal:        "blocked start",
		Config: json.RawMessage(`{
			"members":[{"id":"offline","session_id":"session-offline","state":"offline"}],
			"tasks":[{"id":"task-1","title":"Blocked","goal":"cannot run","assignee":"offline"}]
		}`),
		IdempotencyKey: "blocked-start",
	})
	require.NoError(t, err)

	_, err = store.StartTeamRun(ctx, TeamRunStartRequest{
		OwnerID:         "owner-a",
		WorkspaceID:     "workspace-a",
		RunID:           run.ID,
		ExpectedVersion: 1,
		IdempotencyKey:  "blocked-start-op",
	})
	var blocked *TeamRunPreflightBlockedError
	require.ErrorAs(t, err, &blocked)
	require.NotEmpty(t, blocked.Findings)
	require.ErrorIs(t, err, ErrTeamRunPreflightBlocked)

	unchanged, err := store.GetTeamRun(ctx, "owner-a", run.ID)
	require.NoError(t, err)
	require.Equal(t, TeamRunStatusDraft, unchanged.Status)
	require.Equal(t, TeamRunPhaseDraft, unchanged.Phase)
	teams, err := store.ListTeams(ctx, TeamFilter{WorkspaceID: "workspace-a"})
	require.NoError(t, err)
	require.Empty(t, teams)
}

func TestSQLiteTeamRunPreflightRejectsBusyAssigneeAndStartKeepsDraft(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	run, err := store.CreateTeamRunDraft(ctx, TeamRunCreateRequest{
		OwnerID:     "owner-a",
		WorkspaceID: "workspace-a",
		Goal:        "busy assignee must not start",
		Config: json.RawMessage(`{
			"members":[{"id":"busy-worker","session_id":"session-busy","state":"busy"}],
			"tasks":[{"id":"task-1","title":"Wait for worker","goal":"must remain pending","assignee":"busy-worker"}]
		}`),
		IdempotencyKey: "busy-assignee-draft",
	})
	require.NoError(t, err)

	report, err := store.PreflightTeamRun(ctx, TeamRunPreflightRequest{
		OwnerID:     "owner-a",
		WorkspaceID: "workspace-a",
		RunID:       run.ID,
	})
	require.NoError(t, err)
	require.False(t, report.Ready)
	require.Contains(t, teamRunPreflightFindingCodes(report.Blockers), "team_run_no_ready_teammate")
	require.Contains(t, teamRunPreflightFindingCodes(report.Blockers), PreflightCodeTaskAssigneeBusy)

	_, err = store.StartTeamRun(ctx, TeamRunStartRequest{
		OwnerID:         "owner-a",
		WorkspaceID:     "workspace-a",
		RunID:           run.ID,
		ExpectedVersion: run.Version,
		IdempotencyKey:  "busy-assignee-start",
	})
	var blocked *TeamRunPreflightBlockedError
	require.ErrorAs(t, err, &blocked)
	require.Contains(t, teamRunPreflightFindingCodes(blocked.Findings), PreflightCodeTaskAssigneeBusy)

	unchanged, err := store.GetTeamRun(ctx, "owner-a", run.ID)
	require.NoError(t, err)
	require.Equal(t, TeamRunStatusDraft, unchanged.Status)
	require.Equal(t, TeamRunPhaseDraft, unchanged.Phase)
	teams, err := store.ListTeams(ctx, TeamFilter{WorkspaceID: "workspace-a"})
	require.NoError(t, err)
	require.Empty(t, teams, "a busy-only snapshot must not project an un-runnable Team")
}

func TestSQLiteTeamRunPreflightUsesBlockedAssigneeCode(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	run, err := store.CreateTeamRunDraft(ctx, TeamRunCreateRequest{
		OwnerID:     "owner-a",
		WorkspaceID: "workspace-a",
		Goal:        "blocked assignee must be actionable",
		Config: json.RawMessage(`{
			"members":[{"id":"blocked-worker","session_id":"session-blocked","state":"blocked"}],
			"tasks":[{"id":"task-1","title":"Unblock worker","goal":"must remain pending","assignee":"blocked-worker"}]
		}`),
		IdempotencyKey: "blocked-assignee-draft",
	})
	require.NoError(t, err)

	report, err := store.PreflightTeamRun(ctx, TeamRunPreflightRequest{
		OwnerID:     "owner-a",
		WorkspaceID: "workspace-a",
		RunID:       run.ID,
	})
	require.NoError(t, err)
	require.False(t, report.Ready)
	require.Contains(t, teamRunPreflightFindingCodes(report.Blockers), PreflightCodeTaskAssigneeBlocked)
	require.NotContains(t, teamRunPreflightFindingCodes(report.Blockers), PreflightCodeTaskAssigneeOffline)
}

func TestSQLiteTeamRunPreflightRejectsWhenBusyMembersExhaustConcurrency(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	run, err := store.CreateTeamRunDraft(ctx, TeamRunCreateRequest{
		OwnerID:     "owner-a",
		WorkspaceID: "workspace-a",
		Goal:        "active members consume configured concurrency",
		Config: json.RawMessage(`{
			"max_teammates":1,
			"members":[
				{"id":"busy-worker","session_id":"session-busy","state":"busy"},
				{"id":"idle-worker","session_id":"session-idle","state":"idle"}
			],
			"tasks":[{"id":"task-1","title":"Queued work","goal":"must be claimable"}]
		}`),
		IdempotencyKey: "busy-slot-exhausted",
	})
	require.NoError(t, err)

	report, err := store.PreflightTeamRun(ctx, TeamRunPreflightRequest{
		OwnerID:     "owner-a",
		WorkspaceID: "workspace-a",
		RunID:       run.ID,
	})
	require.NoError(t, err)
	require.False(t, report.Ready)
	require.Contains(t, teamRunPreflightFindingCodes(report.Blockers), "team_run_no_ready_teammate")
}

func TestSQLiteTeamRunPreflightReportsReadinessWithoutMutation(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	run, err := store.CreateTeamRunDraft(ctx, TeamRunCreateRequest{
		OwnerID:     "owner-a",
		WorkspaceID: "workspace-a",
		Goal:        "preflight a valid draft",
		Config: json.RawMessage(`{
			"members":[{"id":"worker","session_id":"session-worker","state":"idle"}],
			"tasks":[{"id":"task-1","title":"Work","goal":"do work","assignee":"worker"}]
		}`),
		IdempotencyKey: "preflight-ready",
	})
	require.NoError(t, err)

	report, err := store.PreflightTeamRun(ctx, TeamRunPreflightRequest{
		OwnerID:     "owner-a",
		WorkspaceID: "workspace-a",
		RunID:       run.ID,
	})
	require.NoError(t, err)
	require.NotNil(t, report)
	require.True(t, report.Ready)
	require.Equal(t, run.ID, report.RunID)
	require.Empty(t, report.LegacyTeamID, "a read-only preflight must not claim a projection already exists")
	require.Equal(t, int64(1), report.Version)
	require.Equal(t, TeamRunStatusDraft, report.Status)
	require.Equal(t, TeamRunPhaseDraft, report.Phase)
	require.Equal(t, PreflightRouteCapabilityDisabled, report.RoutePreviewCapability)
	require.Zero(t, report.BlockerCount)
	require.Zero(t, report.WarningCount)
	require.Zero(t, report.InfoCount)

	persisted, err := store.GetTeamRun(ctx, "owner-a", run.ID)
	require.NoError(t, err)
	require.Equal(t, run.Version, persisted.Version)
	require.Equal(t, run.Status, persisted.Status)
	require.Equal(t, run.Phase, persisted.Phase)
	require.Empty(t, persisted.LegacyTeamID)
	teams, err := store.ListTeams(ctx, TeamFilter{WorkspaceID: "workspace-a"})
	require.NoError(t, err)
	require.Empty(t, teams, "preflight must not create the legacy Team projection")

	_, err = store.PreflightTeamRun(ctx, TeamRunPreflightRequest{
		OwnerID:     "owner-a",
		WorkspaceID: "workspace-b",
		RunID:       run.ID,
	})
	require.ErrorIs(t, err, ErrTeamRunNotFound)
	_, err = store.PreflightTeamRun(ctx, TeamRunPreflightRequest{
		OwnerID:     "owner-b",
		WorkspaceID: "workspace-a",
		RunID:       run.ID,
	})
	require.ErrorIs(t, err, ErrTeamRunNotFound)

	blockedRun, err := store.CreateTeamRunDraft(ctx, TeamRunCreateRequest{
		OwnerID:     "owner-a",
		WorkspaceID: "workspace-a",
		Goal:        "preflight a blocked draft",
		Config: json.RawMessage(`{
			"members":[{"id":"offline","session_id":"session-offline","state":"offline"}],
			"tasks":[{"id":"task-1","title":"Work","goal":"do work","assignee":"offline"}]
		}`),
		IdempotencyKey: "preflight-blocked",
	})
	require.NoError(t, err)
	blockedReport, err := store.PreflightTeamRun(ctx, TeamRunPreflightRequest{
		OwnerID:     "owner-a",
		WorkspaceID: "workspace-a",
		RunID:       blockedRun.ID,
	})
	require.NoError(t, err)
	require.False(t, blockedReport.Ready)
	require.Greater(t, blockedReport.BlockerCount, 0)
	require.NotEmpty(t, blockedReport.Blockers)
	require.Contains(t, teamRunPreflightFindingCodes(blockedReport.Blockers), PreflightCodeTaskAssigneeOffline)
	blockedPersisted, err := store.GetTeamRun(ctx, "owner-a", blockedRun.ID)
	require.NoError(t, err)
	require.Equal(t, TeamRunStatusDraft, blockedPersisted.Status)
	require.Empty(t, blockedPersisted.LegacyTeamID)
	teams, err = store.ListTeams(ctx, TeamFilter{WorkspaceID: "workspace-a"})
	require.NoError(t, err)
	require.Empty(t, teams, "blocked preflight must not create a legacy Team projection")
}

func TestSQLiteTeamRunPreflightBlocksNonDraftRun(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	run, err := store.CreateTeamRunDraft(ctx, TeamRunCreateRequest{
		OwnerID:     "owner-a",
		WorkspaceID: "workspace-a",
		Goal:        "start then preflight",
		Config: json.RawMessage(`{
			"members":[{"id":"worker","session_id":"session-worker","state":"idle"}],
			"tasks":[{"id":"task-1","title":"Work","goal":"do work","assignee":"worker"}]
		}`),
		IdempotencyKey: "preflight-nondraft",
	})
	require.NoError(t, err)
	started, err := store.StartTeamRun(ctx, TeamRunStartRequest{
		OwnerID:         "owner-a",
		WorkspaceID:     "workspace-a",
		RunID:           run.ID,
		ExpectedVersion: run.Version,
		IdempotencyKey:  "start-before-preflight",
	})
	require.NoError(t, err)

	report, err := store.PreflightTeamRun(ctx, TeamRunPreflightRequest{
		OwnerID:     "owner-a",
		WorkspaceID: "workspace-a",
		RunID:       run.ID,
	})
	require.NoError(t, err)
	require.False(t, report.Ready)
	require.Equal(t, started.Version, report.Version)
	require.Equal(t, started.LegacyTeamID, report.LegacyTeamID)
	require.Contains(t, teamRunPreflightFindingCodes(report.Blockers), "team_run_not_draft")

	persisted, err := store.GetTeamRun(ctx, "owner-a", run.ID)
	require.NoError(t, err)
	require.Equal(t, started.Version, persisted.Version)
	require.Equal(t, started.Status, persisted.Status)
	require.Equal(t, started.LegacyTeamID, persisted.LegacyTeamID)
}

func teamRunPreflightFindingCodes(findings []PreflightFinding) []string {
	codes := make([]string, 0, len(findings))
	for _, finding := range findings {
		codes = append(codes, finding.Code)
	}
	return codes
}

func TestSQLiteTeamRunStartConcurrentCallsCreateOneProjection(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	run, err := store.CreateTeamRunDraft(ctx, TeamRunCreateRequest{
		OwnerID:     "owner-a",
		WorkspaceID: "workspace-a",
		Goal:        "concurrent start",
		Config: json.RawMessage(`{
			"members":[{"id":"worker","session_id":"session-worker","state":"idle"}],
			"tasks":[{"id":"task-1","title":"Work","goal":"do work","assignee":"worker"}]
		}`),
		IdempotencyKey: "concurrent-start-draft",
	})
	require.NoError(t, err)

	const callers = 8
	results := make(chan struct {
		run *TeamRun
		err error
	}, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started, startErr := store.StartTeamRun(ctx, TeamRunStartRequest{
				OwnerID:         "owner-a",
				WorkspaceID:     "workspace-a",
				RunID:           run.ID,
				ExpectedVersion: 1,
				IdempotencyKey:  "concurrent-start-op",
			})
			results <- struct {
				run *TeamRun
				err error
			}{started, startErr}
		}()
	}
	wg.Wait()
	close(results)
	for result := range results {
		require.NoError(t, result.err)
		require.NotNil(t, result.run)
		require.Equal(t, run.ID, result.run.ID)
		require.Equal(t, int64(2), result.run.Version)
	}
	teams, err := store.ListTeams(ctx, TeamFilter{WorkspaceID: "workspace-a"})
	require.NoError(t, err)
	require.Len(t, teams, 1)
	events, err := store.ListTeamEvents(ctx, TeamEventFilter{TeamID: teams[0].ID})
	require.NoError(t, err)
	startEvents := 0
	for _, event := range events {
		if event.Type == "team.run.started" {
			startEvents++
		}
	}
	require.Equal(t, 1, startEvents)
}
