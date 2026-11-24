package team

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPreflightTwoMemberTwoTaskDependencyPassIsDeterministic(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{ID: "team-preflight-pass", Status: TeamStatusActive})
	require.NoError(t, err)
	for _, mate := range []Teammate{
		{ID: "member-a", TeamID: teamID, SessionID: "session-a", State: TeammateStateIdle},
		{ID: "member-b", TeamID: teamID, SessionID: "session-b", State: TeammateStateIdle},
	} {
		_, err = store.UpsertTeammate(ctx, mate)
		require.NoError(t, err)
	}
	taskA, err := store.CreateTask(ctx, Task{
		ID:       "task-a",
		TeamID:   teamID,
		Title:    "inspect",
		Status:   TaskStatusReady,
		Assignee: preflightString("member-a"),
		WritePaths: []string{
			"src",
		},
	})
	require.NoError(t, err)
	taskB, err := store.CreateTask(ctx, Task{
		ID:       "task-b",
		TeamID:   teamID,
		Title:    "implement",
		Status:   TaskStatusPending,
		Assignee: preflightString("member-b"),
		WritePaths: []string{
			"workspace/src/main.go",
		},
	})
	require.NoError(t, err)
	require.Equal(t, "task-a", taskA)
	require.Equal(t, "task-b", taskB)
	require.NoError(t, store.AddTaskDependency(ctx, taskB, taskA))

	service := NewPreflightService(store, PreflightOptions{
		WorkspaceRoot: "workspace",
		Now:           func() time.Time { return time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC) },
	})
	first, err := service.Build(ctx, teamID)
	require.NoError(t, err)
	second, err := service.Build(ctx, teamID)
	require.NoError(t, err)

	require.True(t, first.TeamExists)
	require.True(t, first.Ready)
	require.Empty(t, first.Blockers)
	require.NotContains(t, preflightCodes(first), PreflightCodePathWriteConflict)
	require.Equal(t, PreflightRouteCapabilityUnavailable, first.RoutePreviewCapability)
	require.Equal(t, first, second)
	requirePreflightHasCode(t, first, PreflightCodeRoutePreviewUnavailable)
}

func TestPreflightUnknownAssigneeBlocks(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{ID: "team-preflight-assignee"})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID: "member-a", TeamID: teamID, SessionID: "session-a", State: TeammateStateIdle,
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		ID: "task-unknown-assignee", TeamID: teamID, Status: TaskStatusReady,
		Assignee: preflightString("missing-member"),
	})
	require.NoError(t, err)

	report, err := NewPreflightService(store).Build(ctx, teamID)
	require.NoError(t, err)
	require.False(t, report.Ready)
	requirePreflightHasCode(t, report, PreflightCodeTaskAssigneeUnknown)
	requirePreflightFinding(t, report, PreflightCodeTaskAssigneeUnknown, func(f PreflightFinding) {
		require.Equal(t, "task-unknown-assignee", f.TaskID)
		require.NotEmpty(t, f.Remediation)
	})
}

func TestPreflightDetectsSelfDependencyAndCycle(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{ID: "team-preflight-cycle"})
	require.NoError(t, err)
	for _, taskID := range []string{"task-a", "task-b"} {
		_, err = store.CreateTask(ctx, Task{ID: taskID, TeamID: teamID, Status: TaskStatusPending})
		require.NoError(t, err)
	}
	require.NoError(t, store.AddTaskDependency(ctx, "task-a", "task-a"))
	require.NoError(t, store.AddTaskDependency(ctx, "task-a", "task-b"))
	require.NoError(t, store.AddTaskDependency(ctx, "task-b", "task-a"))

	report, err := NewPreflightService(store).Build(ctx, teamID)
	require.NoError(t, err)
	requirePreflightHasCode(t, report, PreflightCodeDependencySelf)
	requirePreflightFinding(t, report, PreflightCodeDependencyCycle, func(f PreflightFinding) {
		require.Equal(t, "task-a", f.TaskID)
	})
	require.True(t, preflightHasFinding(report, PreflightCodeDependencyCycle, "task-b"))
}

