package team

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dsn := fmt.Sprintf("file:team_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	store, err := NewSQLiteStore(&StoreConfig{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

type notifyingClaimStore struct {
	Store
	claimed chan struct{}
	once    sync.Once
}

func newNotifyingClaimStore(store Store) *notifyingClaimStore {
	return &notifyingClaimStore{
		Store:   store,
		claimed: make(chan struct{}),
	}
}

func (s *notifyingClaimStore) ClaimTask(ctx context.Context, id string, assignee string, leaseUntil time.Time, expectedVersion int64) (bool, error) {
	claimed, err := s.Store.ClaimTask(ctx, id, assignee, leaseUntil, expectedVersion)
	if claimed {
		s.once.Do(func() {
			close(s.claimed)
		})
	}
	return claimed, err
}

func (s *notifyingClaimStore) WatchMail(ctx context.Context, teamID string) (<-chan MailMessage, func()) {
	if watcher, ok := s.Store.(MailWatcherStore); ok {
		return watcher.WatchMail(ctx, teamID)
	}
	return make(chan MailMessage), func() {}
}

func (s *notifyingClaimStore) LastMailSeq(ctx context.Context, teamID string) (int64, error) {
	if sequencer, ok := s.Store.(MailSequenceStore); ok {
		return sequencer.LastMailSeq(ctx, teamID)
	}
	return 0, nil
}

func (s *notifyingClaimStore) WatchTaskSignals(ctx context.Context, teamID string) (<-chan TaskSignal, func()) {
	if watcher, ok := s.Store.(TaskWatcherStore); ok {
		return watcher.WatchTaskSignals(ctx, teamID)
	}
	return make(chan TaskSignal), func() {}
}

func (s *notifyingClaimStore) LastTaskSignalSeq(ctx context.Context, teamID string) (int64, error) {
	if sequencer, ok := s.Store.(TaskSequenceStore); ok {
		return sequencer.LastTaskSignalSeq(ctx, teamID)
	}
	return 0, nil
}

type testAgentControlTaskWakeSource struct {
	events chan agentcontrol.TaskWakeEvent
	mu     sync.Mutex
	seq    int64
}

func newTestAgentControlTaskWakeSource() *testAgentControlTaskWakeSource {
	return &testAgentControlTaskWakeSource{
		events: make(chan agentcontrol.TaskWakeEvent, 1),
	}
}

func (s *testAgentControlTaskWakeSource) WatchAgentControlTaskWake(ctx context.Context, filter agentcontrol.TaskWakeFilter) (<-chan agentcontrol.TaskWakeEvent, func()) {
	return s.events, func() {}
}

func (s *testAgentControlTaskWakeSource) LastAgentControlTaskWakeSeq(ctx context.Context, filter agentcontrol.TaskWakeFilter) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq, nil
}

func (s *testAgentControlTaskWakeSource) Wake(teamID, taskID string) {
	s.mu.Lock()
	s.seq++
	seq := s.seq
	s.mu.Unlock()
	s.events <- agentcontrol.TaskWakeEvent{
		Seq:       seq,
		Workflow:  agentcontrol.WorkflowSpawnTeam,
		TeamID:    teamID,
		TaskID:    taskID,
		Kind:      "test.wake",
		Status:    string(TaskStatusReady),
		CreatedAt: time.Now().UTC(),
	}
}

func TestOrchestratorClaimReadyTasksHonorsMaxWriters(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{MaxWriters: 1})
	require.NoError(t, err)

	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-a", TeamID: teamID, State: TeammateStateIdle})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-b", TeamID: teamID, State: TeammateStateIdle})
	require.NoError(t, err)

	_, err = store.CreateTask(ctx, Task{
		TeamID:     teamID,
		Title:      "task-1",
		Status:     TaskStatusReady,
		WritePaths: []string{"a.txt"},
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		TeamID:     teamID,
		Title:      "task-2",
		Status:     TaskStatusReady,
		WritePaths: []string{"b.txt"},
	})
	require.NoError(t, err)

	orchestrator := NewOrchestrator(store, nil, nil)
	assignments, err := orchestrator.ClaimReadyTasks(ctx, teamID, 0)
	require.NoError(t, err)
	require.Len(t, assignments, 1)

	running, err := store.ListTasks(ctx, TaskFilter{
		TeamID: teamID,
		Status: []TaskStatus{TaskStatusRunning},
	})
	require.NoError(t, err)
	require.Len(t, running, 1)
}

