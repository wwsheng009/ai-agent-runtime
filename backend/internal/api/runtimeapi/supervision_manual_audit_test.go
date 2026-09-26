package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// newAPISupervisionManualAuditFixture seeds one critical lifecycle row plus its
// durable wake on top of the budget-test handler. The subject execution run is
// seeded too: without it the P2-12 stale probe downgrades the row and the
// digest stops reporting it as critical_unresolved.
func newAPISupervisionManualAuditFixture(t *testing.T, name string) (*Handler, *supervision.SQLiteSupervisionStore) {
	t.Helper()
	handler, store := newAPISupervisionBudgetTestHandler(t, name)
	_, err := store.CreateExecutionRun(context.Background(), supervision.ExecutionRun{
		RunID:     "child-1",
		Kind:      supervision.RunKindAgentRun,
		Workflow:  supervision.RunWorkflowSpawnAgent,
		SessionID: "child-1",
		AgentID:   "child-1",
		Status:    supervision.RunStatusRunning,
		OwnerID:   "root-session",
		StartedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = supervision.ProjectLifecycle(context.Background(), store, handler.getSupervisionWakeScheduler(), supervision.LifecycleProjection{
		RootScopeID:           "root-session",
		TargetParentSessionID: "root-session",
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "child-1",
		EventType:             "exception",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionBlocked,
	})
	require.NoError(t, err)
	return handler, store
}

func apiSupervisionPendingWakes(t *testing.T, store *supervision.SQLiteSupervisionStore, scope string) []supervision.WakePending {
	t.Helper()
	pending, err := store.ListWakePending(context.Background(), supervision.WakeFilter{
		RootScopeID:   scope,
		UnclaimedOnly: true,
	})
	require.NoError(t, err)
	return pending
}

// apiAuditPayload mirrors the manual audit projection. The JSON keys are part
// of the contract: scripts drive the manual audit from them.
type apiAuditPayload struct {
	AutoAuditEnabled bool                          `json:"auto_audit_enabled"`
	TurnEndCheck     bool                          `json:"turn_end_check"`
	RootScopeID      string                        `json:"root_scope_id"`
	Digest           *supervision.Digest           `json:"digest"`
	Snapshot         *supervision.Snapshot         `json:"snapshot"`
	PendingWakes     []supervision.WakePending     `json:"pending_wakes"`
	PendingWakeCount int                           `json:"pending_wake_count"`
	WakeBudget       []supervision.WakeBudgetState `json:"wake_budget"`
}

// apiDrainPayload mirrors the manual drain response.
type apiDrainPayload struct {
	RootScopeID      string `json:"root_scope_id"`
	Reason           string `json:"reason"`
	DryRun           bool   `json:"dry_run"`
	PendingWakeCount int    `json:"pending_wake_count"`
	PendingRemaining int    `json:"pending_remaining"`
	Consumed         int    `json:"consumed"`
	DeliveryError    string `json:"delivery_error"`
}

func getSupervisionAudit(t *testing.T, handler *Handler, query string) (*httptest.ResponseRecorder, apiAuditPayload) {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.GetSupervisionAudit(rec, httptest.NewRequest(http.MethodGet, "/api/runtime/supervision/audit?"+query, nil))
	var payload apiAuditPayload
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	}
	return rec, payload
}

func postSupervisionDrain(t *testing.T, handler *Handler, body string) (*httptest.ResponseRecorder, apiDrainPayload) {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.DrainSupervisionWakes(rec, httptest.NewRequest(http.MethodPost, "/api/runtime/supervision/wake/drain", strings.NewReader(body)))
	var payload apiDrainPayload
	if rec.Code == http.StatusOK || rec.Code == http.StatusConflict || rec.Code == http.StatusTooManyRequests {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	}
	return rec, payload
}

// TestSupervisionAuditEndpointIsReadOnly pins the manual audit endpoint
// (docs/plan/supervision-manual-audit-plan-20260922.md §4): one GET returns the
// digest + descendants matrix + durable backlog + budget, and it must never
// claim, resolve or deliver anything.
func TestSupervisionAuditEndpointIsReadOnly(t *testing.T) {
	handler, store := newAPISupervisionManualAuditFixture(t, "api-manual-audit")

	rec, payload := getSupervisionAudit(t, handler, "root_scope_id=root-session")
	require.Equal(t, http.StatusOK, rec.Code)
	require.False(t, payload.AutoAuditEnabled, "默认（turn_end_check 未设置）必须是手动核查")
	require.False(t, payload.TurnEndCheck)
	require.Equal(t, "root-session", payload.RootScopeID)
	require.NotNil(t, payload.Digest)
	require.Equal(t, 1, payload.Digest.CriticalUnresolved)
	require.NotNil(t, payload.Snapshot, "audit 必须带 descendants 矩阵")
	require.Len(t, payload.Snapshot.Descendants, 1)
	require.Equal(t, 1, payload.PendingWakeCount)
	require.Len(t, payload.PendingWakes, 1)
	require.NotEmpty(t, payload.WakeBudget)

	require.Len(t, apiSupervisionPendingWakes(t, store, "root-session"), 1, "audit 必须只读：不得 claim/resolve wake")
}

// TestSupervisionAuditEndpointRequiresScope keeps the endpoint honest: a
// missing scope is a client error, never an implicit "read everything" query.
func TestSupervisionAuditEndpointRequiresScope(t *testing.T) {
	handler, _ := newAPISupervisionManualAuditFixture(t, "api-manual-audit-scope")

	rec, _ := getSupervisionAudit(t, handler, "")
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// root_session_id 是 root_scope_id 的等价别名（宿主两种调用方都写）。
	rec, payload := getSupervisionAudit(t, handler, "root_session_id=root-session")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, payload.PendingWakeCount)
}

