package skills

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

type executionDiagnosticsBackgroundStub struct {
	jobs []background.Job
	err  error
}

func (s executionDiagnosticsBackgroundStub) ListJobs(context.Context, background.JobFilter) ([]background.Job, error) {
	return append([]background.Job(nil), s.jobs...), s.err
}

func TestCollectBackgroundExecutionDiagnosticsCountsKnownAndUnknownStatuses(t *testing.T) {
	result := collectBackgroundExecutionDiagnostics(
		context.Background(),
		executionDiagnosticsBackgroundStub{jobs: []background.Job{
			{Status: background.StatusPending},
			{Status: background.StatusRunning},
			{Status: background.StatusCompleted},
			{Status: background.StatusFailed},
			{Status: background.StatusTimedOut},
			{Status: background.StatusCancelled},
			{Status: background.StatusOrphaned},
			{Status: background.JobStatus("future_background_status")},
		}},
		executionDiagnosticsResult{counts: newExecutionDiagnosticsCounts(
			"pending", "running", "completed", "failed", "timed_out", "cancelled", "orphaned",
		)},
	)

	require.Equal(t, executionDiagnosticsSourceOK, result.source["status"])
	require.Equal(t, 8, result.counts["total"])
	require.Equal(t, 1, result.counts["pending"])
	require.Equal(t, 1, result.counts["running"])
	require.Equal(t, 1, result.counts["orphaned"])
	require.Equal(t, 1, result.counts["unknown"])
}

