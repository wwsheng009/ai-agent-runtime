package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// newAPISupervisionBudgetTestHandler wires a durable-mode wake scheduler on top
// of a fresh supervision store, so budget usage recorded by one process is
// visible to the API-side projection (P1-6 方案 2).
func newAPISupervisionBudgetTestHandler(t *testing.T, name string) (*Handler, *supervision.SQLiteSupervisionStore) {
	t.Helper()
	store, err := supervision.NewSQLiteSupervisionStore(&supervision.StoreConfig{
		DSN: "file:" + name + "?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	scheduler := supervision.NewWakeScheduler(store, supervision.WakeSchedulerConfig{
		BudgetMode: supervision.WakeBudgetModeDurable,
	})
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSupervisionStore(store)
	handler.SetSupervisionWakeScheduler(scheduler)
	return handler, store
}

func recordBudgetClaim(t *testing.T, store *supervision.SQLiteSupervisionStore, claimID, scope string, class supervision.WakeBudgetClass, reason string) {
	t.Helper()
	require.NoError(t, store.RecordWakeClaim(context.Background(), supervision.WakeClaim{
		ClaimID:     claimID,
		RootScopeID: scope,
		BudgetClass: class,
		WakeReason:  reason,
		ClaimedAt:   time.Now().UTC(),
	}))
}

