package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// C0-E 采集入口的接线用例：同一份 store 上，命令打印的快照必须与
// /supervision/metrics 的载荷同形同数（两者共用 CollectMetricsSnapshot）。

func seedSamplerRun(t *testing.T, store *supervision.SQLiteSupervisionStore, runID, rootSession, childSession string, createdAt time.Time, terminal, cancelSource string, finishedAt time.Time) {
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

func TestRun_PrintsSnapshotForStore(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "supervision.db")
	store, err := supervision.NewSQLiteSupervisionStore(&supervision.StoreConfig{Path: storePath})
	require.NoError(t, err)
	now := time.Now().UTC()
	seedSamplerRun(t, store, "run-forced", "root-a", "child-a", now.Add(-4*time.Hour), supervision.RunStatusTimedOut, supervision.CancelSourceDecisionWindowExpired, now.Add(-3*time.Hour))
	seedSamplerRun(t, store, "run-live", "root-b", "child-b", now.Add(-2*time.Hour), "", "", time.Time{})
	require.NoError(t, store.Close())

	var stdout bytes.Buffer
	require.NoError(t, run([]string{"-store", storePath, "-since", "24h"}, &stdout))

	var snapshot struct {
		ReadPath string `json:"read_path"`
		Cancel   struct {
			Total        int `json:"total"`
			ForcedCancel int `json:"forced_cancel"`
		} `json:"cancel"`
		MisKillCandidates []struct {
			RunID string `json:"run_id"`
		} `json:"mis_kill_candidates"`
		Notes []string `json:"notes"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &snapshot))
	require.Equal(t, "window", snapshot.ReadPath)
	require.Equal(t, 2, snapshot.Cancel.Total)
	require.Equal(t, 1, snapshot.Cancel.ForcedCancel)
	require.Len(t, snapshot.MisKillCandidates, 1)
	require.Equal(t, "run-forced", snapshot.MisKillCandidates[0].RunID)
	require.NotEmpty(t, snapshot.Notes)

	// -root 缩小到一个 scope：只有该 root 的 run 参与分母。
	var scoped bytes.Buffer
	require.NoError(t, run([]string{"-store", storePath, "-root", "root-b", "-since", "24h"}, &scoped))
	var scopedSnapshot struct {
		RootSessionID string `json:"root_session_id"`
		Cancel        struct {
			Total int `json:"total"`
		} `json:"cancel"`
	}
	require.NoError(t, json.Unmarshal(scoped.Bytes(), &scopedSnapshot))
	require.Equal(t, "root-b", scopedSnapshot.RootSessionID)
	require.Equal(t, 1, scopedSnapshot.Cancel.Total)
}

func TestRun_RejectsBadInput(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "supervision.db")

	var stdout bytes.Buffer
	require.Error(t, run(nil, &stdout), "-store 必填")
	require.Error(t, run([]string{"-store", storePath, "-since", "banana"}, &stdout))
	require.Error(t, run([]string{"-store", storePath, "-since", "7d", "extra"}, &stdout))
}
