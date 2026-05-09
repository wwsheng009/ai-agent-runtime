package commands

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func TestSignalHandlerFirstInterruptCancelsActiveTeamRuns(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	runtimeStore := runtimechat.NewInMemoryRuntimeStore(32)
	host := &localChatRuntimeHost{
		TeamStore:    store,
		RuntimeStore: runtimeStore,
		EventStore:   runtimeStore,
	}

	teamID, err := store.CreateTeam(ctx, team.Team{
		ID:            "team-signal-interrupt",
		LeadSessionID: "parent-session",
		Status:        team.TeamStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	assignee := "mate-1"
	leaseUntil := time.Now().UTC().Add(time.Minute)
	if _, err := store.UpsertTeammate(ctx, team.Teammate{
		ID:        assignee,
		TeamID:    teamID,
		SessionID: "child-session",
		State:     team.TeammateStateBusy,
	}); err != nil {
		t.Fatalf("UpsertTeammate: %v", err)
	}
	if _, err := store.CreateTask(ctx, team.Task{
		ID:         "task-signal-interrupt",
		TeamID:     teamID,
		Title:      "running task",
		Status:     team.TaskStatusRunning,
		Assignee:   &assignee,
		LeaseUntil: &leaseUntil,
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	for _, sessionID := range []string{"parent-session", "child-session"} {
		if err := runtimeStore.SaveState(ctx, &runtimechat.RuntimeState{
			SessionID:     sessionID,
			Status:        runtimechat.SessionRunning,
			CurrentTurnID: "turn-1",
			CurrentRunMeta: &team.RunMeta{Team: &team.TeamRunMeta{
				TeamID: teamID,
			}},
			UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("SaveState(%s): %v", sessionID, err)
		}
	}

	runtimeSession := runtimechat.NewSession("user-1")
	runtimeSession.ID = "parent-session"
	session := &ChatSession{
		RuntimeSession:   runtimeSession,
		SessionUserID:    "user-1",
		LocalRuntimeHost: host,
		ActiveTeam:       &chatTeamBinding{TeamID: teamID, AgentID: "lead"},
	}

	sigChan := make(chan os.Signal, 1)
	sigCountChan := make(chan int, 1)
	var shouldExit atomic.Bool
	setupSignalHandler(session, sigChan, sigCountChan, &shouldExit)
	defer signal.Stop(sigChan)

	sigChan <- os.Interrupt
	waitForSignalInterruptTaskCancelled(t, store, runtimeStore, teamID, "task-signal-interrupt")
	if shouldExit.Load() {
		t.Fatal("first interrupt should cancel active work without requesting chat loop exit")
	}

	sigChan <- os.Interrupt
	deadline := time.Now().Add(2 * time.Second)
	for !shouldExit.Load() {
		if time.Now().After(deadline) {
			t.Fatal("expected second interrupt to request chat loop exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForSignalInterruptTaskCancelled(t *testing.T, store team.Store, runtimeStore *runtimechat.InMemoryRuntimeStore, teamID, taskID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		taskRecord, err := store.GetTask(context.Background(), taskID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		teamRecord, err := store.GetTeam(context.Background(), teamID)
		if err != nil {
			t.Fatalf("GetTeam: %v", err)
		}
		parentState, err := runtimeStore.LoadState(context.Background(), "parent-session")
		if err != nil {
			t.Fatalf("LoadState parent: %v", err)
		}
		childState, err := runtimeStore.LoadState(context.Background(), "child-session")
		if err != nil {
			t.Fatalf("LoadState child: %v", err)
		}
		if taskRecord != nil &&
			taskRecord.Status == team.TaskStatusCancelled &&
			taskRecord.Assignee == nil &&
			taskRecord.LeaseUntil == nil &&
			teamRecord != nil &&
			teamRecord.Status == team.TeamStatusPaused &&
			parentState != nil &&
			parentState.Status == runtimechat.SessionStopped &&
			childState != nil &&
			childState.Status == runtimechat.SessionStopped {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected signal interrupt cleanup, got task=%+v team=%+v parent=%+v child=%+v", taskRecord, teamRecord, parentState, childState)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestInterruptActiveRunsPausesTeamAndStopsRuntimeStates(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	runtimeStore := runtimechat.NewInMemoryRuntimeStore(32)
	host := &localChatRuntimeHost{
		TeamStore:    store,
		RuntimeStore: runtimeStore,
		EventStore:   runtimeStore,
	}

	teamID, err := store.CreateTeam(ctx, team.Team{
		ID:            "team-interrupt",
		LeadSessionID: "parent-session",
		Status:        team.TeamStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := store.UpsertTeammate(ctx, team.Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: "child-session",
		State:     team.TeammateStateBusy,
	}); err != nil {
		t.Fatalf("UpsertTeammate: %v", err)
	}
	assignee := "mate-1"
	leaseUntil := time.Now().UTC().Add(time.Minute)
	if _, err := store.CreateTask(ctx, team.Task{
		ID:         "task-1",
		TeamID:     teamID,
		Title:      "running task",
		Goal:       "keep running",
		Status:     team.TaskStatusRunning,
		Assignee:   &assignee,
		LeaseUntil: &leaseUntil,
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	for _, sessionID := range []string{"parent-session", "child-session"} {
		if err := runtimeStore.SaveState(ctx, &runtimechat.RuntimeState{
			SessionID:     sessionID,
			Status:        runtimechat.SessionRunning,
			CurrentTurnID: "turn-1",
			CurrentRunMeta: &team.RunMeta{Team: &team.TeamRunMeta{
				TeamID: teamID,
			}},
			UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("SaveState(%s): %v", sessionID, err)
		}
	}

	host.interruptActiveRuns(ctx, "parent-session", "", "")

	record, err := store.GetTeam(ctx, teamID)
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if record.Status != team.TeamStatusPaused {
		t.Fatalf("expected team paused, got %s", record.Status)
	}
	taskRecord, err := store.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if taskRecord.Status != team.TaskStatusCancelled {
		t.Fatalf("expected task cancelled, got %s", taskRecord.Status)
	}
	if taskRecord.Assignee != nil || taskRecord.LeaseUntil != nil {
		t.Fatalf("expected task lease released, got assignee=%v lease=%v", taskRecord.Assignee, taskRecord.LeaseUntil)
	}
	mate, err := store.GetTeammate(ctx, "mate-1")
	if err != nil {
		t.Fatalf("GetTeammate: %v", err)
	}
	if mate.State != team.TeammateStateIdle {
		t.Fatalf("expected teammate idle, got %s", mate.State)
	}
	for _, sessionID := range []string{"parent-session", "child-session"} {
		state, err := runtimeStore.LoadState(ctx, sessionID)
		if err != nil {
			t.Fatalf("LoadState(%s): %v", sessionID, err)
		}
		if state == nil || state.Status != runtimechat.SessionStopped || state.CurrentTurnID != "" || state.CurrentRunMeta != nil {
			t.Fatalf("expected stopped clean state for %s, got %+v", sessionID, state)
		}
	}
	events, err := store.ListTeamEvents(ctx, team.TeamEventFilter{TeamID: teamID})
	if err != nil {
		t.Fatalf("ListTeamEvents: %v", err)
	}
	foundCancelledEvent := false
	for _, event := range events {
		if event.Type == "task.cancelled" &&
			payloadStringValue(event.Payload["task_id"]) == "task-1" &&
			payloadStringValue(event.Payload["assignee"]) == "mate-1" &&
			payloadStringValue(event.Payload["status"]) == string(team.TaskStatusCancelled) {
			foundCancelledEvent = true
			break
		}
	}
	if !foundCancelledEvent {
		t.Fatalf("expected task.cancelled team event, got %+v", events)
	}
	messages, err := runtimeStore.ListAgentControlMailbox(ctx, "child-session", 0, 0)
	if err != nil {
		t.Fatalf("ListAgentControlMailbox child: %v", err)
	}
	foundCancelledMailbox := false
	for _, message := range messages {
		if message.Kind == team.TaskLifecycleMailboxKind &&
			payloadStringValue(message.Metadata["event_type"]) == "task.cancelled" &&
			payloadStringValue(message.Metadata["task_id"]) == "task-1" &&
			payloadStringValue(message.Metadata["assignee"]) == "mate-1" {
			foundCancelledMailbox = true
			break
		}
	}
	if !foundCancelledMailbox {
		t.Fatalf("expected task.cancelled agent-control mailbox, got %+v", messages)
	}
}

func TestInterruptActiveRunsStopsDirectChildAgentState(t *testing.T) {
	ctx := context.Background()
	userID := "user-1"
	storage := runtimechat.NewInMemoryStorage()
	parent := runtimechat.NewSession(userID)
	parent.ID = "parent-session"
	if err := storage.Save(ctx, parent); err != nil {
		t.Fatalf("Save parent: %v", err)
	}
	child := runtimechat.NewSession(userID)
	child.ID = "child-agent-session"
	child.SetContext(toolbroker.AgentSessionContextParentSessionID, parent.ID)
	if err := storage.Save(ctx, child); err != nil {
		t.Fatalf("Save child: %v", err)
	}

	runtimeStore := runtimechat.NewInMemoryRuntimeStore(32)
	if err := runtimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID:     child.ID,
		Status:        runtimechat.SessionRunning,
		CurrentTurnID: "turn-child",
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveState child: %v", err)
	}
	host := &localChatRuntimeHost{
		SessionStore: storage,
		RuntimeStore: runtimeStore,
	}

	host.interruptActiveRuns(ctx, parent.ID, userID, "")

	state, err := runtimeStore.LoadState(ctx, child.ID)
	if err != nil {
		t.Fatalf("LoadState child: %v", err)
	}
	if state == nil || state.Status != runtimechat.SessionStopped || state.CurrentTurnID != "" {
		t.Fatalf("expected stopped child state, got %+v", state)
	}
}

func TestInterruptActiveRunsStopsNestedChildAgentState(t *testing.T) {
	ctx := context.Background()
	userID := "user-1"
	storage := runtimechat.NewInMemoryStorage()

	parent := runtimechat.NewSession(userID)
	parent.ID = "parent-session"
	if err := storage.Save(ctx, parent); err != nil {
		t.Fatalf("Save parent: %v", err)
	}

	child := runtimechat.NewSession(userID)
	child.ID = "child-agent-session"
	child.SetContext(toolbroker.AgentSessionContextParentSessionID, parent.ID)
	if err := storage.Save(ctx, child); err != nil {
		t.Fatalf("Save child: %v", err)
	}

	grandchild := runtimechat.NewSession(userID)
	grandchild.ID = "grandchild-agent-session"
	grandchild.SetContext(toolbroker.AgentSessionContextParentSessionID, child.ID)
	if err := storage.Save(ctx, grandchild); err != nil {
		t.Fatalf("Save grandchild: %v", err)
	}

	siblingParent := runtimechat.NewSession(userID)
	siblingParent.ID = "other-parent-session"
	if err := storage.Save(ctx, siblingParent); err != nil {
		t.Fatalf("Save sibling parent: %v", err)
	}

	sibling := runtimechat.NewSession(userID)
	sibling.ID = "sibling-child-session"
	sibling.SetContext(toolbroker.AgentSessionContextParentSessionID, siblingParent.ID)
	if err := storage.Save(ctx, sibling); err != nil {
		t.Fatalf("Save sibling: %v", err)
	}

	runtimeStore := runtimechat.NewInMemoryRuntimeStore(32)
	for _, sessionID := range []string{child.ID, grandchild.ID, sibling.ID} {
		if err := runtimeStore.SaveState(ctx, &runtimechat.RuntimeState{
			SessionID:     sessionID,
			Status:        runtimechat.SessionRunning,
			CurrentTurnID: "turn-" + sessionID,
			UpdatedAt:     time.Now().UTC(),
		}); err != nil {
			t.Fatalf("SaveState(%s): %v", sessionID, err)
		}
	}

	host := &localChatRuntimeHost{
		SessionStore: storage,
		RuntimeStore: runtimeStore,
	}

	host.interruptActiveRuns(ctx, parent.ID, userID, "")

	for _, sessionID := range []string{child.ID, grandchild.ID} {
		state, err := runtimeStore.LoadState(ctx, sessionID)
		if err != nil {
			t.Fatalf("LoadState(%s): %v", sessionID, err)
		}
		if state == nil || state.Status != runtimechat.SessionStopped || state.CurrentTurnID != "" {
			t.Fatalf("expected stopped descendant state for %s, got %+v", sessionID, state)
		}
	}

	state, err := runtimeStore.LoadState(ctx, sibling.ID)
	if err != nil {
		t.Fatalf("LoadState sibling: %v", err)
	}
	if state == nil || state.Status != runtimechat.SessionRunning || state.CurrentTurnID == "" {
		t.Fatalf("expected unrelated sibling to keep running, got %+v", state)
	}
}