// TestSupervisionAuditEndpointReflectsTurnEndCheckSwitch pins the灰度回退投影:
// supervision.turn_end_check=true must be visible to the operator through the
// same endpoint that reports the manual default.
func TestSupervisionAuditEndpointReflectsTurnEndCheckSwitch(t *testing.T) {
	handler, _ := newAPISupervisionManualAuditFixture(t, "api-manual-audit-switch")

	enabled := true
	handler.SetSupervisionConfig(supervision.Config{TurnEndCheck: &enabled})

	_, payload := getSupervisionAudit(t, handler, "root_scope_id=root-session")
	require.True(t, payload.AutoAuditEnabled)
	require.True(t, payload.TurnEndCheck)
}

// TestSupervisionWakeDrainEndpointDryRunKeepsWakeDurable pins the preview mode:
// dry_run reports the backlog and consumes nothing.
func TestSupervisionWakeDrainEndpointDryRunKeepsWakeDurable(t *testing.T) {
	handler, store := newAPISupervisionManualAuditFixture(t, "api-manual-drain-dryrun")

	rec, payload := postSupervisionDrain(t, handler, `{"root_scope_id":"root-session","dry_run":true}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, payload.DryRun)
	require.Equal(t, "dry_run", payload.Reason)
	require.Equal(t, 1, payload.PendingWakeCount)
	require.Len(t, apiSupervisionPendingWakes(t, store, "root-session"), 1, "dry_run 不得消费 wake")
}

// TestSupervisionWakeDrainEndpointRefusesBusyParent pins the shared gate: with
// no runnable parent actor the endpoint answers 409 and the wake stays durable
// for the next natural turn preflight (手动投递 ≠ 绕过 runnable 门).
func TestSupervisionWakeDrainEndpointRefusesBusyParent(t *testing.T) {
	handler, store := newAPISupervisionManualAuditFixture(t, "api-manual-drain-busy")

	rec, payload := postSupervisionDrain(t, handler, `{"root_scope_id":"root-session","target_parent_session_id":"root-session"}`)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "parent_busy", payload.Reason)
	require.Equal(t, 0, payload.Consumed)
	require.Equal(t, 1, payload.PendingRemaining)
	require.NotEmpty(t, payload.DeliveryError)
	require.Len(t, apiSupervisionPendingWakes(t, store, "root-session"), 1, "被闸门拦下时 wake 必须保持 durable")
}

// TestSupervisionWakeDrainEndpointDeliversWhenRunnable covers the success path:
// the handler's single consumer performs the delivery and the durable row is
// consumed exactly once.
func TestSupervisionWakeDrainEndpointDeliversWhenRunnable(t *testing.T) {
	handler, store := newAPISupervisionManualAuditFixture(t, "api-manual-drain-delivered")

	deliveries := 0
	handler.supervisionWakeMu.Lock()
	handler.supervisionWake = &supervision.WakeConsumer{
		Wakes:    handler.getSupervisionWakeScheduler(),
		Runnable: func(context.Context, string, string, string) bool { return true },
		Deliver: func(context.Context, string, string, *supervision.Digest, []string) error {
			deliveries++
			return nil
		},
	}
	handler.supervisionWakeMu.Unlock()

	rec, payload := postSupervisionDrain(t, handler, `{"root_scope_id":"root-session","target_parent_session_id":"root-session"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "delivered", payload.Reason)
	require.Equal(t, 1, payload.Consumed)
	require.Equal(t, 0, payload.PendingRemaining)
	require.Equal(t, 1, deliveries)
	require.Empty(t, apiSupervisionPendingWakes(t, store, "root-session"))
}

// TestSupervisionWakeDrainEndpointRequiresScope keeps the manual entry strict:
// the scope must be explicit, otherwise the drain could target the wrong tree.
func TestSupervisionWakeDrainEndpointRequiresScope(t *testing.T) {
	handler, _ := newAPISupervisionManualAuditFixture(t, "api-manual-drain-scope")

	rec, _ := postSupervisionDrain(t, handler, `{}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestSupervisionTurnEndDrainSwitchOnAPISide pins the API-side half of the
// manual audit default (docs/plan/supervision-manual-audit-plan-20260922.md
// §2): a session turn end must not drain a pending wake while
// supervision.turn_end_check is unset, and must drain it again once the
// fallback switch is explicitly enabled.
//
// The consumer is replaced by a counting one so the assertion measures the
// switch (the callback's early return) instead of agent execution.
func TestSupervisionTurnEndDrainSwitchOnAPISide(t *testing.T) {
	handler, store, scheduler := newAPIWakeTestHandler(t, "api-turn-end-switch")
	ctx := context.Background()
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	handler.SetSessionManager(sessionManager)
	session, err := sessionManager.Create(ctx, "user-supervision-turn-end")
	require.NoError(t, err)

	var deliveries atomic.Int64
	handler.supervisionWakeMu.Lock()
	handler.supervisionWake = &supervision.WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(context.Context, string, string, string) bool { return true },
		Deliver: func(context.Context, string, string, *supervision.Digest, []string) error {
			deliveries.Add(1)
			return nil
		},
	}
	handler.supervisionWakeMu.Unlock()

	_, err = supervision.ProjectLifecycle(ctx, store, scheduler, supervision.LifecycleProjection{
		RootScopeID:           session.ID,
		TargetParentSessionID: session.ID,
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "child-1",
		EventType:             "exception",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionBlocked,
	})
	require.NoError(t, err)

	bus := handler.getRuntimeEventBus()
	require.NotNil(t, bus)
	publishTurnEnd := func() {
		bus.Publish(runtimeevents.Event{
			Type:      chat.EventSessionEnd,
			SessionID: session.ID,
			Payload:   map[string]interface{}{"success": true},
		})
	}
	pendingForSession := func() []supervision.WakePending {
		pending, listErr := store.ListWakePending(ctx, supervision.WakeFilter{
			RootScopeID:           session.ID,
			TargetParentSessionID: session.ID,
			UnclaimedOnly:         true,
		})
		require.NoError(t, listErr)
		return pending
	}

	// 默认：turn 结束不 drain（订阅仍在，只是回调提前返回）。
	publishTurnEnd()
	time.Sleep(200 * time.Millisecond)
	require.Zero(t, deliveries.Load(), "turn_end_check 未设置时 turn 结束不得 drain")
	require.Len(t, pendingForSession(), 1, "wake 必须保持 durable")

	// 显式回退开关：恢复 turn 结束自动闭合语义。
	enabled := true
	handler.SetSupervisionConfig(supervision.Config{TurnEndCheck: &enabled})
	publishTurnEnd()
	require.Eventually(t, func() bool { return deliveries.Load() == 1 }, 3*time.Second, 20*time.Millisecond,
		"turn_end_check=true 时 turn 结束必须 drain")
	require.Empty(t, pendingForSession())
}
