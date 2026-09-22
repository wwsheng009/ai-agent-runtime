package commands

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// manualAuditDeliveries records explicit wake deliveries requested by
// /supervision wake --deliver.
type manualAuditDeliveries struct {
	mu    sync.Mutex
	total int
}

func (d *manualAuditDeliveries) record() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.total++
}

func (d *manualAuditDeliveries) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.total
}

// newManualAuditSession wires the manual audit entry (/supervision) on top of
// the standard local supervision host: durable store + control plane, plus a
// wake consumer whose gates the tests can flip. turnEndCheck=false is the
// 2026-09-22 manual audit default (supervision.turn_end_check 未设置).
func newManualAuditSession(t *testing.T, turnEndCheck bool) (*ChatSession, *localChatRuntimeHost, *supervision.WakeScheduler, *manualAuditDeliveries) {
	t.Helper()
	host := newLocalSupervisionTestHost(t)
	if turnEndCheck {
		enabled := true
		host.supervisionConfig = supervision.Config{TurnEndCheck: &enabled}
	}
	require.NotNil(t, host.Supervision.Wakes, "控制面必须装配 wake scheduler")
	scheduler := host.Supervision.Wakes
	deliveries := &manualAuditDeliveries{}
	host.supervisionWake = &supervision.WakeConsumer{
		Wakes: scheduler,
		Runnable: func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool {
			return true
		},
		Deliver: func(ctx context.Context, parentSessionID, rootScopeID string, digest *supervision.Digest, wakeIDs []string) error {
			deliveries.record()
			return nil
		},
	}
	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "root-session"},
		LocalRuntimeHost: host,
	}
	return session, host, scheduler, deliveries
}

func manualAuditPendingWakes(t *testing.T, host *localChatRuntimeHost) []supervision.WakePending {
	t.Helper()
	pending, err := host.Supervision.Store.ListWakePending(context.Background(), supervision.WakeFilter{
		RootScopeID:   "root-session",
		UnclaimedOnly: true,
	})
	require.NoError(t, err)
	return pending
}

// scheduleManualAuditCriticalWake projects one critical lifecycle row plus its
// durable wake through the plane's own scheduler. The subject execution run is
// seeded as well, otherwise the CLI's P2-12 stale probe (which has no durable
// existence proof for a fictional child) downgrades the row and it stops
// counting as critical_unresolved.
func scheduleManualAuditCriticalWake(t *testing.T, host *localChatRuntimeHost, scheduler *supervision.WakeScheduler) {
	t.Helper()
	seedSupervisionExecutionRun(t, host, "child-1")
	_, err := supervision.ProjectLifecycle(context.Background(), host.Supervision.Store, scheduler, supervision.LifecycleProjection{
		RootScopeID:           "root-session",
		TargetParentSessionID: "root-session",
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "child-1",
		EventType:             "exception",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionBlocked,
	})
	require.NoError(t, err)
}

