package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

func newChatDebugSupervisionSession(host *localChatRuntimeHost, sessionID string) *ChatSession {
	return &ChatSession{
		LocalRuntimeHost: host,
		RuntimeSession:   &runtimechat.Session{ID: sessionID},
	}
}

func upsertChatDebugSupervisionNotification(t *testing.T, host *localChatRuntimeHost, scopeID, subjectID string, severity supervision.Severity, seq int64) supervision.Notification {
	t.Helper()
	now := time.Now().UTC()
	record, err := host.Supervision.Store.UpsertNotification(context.Background(), supervision.Notification{
		NotificationID:        "n-" + subjectID,
		RootScopeID:           scopeID,
		TargetParentSessionID: scopeID,
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             subjectID,
		SubjectVersion:        1,
		EventSeq:              seq,
		EventType:             "subagent.batch.failed",
		Severity:              severity,
		SupervisionState:      supervision.SupervisionBlocked,
		Reason:                "batch failed",
		ResolutionState:       supervision.ResolutionUnresolved,
		CreatedAt:             now,
		UpdatedAt:             now,
	})
	require.NoError(t, err)
	return record
}

func buildChatDebugSupervisionDigest(t *testing.T, host *localChatRuntimeHost, scopeID string) *supervision.Digest {
	t.Helper()
	digest, err := supervision.BuildDigest(context.Background(), host.Supervision.Store, supervision.DigestRequest{
		RootScopeID:           scopeID,
		TargetParentSessionID: scopeID,
	})
	require.NoError(t, err)
	return digest
}

// TestChatDebugSupervisionAckConvergesCritical is the P2-12 acceptance shape:
// a critical local notification is acknowledged through the CLI entry and the
// next preflight digest no longer counts it.
func TestChatDebugSupervisionAckConvergesCritical(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	session := newChatDebugSupervisionSession(host, "parent-session")
	notification := upsertChatDebugSupervisionNotification(t, host, "parent-session", "batch-ack-1", supervision.SeverityCritical, 1)

	require.Equal(t, 1, buildChatDebugSupervisionDigest(t, host, "parent-session").CriticalUnresolved)

	listText, err := handleChatDebugSupervisionCommand(session, "supervision list")
	require.NoError(t, err)
	require.Contains(t, listText, notification.NotificationID)
	require.Contains(t, listText, "action_required=true")

	const note = "reviewed dead batch in terminal"
	ackText, err := handleChatDebugSupervisionCommand(session, "supervision ack "+notification.NotificationID+" --note \""+note+"\"")
	require.NoError(t, err)
	require.Contains(t, ackText, "acknowledged "+notification.NotificationID)

	digest := buildChatDebugSupervisionDigest(t, host, "parent-session")
	require.Equal(t, 0, digest.CriticalUnresolved, "acknowledged critical must leave critical_unresolved")
	require.Equal(t, 0, digest.ActionRequired)
	require.Empty(t, digest.Items)

	actions, err := host.Supervision.Store.ListActions(context.Background(), supervision.ActionFilter{
		RootScopeID: "parent-session",
	})
	require.NoError(t, err)
	require.Len(t, actions, 1, "ack must persist a durable audit row")
	require.Equal(t, supervision.ActionAcknowledge, actions[0].Action)
	require.Equal(t, note, actions[0].Reason)
}