func TestPreflightDetectsMissingAndCrossTeamDependencies(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{ID: "team-preflight-dependency"})
	require.NoError(t, err)
	otherTeamID, err := store.CreateTeam(ctx, Team{ID: "team-preflight-other"})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{ID: "task-current", TeamID: teamID, Status: TaskStatusReady})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{ID: "task-foreign", TeamID: otherTeamID, Status: TaskStatusReady})
	require.NoError(t, err)
	require.NoError(t, store.AddTaskDependency(ctx, "task-current", "task-foreign"))
	require.NoError(t, store.AddTaskDependency(ctx, "task-current", "task-missing"))

	report, err := NewPreflightService(store).Build(ctx, teamID)
	require.NoError(t, err)
	requirePreflightHasCode(t, report, PreflightCodeDependencyCrossTeam)
	requirePreflightHasCode(t, report, PreflightCodeDependencyTaskMissing)
}

func TestPreflightAssignedSessionStatesBlock(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		sessionID string
		state     TeammateState
		expected  string
	}{
		{
			name:      "missing session",
			sessionID: "",
			state:     TeammateStateIdle,
			expected:  PreflightCodeTaskAssigneeSessionMissing,
		},
		{
			name:      "offline session",
			sessionID: "session-offline",
			state:     TeammateStateOffline,
			expected:  PreflightCodeTaskAssigneeOffline,
		},
		{
			name:      "busy session",
			sessionID: "session-busy",
			state:     TeammateStateBusy,
			expected:  PreflightCodeTaskAssigneeBusy,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			store := newTestStore(t)
			teamID, err := store.CreateTeam(ctx, Team{ID: "team-preflight-session"})
			require.NoError(t, err)
			_, err = store.UpsertTeammate(ctx, Teammate{
				ID: "member-a", TeamID: teamID, SessionID: testCase.sessionID, State: testCase.state,
			})
			require.NoError(t, err)
			_, err = store.CreateTask(ctx, Task{
				ID: "task-a", TeamID: teamID, Status: TaskStatusReady, Assignee: preflightString("member-a"),
			})
			require.NoError(t, err)

			report, err := NewPreflightService(store).Build(ctx, teamID)
			require.NoError(t, err)
			require.False(t, report.Ready)
			requirePreflightHasCode(t, report, testCase.expected)
		})
	}
}

func TestPreflightReportsBusyUnassignedTeammateAsWarning(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{ID: "team-preflight-busy-warning"})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID: "member-busy", TeamID: teamID, SessionID: "session-busy", State: TeammateStateBusy,
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		ID: "task-unassigned", TeamID: teamID, Status: TaskStatusReady,
	})
	require.NoError(t, err)

	report, err := NewPreflightService(store).Build(ctx, teamID)
	require.NoError(t, err)
	require.True(t, report.Ready, "busy unassigned teammates are advisory; an unassigned task can still be scheduler-selected")
	requirePreflightFinding(t, report, PreflightCodeTeammateBusy, func(f PreflightFinding) {
		require.Equal(t, PreflightSeverityWarning, f.Severity)
		require.Equal(t, "member-busy", f.TeammateID)
		require.NotEmpty(t, f.Remediation)
	})
}

func TestPreflightDoesNotBlockRunningTaskAssignedToBusyTeammate(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{ID: "team-preflight-running-busy"})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID: "member-busy", TeamID: teamID, SessionID: "session-busy", State: TeammateStateBusy,
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		ID: "task-running", TeamID: teamID, Status: TaskStatusRunning,
		Assignee: preflightString("member-busy"),
	})
	require.NoError(t, err)

	report, err := NewPreflightService(store).Build(ctx, teamID)
	require.NoError(t, err)
	require.True(t, report.Ready)
	require.NotContains(t, preflightCodes(report), PreflightCodeTaskAssigneeBusy)
}