// TestSupervisionManualAuditStatusAndAuditAreReadOnly pins the manual audit
// contract (docs/plan/supervision-manual-audit-plan-20260922.md §3): status and
// audit report the same picture as the HTTP endpoint, never claim or resolve a
// durable wake, and status additionally stays short (counters, no matrix).
func TestSupervisionManualAuditStatusAndAuditAreReadOnly(t *testing.T) {
	session, host, scheduler, deliveries := newManualAuditSession(t, false)
	scheduleManualAuditCriticalWake(t, host, scheduler)

	status, err := handleChatSupervisionCommand(session, "status")
	require.NoError(t, err)
	require.Contains(t, status, "supervision 状态（manual audit / status）")
	require.Contains(t, status, "自动核查（turn 结束 drain + digest-only self-check）：关闭（默认；")
	require.Contains(t, status, "lifecycle digest：critical_unresolved=1")
	require.Contains(t, status, "未领取 durable wake：1")
	require.Contains(t, status, "wake 预算")
	require.NotContains(t, status, "descendants 矩阵", "status 只给计数，矩阵属于 audit")

	audit, err := handleChatSupervisionCommand(session, "audit")
	require.NoError(t, err)
	require.Contains(t, audit, "supervision 手动核查（manual audit / audit）")
	require.Contains(t, audit, "descendants 矩阵：rows=")
	require.Contains(t, audit, "agent_run:child-1 state=blocked")
	require.Contains(t, audit, "notification=")
	require.Contains(t, audit, "提示：/supervision list 查看通知明细")

	require.Equal(t, 0, deliveries.count(), "只读核查不得投递")
	require.Len(t, manualAuditPendingWakes(t, host), 1, "只读核查不得 claim durable wake")

	// --json 与文本同源：脚本与人类永远看到同一组数字。
	payload, err := handleChatSupervisionCommand(session, "status --json")
	require.NoError(t, err)
	var report chatSupervisionReport
	require.NoError(t, json.Unmarshal([]byte(payload), &report))
	require.Equal(t, []string{"root-session"}, report.Scopes)
	require.False(t, report.AutoAuditEnabled)
	require.False(t, report.TurnEndCheck)
	require.Equal(t, 1, report.PendingWakeCount)
	require.NotNil(t, report.Digest)
	require.Equal(t, 1, report.Digest.CriticalUnresolved)
	require.Nil(t, report.Snapshot, "status --json 不带 descendants 矩阵")
	require.NotEmpty(t, report.WakeBudget)

	detailed, err := handleChatSupervisionCommand(session, "audit --json")
	require.NoError(t, err)
	var auditReport chatSupervisionReport
	require.NoError(t, json.Unmarshal([]byte(detailed), &auditReport))
	require.NotNil(t, auditReport.Snapshot, "audit --json 必须带 descendants 矩阵")
	require.Len(t, manualAuditPendingWakes(t, host), 1)
}

// TestSupervisionManualAuditStatusReflectsFallbackSwitch covers the灰度回退
// projection: supervision.turn_end_check=true restores the automatic check and
// both the text and --json report must say so.
func TestSupervisionManualAuditStatusReflectsFallbackSwitch(t *testing.T) {
	session, _, _, _ := newManualAuditSession(t, true)

	status, err := handleChatSupervisionCommand(session, "status")
	require.NoError(t, err)
	require.Contains(t, status, "自动核查（turn 结束 drain + digest-only self-check）：开启（supervision.turn_end_check=true")

	payload, err := handleChatSupervisionCommand(session, "status --json")
	require.NoError(t, err)
	var report chatSupervisionReport
	require.NoError(t, json.Unmarshal([]byte(payload), &report))
	require.True(t, report.AutoAuditEnabled)
	require.True(t, report.TurnEndCheck)
}

// TestSupervisionManualAuditWakePreviewThenDeliver pins the two-step wake
// entry: bare `wake` is a preview that keeps the row durable, --deliver is the
// explicit manual drain that consumes it when the parent is runnable.
func TestSupervisionManualAuditWakePreviewThenDeliver(t *testing.T) {
	session, host, scheduler, deliveries := newManualAuditSession(t, false)
	scheduleManualAuditCriticalWake(t, host, scheduler)

	preview, err := handleChatSupervisionCommand(session, "wake")
	require.NoError(t, err)
	require.Contains(t, preview, "supervision wake（手动投递）")
	require.Contains(t, preview, "以上仅为预览（不 claim、不 resolve）")
	require.Equal(t, 0, deliveries.count())
	require.Len(t, manualAuditPendingWakes(t, host), 1, "预览不得 claim wake")

	delivered, err := handleChatSupervisionCommand(session, "wake --deliver")
	require.NoError(t, err)
	require.Contains(t, delivered, "投递结果：已请求投递（scope=root-session）")
	require.Equal(t, 1, deliveries.count())
	require.Empty(t, manualAuditPendingWakes(t, host), "投递成功后 wake 行必须被消费")
}

