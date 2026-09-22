package commands

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// loadChatDebugSupervisionRow reads the durable notification row so a test can
// assert on the CAS bookkeeping (state + version) instead of the rendered text.
func loadChatDebugSupervisionRow(t *testing.T, host *localChatRuntimeHost, notificationID string) supervision.Notification {
	t.Helper()
	record, err := host.Supervision.Store.GetNotification(context.Background(), notificationID)
	require.NoError(t, err)
	require.NotNil(t, record, "notification %s must stay readable", notificationID)
	return *record
}

// listChatDebugSupervisionActions returns the durable audit rows for a scope so a
// test can assert "rejected mutation wrote nothing" without hard-coding how many
// rows the accepted mutation itself produces.
func listChatDebugSupervisionActions(t *testing.T, host *localChatRuntimeHost, scopeID string) []supervision.ActionRecord {
	t.Helper()
	actions, err := host.Supervision.Store.ListActions(context.Background(), supervision.ActionFilter{
		RootScopeID: scopeID,
	})
	require.NoError(t, err)
	return actions
}

// TestChatDebugSupervisionCASVersionMatchSucceeds pins the happy path of the
// optimistic-concurrency guard: `--expected-version` equal to the version the
// operator just listed must be accepted, and the accepted mutation must advance
// the durable row version by exactly one.
//
// 覆盖缺口（2026-09-22）：此前所有成功用例都省略 --expected-version（走
// HasVersion=false 分支，CLI 直接采用刚读到的版本），唯一带版本的是冲突用例。
// 于是把 CLI 的比较写反（== 代替 !=）这套用例仍然全绿：成功用例不传版本，
// 冲突用例本来就期待报错。本用例是"匹配即放行"分支的直接探测器。
func TestChatDebugSupervisionCASVersionMatchSucceeds(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	session := newChatDebugSupervisionSession(host, "parent-session")

	// ack：版本匹配 → 成功，且只推进一次。
	ackTarget := upsertChatDebugSupervisionNotification(t, host, "parent-session", "batch-cas-ack", supervision.SeverityCritical, 1)
	require.EqualValues(t, 1, loadChatDebugSupervisionRow(t, host, ackTarget.NotificationID).Version)

	text, err := handleChatDebugSupervisionCommand(session, fmt.Sprintf(
		"supervision ack %s --note \"reviewed dead batch\" --expected-version %d",
		ackTarget.NotificationID, ackTarget.Version))
	require.NoError(t, err)
	require.Contains(t, text, "acknowledged "+ackTarget.NotificationID)
	acked := loadChatDebugSupervisionRow(t, host, ackTarget.NotificationID)
	require.Equal(t, supervision.DecisionAcknowledged, acked.DecisionState)
	require.EqualValues(t, 2, acked.Version, "ack 成功必须把 version 从 1 推到 2")

	// defer：版本匹配 → 成功，且 defer_until 生效（digest 不再计入）。
	deferTarget := upsertChatDebugSupervisionNotification(t, host, "parent-session", "batch-cas-defer", supervision.SeverityCritical, 2)
	text, err = handleChatDebugSupervisionCommand(session, fmt.Sprintf(
		"supervision defer %s --until 1h --reason \"waiting for provider\" --expected-version %d",
		deferTarget.NotificationID, deferTarget.Version))
	require.NoError(t, err)
	require.Contains(t, text, "deferred "+deferTarget.NotificationID)
	deferred := loadChatDebugSupervisionRow(t, host, deferTarget.NotificationID)
	require.Equal(t, supervision.DecisionDeferred, deferred.DecisionState)
	require.EqualValues(t, 2, deferred.Version)

	// resolve：版本匹配 → 成功并落 closed。
	resolveTarget := upsertChatDebugSupervisionNotification(t, host, "parent-session", "batch-cas-resolve", supervision.SeverityCritical, 3)
	text, err = handleChatDebugSupervisionCommand(session, fmt.Sprintf(
		"supervision resolve %s --state closed --expected-version %d",
		resolveTarget.NotificationID, resolveTarget.Version))
	require.NoError(t, err)
	require.Contains(t, text, "resolved "+resolveTarget.NotificationID)
	resolved := loadChatDebugSupervisionRow(t, host, resolveTarget.NotificationID)
	require.Equal(t, supervision.ResolutionClosed, resolved.ResolutionState)
	require.EqualValues(t, 2, resolved.Version)
}

