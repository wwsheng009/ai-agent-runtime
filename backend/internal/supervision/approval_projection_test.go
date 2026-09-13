package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testApprovalNotice(requestID string) ApprovalNotice {
	return ApprovalNotice{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		ChildSessionID:        "child-1",
		ChildPath:             "/root/child-1",
		RequestID:             requestID,
		ToolName:              "write_file",
		Mode:                  ApprovalModeApproval,
		RequestedAt:           time.Now().Add(-90 * time.Second),
		Wake:                  true,
	}
}

// TestProjectApprovalRequest_DigestCarriesRequestDetails verifies the doc
// P0-2 contract: a child approval becomes an action-required notification and
// its preflight line exposes session/path/tool/request_id/waiting so a parent
// turn can act without an extra snapshot round.
func TestProjectApprovalRequest_DigestCarriesRequestDetails(t *testing.T) {
	store := newTestStore(t, "supervision-approval-digest")
	ctx := context.Background()

	notification, err := ProjectApprovalRequest(ctx, store, nil, testApprovalNotice("req-1"))
	require.NoError(t, err)
	require.Equal(t, SubjectAgentSession, notification.SubjectKind)
	require.Equal(t, "child-1", notification.SubjectID)
	require.Equal(t, EventAgentApprovalRequested, notification.EventType)
	require.Equal(t, ResolutionUnresolved, notification.ResolutionState)
	require.True(t, notification.ActionRequired())

	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
	})
	require.NoError(t, err)
	require.Equal(t, 1, digest.ActionRequired)
	require.Contains(t, digest.Text, "request_id=req-1")
	require.Contains(t, digest.Text, "tool=write_file")
	require.Contains(t, digest.Text, "/root/child-1")
	require.Contains(t, digest.Text, "waiting=")
}

// TestProjectApprovalRequest_IdempotentPerRequest verifies doc P0-2 rule 3:
// the same child session and request id only ever produce one durable row,
// while a second request from the same child still gets its own notification.
func TestProjectApprovalRequest_IdempotentPerRequest(t *testing.T) {
	store := newTestStore(t, "supervision-approval-dedup")
	ctx := context.Background()

	first, err := ProjectApprovalRequest(ctx, store, nil, testApprovalNotice("req-1"))
	require.NoError(t, err)
	replayed, err := ProjectApprovalRequest(ctx, store, nil, testApprovalNotice("req-1"))
	require.NoError(t, err)
	require.Equal(t, first.NotificationID, replayed.NotificationID)

	second, err := ProjectApprovalRequest(ctx, store, nil, testApprovalNotice("req-2"))
	require.NoError(t, err)
	require.NotEqual(t, first.NotificationID, second.NotificationID)

	all, err := store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-session-1"})
	require.NoError(t, err)
	require.Len(t, all, 2, "one durable row per request id")
}

// TestProjectApprovalRequest_RequestsParentWake verifies the controlled wake
// path: an approval schedules a durable wake so a parent that already ended
// its turn can still be started, and the wake follows the scheduler budget.
func TestProjectApprovalRequest_RequestsParentWake(t *testing.T) {
	store := newTestStore(t, "supervision-approval-wake")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})

	notification, err := ProjectApprovalRequest(ctx, store, scheduler, testApprovalNotice("req-wake"))
	require.NoError(t, err)
	require.Equal(t, SeverityCritical, notification.Severity)

	pending, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-session-1", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "root-session-1", pending[0].TargetParentSessionID)
}

// TestProjectApprovalRequest_QuestionStaysDigestOnly verifies that a child
// question needs parent attention without spending the critical wake budget.
func TestProjectApprovalRequest_QuestionStaysDigestOnly(t *testing.T) {
	store := newTestStore(t, "supervision-question-digest")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})

	notice := testApprovalNotice("q-1")
	notice.Mode = ApprovalModeQuestion
	notice.ToolName = ""
	notice.Summary = "Which database should the migration target?"

	notification, err := ProjectApprovalRequest(ctx, store, scheduler, notice)
	require.NoError(t, err)
	require.Equal(t, EventAgentQuestionAsked, notification.EventType)
	require.Equal(t, SeverityWarning, notification.Severity)
	require.True(t, notification.ActionRequired())

	pending, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-session-1", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Empty(t, pending, "questions must not auto-wake the parent")

	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
	})
	require.NoError(t, err)
	require.Equal(t, 1, digest.ActionRequired)
	require.Contains(t, digest.Text, "child question pending")
	require.Contains(t, digest.Text, "Which database")
}

// TestResolveApprovalRequest_ClosesDigestEntry verifies doc P0-2 rule 3: once
// the child decision is delivered the stale entry leaves the action-required
// digest and only a compact resolved summary remains.
func TestResolveApprovalRequest_ClosesDigestEntry(t *testing.T) {
	store := newTestStore(t, "supervision-approval-resolve")
	ctx := context.Background()

	notice := testApprovalNotice("req-resolve")
	_, err := ProjectApprovalRequest(ctx, store, nil, notice)
	require.NoError(t, err)

	resolved, err := ResolveApprovalRequest(ctx, store, notice, "allowed")
	require.NoError(t, err)
	require.Equal(t, ResolutionClosed, resolved.ResolutionState)
	require.False(t, resolved.ActionRequired())

	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
	})
	require.NoError(t, err)
	require.Equal(t, 0, digest.ActionRequired)
	require.Equal(t, 0, digest.CriticalUnresolved)
	require.Empty(t, digest.Items, "a resolved approval must leave ordinary preflight")

	digest, err = BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		IncludeResolvedSince:  true,
	})
	require.NoError(t, err)
	require.Equal(t, 1, digest.ResolvedSinceLastTurn)
	require.Contains(t, digest.Text, "resolved")
	require.Contains(t, digest.Text, "request_id=req-resolve")
}

// TestApprovalNoticeFromEvent verifies payload parsing used by both hosts.
func TestApprovalNoticeFromEvent(t *testing.T) {
	notice, ok := ApprovalNoticeFromEvent(
		"root-1", "root-1", "", "child-9", "/root/child-9",
		EventAgentQuestionAsked,
		map[string]interface{}{"question_id": "q-9", "prompt": "proceed?", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339Nano)},
		time.Now(),
	)
	require.True(t, ok)
	require.Equal(t, "q-9", notice.RequestID)
	require.Equal(t, ApprovalModeQuestion, notice.Mode)
	require.False(t, notice.Wake)
	require.False(t, notice.ExpiresAt.IsZero())

	_, ok = ApprovalNoticeFromEvent("root-1", "root-1", "", "child-9", "", EventAgentQuestionAsked, map[string]interface{}{}, time.Now())
	require.False(t, ok, "an event without a request id must not be projected")
}

// TestApprovalResolutionFromPayload verifies the outcome mapping for expired
// and denied decisions.
func TestApprovalResolutionFromPayload(t *testing.T) {
	require.Equal(t, "expired", ApprovalResolutionFromPayload(map[string]interface{}{"resolution": "expired"}))
	require.Equal(t, "denied", ApprovalResolutionFromPayload(map[string]interface{}{"allowed": false}))
	require.Equal(t, "resolved", ApprovalResolutionFromPayload(map[string]interface{}{"allowed": true}))
	require.Equal(t, "resolved", ApprovalResolutionFromPayload(nil))
}