func TestOrchestratorClaimReadyTasksRespectsPinnedAssignee(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-a", TeamID: teamID, State: TeammateStateIdle})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-b", TeamID: teamID, State: TeammateStateIdle})
	require.NoError(t, err)

	assignee := "mate-b"
	_, err = store.CreateTask(ctx, Task{
		TeamID:   teamID,
		Title:    "pinned",
		Status:   TaskStatusReady,
		Assignee: &assignee,
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "unassigned",
		Status: TaskStatusReady,
	})
	require.NoError(t, err)

	orchestrator := NewOrchestrator(store, nil, nil)
	assignments, err := orchestrator.ClaimReadyTasks(ctx, teamID, 1)
	require.NoError(t, err)
	require.Len(t, assignments, 1)
	require.Equal(t, assignee, assignments[0].Teammate.ID)
}

func TestOrchestratorClaimReadyTasksWithClaimsCreatesPathClaim(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-a", TeamID: teamID, State: TeammateStateIdle})
	require.NoError(t, err)

	taskID, err := store.CreateTask(ctx, Task{
		TeamID:     teamID,
		Title:      "claimed-with-paths",
		Status:     TaskStatusReady,
		WritePaths: []string{"src/file.txt"},
	})
	require.NoError(t, err)

	claims := NewPathClaimManager(store, "workspace")
	orchestrator := NewOrchestrator(store, claims, nil)
	assignments, err := orchestrator.ClaimReadyTasks(ctx, teamID, 0)
	require.NoError(t, err)
	require.Len(t, assignments, 1)
	require.Equal(t, taskID, assignments[0].Task.ID)
	require.Equal(t, "mate-a", assignments[0].Teammate.ID)

	pathClaims, err := store.ListPathClaims(ctx, teamID)
	require.NoError(t, err)
	require.Len(t, pathClaims, 1)
	assert.Equal(t, "workspace/src/file.txt", pathClaims[0].Path)
	assert.Equal(t, PathClaimWrite, pathClaims[0].Mode)

	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	require.Equal(t, TaskStatusRunning, task.Status)
	require.NotNil(t, task.Assignee)
	assert.Equal(t, "mate-a", *task.Assignee)
}

func TestOrchestratorRunStopsWhenTeamNotActive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{
		Status: TeamStatusDone,
	})
	require.NoError(t, err)

	orchestrator := NewOrchestrator(store, nil, nil)
	orchestrator.TickInterval = 10 * time.Millisecond
	require.NoError(t, orchestrator.Run(ctx, teamID))
}

func TestOrchestratorRunTicksImmediately(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-a", TeamID: teamID, State: TeammateStateIdle})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "ready task",
		Status: TaskStatusReady,
	})
	require.NoError(t, err)

	claimStore := newNotifyingClaimStore(store)
	orchestrator := NewOrchestrator(claimStore, nil, nil)
	orchestrator.TickInterval = time.Hour
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- orchestrator.Run(runCtx, teamID)
	}()

	select {
	case <-claimStore.claimed:
	case err := <-errCh:
		t.Fatalf("orchestrator exited before claiming a ready task: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("orchestrator did not claim a ready task immediately")
	}

	cancel()
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("orchestrator did not stop after context cancellation")
	}
}