func TestChatDebugSupervisionAckRequiresNote(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	session := newChatDebugSupervisionSession(host, "parent-session")
	notification := upsertChatDebugSupervisionNotification(t, host, "parent-session", "batch-note", supervision.SeverityCritical, 1)

	_, err := handleChatDebugSupervisionCommand(session, "supervision ack "+notification.NotificationID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--note")

	stored, err := host.Supervision.Store.GetNotification(context.Background(), notification.NotificationID)
	require.NoError(t, err)
	require.NotEqual(t, supervision.DecisionAcknowledged, stored.DecisionState, "a missing note must not acknowledge")
}

func TestChatDebugSupervisionRejectsForeignScope(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	session := newChatDebugSupervisionSession(host, "parent-session")
	notification := upsertChatDebugSupervisionNotification(t, host, "other-session", "batch-foreign", supervision.SeverityCritical, 1)

	_, err := handleChatDebugSupervisionCommand(session, "supervision ack "+notification.NotificationID+" --note=foreign")
	require.Error(t, err)
	require.Contains(t, err.Error(), "不属于当前会话")

	stored, err := host.Supervision.Store.GetNotification(context.Background(), notification.NotificationID)
	require.NoError(t, err)
	require.NotEqual(t, supervision.DecisionAcknowledged, stored.DecisionState)
}

func TestChatDebugSupervisionDeferLeavesPreflightUntilDue(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	session := newChatDebugSupervisionSession(host, "parent-session")
	notification := upsertChatDebugSupervisionNotification(t, host, "parent-session", "batch-defer", supervision.SeverityCritical, 1)

	text, err := handleChatDebugSupervisionCommand(session, "supervision defer "+notification.NotificationID+" --until 1h --reason \"waiting for provider\"")
	require.NoError(t, err)
	require.Contains(t, text, "deferred "+notification.NotificationID)

	digest := buildChatDebugSupervisionDigest(t, host, "parent-session")
	require.Equal(t, 0, digest.CriticalUnresolved, "deferred-not-due must leave the ordinary preflight")
	require.Empty(t, digest.Items)

	_, err = handleChatDebugSupervisionCommand(session, "supervision defer "+notification.NotificationID+" --until 5")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--until")
}

func TestChatDebugSupervisionResolveClosesNotification(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	session := newChatDebugSupervisionSession(host, "parent-session")
	notification := upsertChatDebugSupervisionNotification(t, host, "parent-session", "batch-resolve", supervision.SeverityCritical, 1)

	text, err := handleChatDebugSupervisionCommand(session, "supervision resolve "+notification.NotificationID+" --state closed")
	require.NoError(t, err)
	require.Contains(t, text, "resolved "+notification.NotificationID)

	stored, err := host.Supervision.Store.GetNotification(context.Background(), notification.NotificationID)
	require.NoError(t, err)
	require.Equal(t, supervision.ResolutionClosed, stored.ResolutionState)
	require.Equal(t, 0, buildChatDebugSupervisionDigest(t, host, "parent-session").CriticalUnresolved)
}

func TestChatDebugSupervisionVersionConflictIsReported(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	session := newChatDebugSupervisionSession(host, "parent-session")
	notification := upsertChatDebugSupervisionNotification(t, host, "parent-session", "batch-conflict", supervision.SeverityCritical, 1)

	_, err := handleChatDebugSupervisionCommand(session,
		"supervision ack "+notification.NotificationID+" --note=conflict --expected-version 99")
	require.Error(t, err)
	require.Contains(t, err.Error(), "version")

	stored, err := host.Supervision.Store.GetNotification(context.Background(), notification.NotificationID)
	require.NoError(t, err)
	require.NotEqual(t, supervision.DecisionAcknowledged, stored.DecisionState, "stale version must not acknowledge")
}

func TestChatDebugSupervisionWithoutHostStore(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "parent-session"}}
	_, err := handleChatDebugSupervisionCommand(session, "supervision list")
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "supervision store"))
}

func TestIsChatDebugSupervisionArgument(t *testing.T) {
	require.True(t, isChatDebugSupervisionArgument("supervision list"))
	require.True(t, isChatDebugSupervisionArgument("  Supervision ack n-1"))
	require.False(t, isChatDebugSupervisionArgument("status"))
	require.False(t, isChatDebugSupervisionArgument(""))
}

// TestChatDebugSupervisionListShowsWakeBudget covers the P0-4/P1-6 CLI half:
// the same local entry point that lists notifications also reports the
// auto-wake budget per root scope and class.
func TestChatDebugSupervisionListShowsWakeBudget(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	session := newChatDebugSupervisionSession(host, "parent-session")

	text, err := handleChatDebugSupervisionCommand(session, "supervision list")
	require.NoError(t, err)
	require.Contains(t, text, "wake 预算")
	require.Contains(t, text, "approval=unlimited")
	require.Contains(t, text, "failure=0/5")
	require.Contains(t, text, "other=0/5")
}

// TestLocalSupervisionSubjectPresenceMarksMissingRunStale covers the CLI host
// half of P2-12: a critical notification whose execution run row is gone is
// downgraded to stale instead of being re-counted as critical every turn.
func TestLocalSupervisionSubjectPresenceMarksMissingRunStale(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	ctx := context.Background()
	upsertChatDebugSupervisionNotification(t, host, "parent-session", "batch-missing", supervision.SeverityCritical, 1)
	upsertChatDebugSupervisionNotification(t, host, "parent-session", "batch-alive", supervision.SeverityCritical, 2)

	runStore, ok := host.Supervision.Store.(supervision.ExecutionRunStore)
	require.True(t, ok)
	now := time.Now().UTC()
	_, err := runStore.CreateExecutionRun(ctx, supervision.ExecutionRun{
		RunID:           "batch-alive",
		Kind:            supervision.RunKindAgentRun,
		RootSessionID:   "parent-session",
		ParentSessionID: "parent-session",
		Status:          supervision.RunStatusRunning,
		StartedAt:       now,
		LastHeartbeatAt: now,
		LastProgressAt:  now,
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	require.NoError(t, err)

	digest, err := supervision.BuildDigest(ctx, host.Supervision.Store, supervision.DigestRequest{
		RootScopeID:           "parent-session",
		TargetParentSessionID: "parent-session",
		SubjectPresence:       localSupervisionSubjectPresence(host),
	})
	require.NoError(t, err)
	require.Equal(t, 1, digest.CriticalUnresolved, "only the live run stays critical")
	require.Equal(t, 1, digest.StaleSubjects)
	require.Contains(t, digest.Text, "stale_subjects: 1")
	require.Contains(t, digest.Text, "- agent_run batch-missing: stale")
	require.Contains(t, digest.Text, "batch-alive")
}
