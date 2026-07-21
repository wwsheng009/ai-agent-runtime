package commands

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelrouting"
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

func TestLocalActorRegistry_AgentSnapshotReconcilesStaleRunningState(t *testing.T) {
	ctx := context.Background()
	sessionStore := runtimechat.NewInMemoryStorage()
	session := runtimechat.NewSession("stale-agent-user")
	session.ID = "stale-agent-session"
	if err := sessionStore.Save(ctx, session); err != nil {
		t.Fatalf("save session: %v", err)
	}
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	if err := runtimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID:     session.ID,
		Status:        runtimechat.SessionRunning,
		CurrentTurnID: "turn-stale",
		UpdatedAt:     time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("save runtime state: %v", err)
	}
	if _, err := runtimeStore.AcquireLease(ctx, runtimechat.LeaseRequest{
		SessionID: session.ID,
		OwnerID:   "stale-owner",
		OwnerKind: "test",
		TTL:       time.Second,
		Now:       time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("save expired lease: %v", err)
	}
	registry := newLocalActorRegistry(&localChatRuntimeHost{
		SessionStore: sessionStore,
		RuntimeStore: runtimeStore,
	})

	result, err := registry.agentSnapshot(ctx, session.ID)
	if err != nil {
		t.Fatalf("agentSnapshot failed: %v", err)
	}
	if result == nil || result.Status != string(runtimechat.SessionStopped) {
		t.Fatalf("expected stale session to report stopped, got %#v", result)
	}
	if result.CurrentTurnID != "" {
		t.Fatalf("expected stale turn id to be cleared, got %#v", result)
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
	if len(events) != 3 {
		t.Fatalf("expected requested/started/completed dispatch events, got %#v", events)
	}
	if events[0].Type != team.TaskDispatchRequestedEvent {
		t.Fatalf("unexpected first event type: %s", events[0].Type)
	}
	if events[1].Type != team.TaskDispatchStartedEvent {
		t.Fatalf("unexpected second event type: %s", events[1].Type)
	}
	if events[2].Type != team.TaskDispatchCompletedEvent {
		t.Fatalf("unexpected third event type: %s", events[2].Type)
	}
	payload := events[2].Payload
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

func TestLocalActorRegistry_TriggerTaskDetachesRunFromCallerCancel(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	if _, err := store.CreateTeam(context.Background(), team.Team{ID: "team-1"}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &cancelingContextProvider{cancel: cancel}
	registry := newLocalActorRegistry(&localChatRuntimeHost{
		SessionHub: buildTestSessionHubWithProvider(t, provider),
		EventStore: runtimechat.NewInMemoryRuntimeStore(16),
		EventBus:   runtimeevents.NewBusWithRetention(16),
		TeamStore:  store,
	})

	result, err := registry.TriggerTask(ctx, team.TaskTriggerRequest{
		SessionID: "session-1",
		TeamID:    "team-1",
		AgentID:   "mate-1",
		TaskID:    "task-1",
		Prompt:    "inspect",
	})
	if err != nil {
		t.Fatalf("TriggerTask failed after caller cancel: %v", err)
	}
	if result == nil || result.Output != "inspection complete" {
		t.Fatalf("unexpected result after caller cancel: %#v", result)
	}

	events, err := store.ListTeamEvents(context.Background(), team.TeamEventFilter{TeamID: "team-1"})
	if err != nil {
		t.Fatalf("ListTeamEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected requested/started/completed dispatch events, got %#v", events)
	}
	if events[2].Type != team.TaskDispatchCompletedEvent {
		t.Fatalf("expected completed dispatch event, got %#v", events[2])
	}
	if events[2].Payload["success"] != true {
		t.Fatalf("expected successful completed dispatch payload, got %#v", events[2].Payload)
	}
}

func TestLocalActorRegistry_TriggerTaskAppliesRouteOverridePerRun(t *testing.T) {
	registry, baseProvider, routedProvider := buildRouteOverrideTestRegistry(t)
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	if _, err := store.CreateTeam(context.Background(), team.Team{ID: "team-1"}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	registry.Host.TeamStore = store

	result, err := registry.TriggerTask(context.Background(), team.TaskTriggerRequest{
		SessionID: "session-1",
		TeamID:    "team-1",
		AgentID:   "mate-1",
		TaskID:    "task-routed",
		Prompt:    "use routed model",
		Route: &team.TaskExecutionRoute{
			Provider:        "routed-provider",
			Model:           "routed-model",
			ReasoningEffort: "high",
			Source:          modelrouting.SourceDifficultyLevel,
		},
		RunMeta: &team.RunMeta{
			PermissionMode: "bypass_permissions",
			Team: &team.TeamRunMeta{
				TeamID:        "team-1",
				AgentID:       "mate-1",
				CurrentTaskID: "task-routed",
			},
		},
	})
	if err != nil {
		t.Fatalf("TriggerTask routed failed: %v", err)
	}
	if result == nil || result.Output != "routed ok" {
		t.Fatalf("unexpected routed result: %#v", result)
	}
	if len(routedProvider.requests) != 1 {
		t.Fatalf("expected one routed-provider request, got %d", len(routedProvider.requests))
	}
	routedRequest := routedProvider.requests[0]
	if routedRequest.Provider != "routed-provider" || routedRequest.Model != "routed-model" || routedRequest.ReasoningEffort != "high" {
		t.Fatalf("unexpected routed request: %#v", routedRequest)
	}
	events, err := store.ListTeamEvents(context.Background(), team.TeamEventFilter{TeamID: "team-1"})
	if err != nil {
		t.Fatalf("ListTeamEvents: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("expected dispatch events, got %#v", events)
	}
	payload := events[1].Payload
	if payload["permission_mode"] != "bypass_permissions" {
		t.Fatalf("permission mode changed or missing in dispatch payload: %#v", payload)
	}
	if payload["route_provider"] != "routed-provider" || payload["route_model"] != "routed-model" || payload["route_reasoning_effort"] != "high" {
		t.Fatalf("route metadata missing from dispatch payload: %#v", payload)
	}

	result, err = registry.TriggerTask(context.Background(), team.TaskTriggerRequest{
		SessionID: "session-1",
		TeamID:    "team-1",
		AgentID:   "mate-1",
		TaskID:    "task-base",
		Prompt:    "use base model",
	})
	if err != nil {
		t.Fatalf("TriggerTask base failed: %v", err)
	}
	if result == nil || result.Output != "base ok" {
		t.Fatalf("unexpected base result: %#v", result)
	}
	if len(baseProvider.requests) != 1 {
		t.Fatalf("expected one base-provider request, got %d", len(baseProvider.requests))
	}
	baseRequest := baseProvider.requests[0]
	if baseRequest.Provider != "base-provider" || baseRequest.Model != "base-model" || baseRequest.ReasoningEffort != "" {
		t.Fatalf("route override leaked into base request: %#v", baseRequest)
	}
}

func TestLocalActorRegistry_TriggerTaskIgnoresDisabledRouteOverride(t *testing.T) {
	registry, baseProvider, routedProvider := buildRouteOverrideTestRegistry(t)

	result, err := registry.TriggerTask(context.Background(), team.TaskTriggerRequest{
		SessionID: "session-1",
		TeamID:    "team-1",
		AgentID:   "mate-1",
		TaskID:    "task-disabled",
		Prompt:    "disabled route should use base",
		Route: &team.TaskExecutionRoute{
			Provider:        "routed-provider",
			Model:           "routed-model",
			ReasoningEffort: "high",
			Source:          modelrouting.SourceDisabled,
		},
	})
	if err != nil {
		t.Fatalf("TriggerTask disabled route failed: %v", err)
	}
	if result == nil || result.Output != "base ok" {
		t.Fatalf("unexpected disabled-route result: %#v", result)
	}
	if len(routedProvider.requests) != 0 {
		t.Fatalf("disabled route should not call routed provider: %#v", routedProvider.requests)
	}
	if len(baseProvider.requests) != 1 {
		t.Fatalf("expected one base request, got %d", len(baseProvider.requests))
	}
}

func TestLocalTeamTaskRouteResolverEnabledDisabledAndStrict(t *testing.T) {
	enabled := true
	host := &localChatRuntimeHost{
		BaseSession: &ChatSession{
			ProviderName:    "base-provider",
			Model:           "base-model",
			ReasoningEffort: "low",
			Config: &agentconfig.Config{
				AICLI: &agentconfig.AICLIConfig{
					Subagents: &agentconfig.AICLISubagentsConfig{
						Routing: &agentconfig.AICLISubagentRoutingConfig{
							Enabled:           &enabled,
							DefaultDifficulty: "normal",
							Levels: map[string]agentconfig.AICLISubagentRouteProfile{
								"hard": {
									Provider:        "hard-provider",
									Model:           "hard-model",
									ReasoningEffort: "high",
								},
							},
						},
					},
				},
			},
		},
	}
	resolver := newLocalTeamTaskRouteResolver(host)
	resolution, err := resolver.ResolveTaskRoute(context.Background(), team.TaskRouteRequest{
		Teammate: team.Teammate{ID: "mate-1", Profile: "writer", SessionID: "session-1"},
		Task: team.Task{
			ID:         "task-hard",
			Title:      "Implement routing",
			Difficulty: team.TaskDifficultyHard,
		},
		Attempt: 2,
	})
	if err != nil {
		t.Fatalf("ResolveTaskRoute enabled failed: %v", err)
	}
	if resolution == nil || resolution.Disabled || resolution.Strict {
		t.Fatalf("unexpected enabled resolution flags: %#v", resolution)
	}
	if resolution.Route == nil || resolution.Route.Provider != "hard-provider" || resolution.Route.Model != "hard-model" || resolution.Route.ReasoningEffort != "high" {
		t.Fatalf("unexpected enabled route: %#v", resolution)
	}
	if resolution.Route.Attempt != 2 || resolution.Route.Source != modelrouting.SourceDifficultyLevel {
		t.Fatalf("unexpected enabled route audit fields: %#v", resolution.Route)
	}

	disabled := false
	host.BaseSession.Config.AICLI.Subagents.Routing.Enabled = &disabled
	resolution, err = resolver.ResolveTaskRoute(context.Background(), team.TaskRouteRequest{
		Task: team.Task{
			ID:         "task-disabled",
			Title:      "Implement routing",
			Difficulty: team.TaskDifficultyHard,
		},
	})
	if err != nil {
		t.Fatalf("ResolveTaskRoute disabled failed: %v", err)
	}
	if resolution == nil || !resolution.Disabled {
		t.Fatalf("expected disabled resolution, got %#v", resolution)
	}
	if override := localChatRunRouteOverrideFromTeamRoute(resolution.Route); override != nil {
		t.Fatalf("disabled route must not become a run override: %#v", override)
	}

	enabled = true
	host.BaseSession.Config.AICLI.Subagents.Routing.Enabled = &enabled
	host.BaseSession.Config.AICLI.Subagents.Routing.CompatibilityMode = modelrouting.CompatibilityStrict
	resolution, err = resolver.ResolveTaskRoute(context.Background(), team.TaskRouteRequest{
		Task: team.Task{
			ID:         "task-invalid",
			Title:      "Invalid difficulty",
			Difficulty: "impossible",
		},
	})
	if err == nil {
		t.Fatal("expected strict invalid difficulty error")
	}
	if resolution == nil || !resolution.Strict {
		t.Fatalf("strict failures must be marked for TeammateRunner blocking: %#v", resolution)
	}
}

func TestLocalChatTeamExpertConcurrencyLimitRequiresRoutingEnabled(t *testing.T) {
	enabled := true
	session := &ChatSession{
		Config: &agentconfig.Config{
			AICLI: &agentconfig.AICLIConfig{
				Teams: &agentconfig.AICLITeamsConfig{
					Routing: &agentconfig.AICLISubagentRoutingConfig{
						Enabled:              &enabled,
						MaxExpertConcurrency: 2,
					},
				},
			},
		},
	}
	if got := localChatTeamExpertConcurrencyLimit(session); got != 2 {
		t.Fatalf("expected enabled expert limit 2, got %d", got)
	}

	disabled := false
	session.Config.AICLI.Teams.Routing.Enabled = &disabled
	if got := localChatTeamExpertConcurrencyLimit(session); got != 0 {
		t.Fatalf("expected disabled expert limit 0, got %d", got)
	}
}

func TestLocalTeamTaskRouteAuditSinkPublishesTaskRouteResolvedToRuntimeBus(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	if _, err := store.CreateTeam(ctx, team.Team{
		ID:            "team-route",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusActive,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	taskID, err := store.CreateTask(ctx, team.Task{
		ID:         "task-route",
		TeamID:     "team-route",
		Title:      "Route task",
		Goal:       "Verify route audit sink",
		Difficulty: team.TaskDifficultyHard,
		Status:     team.TaskStatusPending,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	host := &localChatRuntimeHost{
		EventStore: runtimechat.NewInMemoryRuntimeStore(16),
		EventBus:   runtimeevents.NewBusWithRetention(16),
		TeamStore:  store,
		Orchestrator: &team.Orchestrator{
			Events: team.NewTeamEventBus(),
		},
		BaseSession: &ChatSession{
			RuntimeSession: &runtimechat.Session{ID: "base-session"},
		},
	}
	host.bindTeamLifecycleEvents()
	sink := newLocalTeamTaskRouteAuditSink(host)
	if sink == nil {
		t.Fatal("expected route audit sink")
	}

	recordedAt := time.Now().UTC()
	err = sink.RecordTaskRouteAudit(ctx, team.TaskRouteAudit{
		TeamID:    "team-route",
		AgentID:   "mate-1",
		TaskID:    taskID,
		SessionID: "mate-session",
		Route: &team.TaskExecutionRoute{
			Difficulty:      team.TaskDifficultyHard,
			Provider:        "openai",
			Model:           "gpt-test",
			ReasoningEffort: "high",
			Source:          modelrouting.SourceDifficultyLevel,
			Warnings:        []string{"provider_fallback_parent"},
			ResolvedAt:      recordedAt,
			Attempt:         2,
		},
		RecordedAt: recordedAt,
	})
	if err != nil {
		t.Fatalf("RecordTaskRouteAudit: %v", err)
	}

	events, err := store.ListTeamEvents(ctx, team.TeamEventFilter{
		TeamID:    "team-route",
		EventType: team.TaskRouteResolvedEvent,
	})
	if err != nil {
		t.Fatalf("ListTeamEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected one persisted route event, got %#v", events)
	}
	payload := events[0].Payload
	if payload["task_id"] != taskID || payload["agent_id"] != "mate-1" || payload["session_id"] != "mate-session" {
		t.Fatalf("unexpected persisted route identity payload: %#v", payload)
	}
	if payload["route_provider"] != "openai" || payload["route_model"] != "gpt-test" || payload["route_reasoning_effort"] != "high" {
		t.Fatalf("unexpected persisted route model payload: %#v", payload)
	}
	if payload["route_source"] != modelrouting.SourceDifficultyLevel {
		t.Fatalf("unexpected persisted route audit payload: %#v", payload)
	}
	if attempt, ok := payloadIntValue(payload["route_attempt"]); !ok || attempt != 2 {
		t.Fatalf("unexpected persisted route attempt: %#v", payload["route_attempt"])
	}

	recent := host.EventBus.Recent(10)
	if len(recent) != 1 || recent[0].Type != team.TaskRouteResolvedEvent {
		t.Fatalf("expected route resolved on runtime bus, got %#v", recent)
	}
	if recent[0].SessionID != "lead-session" {
		t.Fatalf("expected route event to use team lead session, got %#v", recent[0])
	}
	if recent[0].Payload["route_provider"] != "openai" || recent[0].Payload["route_model"] != "gpt-test" {
		t.Fatalf("unexpected runtime bus route payload: %#v", recent[0].Payload)
	}
	if attempt, ok := payloadIntValue(recent[0].Payload["route_attempt"]); !ok || attempt != 2 {
		t.Fatalf("unexpected runtime bus route attempt: %#v", recent[0].Payload["route_attempt"])
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

func TestLocalActorRegistry_DispatchBlockedHandoffMailboxPersistsAgentControlRow(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-1",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	_, err = store.UpsertTeammate(context.Background(), team.Teammate{
		ID:        "mate-2",
		TeamID:    teamID,
		SessionID: "mate-session-2",
		State:     team.TeammateStateIdle,
	})
	if err != nil {
		t.Fatalf("UpsertTeammate: %v", err)
	}

	host := &localChatRuntimeHost{
		SessionHub: buildTestSessionHub(t),
		EventStore: runtimechat.NewInMemoryRuntimeStore(16),
		EventBus:   runtimeevents.NewBusWithRetention(16),
		TeamStore:  store,
	}
	registry := newLocalActorRegistry(host)
	host.SessionHub.Stop("mate-session-2")

	message := team.BuildBlockedTaskOutcomeMailboxMessage(teamID, "task-1", "mate-1", "mate-2", "pass to reviewer", team.TaskOutcomeContract{
		Status:    team.TaskOutcomeHandoff,
		Summary:   "pass to reviewer",
		Blocker:   "need review",
		HandoffTo: "mate-2",
	})
	message.ID = "handoff-mail-1"
	if err := registry.DispatchTeamMailboxMessage(context.Background(), message); err != nil {
		t.Fatalf("DispatchTeamMailboxMessage failed: %v", err)
	}
	if _, ok := host.SessionHub.Get("mate-session-2"); ok {
		t.Fatal("handoff mailbox dispatch should persist event without starting target actor")
	}

	controlReader, ok := host.EventStore.(runtimechat.AgentControlMailboxReaderStore)
	if !ok {
		t.Fatal("expected runtime store to expose agent-control mailbox reader")
	}
	controlMessages, err := controlReader.ListAgentControlMailbox(context.Background(), "mate-session-2", 0, 10)
	if err != nil {
		t.Fatalf("ListAgentControlMailbox: %v", err)
	}
	if len(controlMessages) != 1 {
		t.Fatalf("expected one control mailbox message, got %#v", controlMessages)
	}
	if controlMessages[0].Kind != "handoff" ||
		controlMessages[0].Metadata["message_type"] != team.TaskLifecycleControlMessageType ||
		controlMessages[0].Metadata["control_action"] != team.TaskLifecycleControlAction ||
		controlMessages[0].Metadata["workflow"] != agentcontrol.WorkflowSpawnTeam ||
		controlMessages[0].Metadata["event_type"] != "task.handoff" ||
		controlMessages[0].Metadata["handoff_to"] != "mate-2" {
		t.Fatalf("unexpected handoff control mailbox row: %#v", controlMessages[0])
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

func buildRouteOverrideTestRegistry(t *testing.T) (*localActorRegistry, *capturingLocalChatProvider, *capturingLocalChatProvider) {
	t.Helper()

	sessionStore := runtimechat.NewInMemoryStorage()
	session := runtimechat.NewSession("tester")
	session.ID = "session-1"
	if err := sessionStore.Save(context.Background(), session); err != nil {
		t.Fatalf("sessionStore.Save: %v", err)
	}

	baseProvider := &capturingLocalChatProvider{
		name: "base-provider",
		responses: []*runtimellm.LLMResponse{
			{Content: "base ok", Model: "base-model"},
		},
	}
	routedProvider := &capturingLocalChatProvider{
		name: "routed-provider",
		responses: []*runtimellm.LLMResponse{
			{Content: "routed ok", Model: "routed-model"},
		},
	}
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "base-provider",
		DefaultModel:    "base-model",
	})
	if err := llmRuntime.RegisterProvider("base-provider", baseProvider); err != nil {
		t.Fatalf("Register base provider: %v", err)
	}
	if err := llmRuntime.RegisterProvider("routed-provider", routedProvider); err != nil {
		t.Fatalf("Register routed provider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("base-model", "base-provider"); err != nil {
		t.Fatalf("Register base alias: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("routed-model", "routed-provider"); err != nil {
		t.Fatalf("Register routed alias: %v", err)
	}
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(32),
		EventStore:   runtimeStore,
		RuntimeStore: runtimeStore,
		SessionStore: sessionStore,
		SessionHub: runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
			a := agent.NewAgentWithLLM(&agent.Config{
				Name:     "route-override-test",
				Provider: "base-provider",
				Model:    "base-model",
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
		}),
	}
	return newLocalActorRegistry(host), baseProvider, routedProvider
}

type cancelingContextProvider struct {
	cancel context.CancelFunc
}

func (p *cancelingContextProvider) Name() string { return "mock" }

func (p *cancelingContextProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	if p.cancel != nil {
		p.cancel()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(20 * time.Millisecond):
	}
	return &runtimellm.LLMResponse{
		Content: "inspection complete",
		Model:   req.Model,
	}, nil
}

func (p *cancelingContextProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk, 2)
	go func() {
		defer close(ch)
		resp, err := p.Call(ctx, req)
		if err != nil {
			ch <- runtimellm.StreamChunk{Type: runtimellm.EventTypeError, Error: err.Error()}
			return
		}
		ch <- runtimellm.StreamChunk{Type: runtimellm.EventTypeText, Content: resp.Content}
		ch <- runtimellm.StreamChunk{Type: runtimellm.EventTypeDone, Done: true}
	}()
	return ch, nil
}

func (p *cancelingContextProvider) CountTokens(text string) int { return len(text) }

func (p *cancelingContextProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsTools:     true,
		SupportsStreaming: false,
	}
}

func (p *cancelingContextProvider) CheckHealth(ctx context.Context) error { return nil }

func buildTestSessionHub(t *testing.T) *runtimechat.SessionHub {
	provider := runtimellm.NewMockProvider("mock", 0)
	provider.SetResponse("inspect", "inspection complete")
	return buildTestSessionHubWithProvider(t, provider)
}

func buildTestSessionHubWithProvider(t *testing.T, provider runtimellm.Provider) *runtimechat.SessionHub {
	t.Helper()

	sessionStore := runtimechat.NewInMemoryStorage()
	session := runtimechat.NewSession("tester")
	session.ID = "session-1"
	if err := sessionStore.Save(context.Background(), session); err != nil {
		t.Fatalf("sessionStore.Save: %v", err)
	}

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