func TestOrchestratorRunWithWakeProcessesReadyTaskBeforeFallbackTick(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-a", TeamID: teamID, State: TeammateStateIdle})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-b", TeamID: teamID, State: TeammateStateBusy})
	require.NoError(t, err)
	assignee := "mate-b"
	_, err = store.CreateTask(ctx, Task{
		TeamID:   teamID,
		Title:    "already running task",
		Status:   TaskStatusRunning,
		Assignee: &assignee,
	})
	require.NoError(t, err)

	claimStore := newNotifyingClaimStore(store)
	orchestrator := NewOrchestrator(claimStore, nil, nil)
	orchestrator.TickInterval = time.Hour
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	wake := make(chan struct{}, 1)

	errCh := make(chan error, 1)
	go func() {
		errCh <- orchestrator.RunWithWake(runCtx, teamID, wake)
	}()

	time.Sleep(25 * time.Millisecond)
	_, err = store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "wake task",
		Status: TaskStatusReady,
	})
	require.NoError(t, err)
	wake <- struct{}{}

	select {
	case <-claimStore.claimed:
	case err := <-errCh:
		t.Fatalf("orchestrator exited before wake task was claimed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("orchestrator did not claim wake task before fallback tick")
	}

	cancel()
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("orchestrator did not stop after context cancellation")
	}
}

func TestOrchestratorRunWakesFromMailboxSequenceBeforeFallbackTick(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-a", TeamID: teamID, State: TeammateStateIdle})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-b", TeamID: teamID, State: TeammateStateBusy})
	require.NoError(t, err)
	assignee := "mate-b"
	_, err = store.CreateTask(ctx, Task{
		TeamID:   teamID,
		Title:    "already running task",
		Status:   TaskStatusRunning,
		Assignee: &assignee,
	})
	require.NoError(t, err)

	claimStore := newNotifyingClaimStore(store)
	orchestrator := NewOrchestrator(claimStore, nil, nil)
	orchestrator.TickInterval = time.Hour
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- orchestrator.Run(runCtx, teamID)
	}()

	time.Sleep(25 * time.Millisecond)
	_, err = store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "mailbox wake task",
		Status: TaskStatusReady,
	})
	require.NoError(t, err)
	_, err = store.InsertMail(ctx, MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "orchestrator",
		Kind:      "wake",
		Body:      "ready task added",
	})
	require.NoError(t, err)

	select {
	case <-claimStore.claimed:
	case err := <-errCh:
		t.Fatalf("orchestrator exited before mailbox wake task was claimed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("orchestrator did not claim mailbox wake task before fallback tick")
	}

	cancel()
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("orchestrator did not stop after context cancellation")
	}
}

func TestOrchestratorRunWakesFromTaskSignalBeforeFallbackTick(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-a", TeamID: teamID, State: TeammateStateIdle})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-b", TeamID: teamID, State: TeammateStateBusy})
	require.NoError(t, err)
	assignee := "mate-b"
	_, err = store.CreateTask(ctx, Task{
		TeamID:   teamID,
		Title:    "already running task",
		Status:   TaskStatusRunning,
		Assignee: &assignee,
	})
	require.NoError(t, err)

	claimStore := newNotifyingClaimStore(store)
	orchestrator := NewOrchestrator(claimStore, nil, nil)
	orchestrator.TickInterval = time.Hour
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- orchestrator.Run(runCtx, teamID)
	}()

	time.Sleep(25 * time.Millisecond)
	_, err = store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "task-signal wake task",
		Status: TaskStatusReady,
	})
	require.NoError(t, err)

	select {
	case <-claimStore.claimed:
	case err := <-errCh:
		t.Fatalf("orchestrator exited before task signal wake task was claimed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("orchestrator did not claim task signal wake task before fallback tick")
	}

	cancel()
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("orchestrator did not stop after context cancellation")
	}
}