// TestSupervisionManualAuditWakeHonorsRunnableAndBudgetGates pins the 2026-09-22
// 边界：手动投递不是"绕过闸门"。父会话忙或预算耗尽时 wake 必须保持 durable，
// 由下一次自然 turn 的 preflight digest 或滚动窗口继续兜底。
func TestSupervisionManualAuditWakeHonorsRunnableAndBudgetGates(t *testing.T) {
	session, host, scheduler, deliveries := newManualAuditSession(t, false)
	scheduleManualAuditCriticalWake(t, host, scheduler)

	host.supervisionWake.Runnable = func(context.Context, string, string, string) bool { return false }
	busy, err := handleChatSupervisionCommand(session, "wake --deliver")
	require.NoError(t, err)
	require.Contains(t, busy, "父会话当前不可运行")
	require.Equal(t, 0, deliveries.count())
	require.Len(t, manualAuditPendingWakes(t, host), 1)

	// 预算耗尽：durable 账本里该 scope 的 other class 已用掉窗口内唯一额度
	// （exception → WakeBudgetClassOther）。
	tight := supervision.NewWakeScheduler(host.Supervision.Store, supervision.WakeSchedulerConfig{
		BudgetMode:           supervision.WakeBudgetModeDurable,
		MaxAutoWakePerWindow: 1,
	})
	host.Supervision.Wakes = tight
	host.supervisionWake.Wakes = tight
	host.supervisionWake.Runnable = func(context.Context, string, string, string) bool { return true }
	require.NoError(t, host.Supervision.Store.RecordWakeClaim(context.Background(), supervision.WakeClaim{
		ClaimID:     "manual-audit-budget-1",
		RootScopeID: "root-session",
		BudgetClass: supervision.WakeBudgetClassOther,
		WakeReason:  "exception",
		ClaimedAt:   time.Now().UTC(),
	}))

	limited, err := handleChatSupervisionCommand(session, "wake --deliver")
	require.NoError(t, err)
	require.Contains(t, limited, "wake 预算已耗尽")
	require.Equal(t, 0, deliveries.count())
	require.Len(t, manualAuditPendingWakes(t, host), 1)
}

// TestSupervisionManualAuditDelegatesActionSubcommands pins that list/ack/...
// stay on the /debug supervision implementation: one parser, one CAS/audit
// semantics, no second entry point that could drift.
func TestSupervisionManualAuditDelegatesActionSubcommands(t *testing.T) {
	session, host, scheduler, _ := newManualAuditSession(t, false)
	scheduleManualAuditCriticalWake(t, host, scheduler)

	list, err := handleChatSupervisionCommand(session, "list")
	require.NoError(t, err)
	require.Contains(t, list, "supervision 通知（root_scope=root-session）")
	require.Contains(t, list, "child-1")

	_, err = handleChatSupervisionCommand(session, "ack")
	require.Error(t, err, "缺参数的委托子命令必须沿用 /debug supervision 的既有报错")

	help, err := handleChatSupervisionCommand(session, "help")
	require.NoError(t, err)
	require.Contains(t, help, "/supervision audit")
	require.Contains(t, help, "/debug supervision")
}

// TestSupervisionManualAuditParserAndQueueSafety pins the input contract: bare
// /supervision is the read-only status, unknown flags fail loudly, and only
// read-only subcommands may park in the busy input queue.
func TestSupervisionManualAuditParserAndQueueSafety(t *testing.T) {
	session, _, _, _ := newManualAuditSession(t, false)

	bare, err := handleChatSupervisionCommand(session, "")
	require.NoError(t, err)
	require.Contains(t, bare, "supervision 状态（manual audit / status）", "裸 /supervision 等价 status")

	_, err = handleChatSupervisionCommand(session, "bogus")
	require.ErrorContains(t, err, "未知 supervision 子命令")

	_, err = handleChatSupervisionCommand(session, "status --deliver")
	require.ErrorContains(t, err, "--deliver 仅适用于")

	_, err = handleChatSupervisionCommand(session, "status --limit 0")
	require.ErrorContains(t, err, "--limit 需要正整数")

	require.True(t, chatSupervisionSubcommandQueueSafe(nil))
	require.True(t, chatSupervisionSubcommandQueueSafe([]string{"status"}))
	require.True(t, chatSupervisionSubcommandQueueSafe([]string{"audit", "--json"}))
	require.True(t, chatSupervisionSubcommandQueueSafe([]string{"list", "--all"}))
	require.False(t, chatSupervisionSubcommandQueueSafe([]string{"wake"}))
	require.False(t, chatSupervisionSubcommandQueueSafe([]string{"wake", "--deliver"}))
	require.False(t, chatSupervisionSubcommandQueueSafe([]string{"ack", "n1", "--note", "x"}))
	require.False(t, chatSupervisionSubcommandQueueSafe([]string{"resolve", "n1"}))
	require.False(t, chatSupervisionSubcommandQueueSafe([]string{"control", "close", "n1"}))
}