func TestPreflightDetectsCanonicalWriterConflicts(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{ID: "team-preflight-paths"})
	require.NoError(t, err)
	for _, mate := range []Teammate{
		{ID: "member-a", TeamID: teamID, SessionID: "session-a", State: TeammateStateIdle},
		{ID: "member-b", TeamID: teamID, SessionID: "session-b", State: TeammateStateIdle},
	} {
		_, err = store.UpsertTeammate(ctx, mate)
		require.NoError(t, err)
	}
	_, err = store.CreateTask(ctx, Task{
		ID: "task-a", TeamID: teamID, Status: TaskStatusReady, Assignee: preflightString("member-a"),
		WritePaths: []string{"src"},
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		ID: "task-b", TeamID: teamID, Status: TaskStatusReady, Assignee: preflightString("member-b"),
		ReadPaths:  []string{"workspace/docs/spec.md"},
		WritePaths: []string{"workspace/src/main.go"},
	})
	require.NoError(t, err)
	asOf := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.CreatePathClaims(ctx, []PathClaim{{
		TeamID: teamID, TaskID: "task-existing", OwnerAgentID: "member-c",
		Path: "workspace/docs", Mode: PathClaimWrite, LeaseUntil: asOf.Add(time.Minute),
	}, {
		TeamID: teamID, TaskID: "task-no-lease", OwnerAgentID: "member-d",
		Path: "workspace/src/main.go", Mode: PathClaimWrite,
	}}))

	report, err := NewPreflightService(store, PreflightOptions{
		WorkspaceRoot: "workspace",
		Now:           func() time.Time { return asOf },
	}).Build(ctx, teamID)
	require.NoError(t, err)
	requirePreflightHasCode(t, report, PreflightCodePathWriteConflict)
	requirePreflightHasCode(t, report, PreflightCodePathClaimConflict)
	requirePreflightFinding(t, report, PreflightCodePathWriteConflict, func(f PreflightFinding) {
		require.Equal(t, "task-a", f.TaskID)
		require.Equal(t, "task-b", f.RelatedTaskID)
		require.Equal(t, "workspace/src", f.Path)
		require.Equal(t, "workspace/src/main.go", f.RelatedPath)
	})
	sawExistingClaim := false
	sawZeroLeaseConflictForTaskA, sawZeroLeaseConflictForTaskB := false, false
	for _, finding := range report.Findings {
		if finding.Code != PreflightCodePathClaimConflict {
			continue
		}
		switch finding.RelatedTaskID {
		case "task-existing":
			sawExistingClaim = true
			require.Equal(t, "task-b", finding.TaskID)
			require.Equal(t, "workspace/docs/spec.md", finding.Path)
			require.Equal(t, "workspace/docs", finding.RelatedPath)
		case "task-no-lease":
			require.Contains(t, []string{"task-a", "task-b"}, finding.TaskID)
			require.Equal(t, "workspace/src/main.go", finding.RelatedPath)
			switch finding.TaskID {
			case "task-a":
				sawZeroLeaseConflictForTaskA = true
				require.Equal(t, "workspace/src", finding.Path)
			case "task-b":
				sawZeroLeaseConflictForTaskB = true
				require.Equal(t, "workspace/src/main.go", finding.Path)
			}
		}
	}
	require.True(t, sawExistingClaim, "active leased claim should conflict")
	require.True(t, sawZeroLeaseConflictForTaskA, "zero-value lease claim should conflict with the parent write scope")
	require.True(t, sawZeroLeaseConflictForTaskB, "zero-value lease claim should conflict with the exact write scope")
}

func TestPreflightEmptyTeamBlocks(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{ID: "team-preflight-empty"})
	require.NoError(t, err)

	report, err := NewPreflightService(store).Build(ctx, teamID)
	require.NoError(t, err)
	require.True(t, report.TeamExists)
	require.False(t, report.Ready)
	requirePreflightHasCode(t, report, PreflightCodeTeamEmpty)
}