func TestOrchestratorRunUsesAgentControlTaskWakeSourceBeforeFallbackTick(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-a", TeamID: teamID, State: TeammateStateIdle})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-b", TeamID: teamID, State: TeammateStateBusy})
	require.NoError(t, err)
	assignee := "mate-b"
	_, err = store.CreateTask(ctx, Task{
		TeamID:   teamID,
		Title:    "already running task",
		Status:   TaskStatusRunning,
		Assignee: &assignee,
	})
	require.NoError(t, err)

	claimStore := newNotifyingClaimStore(store)
	wakeSource := newTestAgentControlTaskWakeSource()
	orchestrator := NewOrchestrator(claimStore, nil, nil)
	orchestrator.TaskWake = wakeSource
	orchestrator.TickInterval = time.Hour
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- orchestrator.Run(runCtx, teamID)
	}()

	time.Sleep(25 * time.Millisecond)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "agent-control wake task",
		Status: TaskStatusReady,
	})
	require.NoError(t, err)
	wakeSource.Wake(teamID, taskID)

	select {
	case <-claimStore.claimed:
	case err := <-errCh:
		t.Fatalf("orchestrator exited before AgentControl wake task was claimed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("orchestrator did not claim AgentControl wake task before fallback tick")
	}

	cancel()
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("orchestrator did not stop after context cancellation")
	}
}

func TestOrchestratorExecuteAssignmentBlocksTaskAndReplans(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{
		LeadSessionID: "lead-session",
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: "mate-session",
		State:     TeammateStateBusy,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "blocked task",
		Status: TaskStatusRunning,
	})
	require.NoError(t, err)

	orchestrator := NewOrchestrator(store, nil, nil)
	orchestrator.Mailbox = NewMailboxService(store)
	orchestrator.Runner = &TeammateRunner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "```json\n{\"task_status\":\"blocked\",\"summary\":\"waiting on architecture review\",\"blocker\":\"need architecture review\"}\n```",
			},
		},
	}
	orchestrator.LeadPlanner = &LeadPlanner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  `{"tasks":[{"id":"task-followup","title":"follow up","goal":"collect missing info"}]}`,
			},
		},
		Store: store,
	}

	orchestrator.executeAssignment(ctx, teamID, Assignment{
		Task: Task{
			ID:     taskID,
			TeamID: teamID,
			Title:  "blocked task",
		},
		Teammate: Teammate{
			ID:        "mate-1",
			TeamID:    teamID,
			SessionID: "mate-session",
		},
	})

	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, TaskStatusBlocked, task.Status)

	mate, err := store.GetTeammate(ctx, "mate-1")
	require.NoError(t, err)
	require.NotNil(t, mate)
	assert.Equal(t, TeammateStateBlocked, mate.State)

	tasks, err := store.ListTasks(ctx, TaskFilter{TeamID: teamID})
	require.NoError(t, err)
	require.Len(t, tasks, 2)
}