func TestExecutionDiagnosticsSnapshotReadsReopenedSQLiteStores(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	sessionStorage := chat.NewInMemoryStorage()
	for _, sessionID := range []string{"session-waiting", "session-future"} {
		session := chat.NewSession("diagnostics-user")
		session.ID = sessionID
		require.NoError(t, sessionStorage.Save(ctx, session))
	}
	sessionManager := chat.NewSessionManager(sessionStorage, chat.DefaultSessionManagerConfig())
	t.Cleanup(sessionManager.Stop)

	runtimePath := filepath.Join(root, "runtime.sqlite")
	runtimeSeed, err := chat.NewSQLiteRuntimeStore(&chat.RuntimeStoreConfig{Path: runtimePath})
	require.NoError(t, err)
	require.NoError(t, runtimeSeed.SaveState(ctx, &chat.RuntimeState{
		SessionID: "session-waiting",
		Status:    chat.SessionWaitingApproval,
		PendingApproval: &chat.ApprovalRequest{
			ID:        "approval-1",
			SessionID: "session-waiting",
			ToolName:  "shell",
			ArgsJSON:  json.RawMessage(`{"command":"approval-secret-command"}`),
		},
	}))
	require.NoError(t, runtimeSeed.SaveState(ctx, &chat.RuntimeState{
		SessionID: "session-future",
		Status:    chat.SessionStatus("future_session_status"),
	}))
	require.NoError(t, runtimeSeed.Close())
	runtimeStore, err := chat.NewSQLiteRuntimeStore(&chat.RuntimeStoreConfig{Path: runtimePath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	teamPath := filepath.Join(root, "team.sqlite")
	teamSeed, err := team.NewSQLiteStore(&team.StoreConfig{Path: teamPath})
	require.NoError(t, err)
	for _, item := range []team.Team{
		{ID: "team-active", Status: team.TeamStatusActive, Strategy: "team-secret-strategy"},
		{ID: "team-partial", Status: team.TeamStatusPartiallyCompleted},
		{ID: "team-future", Status: team.TeamStatus("future_team_status")},
	} {
		_, err = teamSeed.CreateTeam(ctx, item)
		require.NoError(t, err)
	}
	for _, task := range []team.Task{
		{ID: "task-running", TeamID: "team-active", Title: "team-secret-title", Status: team.TaskStatusRunning},
		{ID: "task-blocked", TeamID: "team-active", Status: team.TaskStatusBlocked},
		{ID: "task-failed", TeamID: "team-active", Status: team.TaskStatusFailed},
		{ID: "task-future", TeamID: "team-active", Status: team.TaskStatus("future_task_status")},
	} {
		_, err = teamSeed.CreateTask(ctx, task)
		require.NoError(t, err)
	}
	require.NoError(t, teamSeed.Close())
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: teamPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = teamStore.Close() })

	backgroundPath := filepath.Join(root, "background.sqlite")
	backgroundSeed, err := background.NewSQLiteStore(&background.StoreConfig{Path: backgroundPath})
	require.NoError(t, err)
	for _, job := range []background.Job{
		{ID: "job-completed", Status: background.StatusCompleted, Command: "background-secret-command", CreatedAt: time.Now().UTC()},
		{ID: "job-failed", Status: background.StatusFailed, CreatedAt: time.Now().UTC()},
		{ID: "job-future", Status: background.JobStatus("future_background_status"), CreatedAt: time.Now().UTC()},
	} {
		require.NoError(t, backgroundSeed.SaveJob(ctx, job))
	}
	require.NoError(t, backgroundSeed.Close())
	backgroundManager := background.NewManager(background.Config{
		StorePath:       backgroundPath,
		CleanupInterval: time.Hour,
	})
	t.Cleanup(func() { _ = backgroundManager.Close() })

	agentPath := filepath.Join(root, "agents.sqlite")
	agentSeed, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{Path: agentPath})
	require.NoError(t, err)
	closedAt := time.Now().UTC()
	for _, record := range []agentcontrol.AgentRecord{
		{AgentID: "agent-active", RootSessionID: "session-waiting", SessionID: "session-waiting", AgentPath: "/root", Status: agentcontrol.AgentStatusActive, Nickname: "agent-secret-nickname"},
		{AgentID: "agent-closed", RootSessionID: "session-waiting", SessionID: "session-closed", AgentPath: "/root/closed", Status: agentcontrol.AgentStatusClosed, ClosedAt: &closedAt},
		{AgentID: "agent-future", RootSessionID: "session-waiting", SessionID: "session-future", AgentPath: "/root/future", Status: "future_agent_status"},
	} {
		_, err = agentSeed.UpsertAgentControlAgent(ctx, record)
		require.NoError(t, err)
	}
	require.NoError(t, agentSeed.Close())
	agentStore, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{Path: agentPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = agentStore.Close() })

	handler := NewHandler(nil, nil, nil)
	handler.SetSessionManager(sessionManager)
	handler.sessionRuntimeStore = runtimeStore
	handler.teamStore = teamStore
	handler.backgroundManager = backgroundManager
	handler.agentControlAgentStore = agentStore

	runtimeSnapshot := handler.runtimeStatusSnapshot(ctx, llm.HealthCheckModeNone)
	core, ok := runtimeSnapshot["execution_core"].(chat.RuntimeCoreDescriptor)
	require.True(t, ok)
	require.True(t, chat.IsSessionActorRuntimeCore(core))
	diagnostics, ok := runtimeSnapshot["execution_diagnostics"].(map[string]interface{})
	require.True(t, ok)
	counts := diagnostics["counts"].(map[string]interface{})
	require.Equal(t, 2, counts["sessions"].(map[string]int)["total"])
	require.Equal(t, 1, counts["sessions"].(map[string]int)["waiting_approval"])
	require.Equal(t, 1, counts["sessions"].(map[string]int)["unknown"])
	require.Equal(t, 1, counts["approvals"].(map[string]int)["waiting"])
	require.Equal(t, 3, counts["background"].(map[string]int)["total"])
	require.Equal(t, 1, counts["background"].(map[string]int)["unknown"])
	require.Equal(t, 3, counts["teams"].(map[string]int)["total"])
	require.Equal(t, 1, counts["teams"].(map[string]int)["partially_completed"])
	require.Equal(t, 4, counts["team_tasks"].(map[string]int)["total"])
	require.Equal(t, 1, counts["team_tasks"].(map[string]int)["unknown"])
	orchestrators := counts["team_orchestrators"].(map[string]int)
	require.Equal(t, 1, orchestrators["active_teams"])
	require.Equal(t, 0, orchestrators["live_loops"])
	require.Equal(t, 1, orchestrators["loop_gap"])
	require.Equal(t, 0, orchestrators["extra_loops"])
	require.Equal(t, 0, orchestrators["restart_total"])
	require.Equal(t, 0, orchestrators["restart_pending"])
	require.Equal(t, 0, orchestrators["degraded_loops"])
	require.Equal(t, 3, counts["agents"].(map[string]int)["total"])
	require.Equal(t, 1, counts["agents"].(map[string]int)["unknown"])
	require.True(t, diagnostics["capabilities"].(map[string]bool)["team_loop_consistency"])

	sources := diagnostics["sources"].(map[string]interface{})
	sessionSource := sources["sessions"].(map[string]interface{})
	require.Equal(t, "session_storage", sessionSource["discovery_authority"])
	require.Equal(t, false, sessionSource["orphan_runtime_states_included"])
	require.Equal(t, "runtime_state", sessionSource["pending_approval_authority"])

	payload, err := json.Marshal(diagnostics)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "approval-secret-command")
	require.NotContains(t, string(payload), "background-secret-command")
	require.NotContains(t, string(payload), "team-secret")
	require.NotContains(t, string(payload), "agent-secret")
}

type blockingExecutionDiagnosticsRuntimeStore struct {
	started chan struct{}
	release chan struct{}
}

