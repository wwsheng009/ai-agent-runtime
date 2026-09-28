package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/runtimeserver"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// newLocalProgressTestHost builds a host backed by a real durable supervision
// plane so the progress recorder exercises the actual SQLite write path.
func newLocalProgressTestHost(t *testing.T) (*localChatRuntimeHost, supervision.ExecutionRunStore) {
	t.Helper()
	plane, err := runtimeserver.BuildSupervisionControlPlane(t.TempDir(), supervision.Config{}, runtimeserver.SupervisionRuntimeHooks{
		Authorize: func(context.Context, string, string, string, string, string) error { return nil },
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = plane.Close() })
	store, ok := plane.Store.(supervision.ExecutionRunStore)
	require.True(t, ok, "supervision plane store must expose the execution run store")
	return &localChatRuntimeHost{Supervision: plane}, store
}

func seedLocalProgressRun(t *testing.T, store supervision.ExecutionRunStore, sessionID string) string {
	t.Helper()
	now := time.Now().UTC()
	deadline := now.Add(30 * time.Minute)
	progressDeadline := now.Add(5 * time.Minute)
	run := supervision.ExecutionRun{
		RunID:               "run_progress_" + sessionID,
		Kind:                supervision.RunKindAgentRun,
		Workflow:            supervision.RunWorkflowSpawnAgent,
		RootSessionID:       "root-session",
		ParentSessionID:     "parent-session",
		SessionID:           sessionID,
		AgentID:             sessionID,
		Attempt:             1,
		Status:              supervision.RunStatusRunning,
		OwnerID:             "host-1",
		StartedAt:           now,
		LastHeartbeatAt:     now,
		ExecutionDeadlineAt: &deadline,
		ProgressDeadlineAt:  &progressDeadline,
		MaxAttempts:         1,
		FencingToken:        1,
		Version:             1,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	created, err := store.CreateExecutionRun(context.Background(), run)
	require.NoError(t, err)
	require.True(t, created)
	return run.RunID
}

// TestLocalHostProgressRecorderWritesExecutionRunProgress pins P0-1 end to
// end on the CLI host: loop ticks must land as last_progress_at /
// progress_seq on the session's active execution run.
func TestLocalHostProgressRecorderWritesExecutionRunProgress(t *testing.T) {
	host, store := newLocalProgressTestHost(t)
	runID := seedLocalProgressRun(t, store, "child-progress")

	recorder := host.progressRecorderForSession("child-progress")
	require.NotNil(t, recorder, "有监督面且有 run 的会话必须拿到写回回调")
	recorder("llm_response")
	recorder("tool_call_end")

	got, err := store.GetExecutionRun(context.Background(), runID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.False(t, got.LastProgressAt.IsZero(), "last_progress_at 必须被真实推进")
	require.EqualValues(t, 2, got.ProgressSeq, "每次 tick 递增一次 progress_seq")
}

// TestLocalHostProgressRecorderNoRunForSession: sessions without a registered
// execution run (non-spawn chats) must resolve to a no-op instead of failing or
// creating rows.
func TestLocalHostProgressRecorderNoRunForSession(t *testing.T) {
	host, store := newLocalProgressTestHost(t)

	recorder := host.progressRecorderForSession("session-without-run")
	require.NotNil(t, recorder)
	recorder("llm_response")

	runs, err := store.ListExecutionRunsBySession(context.Background(), "session-without-run", 8)
	require.NoError(t, err)
	require.Empty(t, runs, "无 run 会话不得创建任何执行账本行")
}

// TestLocalHostProgressRecorderDisabledWithoutSupervision: hosts without the
// durable plane keep the historical behavior (nil hook = loop no-op).
func TestLocalHostProgressRecorderDisabledWithoutSupervision(t *testing.T) {
	require.Nil(t, (&localChatRuntimeHost{}).progressRecorderForSession("child"))

	host, _ := newLocalProgressTestHost(t)
	require.Nil(t, host.progressRecorderForSession("   "), "空会话 ID 不得返回回调")
}