func TestOrchestratorBlockedReplanFollowupClaimsDifferentIdleTeammate(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{
		LeadSessionID: "lead-session",
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: "mate-session-1",
		State:     TeammateStateBusy,
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "mate-2",
		TeamID:    teamID,
		SessionID: "mate-session-2",
		State:     TeammateStateIdle,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "blocked task",
		Status: TaskStatusRunning,
	})
	require.NoError(t, err)

	orchestrator := NewOrchestrator(store, nil, nil)
	orchestrator.Mailbox = NewMailboxService(store)
	orchestrator.Runner = &TeammateRunner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "```json\n{\"task_status\":\"blocked\",\"summary\":\"waiting on architecture review\",\"blocker\":\"need architecture review\"}\n```",
			},
		},
	}
	orchestrator.LeadPlanner = &LeadPlanner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  `{"tasks":[{"id":"task-followup","title":"follow up","goal":"collect missing info"}]}`,
			},
		},
		Store: store,
	}

	orchestrator.executeAssignment(ctx, teamID, Assignment{
		Task: Task{
			ID:     taskID,
			TeamID: teamID,
			Title:  "blocked task",
		},
		Teammate: Teammate{
			ID:        "mate-1",
			TeamID:    teamID,
			SessionID: "mate-session-1",
		},
	})

	assignments, err := orchestrator.ClaimReadyTasks(ctx, teamID, 0)
	require.NoError(t, err)
	require.Len(t, assignments, 1)
	assert.Equal(t, "follow up", assignments[0].Task.Title)
	assert.Equal(t, "mate-2", assignments[0].Teammate.ID)

	followupTasks, err := store.ListTasks(ctx, TaskFilter{TeamID: teamID})
	require.NoError(t, err)
	var followupTask *Task
	for i := range followupTasks {
		if followupTasks[i].Title == "follow up" {
			followupTask = &followupTasks[i]
			break
		}
	}
	require.NotNil(t, followupTask)
	assert.Equal(t, TaskStatusRunning, followupTask.Status)
	require.NotNil(t, followupTask.Assignee)
	assert.Equal(t, "mate-2", *followupTask.Assignee)

	mate1, err := store.GetTeammate(ctx, "mate-1")
	require.NoError(t, err)
	require.NotNil(t, mate1)
	assert.Equal(t, TeammateStateBlocked, mate1.State)

	mate2, err := store.GetTeammate(ctx, "mate-2")
	require.NoError(t, err)
	require.NotNil(t, mate2)
	assert.Equal(t, TeammateStateBusy, mate2.State)
}

func TestOrchestratorExecuteAssignmentFailsProtocolErrorOutput(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: "mate-session",
		State:     TeammateStateBusy,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "protocol task",
		Status: TaskStatusRunning,
	})
	require.NoError(t, err)

	orchestrator := NewOrchestrator(store, nil, nil)
	orchestrator.Runner = &TeammateRunner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "blocked: waiting on architecture review",
			},
		},
	}

	orchestrator.executeAssignment(ctx, teamID, Assignment{
		Task: Task{
			ID:     taskID,
			TeamID: teamID,
			Title:  "protocol task",
		},
		Teammate: Teammate{
			ID:        "mate-1",
			TeamID:    teamID,
			SessionID: "mate-session",
		},
	})

	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, TaskStatusFailed, task.Status)
	assert.Contains(t, task.Summary, "protocol error")

	mate, err := store.GetTeammate(ctx, "mate-1")
	require.NoError(t, err)
	require.NotNil(t, mate)
	assert.Equal(t, TeammateStateIdle, mate.State)
}

func TestOrchestratorExecuteAssignmentPublishesPromptPreflightFailureMetadata(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: "mate-session",
		State:     TeammateStateBusy,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "preflight task",
		Status: TaskStatusRunning,
	})
	require.NoError(t, err)

	orchestrator := NewOrchestrator(store, nil, nil)
	orchestrator.Runner = &TeammateRunner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success:   false,
				Error:     "prompt preflight budget exceeded",
				TraceID:   "trace-team-preflight",
				ErrorType: "prompt_preflight",
				ErrorMetadata: map[string]interface{}{
					"failure_reason_code":               "prompt_still_exceeds_budget_after_compaction",
					"replacement_history_applied":       true,
					"replacement_history_available":     true,
					"replacement_history_message_count": 4,
				},
			},
			err: assert.AnError,
		},
	}
	orchestrator.Events = NewTeamEventBus()

	var failedEvent *TeamEvent
	orchestrator.Events.Subscribe("task.failed", func(event TeamEvent) {
		cloned := event
		failedEvent = &cloned
	})

	orchestrator.executeAssignment(ctx, teamID, Assignment{
		Task: Task{
			ID:     taskID,
			TeamID: teamID,
			Title:  "preflight task",
		},
		Teammate: Teammate{
			ID:        "mate-1",
			TeamID:    teamID,
			SessionID: "mate-session",
		},
	})

	require.NotNil(t, failedEvent)
	assert.Equal(t, "task.failed", failedEvent.Type)
	assert.Equal(t, "trace-team-preflight", failedEvent.Payload["trace_id"])
	assert.Equal(t, "prompt_preflight", failedEvent.Payload["error_type"])
	assert.Equal(t, "prompt_still_exceeds_budget_after_compaction", failedEvent.Payload["failure_reason_code"])
	assert.Equal(t, true, failedEvent.Payload["replacement_history_applied"])
}