func decodeWakeBudget(t *testing.T, rec *httptest.ResponseRecorder) []supervision.WakeBudgetState {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		WakeBudget []supervision.WakeBudgetState `json:"wake_budget"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	return payload.WakeBudget
}

// TestSupervisionDigest_WakeBudgetProjection covers the P1-6 follow-up item: the
// API host renders the same scope × class auto-wake budget as the CLI
// `/debug supervision list` view, over the shared (durable) ledger.
func TestSupervisionDigest_WakeBudgetProjection(t *testing.T) {
	handler, store := newAPISupervisionBudgetTestHandler(t, "api-wake-budget-digest")
	recordBudgetClaim(t, store, "claim-1", "root-1", supervision.WakeBudgetClassFailure, supervision.WakeReasonExecutionFailed)
	recordBudgetClaim(t, store, "claim-2", "root-1", supervision.WakeBudgetClassFailure, supervision.WakeReasonLifecycleFailed)
	// The same claim id must not double count (idempotency key).
	recordBudgetClaim(t, store, "claim-2", "root-1", supervision.WakeBudgetClassFailure, supervision.WakeReasonLifecycleFailed)

	rec := httptest.NewRecorder()
	handler.GetSupervisionDigest(rec, httptest.NewRequest(http.MethodGet, "/api/runtime/supervision/digest?root_scope_id=root-1", nil))
	budget := decodeWakeBudget(t, rec)
	require.Len(t, budget, 4, "one row per budget class (approval/failure/other/progress)")

	byClass := make(map[supervision.WakeBudgetClass]supervision.WakeBudgetState, len(budget))
	for _, state := range budget {
		require.Equal(t, "root-1", state.RootScopeID)
		require.Greater(t, state.Window, time.Duration(0))
		require.False(t, state.WindowStart.IsZero())
		byClass[state.BudgetClass] = state
	}
	approval := byClass[supervision.WakeBudgetClassApproval]
	require.True(t, approval.Unlimited, "approval budget is unlimited by default")
	require.Zero(t, approval.Used)
	failure := byClass[supervision.WakeBudgetClassFailure]
	require.Equal(t, 2, failure.Used, "idempotent claims count once")
	require.Equal(t, 5, failure.Limit)
	require.False(t, failure.Unlimited)
	other := byClass[supervision.WakeBudgetClassOther]
	require.Zero(t, other.Used)
	require.Equal(t, 5, other.Limit)
	progress := byClass[supervision.WakeBudgetClassProgress]
	require.Zero(t, progress.Used, "progress has an independent ledger")
	require.Equal(t, 6, progress.Limit, "progress defaults to 6 per window (ADR-2)")
	require.False(t, progress.Unlimited)
}

// TestSupervisionSnapshot_WakeBudgetProjection covers the snapshot half and the
// session/team scope fallback of the same projection.
func TestSupervisionSnapshot_WakeBudgetProjection(t *testing.T) {
	handler, store := newAPISupervisionBudgetTestHandler(t, "api-wake-budget-snapshot")
	recordBudgetClaim(t, store, "claim-1", "team-1", supervision.WakeBudgetClassApproval, supervision.WakeReasonApprovalRequired)

	rec := httptest.NewRecorder()
	handler.GetSupervisionSnapshot(rec, httptest.NewRequest(http.MethodGet, "/api/runtime/supervision/snapshot?root_team_id=team-1", nil))
	budget := decodeWakeBudget(t, rec)
	require.Len(t, budget, 4)
	for _, state := range budget {
		require.Equal(t, "team-1", state.RootScopeID)
		if state.BudgetClass == supervision.WakeBudgetClassApproval {
			require.True(t, state.Unlimited)
			require.Equal(t, 1, state.Used, "unlimited classes still report usage")
		}
	}

	// Session and team scope are both projected; duplicates collapse.
	rec = httptest.NewRecorder()
	handler.GetSupervisionSnapshot(rec, httptest.NewRequest(http.MethodGet, "/api/runtime/supervision/snapshot?root_session_id=team-1&root_team_id=team-1", nil))
	require.Len(t, decodeWakeBudget(t, rec), 4, "same scope requested twice must not duplicate rows")

	rec = httptest.NewRecorder()
	handler.GetSupervisionSnapshot(rec, httptest.NewRequest(http.MethodGet, "/api/runtime/supervision/snapshot?root_session_id=session-1&root_team_id=team-1", nil))
	budget = decodeWakeBudget(t, rec)
	require.Len(t, budget, 8)
	scopes := make(map[string]int, 2)
	for _, state := range budget {
		scopes[state.RootScopeID]++
	}
	require.Equal(t, map[string]int{"session-1": 4, "team-1": 4}, scopes)
}

// TestSupervisionWakeBudget_OmittedWithoutScheduler verifies the field is absent
// (rather than a zero-valued 0/limit row) when the host has no wake ledger, or
// when no scope was requested.
func TestSupervisionWakeBudget_OmittedWithoutScheduler(t *testing.T) {
	store, err := supervision.NewSQLiteSupervisionStore(&supervision.StoreConfig{
		DSN: "file:api-wake-budget-absent?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	bare := NewHandler(skill.NewRegistry(nil), nil, nil)
	bare.SetSupervisionStore(store)

	rec := httptest.NewRecorder()
	bare.GetSupervisionDigest(rec, httptest.NewRequest(http.MethodGet, "/api/runtime/supervision/digest?root_scope_id=root-1", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.False(t, hasWakeBudgetField(t, rec.Body.Bytes()))

	// Scheduler wired but no scope requested: the projection stays empty and
	// the response omits the field instead of implying an unused budget.
	handler, _ := newAPISupervisionBudgetTestHandler(t, "api-wake-budget-empty")
	rec = httptest.NewRecorder()
	handler.GetSupervisionDigest(rec, httptest.NewRequest(http.MethodGet, "/api/runtime/supervision/digest", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.False(t, hasWakeBudgetField(t, rec.Body.Bytes()))

	rec = httptest.NewRecorder()
	handler.GetSupervisionSnapshot(rec, httptest.NewRequest(http.MethodGet, "/api/runtime/supervision/snapshot", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.False(t, hasWakeBudgetField(t, rec.Body.Bytes()))

	// Store missing entirely: handlers keep degrading to 503.
	empty := NewHandler(skill.NewRegistry(nil), nil, nil)
	rec = httptest.NewRecorder()
	empty.GetSupervisionDigest(rec, httptest.NewRequest(http.MethodGet, "/api/runtime/supervision/digest?root_scope_id=root-1", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func hasWakeBudgetField(t *testing.T, body []byte) bool {
	t.Helper()
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &raw))
	_, ok := raw["wake_budget"]
	return ok
}

// TestSupervisionPreflight_IncludesWakeBudgetLine covers the P1-6 follow-up: the
// parent turn's model-visible digest text carries the auto-wake ledger, so a
// wake that was deferred (rate-limited) instead of delivered is observable
// inside the turn rather than only through host diagnostics. Hosts without a
// wired scheduler keep the previous text.
func TestSupervisionPreflight_IncludesWakeBudgetLine(t *testing.T) {
	handler, store := newAPISupervisionBudgetTestHandler(t, "api-preflight-wake-budget")
	ctx := context.Background()
	_, err := supervision.ProjectLifecycle(ctx, store, handler.getSupervisionWakeScheduler(), supervision.LifecycleProjection{
		RootScopeID:           "root-1",
		TargetParentSessionID: "root-1",
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "child-1",
		EventType:             supervision.WakeReasonExecutionFailed,
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionBlocked,
	})
	require.NoError(t, err)
	recordBudgetClaim(t, store, "claim-1", "root-1", supervision.WakeBudgetClassFailure, supervision.WakeReasonExecutionFailed)

	prompt, err := handler.InjectSupervisionPreflight(ctx, "root-1", "continue the parent turn", nil)
	require.NoError(t, err)
	require.Contains(t, prompt, "wake_budget:")
	require.Contains(t, prompt, "failure=1/5")
	require.Contains(t, prompt, "approval=0/unlimited")
	require.Contains(t, prompt, "continue the parent turn")
	require.True(t, strings.HasPrefix(prompt, "[Child lifecycle preflight]"), "the ledger rides with the digest, ahead of the user prompt")

	// The notification stays in the digest after being marked delivered/seen
	// (seen is not acknowledged), so an unwired host still renders the digest
	// without inventing a budget line.
	bare := NewHandler(skill.NewRegistry(nil), nil, nil)
	bare.SetSupervisionStore(store)
	barePrompt, err := bare.InjectSupervisionPreflight(ctx, "root-1", "continue the parent turn", nil)
	require.NoError(t, err)
	require.NotContains(t, barePrompt, "wake_budget:")
	require.Contains(t, barePrompt, "[Child lifecycle preflight]")
}