func (s *blockingExecutionDiagnosticsRuntimeStore) LoadState(ctx context.Context, sessionID string) (*chat.RuntimeState, error) {
	close(s.started)
	select {
	case <-s.release:
		return &chat.RuntimeState{SessionID: sessionID, Status: chat.SessionRunning}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (*blockingExecutionDiagnosticsRuntimeStore) SaveState(context.Context, *chat.RuntimeState) error {
	return nil
}

func (*blockingExecutionDiagnosticsRuntimeStore) DeleteState(context.Context, string) error {
	return nil
}

func TestSessionExecutionDiagnosticsDoesNotHoldRuntimeLockDuringQuery(t *testing.T) {
	storage := chat.NewInMemoryStorage()
	session := chat.NewSession("lock-test")
	require.NoError(t, storage.Save(context.Background(), session))
	manager := chat.NewSessionManager(storage, chat.DefaultSessionManagerConfig())
	t.Cleanup(manager.Stop)

	store := &blockingExecutionDiagnosticsRuntimeStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	handler := NewHandler(nil, nil, nil)
	handler.SetSessionManager(manager)
	handler.sessionRuntimeStore = store
	done := make(chan struct{})
	go func() {
		_ = handler.sessionExecutionDiagnostics(context.Background())
		close(done)
	}()

	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("runtime state query did not start")
	}
	assertDiagnosticsMutexAvailable(t, &handler.sessionRuntimeMu)
	close(store.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("session diagnostics did not finish")
	}
}

type blockingExecutionDiagnosticsAgentStore struct {
	started chan struct{}
	release chan struct{}
}

func (s *blockingExecutionDiagnosticsAgentStore) ListAgentControlAgents(ctx context.Context, _ agentcontrol.AgentFilter) ([]agentcontrol.AgentRecord, error) {
	close(s.started)
	select {
	case <-s.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (*blockingExecutionDiagnosticsAgentStore) UpsertAgentControlAgent(_ context.Context, record agentcontrol.AgentRecord) (agentcontrol.AgentRecord, error) {
	return record, nil
}

func (*blockingExecutionDiagnosticsAgentStore) CloseAgentControlAgentSubtree(context.Context, string, string, time.Time) (int64, error) {
	return 0, nil
}

func (*blockingExecutionDiagnosticsAgentStore) Close() error { return nil }

func TestAgentExecutionDiagnosticsDoesNotHoldRegistryLockDuringQuery(t *testing.T) {
	store := &blockingExecutionDiagnosticsAgentStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	handler := NewHandler(nil, nil, nil)
	handler.agentControlAgentStore = store
	done := make(chan struct{})
	go func() {
		_ = handler.agentExecutionDiagnostics(context.Background())
		close(done)
	}()

	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("agent registry query did not start")
	}
	assertDiagnosticsMutexAvailable(t, &handler.agentControlMu)
	close(store.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("agent diagnostics did not finish")
	}
}

func assertDiagnosticsMutexAvailable(t *testing.T, mutex *sync.RWMutex) {
	t.Helper()
	acquired := make(chan struct{})
	go func() {
		mutex.Lock()
		mutex.Unlock()
		close(acquired)
	}()
	select {
	case <-acquired:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("diagnostics query held the handler configuration lock")
	}
}

type timeoutExecutionDiagnosticsBackgroundStub struct{}

func (timeoutExecutionDiagnosticsBackgroundStub) ListJobs(ctx context.Context, _ background.JobFilter) ([]background.Job, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCollectBackgroundExecutionDiagnosticsReportsTimeoutWithoutDetails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result := collectBackgroundExecutionDiagnostics(
		ctx,
		timeoutExecutionDiagnosticsBackgroundStub{},
		executionDiagnosticsResult{counts: newExecutionDiagnosticsCounts("pending")},
	)
	require.Equal(t, executionDiagnosticsSourceDegraded, result.source["status"])
	require.Equal(t, "timeout", result.source["error"])
	require.Equal(t, 0, result.counts["total"])
}

func TestCollectSessionExecutionDiagnosticsUsesPendingApprovalAsAuthority(t *testing.T) {
	ctx := context.Background()
	storage := chat.NewInMemoryStorage()
	session := chat.NewSession("approval-authority")
	require.NoError(t, storage.Save(ctx, session))
	stateStore := chat.NewInMemoryRuntimeStore(16)
	require.NoError(t, stateStore.SaveState(ctx, &chat.RuntimeState{
		SessionID: session.ID,
		Status:    chat.SessionRunning,
		PendingApproval: &chat.ApprovalRequest{
			ID:        "approval-persisted",
			SessionID: session.ID,
			ToolName:  "shell",
		},
	}))

	result := collectSessionExecutionDiagnostics(
		ctx,
		storage,
		stateStore,
		executionDiagnosticsSessionResult{
			sessionCounts:  newExecutionDiagnosticsCounts("running"),
			approvalCounts: newExecutionDiagnosticsCounts("waiting"),
		},
	)
	require.Equal(t, 1, result.sessionCounts["running"])
	require.Equal(t, 1, result.approvalCounts["waiting"])
	require.Equal(t, 1, result.approvalCounts["total"])
}
