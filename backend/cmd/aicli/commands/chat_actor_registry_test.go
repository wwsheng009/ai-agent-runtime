package commands

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func TestLocalActorRegistry_SubmitPromptUsesSessionHub(t *testing.T) {
	registry := newLocalActorRegistry(&localChatRuntimeHost{
		SessionHub: buildTestSessionHub(t),
	})

	result, err := registry.SubmitPrompt(context.Background(), "session-1", "inspect", nil)
	if err != nil {
		t.Fatalf("SubmitPrompt failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected session result")
	}
	if result.Output != "inspection complete" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}

func TestLocalActorRegistry_TriggerTaskUsesSessionHub(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	if _, err := store.CreateTeam(context.Background(), team.Team{ID: "team-1"}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	registry := newLocalActorRegistry(&localChatRuntimeHost{
		SessionHub: buildTestSessionHub(t),
		EventStore: runtimechat.NewInMemoryRuntimeStore(16),
		EventBus:   runtimeevents.NewBusWithRetention(16),
		TeamStore:  store,
	})

	result, err := registry.TriggerTask(context.Background(), team.TaskTriggerRequest{
		SessionID: "session-1",
		TeamID:    "team-1",
		AgentID:   "mate-1",
		TaskID:    "task-1",
		Prompt:    "inspect",
		RunMeta: &team.RunMeta{
			PermissionMode: "bypass_permissions",
			Team: &team.TeamRunMeta{
				TeamID:        "team-1",
				AgentID:       "mate-1",
				CurrentTaskID: "task-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("TriggerTask failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected session result")
	}
	if result.Output != "inspection complete" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	events, err := store.ListTeamEvents(context.Background(), team.TeamEventFilter{TeamID: "team-1"})
	if err != nil {
		t.Fatalf("ListTeamEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected requested/completed dispatch events, got %#v", events)
	}
	if events[0].Type != team.TaskDispatchRequestedEvent {
		t.Fatalf("unexpected first event type: %s", events[0].Type)
	}
	if events[1].Type != team.TaskDispatchCompletedEvent {
		t.Fatalf("unexpected second event type: %s", events[1].Type)
	}
	payload := events[1].Payload
	if payload["team_id"] != "team-1" || payload["task_id"] != "task-1" || payload["agent_id"] != "mate-1" {
		t.Fatalf("unexpected dispatch payload: %#v", payload)
	}
	if payload["session_id"] != "session-1" || payload["assignee"] != "mate-1" {
		t.Fatalf("unexpected dispatch target payload: %#v", payload)
	}
	if payload["success"] != true {
		t.Fatalf("expected successful dispatch payload, got %#v", payload)
	}
	agentEvents, err := registry.Host.EventStore.ListEvents(context.Background(), "session-1", 0, 10)
	if err != nil {
		t.Fatalf("ListEvents session-1: %v", err)
	}
	if len(agentEvents) != 1 {
		t.Fatalf("expected assignment mailbox event, got %#v", agentEvents)
	}
	agentEvent := agentEvents[0]
	if agentEvent.Type != runtimechat.EventMailboxReceived || agentEvent.Payload["kind"] != team.TaskAssignmentMailboxKind {
		t.Fatalf("unexpected assignment mailbox event: %#v", agentEvent)
	}
	metadata, ok := agentEvent.Payload["metadata"].(map[string]interface{})
	if !ok || metadata["task_id"] != "task-1" || metadata["team_id"] != "team-1" || metadata["target_session_id"] != "session-1" {
		t.Fatalf("unexpected assignment metadata: %#v", agentEvent.Payload)
	}
	if metadata["message_type"] != team.TaskAssignmentControlMessageType || metadata["control_action"] != team.TaskAssignmentControlAction || metadata["workflow"] != team.TaskAssignmentWorkflow {
		t.Fatalf("expected agent-control task assignment metadata, got %#v", metadata)
	}
	mailboxReader, ok := registry.Host.EventStore.(runtimechat.MailboxReaderStore)
	if !ok {
		t.Fatal("expected runtime store to expose mailbox reader substrate")
	}
	mailboxMessages, err := mailboxReader.ListMailbox(context.Background(), "session-1", 0, 10)
	if err != nil {
		t.Fatalf("ListMailbox session-1: %v", err)
	}
	if len(mailboxMessages) != 1 {
		t.Fatalf("expected assignment mailbox row, got %#v", mailboxMessages)
	}
	mailboxMetadata := mailboxMessages[0].Metadata
	if mailboxMessages[0].Kind != team.TaskAssignmentMailboxKind || mailboxMessages[0].Seq != 1 || mailboxMetadata["message_type"] != team.TaskAssignmentControlMessageType {
		t.Fatalf("unexpected assignment mailbox row: %#v", mailboxMessages[0])
	}
	controlReader, ok := registry.Host.EventStore.(runtimechat.AgentControlMailboxReaderStore)
	if !ok {
		t.Fatal("expected runtime store to expose agent-control mailbox reader substrate")
	}
	controlMessages, err := controlReader.ListAgentControlMailbox(context.Background(), "session-1", 0, 10)
	if err != nil {
		t.Fatalf("ListAgentControlMailbox session-1: %v", err)
	}
	if len(controlMessages) != 1 {
		t.Fatalf("expected assignment agent-control mailbox row, got %#v", controlMessages)
	}
	if controlMessages[0].Kind != team.TaskAssignmentMailboxKind || controlMessages[0].Metadata["message_type"] != team.TaskAssignmentControlMessageType {
		t.Fatalf("unexpected assignment agent-control row: %#v", controlMessages[0])
	}
}

func TestLocalActorRegistry_DispatchTeamMailboxMessageRoutesToActor(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-1",
		LeadSessionID: "session-1",
		Status:        team.TeamStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	host := &localChatRuntimeHost{
		SessionHub: buildTestSessionHub(t),
		TeamStore:  store,
	}
	registry := newLocalActorRegistry(host)

	actor, err := host.SessionHub.GetOrCreate("session-1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	eventCh := make(chan runtimeevents.Event, 1)
	if err := actor.SubscribeEvents(context.Background(), runtimechat.EventMailboxReceived, eventCh); err != nil {
		t.Fatalf("SubscribeEvents: %v", err)
	}

	err = registry.DispatchTeamMailboxMessage(context.Background(), team.MailMessage{
		ID:        "mail-1",
		TeamID:    teamID,
		FromAgent: "planner",
		ToAgent:   "lead",
		Kind:      "progress",
		Body:      "hello lead",
	})
	if err != nil {
		t.Fatalf("DispatchTeamMailboxMessage failed: %v", err)
	}

	select {
	case event := <-eventCh:
		if event.Type != runtimechat.EventMailboxReceived {
			t.Fatalf("unexpected event type: %s", event.Type)
		}
		if got := event.Payload["message_id"]; got != "mail-1" {
			t.Fatalf("unexpected message id: %v", got)
		}
		if got := event.Payload["from_agent"]; got != "planner" {
			t.Fatalf("unexpected from agent: %v", got)
		}
		if got := event.Payload["kind"]; got != "progress" {
			t.Fatalf("unexpected kind: %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for mailbox event")
	}
}

func TestLocalActorRegistry_DispatchTeamMailboxMessagePersistsWithoutActor(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-1",
		LeadSessionID: "session-1",
		Status:        team.TeamStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	host := &localChatRuntimeHost{
		SessionHub: buildTestSessionHub(t),
		EventStore: runtimechat.NewInMemoryRuntimeStore(16),
		EventBus:   runtimeevents.NewBusWithRetention(16),
		TeamStore:  store,
	}
	registry := newLocalActorRegistry(host)
	host.SessionHub.Stop("session-1")

	err = registry.DispatchTeamMailboxMessage(context.Background(), team.MailMessage{
		ID:        "mail-1",
		TeamID:    teamID,
		FromAgent: "planner",
		ToAgent:   "lead",
		Kind:      "progress",
		Body:      "durable team hello",
	})
	if err != nil {
		t.Fatalf("DispatchTeamMailboxMessage failed: %v", err)
	}
	if _, ok := host.SessionHub.Get("session-1"); ok {
		t.Fatal("team mailbox dispatch should persist event without starting target actor")
	}
	events, err := host.EventStore.ListEvents(context.Background(), "session-1", 0, 10)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected durable mailbox event, got %#v", events)
	}
	if events[0].Type != runtimechat.EventMailboxReceived || events[0].Payload["kind"] != "progress" || events[0].Payload["body"] != "durable team hello" {
		t.Fatalf("unexpected mailbox event: %#v", events[0])
	}
}

func TestLocalActorRegistry_AllocatesMissingTeammateSessionIDs(t *testing.T) {
	resolved := ensureTeammateSessionIDs("team-1", []toolbroker.SpawnTeammateSpec{
		{Name: "writer"},
		{ID: "reviewer"},
		{Name: "writer"},
	})

	if len(resolved) != 3 {
		t.Fatalf("unexpected teammate count: %d", len(resolved))
	}
	if resolved[0].SessionID == "" || resolved[1].SessionID == "" || resolved[2].SessionID == "" {
		t.Fatalf("expected missing session ids to be filled: %+v", resolved)
	}
	if resolved[1].SessionID != "team-1__reviewer" {
		t.Fatalf("expected deterministic session id, got %q", resolved[1].SessionID)
	}
	if resolved[0].SessionID == resolved[2].SessionID {
		t.Fatalf("expected duplicate teammate names to get unique session ids: %+v", resolved)
	}
}

func TestLocalActorRegistry_RewritesCurrentTeammateSessionPlaceholder(t *testing.T) {
	resolved := ensureTeammateSessionIDs("team-1", []toolbroker.SpawnTeammateSpec{
		{ID: "writer", SessionID: "current"},
	})

	if len(resolved) != 1 {
		t.Fatalf("unexpected teammate count: %d", len(resolved))
	}
	if resolved[0].SessionID != "team-1__writer" {
		t.Fatalf("expected current placeholder to be replaced, got %+v", resolved[0])
	}
}

func buildTestSessionHub(t *testing.T) *runtimechat.SessionHub {
	t.Helper()

	sessionStore := runtimechat.NewInMemoryStorage()
	session := runtimechat.NewSession("tester")
	session.ID = "session-1"
	if err := sessionStore.Save(context.Background(), session); err != nil {
		t.Fatalf("sessionStore.Save: %v", err)
	}

	provider := runtimellm.NewMockProvider("mock", 0)
	provider.SetResponse("inspect", "inspection complete")
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "mock",
		DefaultModel:    "mock-model",
	})
	if err := llmRuntime.RegisterProvider("mock", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("mock-model", "mock"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	return runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
		a := agent.NewAgentWithLLM(&agent.Config{
			Name:     "test-actor",
			Provider: "mock",
			Model:    "mock-model",
			MaxSteps: 4,
		}, nil, llmRuntime)
		a.SetToolExecutionPolicy(runtimepolicy.NewToolExecutionPolicy([]string{}, false))
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: sessionStore,
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     runtimeevents.NewBusWithRetention(32),
		})
	})
}
