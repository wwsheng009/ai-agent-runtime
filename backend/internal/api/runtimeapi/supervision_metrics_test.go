package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// C0-E（§7.3 度量基线）HTTP 读数的验收用例：线上采样入口与 supervision 包的读数
// 用例调用同一批函数，这里只锁「路由 → 参数 → 载荷」这一段接线。

func seedAPISupervisionMetricsRun(t *testing.T, store *supervision.SQLiteSupervisionStore, runID, rootSession, childSession string, createdAt time.Time, terminal, cancelSource string, finishedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	deadline := createdAt.Add(30 * time.Minute)
	progressDeadline := createdAt.Add(5 * time.Minute)
	created, err := store.CreateExecutionRun(ctx, supervision.ExecutionRun{
		RunID:               runID,
		Kind:                supervision.RunKindAgentRun,
		Workflow:            supervision.RunWorkflowSpawnAgent,
		RootSessionID:       rootSession,
		ParentSessionID:     rootSession,
		SessionID:           childSession,
		AgentID:             childSession,
		Attempt:             1,
		Status:              supervision.RunStatusRunning,
		OwnerID:             "host-1",
		StartedAt:           createdAt,
		LastHeartbeatAt:     createdAt,
		LastProgressAt:      createdAt,
		ProgressSeq:         1,
		ExecutionDeadlineAt: &deadline,
		ProgressDeadlineAt:  &progressDeadline,
		MaxAttempts:         1,
		FencingToken:        1,
		Version:             1,
		CreatedAt:           createdAt,
		UpdatedAt:           createdAt,
	})
	require.NoError(t, err)
	require.True(t, created)
	if terminal == "" {
		return
	}
	if cancelSource != "" {
		ok, err := store.RequestExecutionCancel(ctx, runID, cancelSource, time.Minute, finishedAt.Add(-time.Minute))
		require.NoError(t, err)
		require.True(t, ok)
	}
	ok, err := store.MarkExecutionRunTerminal(ctx, runID, terminal, cancelSource, "", finishedAt)
	require.NoError(t, err)
	require.True(t, ok)
}

func seedAPISupervisionMetricsOutbox(t *testing.T, store *supervision.SQLiteSupervisionStore, outboxID, runID, sessionID string, createdAt, deliveredAt time.Time) {
	t.Helper()
	ctx := context.Background()
	ok, err := store.EnqueueCompletionOutbox(ctx, supervision.CompletionOutboxEntry{
		OutboxID:        outboxID,
		RunID:           runID,
		SessionID:       sessionID,
		ParentSessionID: "root-a",
		RootSessionID:   "root-a",
		Status:          supervision.RunStatusCompleted,
		IdempotencyKey:  "subagent_completion:" + runID + ":" + outboxID,
		PayloadJSON:     "{}",
		CreatedAt:       createdAt,
	})
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = store.MarkOutboxDelivered(ctx, outboxID, 1, deliveredAt)
	require.NoError(t, err)
	require.True(t, ok)
}

// TestGetSupervisionMetrics_ReadsSnapshotFromStore 走真实 store：一条兜底取消 +
// 一条成功完成（含已投递出件），载荷必须给出 §7.3 指标 1–3 与指标 5 的复核清单。
func TestGetSupervisionMetrics_ReadsSnapshotFromStore(t *testing.T) {
	handler, store := newAPISupervisionToolTestHandler(t, "api-metrics-readout")
	now := time.Now().UTC()

	seedAPISupervisionMetricsRun(t, store, "run-forced", "root-a", "child-a", now.Add(-4*time.Hour), supervision.RunStatusTimedOut, supervision.CancelSourceDecisionWindowExpired, now.Add(-3*time.Hour))
	seedAPISupervisionMetricsRun(t, store, "run-ok", "root-a", "child-b", now.Add(-4*time.Hour), supervision.RunStatusCompleted, "", now.Add(-2*time.Hour))
	seedAPISupervisionMetricsRun(t, store, "run-other-root", "root-b", "child-c", now.Add(-4*time.Hour), supervision.RunStatusTimedOut, supervision.CancelSourceProgressStalled, now.Add(-3*time.Hour))
	seedAPISupervisionMetricsOutbox(t, store, "ob-ok", "run-ok", "child-b", now.Add(-2*time.Hour), now.Add(-2*time.Hour).Add(30*time.Second))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/supervision/metrics?since=24h&root_session_id=root-a", nil)
	handler.GetSupervisionMetrics(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		GeneratedAt string `json:"generated_at"`
		ReadPath    string `json:"read_path"`
		Cancel      struct {
			Total                 int            `json:"total"`
			ForcedCancel          int            `json:"forced_cancel"`
			DecisionWindowExpired int            `json:"decision_window_expired"`
			BySource              map[string]int `json:"by_source"`
		} `json:"cancel"`
		ReportLatency struct {
			Samples   int   `json:"samples"`
			P95Millis int64 `json:"p95_ms"`
		} `json:"report_latency"`
		MisKillCandidates []struct {
			RunID        string `json:"run_id"`
			CancelSource string `json:"cancel_source"`
			SessionID    string `json:"session_id"`
		} `json:"mis_kill_candidates"`
		Notes []string `json:"notes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))

	require.Equal(t, "window", payload.ReadPath)
	require.NotEmpty(t, payload.GeneratedAt)
	require.Equal(t, 2, payload.Cancel.Total, "root_session_id 限定在一个 root scope 内")
	require.Equal(t, 1, payload.Cancel.ForcedCancel)
	require.Equal(t, 1, payload.Cancel.DecisionWindowExpired)
	require.Equal(t, map[string]int{supervision.CancelSourceDecisionWindowExpired: 1}, payload.Cancel.BySource)
	require.Equal(t, 1, payload.ReportLatency.Samples)
	require.Equal(t, int64(30000), payload.ReportLatency.P95Millis)
	require.Len(t, payload.MisKillCandidates, 1)
	require.Equal(t, "run-forced", payload.MisKillCandidates[0].RunID)
	require.Equal(t, supervision.CancelSourceDecisionWindowExpired, payload.MisKillCandidates[0].CancelSource)
	require.Equal(t, "child-a", payload.MisKillCandidates[0].SessionID)
	require.NotEmpty(t, payload.Notes, "指标 4/5 的读数边界必须随载荷给出")
}

// TestGetSupervisionMetrics_RejectsBadParams 采样参数错误必须报 400 而不是静默取
// 默认值——否则「最近一周」的采样可能悄悄变成全库。
func TestGetSupervisionMetrics_RejectsBadParams(t *testing.T) {
	handler, _ := newAPISupervisionToolTestHandler(t, "api-metrics-badparams")
	cases := []string{
		"/supervision/metrics?since=banana",
		"/supervision/metrics?until=2026-13-01T00:00:00Z",
		"/supervision/metrics?window_limit=0",
		"/supervision/metrics?window_limit=abc",
		"/supervision/metrics?max_candidates=-1",
	}
	for _, target := range cases {
		rec := httptest.NewRecorder()
		handler.GetSupervisionMetrics(rec, httptest.NewRequest(http.MethodGet, target, nil))
		require.Equal(t, http.StatusBadRequest, rec.Code, target)
	}
}

// TestGetSupervisionMetrics_UnavailableWithoutStore 未接线的宿主必须报 503，而
// 不是回一份全 0 的载荷（假 0 会被当成「没有兜底取消」）。
func TestGetSupervisionMetrics_UnavailableWithoutStore(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	rec := httptest.NewRecorder()
	handler.GetSupervisionMetrics(rec, httptest.NewRequest(http.MethodGet, "/supervision/metrics", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
