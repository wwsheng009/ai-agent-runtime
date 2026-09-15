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

// P2-12 残留（N9 同族）：本地操作员入口此前只有 ack/defer/resolve，卡住的子会话
// 只能靠模型自己去调 control_descendant。本组用例锁定 /debug supervision control
// 与模型侧、HTTP 宿主共用同一条 durable 控制路径。
func TestChatDebugSupervisionControlRunsDurableAction(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	executor := &recordingSupervisionExecutor{}
	host.Supervision.SetActionExecutor(executor)
	session := newChatDebugSupervisionSession(host, "parent-session")
	notification := upsertChatDebugSupervisionNotification(t, host, "parent-session", "batch-debug-control-1", supervision.SeverityCritical, 1)

	require.Contains(t, chatDebugSupervisionUsageText(), "supervision control")

	// 省略 --cascade 等价于只作用于标的本身；只有 cancel_subtree/close/retry
	// 这类动作才允许 descendants（由 ActionService 复核，见下一条用例）。
	const reason = "stuck child never finishes"
	text, err := handleChatDebugSupervisionCommand(session,
		"supervision control "+notification.NotificationID+" --action cancel --reason \""+reason+"\"")
	require.NoError(t, err)
	require.Contains(t, text, "control cancel")
	require.Contains(t, text, notification.NotificationID)
	require.Contains(t, text, "action_id=")
	require.Contains(t, text, "status=completed")
	require.Contains(t, text, "cascade=none")
	require.Len(t, executor.calls, 1)
	require.Equal(t, supervision.ActionCancel, executor.calls[0].Action)
	require.Equal(t, supervision.CascadeNone, executor.calls[0].CascadeMode)
	require.Equal(t, reason, executor.calls[0].Reason)
	require.Equal(t, "parent-session", executor.calls[0].RequestedByID)
	require.Equal(t, notification.SubjectID, executor.calls[0].TargetID)

	// descendants 级联对支持它的动作照常透传到 durable action 行。注意这里换一个
	// 标的新建通知：control 走的是 CAS，动作会为同一标的写入解析通知并推进版本，
	// 沿用旧行 id 再次控制会被正确拒绝（stale view），而不是静默重放。
	cascadeTarget := upsertChatDebugSupervisionNotification(t, host, "parent-session", "batch-debug-control-1b", supervision.SeverityCritical, 2)
	text, err = handleChatDebugSupervisionCommand(session,
		"supervision control "+cascadeTarget.NotificationID+" --action close --reason \"close the stuck subtree\" --cascade descendants")
	require.NoError(t, err)
	require.Contains(t, text, "control close")
	require.Contains(t, text, "cascade=descendants")
	require.Len(t, executor.calls, 2)
	require.Equal(t, supervision.ActionClose, executor.calls[1].Action)
	require.Equal(t, supervision.CascadeDescendants, executor.calls[1].CascadeMode)
}

func TestChatDebugSupervisionControlValidatesOperatorInput(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	executor := &recordingSupervisionExecutor{}
	host.Supervision.SetActionExecutor(executor)
	session := newChatDebugSupervisionSession(host, "parent-session")
	notification := upsertChatDebugSupervisionNotification(t, host, "parent-session", "batch-debug-control-2", supervision.SeverityCritical, 1)

	cases := []struct {
		name    string
		command string
		want    string
	}{
		{"missing action", "supervision control " + notification.NotificationID + " --reason why", "--action"},
		{"unknown action", "supervision control " + notification.NotificationID + " --action explode --reason why", "--action"},
		{"read only action stays out of the operator mutation set", "supervision control " + notification.NotificationID + " --action inspect --reason why", "--action"},
		{"missing reason", "supervision control " + notification.NotificationID + " --action cancel", "--reason"},
		{"bad cascade", "supervision control " + notification.NotificationID + " --action cancel --reason why --cascade everywhere", "--cascade"},
		{"cascade unsupported by the action", "supervision control " + notification.NotificationID + " --action cancel --reason why --cascade descendants", "cascade"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, err := handleChatDebugSupervisionCommand(session, tc.command)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
			require.Empty(t, text)
		})
	}
	require.Empty(t, executor.calls, "invalid input must never reach the executor")
}

func TestChatDebugSupervisionControlRejectsForeignScope(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	executor := &recordingSupervisionExecutor{}
	host.Supervision.SetActionExecutor(executor)
	session := newChatDebugSupervisionSession(host, "parent-session")
	foreign := upsertChatDebugSupervisionNotification(t, host, "other-session", "batch-debug-control-3", supervision.SeverityCritical, 1)

	_, err := handleChatDebugSupervisionCommand(session,
		"supervision control "+foreign.NotificationID+" --action close --reason \"not mine\"")
	require.ErrorIs(t, err, supervision.ErrActionNotAllowed)
	require.Empty(t, executor.calls)
}

func TestChatDebugSupervisionControlRequiresWiredActionService(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	session := newChatDebugSupervisionSession(host, "parent-session")
	notification := upsertChatDebugSupervisionNotification(t, host, "parent-session", "batch-debug-control-4", supervision.SeverityCritical, 1)
	command := "supervision control " + notification.NotificationID + " --action cancel --reason \"why\""

	original := host.Supervision.Actions
	host.Supervision.Actions = nil
	t.Cleanup(func() { host.Supervision.Actions = original })
	_, err := handleChatDebugSupervisionCommand(session, command)
	require.Error(t, err)
	require.Contains(t, err.Error(), "action service")

	// 宿主完全没有监督控制面时，报错同样要指向缺失的那一层（不能悄悄成功）；
	// 这里换一个空宿主而不是把共享 host 的 Store 置 nil，否则 store 不会被关闭，
	// Windows 上的 TempDir 清理会因文件占用失败。
	_, err = handleChatDebugSupervisionCommand(newChatDebugSupervisionSession(&localChatRuntimeHost{}, "parent-session"), command)
	require.Error(t, err)
	require.Contains(t, err.Error(), "supervision store")
}