// TestChatDebugSupervisionCASStaleVersionAfterMatchIsRejected 续接上一条：先用匹配
// 版本改一次（v1→v2），再用已经过期的 v1 重放，必须在 CLI 层被拦下
// （ErrActionConflict + 指向重新 list 的提示），且 durable 行与审计行都不得被二次
// 修改——"CAS 成功一次之后旧版本立刻失效"是同一条不变量。
//
// 为什么是 defer→ack 而不是 ack→ack：Evaluator 规则规定 acknowledged 之后只允许
// inspect（evaluator.go:19），ack→ack 会先撞 allowed-actions 守卫
// （ErrActionNotAllowed），把要验证的 CAS 守卫整个遮住。deferred-but-not-due 仍允许
// inspect+acknowledge（evaluator.go:20），所以第二次 ack 能真正走到版本比较。
func TestChatDebugSupervisionCASStaleVersionAfterMatchIsRejected(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	session := newChatDebugSupervisionSession(host, "parent-session")
	target := upsertChatDebugSupervisionNotification(t, host, "parent-session", "batch-cas-replay", supervision.SeverityCritical, 1)
	listedVersion := target.Version

	_, err := handleChatDebugSupervisionCommand(session, fmt.Sprintf(
		"supervision defer %s --until 30m --reason \"first pass\" --expected-version %d", target.NotificationID, listedVersion))
	require.NoError(t, err)
	afterFirst := loadChatDebugSupervisionRow(t, host, target.NotificationID)
	require.EqualValues(t, 2, afterFirst.Version)
	require.Equal(t, supervision.DecisionDeferred, afterFirst.DecisionState)

	// 第一次变更已经把 version 推到 2；此刻重放操作员先前 list 到的 v1。
	actionsBefore := listChatDebugSupervisionActions(t, host, "parent-session")

	_, err = handleChatDebugSupervisionCommand(session, fmt.Sprintf(
		"supervision ack %s --note \"replay\" --expected-version %d", target.NotificationID, listedVersion))
	require.ErrorIs(t, err, supervision.ErrActionConflict)
	require.Contains(t, err.Error(), "命令传入 expected=",
		"过期版本必须在 CLI 层就被识别为 CAS 冲突")

	after := loadChatDebugSupervisionRow(t, host, target.NotificationID)
	require.EqualValues(t, 2, after.Version, "被拒绝的重放不得推进 version")
	require.Equal(t, supervision.DecisionDeferred, after.DecisionState, "被拒绝的重放不得改动 decision")

	require.Equal(t, len(actionsBefore), len(listChatDebugSupervisionActions(t, host, "parent-session")),
		"被拒绝的重放不得写入新的审计行")
}

// TestChatDebugSupervisionControlCASVersionMatchSucceeds covers the control half
// of the same guard: `--expected-version` must travel through
// LocalControlService → ActionService (target version check) and land on the
// durable action row, so an operator who acted on a stale list view is stopped
// before the child is cancelled.
func TestChatDebugSupervisionControlCASVersionMatchSucceeds(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	executor := &recordingSupervisionExecutor{}
	host.Supervision.SetActionExecutor(executor)
	session := newChatDebugSupervisionSession(host, "parent-session")
	target := upsertChatDebugSupervisionNotification(t, host, "parent-session", "batch-cas-control", supervision.SeverityCritical, 1)

	text, err := handleChatDebugSupervisionCommand(session, fmt.Sprintf(
		"supervision control %s --action cancel --reason \"stuck child never finishes\" --expected-version %d",
		target.NotificationID, target.Version))
	require.NoError(t, err)
	require.Contains(t, text, "control cancel")
	require.Contains(t, text, "status=completed")
	require.Len(t, executor.calls, 1, "匹配版本必须放行到真正的控制执行器")
	require.Equal(t, supervision.ActionCancel, executor.calls[0].Action)

	actions, err := host.Supervision.Store.ListActions(context.Background(), supervision.ActionFilter{
		RootScopeID: "parent-session",
	})
	require.NoError(t, err)
	require.Len(t, actions, 1)
	require.EqualValues(t, target.Version, actions[0].ExpectedVersion,
		"操作员列出的版本必须原样落在 durable action 行上，供事后审计")
}
