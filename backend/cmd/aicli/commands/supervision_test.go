package commands

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	runtimeserver "github.com/wwsheng009/ai-agent-runtime/internal/runtimeserver"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

func newLocalSupervisionTestHost(t *testing.T) *localChatRuntimeHost {
	t.Helper()
	plane, err := runtimeserver.BuildSupervisionControlPlane(t.TempDir(), supervision.Config{}, runtimeserver.SupervisionRuntimeHooks{})
	require.NoError(t, err)
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	host := &localChatRuntimeHost{
		Supervision:       plane,
		supervisionConfig: supervision.DefaultConfig(),
		TeamStore:         teamStore,
	}
	t.Cleanup(func() {
		_ = plane.Close()
		_ = teamStore.Close()
	})
	return host
}

func TestResolveLocalChatSupervisionDataDir(t *testing.T) {
	session := &ChatSession{SessionDir: t.TempDir()}
	require.Equal(t, filepath.Join(session.SessionDir, "runtime", "supervision"), resolveLocalChatSupervisionDataDir(session, nil))

	ephemeral := &ChatSession{Ephemeral: true}
	require.NotEmpty(t, resolveLocalChatSupervisionDataDir(ephemeral, nil))
}

// TestInjectLocalSupervisionPreflight verifies the CLI path automatically
// injects a durable lifecycle digest and only records delivered+seen (not ack).
func TestInjectLocalSupervisionPreflight(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	ctx := context.Background()

	notification, err := host.Supervision.Store.UpsertNotification(ctx, supervision.Notification{
		RootScopeID:           "parent-session",
		TargetParentSessionID: "parent-session",
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "child-agent",
		SubjectVersion:        1,
		EventSeq:              5,
		EventType:             "timeout",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionTimedOut,
		Reason:                "deadline exceeded",
		DecisionState:         supervision.DecisionUnacknowledged,
		ResolutionState:       supervision.ResolutionUnresolved,
	})
	require.NoError(t, err)

	prompt, err := injectLocalSupervisionPreflight(ctx, host, "parent-session", "continue work", nil)
	require.NoError(t, err)
	require.Contains(t, prompt, "[Child lifecycle preflight]")
	require.Contains(t, prompt, "child-agent")
	require.Contains(t, prompt, "continue work")

	updated, err := host.Supervision.Store.GetNotification(ctx, notification.NotificationID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, supervision.DeliverySeen, updated.DeliveryState)
	require.Equal(t, supervision.DecisionUnacknowledged, updated.DecisionState)
}

// TestInjectLocalSupervisionPreflight_DoesNotLeakTeamInboxToWorker ensures a
// team task worker does not consume a Team lead's lifecycle notifications.
func TestInjectLocalSupervisionPreflight_DoesNotLeakTeamInboxToWorker(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	ctx := context.Background()
	_, err := host.TeamStore.CreateTeam(ctx, team.Team{ID: "team-1", LeadSessionID: "lead-session", Status: team.TeamStatusActive})
	require.NoError(t, err)

	notification, err := host.Supervision.Store.UpsertNotification(ctx, supervision.Notification{
		RootScopeID:           "team-1",
		TargetParentSessionID: "lead-session",
		TargetParentTeamID:    "team-1",
		SubjectKind:           supervision.SubjectTeam,
		SubjectID:             "child-team",
		SubjectVersion:        1,
		EventSeq:              7,
		EventType:             "orphaned",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionOrphaned,
		Reason:                "orchestrator missing",
		DecisionState:         supervision.DecisionUnacknowledged,
		ResolutionState:       supervision.ResolutionUnresolved,
	})
	require.NoError(t, err)

	workerPrompt, err := injectLocalSupervisionPreflight(ctx, host, "worker-session", "perform task", &team.RunMeta{Team: &team.TeamRunMeta{TeamID: "team-1"}})
	require.NoError(t, err)
	require.Equal(t, "perform task", workerPrompt)

	updated, err := host.Supervision.Store.GetNotification(ctx, notification.NotificationID)
	require.NoError(t, err)
	require.Equal(t, supervision.DeliveryPending, updated.DeliveryState)

	leadPrompt, err := injectLocalSupervisionPreflight(ctx, host, "lead-session", "review team", &team.RunMeta{Team: &team.TeamRunMeta{TeamID: "team-1"}})
	require.NoError(t, err)
	require.Contains(t, leadPrompt, "child-team")
}

// TestLocalActorRegistry_SubmitPromptInjectsPreflight covers the CLI actor
// registry integration point mandated by the P2 plan. The action aborts at the
// intentionally unavailable SessionHub after preflight, which is enough to
// prove the hook ran and marked the notification seen.
func TestLocalActorRegistry_SubmitPromptInjectsPreflight(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	registry := newLocalActorRegistry(host)
	ctx := context.Background()

	notification, err := host.Supervision.Store.UpsertNotification(ctx, supervision.Notification{
		RootScopeID:           "parent-session",
		TargetParentSessionID: "parent-session",
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "child-agent",
		SubjectVersion:        1,
		EventSeq:              9,
		EventType:             "invalid",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionInvalid,
		Reason:                "worker state invalid",
		DecisionState:         supervision.DecisionUnacknowledged,
		ResolutionState:       supervision.ResolutionUnresolved,
	})
	require.NoError(t, err)

	_, err = registry.SubmitPrompt(ctx, "parent-session", "continue", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "session hub not configured")

	updated, err := host.Supervision.Store.GetNotification(ctx, notification.NotificationID)
	require.NoError(t, err)
	require.Equal(t, supervision.DeliverySeen, updated.DeliveryState)
}