func TestOrchestratorExecuteAssignmentMarksTeamDoneWhenLastTaskCompletes(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{
		Status:        TeamStatusActive,
		LeadSessionID: "lead-session",
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: "mate-session",
		State:     TeammateStateBusy,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "final task",
		Status: TaskStatusRunning,
	})
	require.NoError(t, err)

	orchestrator := NewOrchestrator(store, nil, nil)
	orchestrator.Runner = &TeammateRunner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "```json\n{\"task_status\":\"done\",\"summary\":\"all done\"}\n```",
			},
		},
	}
	orchestrator.LeadPlanner = &LeadPlanner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "lead summary",
			},
		},
		Store: store,
	}

	orchestrator.executeAssignment(ctx, teamID, Assignment{
		Task: Task{
			ID:     taskID,
			TeamID: teamID,
			Title:  "final task",
		},
		Teammate: Teammate{
			ID:        "mate-1",
			TeamID:    teamID,
			SessionID: "mate-session",
		},
	})

	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, TaskStatusDone, task.Status)

	teamRecord, err := store.GetTeam(ctx, teamID)
	require.NoError(t, err)
	require.NotNil(t, teamRecord)
	assert.Equal(t, TeamStatusDone, teamRecord.Status)

	events, err := store.ListTeamEvents(ctx, TeamEventFilter{TeamID: teamID})
	require.NoError(t, err)
	require.Len(t, events, 4)
	assert.Equal(t, "task.started", events[0].Type)
	assert.Equal(t, "task.completed", events[1].Type)
	assert.Equal(t, "all done", events[1].Payload["summary"])
	assert.Equal(t, "team.completed", events[2].Type)
	assert.Equal(t, "done", events[2].Payload["status"])
	assert.Equal(t, "team.summary", events[3].Type)
	assert.Equal(t, "lead summary", events[3].Payload["summary"])
}

func TestOrchestratorExecuteAssignmentSendsLeadProgressMailbox(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{
		Status: TeamStatusActive,
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: "mate-session",
		State:     TeammateStateBusy,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "final task",
		Status: TaskStatusRunning,
	})
	require.NoError(t, err)

	orchestrator := NewOrchestrator(store, nil, nil)
	orchestrator.Mailbox = NewMailboxService(store)
	orchestrator.Runner = &TeammateRunner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "```json\n{\"task_status\":\"done\",\"summary\":\"all done\"}\n```",
			},
		},
	}

	orchestrator.executeAssignment(ctx, teamID, Assignment{
		Task: Task{
			ID:     taskID,
			TeamID: teamID,
			Title:  "final task",
		},
		Teammate: Teammate{
			ID:        "mate-1",
			TeamID:    teamID,
			SessionID: "mate-session",
		},
	})

	messages, err := store.ListMail(ctx, MailFilter{TeamID: teamID})
	require.NoError(t, err)
	require.Len(t, messages, 2)
	byKind := map[string]MailMessage{}
	for _, message := range messages {
		byKind[message.Kind] = message
	}
	progress, ok := byKind["progress"]
	require.True(t, ok, "expected progress mailbox message")
	assert.Equal(t, "lead", progress.ToAgent)
	assert.Equal(t, "mate-1", progress.FromAgent)
	assert.Equal(t, taskID, *progress.TaskID)
	assert.Contains(t, progress.Body, "Started task")

	done, ok := byKind["done"]
	require.True(t, ok, "expected done mailbox message")
	assert.Equal(t, "lead", done.ToAgent)
	assert.Equal(t, "mate-1", done.FromAgent)
	assert.Equal(t, taskID, *done.TaskID)
	assert.Equal(t, "all done", done.Body)
}