func TestPreflightChecksExistingTeamLimits(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{
		ID: "team-preflight-limits", MaxTeammates: 1, MaxWriters: 1,
	})
	require.NoError(t, err)
	for _, mate := range []Teammate{
		{ID: "member-a", TeamID: teamID, SessionID: "session-a", State: TeammateStateBusy},
		{ID: "member-b", TeamID: teamID, SessionID: "session-b", State: TeammateStateBusy},
	} {
		_, err = store.UpsertTeammate(ctx, mate)
		require.NoError(t, err)
	}
	for _, taskID := range []string{"task-a", "task-b"} {
		_, err = store.CreateTask(ctx, Task{
			ID: taskID, TeamID: teamID, Status: TaskStatusRunning,
			WritePaths: []string{taskID + ".go"},
		})
		require.NoError(t, err)
	}

	report, err := NewPreflightService(store).Build(ctx, teamID)
	require.NoError(t, err)
	requirePreflightHasCode(t, report, PreflightCodeTeamActiveTeammatesExceeded)
	requirePreflightHasCode(t, report, PreflightCodeTeamActiveWritersExceeded)
}

func TestPreflightUsesInjectedRouteResolverWithoutSynthesizingRoute(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{ID: "team-preflight-route"})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID: "member-a", TeamID: teamID, SessionID: "session-a", State: TeammateStateIdle,
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		ID: "task-a", TeamID: teamID, Status: TaskStatusReady, Assignee: preflightString("member-a"),
	})
	require.NoError(t, err)
	resolver := preflightRouteResolverFunc(func(_ context.Context, request TaskRouteRequest) (*TaskRouteResolution, error) {
		require.Equal(t, "session-a", request.SessionID)
		require.Equal(t, "task-a", request.Task.ID)
		return &TaskRouteResolution{Route: &TaskExecutionRoute{
			Provider: "injected-provider", Model: "injected-model",
		}}, nil
	})

	report, err := NewPreflightService(store, PreflightOptions{RouteResolver: resolver}).Build(ctx, teamID)
	require.NoError(t, err)
	require.Equal(t, PreflightRouteCapabilityResolved, report.RoutePreviewCapability)
	require.Len(t, report.RoutePreviews, 1)
	require.Equal(t, "injected-provider", report.RoutePreviews[0].Route.Provider)
	require.Equal(t, "injected-model", report.RoutePreviews[0].Route.Model)
	require.NotContains(t, preflightCodes(report), PreflightCodeRoutePreviewUnavailable)
}

type preflightRouteResolverFunc func(context.Context, TaskRouteRequest) (*TaskRouteResolution, error)

func (f preflightRouteResolverFunc) ResolveTaskRoute(
	ctx context.Context,
	request TaskRouteRequest,
) (*TaskRouteResolution, error) {
	return f(ctx, request)
}

func preflightString(value string) *string {
	return &value
}

func requirePreflightHasCode(t *testing.T, report *PreflightReport, code string) {
	t.Helper()
	require.Contains(t, preflightCodes(report), code)
}

func requirePreflightFinding(t *testing.T, report *PreflightReport, code string, check func(PreflightFinding)) {
	t.Helper()
	for _, finding := range report.Findings {
		if finding.Code == code {
			check(finding)
			return
		}
	}
	require.FailNow(t, "preflight finding not found", "code=%s", code)
}

func preflightCodes(report *PreflightReport) []string {
	codes := make([]string, 0, len(report.Findings))
	for _, finding := range report.Findings {
		codes = append(codes, finding.Code)
	}
	return codes
}

func preflightHasFinding(report *PreflightReport, code, taskID string) bool {
	for _, finding := range report.Findings {
		if finding.Code == code && finding.TaskID == taskID {
			return true
		}
	}
	return false
}

func TestPreflightRejectsMissingStore(t *testing.T) {
	_, err := NewPreflightService(nil).Build(context.Background(), "team-a")
	require.Error(t, err)
	require.EqualError(t, err, "team preflight store is not configured")
}
