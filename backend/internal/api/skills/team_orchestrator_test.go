package skills

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type promptPreflightSummarySessionClient struct {
	result *team.SessionResult
	err    error
}

func (c *promptPreflightSummarySessionClient) SubmitPrompt(ctx context.Context, sessionID, prompt string, runMeta *team.RunMeta) (*team.SessionResult, error) {
	return c.result, c.err
}

func TestGetTeamOrchestratorEnrichesInjectedOrchestrator(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{
		DSN: "file:skills-team-orchestrator-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	handler := &Handler{
		teamStore:        store,
		teamOrchestrator: team.NewOrchestrator(store, nil, nil),
	}

	orchestrator := handler.getTeamOrchestrator()
	require.NotNil(t, orchestrator)
	require.NotNil(t, orchestrator.Store)
	require.NotNil(t, orchestrator.Claims)
	require.NotNil(t, orchestrator.Mailbox)
	require.NotNil(t, orchestrator.Events)
	require.NotNil(t, orchestrator.LeaseManager)
	require.NotNil(t, orchestrator.Runner)
	require.NotNil(t, orchestrator.LeadPlanner)
}

func TestSessionActorClientTriggerTaskPersistsDispatchEvents(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{
		DSN: "file:skills-trigger-task-dispatch-events?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.CreateTeam(context.Background(), team.Team{ID: "team-1"})
	require.NoError(t, err)

	eventStore := chat.NewInMemoryRuntimeStore(16)
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(chat.NewSessionManager(chat.NewInMemoryStorage(), nil))
	handler.sessionEventStore = eventStore
	client := &sessionActorClient{
		store:      store,
		eventStore: eventStore,
		eventBus:   handler.getRuntimeEventBus(),
	}
	result, err := client.TriggerTask(context.Background(), team.TaskTriggerRequest{
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
	require.Error(t, err)
	require.Nil(t, result)

	events, err := store.ListTeamEvents(context.Background(), team.TeamEventFilter{TeamID: "team-1"})
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, team.TaskDispatchRequestedEvent, events[0].Type)
	assert.Equal(t, team.TaskDispatchCompletedEvent, events[1].Type)
	assert.Equal(t, "mate-1", events[1].Payload["assignee"])
	assert.Equal(t, "session-1", events[1].Payload["session_id"])
	assert.Equal(t, false, events[1].Payload["success"])
	assert.Contains(t, events[1].Payload["error"], "session hub not configured")
	agentEvents, err := eventStore.ListEvents(context.Background(), "session-1", 0, 10)
	require.NoError(t, err)
	require.Len(t, agentEvents, 1)
	assert.Equal(t, chat.EventMailboxReceived, agentEvents[0].Type)
	assert.Equal(t, team.TaskAssignmentMailboxKind, agentEvents[0].Payload["kind"])
	metadata, ok := agentEvents[0].Payload["metadata"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "task-1", metadata["task_id"])
	assert.Equal(t, "team-1", metadata["team_id"])
	assert.Equal(t, "session-1", metadata["target_session_id"])
	assert.Equal(t, team.TaskAssignmentControlMessageType, metadata["message_type"])
	assert.Equal(t, team.TaskAssignmentControlAction, metadata["control_action"])
	assert.Equal(t, team.TaskAssignmentWorkflow, metadata["workflow"])
	mailboxMessages, err := eventStore.ListMailbox(context.Background(), "session-1", 0, 10)
	require.NoError(t, err)
	require.Len(t, mailboxMessages, 1)
	assert.Equal(t, team.TaskAssignmentMailboxKind, mailboxMessages[0].Kind)
	assert.Equal(t, int64(1), mailboxMessages[0].Seq)
	assert.Equal(t, team.TaskAssignmentControlMessageType, mailboxMessages[0].Metadata["message_type"])
}

func TestHandlerDeliverTeamLifecycleMailboxPersistsToLeadSession(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{
		DSN: "file:skills-team-lifecycle-mailbox?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.CreateTeam(ctx, team.Team{
		ID:            "team-1",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusDone,
	})
	require.NoError(t, err)

	eventStore := chat.NewInMemoryRuntimeStore(16)
	handler := &Handler{
		teamStore:         store,
		sessionEventStore: eventStore,
		runtimeEventBus:   runtimeevents.NewBusWithRetention(16),
	}
	handler.deliverTeamLifecycleMailbox(ctx, team.TeamEvent{
		Type:   "team.summary",
		TeamID: "team-1",
		Payload: map[string]interface{}{
			"summary": "lead summary",
			"status":  "done",
		},
	})

	messages, err := eventStore.ListMailbox(ctx, "lead-session", 0, 10)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, team.TeamLifecycleMailboxKind, messages[0].Kind)
	assert.Equal(t, "lead summary", messages[0].Body)
	assert.Equal(t, "team.summary", messages[0].Metadata["event_type"])
	assert.Equal(t, team.TeamLifecycleControlMessageType, messages[0].Metadata["message_type"])
	assert.Equal(t, "done", messages[0].Metadata["status"])
}

func TestHandlerDispatchTeamMailboxMessageHandlesEmptyTargets(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{
		DSN: "file:skills-team-mail-dispatch-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	teamID, err := store.CreateTeam(context.Background(), team.Team{})
	require.NoError(t, err)

	handler := &Handler{
		teamStore: store,
	}
	err = handler.DispatchTeamMailboxMessage(context.Background(), team.MailMessage{
		ID:        "mail-1",
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "*",
		Kind:      "info",
		Body:      "no session targets",
	})
	require.NoError(t, err)
}

func TestHandlerDispatchTeamMailboxMessagePersistsWithoutActor(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{
		DSN: "file:skills-team-mail-durable-dispatch-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	teamID, err := store.CreateTeam(ctx, team.Team{
		ID:            "team-1",
		LeadSessionID: "session-1",
		Status:        team.TeamStatusActive,
	})
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	t.Cleanup(func() { handler.getSessionHub().StopAll() })
	handler.SetSessionManager(sessionManager)
	handler.teamStore = store
	handler.sessionEventStore = chat.NewInMemoryRuntimeStore(16)
	session := chat.NewSession("user-team-mail-durable")
	session.ID = "session-1"
	require.NoError(t, sessionManager.GetStorage().Save(ctx, session))
	handler.getSessionHub().Stop("session-1")

	err = handler.DispatchTeamMailboxMessage(ctx, team.MailMessage{
		ID:        "mail-1",
		TeamID:    teamID,
		FromAgent: "planner",
		ToAgent:   "lead",
		Kind:      "progress",
		Body:      "durable api team hello",
	})
	require.NoError(t, err)
	if _, ok := handler.getSessionHub().Get("session-1"); ok {
		t.Fatal("team mailbox dispatch should persist event without starting target actor")
	}
	events, err := handler.getSessionEventStore().ListEvents(ctx, "session-1", 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, chat.EventMailboxReceived, events[0].Type)
	assert.Equal(t, "progress", events[0].Payload["kind"])
	assert.Equal(t, "durable api team hello", events[0].Payload["body"])
}

func TestHandlerDispatchTeamMailboxMessagePersistsWithoutSessionManager(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{
		DSN: "file:skills-team-mail-no-session-manager-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	teamID, err := store.CreateTeam(ctx, team.Team{
		ID:            "team-1",
		LeadSessionID: "session-1",
		Status:        team.TeamStatusActive,
	})
	require.NoError(t, err)

	eventStore := chat.NewInMemoryRuntimeStore(16)
	handler := &Handler{
		teamStore:         store,
		sessionEventStore: eventStore,
		runtimeEventBus:   runtimeevents.NewBusWithRetention(16),
	}

	err = handler.DispatchTeamMailboxMessage(ctx, team.MailMessage{
		ID:        "mail-1",
		TeamID:    teamID,
		FromAgent: "planner",
		ToAgent:   "lead",
		Kind:      "progress",
		Body:      "durable no hub",
	})
	require.NoError(t, err)
	events, err := eventStore.ListEvents(ctx, "session-1", 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, chat.EventMailboxReceived, events[0].Type)
	assert.Equal(t, "durable no hub", events[0].Payload["body"])
}

func TestSyncTeamLifecycleLoopsEnrichesInjectedOrchestratorBeforeStartingLoops(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{
		DSN: "file:skills-team-sync-loop-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		Status: team.TeamStatusActive,
	})
	require.NoError(t, err)

	handler := &Handler{
		teamStore:        store,
		teamOrchestrator: team.NewOrchestrator(store, nil, nil),
	}
	handler.SetSessionManager(chat.NewSessionManager(chat.NewInMemoryStorage(), nil))

	handler.teamLifecycleService().SyncLoops()
	t.Cleanup(func() { handler.teamLifecycleService().StopAllLoops() })

	require.NotNil(t, handler.teamOrchestrator.Runner)
	require.NotNil(t, handler.teamOrchestrator.LeadPlanner)

	running := handler.teamLifecycleService().HasLoop(teamID)
	require.True(t, running, "expected team loop to be started for active team")
}

func TestSyncTeamLifecycleLoopsSignalsExistingLoop(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{
		DSN: "file:skills-team-sync-loop-wake-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	teamID, err := store.CreateTeam(ctx, team.Team{
		Status: team.TeamStatusActive,
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, team.Teammate{
		ID:     "mate-a",
		TeamID: teamID,
		State:  team.TeammateStateIdle,
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, team.Teammate{
		ID:     "mate-b",
		TeamID: teamID,
		State:  team.TeammateStateBusy,
	})
	require.NoError(t, err)
	assignee := "mate-b"
	leaseUntil := time.Now().UTC().Add(time.Hour)
	_, err = store.CreateTask(ctx, team.Task{
		TeamID:     teamID,
		Title:      "already running task",
		Status:     team.TaskStatusRunning,
		Assignee:   &assignee,
		LeaseUntil: &leaseUntil,
	})
	require.NoError(t, err)

	handler := &Handler{
		teamStore:        store,
		teamOrchestrator: team.NewOrchestrator(store, nil, nil),
	}
	orchestrator := handler.getTeamOrchestrator()
	require.NotNil(t, orchestrator)
	orchestrator.TickInterval = time.Hour

	handler.teamLifecycleService().SyncLoops()
	t.Cleanup(func() { handler.teamLifecycleService().StopAllLoops() })
	require.True(t, handler.teamLifecycleService().HasLoop(teamID))

	wakeTaskID, err := store.CreateTask(ctx, team.Task{
		TeamID: teamID,
		Title:  "wake task",
		Status: team.TaskStatusReady,
	})
	require.NoError(t, err)
	handler.teamLifecycleService().SyncLoops()

	if !assert.Eventually(t, func() bool {
		task, loadErr := store.GetTask(ctx, wakeTaskID)
		return loadErr == nil && task != nil && task.Status != team.TaskStatusReady
	}, 2*time.Second, 20*time.Millisecond) {
		task, _ := store.GetTask(ctx, wakeTaskID)
		teamRecord, _ := store.GetTeam(ctx, teamID)
		teammates, _ := store.ListTeammates(ctx, teamID)
		t.Fatalf("wake task was not processed; loop=%v team=%+v task=%+v teammates=%+v",
			handler.teamLifecycleService().HasLoop(teamID),
			teamRecord,
			task,
			teammates,
		)
	}
}

func TestTerminalTeamSummaryFallbackPublishesStructuredRuntimeEvents(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{
		DSN: "file:skills-team-terminal-summary-runtime?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	teamID, err := store.CreateTeam(ctx, team.Team{
		Status:        team.TeamStatusActive,
		LeadSessionID: "lead-session",
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, team.Task{
		TeamID:  teamID,
		Title:   "done task",
		Status:  team.TaskStatusDone,
		Summary: "finished",
	})
	require.NoError(t, err)

	handler := &Handler{
		teamStore:        store,
		teamOrchestrator: team.NewOrchestrator(store, nil, nil),
	}
	orchestrator := handler.getTeamOrchestrator()
	require.NotNil(t, orchestrator)
	require.NotNil(t, orchestrator.Events)
	require.NotNil(t, orchestrator.Mailbox)
	require.NotNil(t, orchestrator.LeadPlanner)
	planner := &team.LeadPlanner{
		Sessions: &promptPreflightSummarySessionClient{
			result: &team.SessionResult{
				Success:   false,
				Error:     "prompt preflight budget exceeded",
				TraceID:   "trace-runtime-terminal-summary",
				ErrorType: "prompt_preflight",
				ErrorMetadata: map[string]interface{}{
					"failure_reason_code":         "prompt_still_exceeds_budget_after_compaction",
					"replacement_history_applied": true,
				},
			},
			err: assert.AnError,
		},
		Store: store,
	}

	result, err := team.ReconcileTerminalTeamState(ctx, team.TerminalTeamServices{
		Store:   store,
		Planner: planner,
		Mailbox: orchestrator.Mailbox,
		Events:  orchestrator.Events,
	}, teamID)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, team.FinalSummarySourceFallback, result.SummarySource)
	assert.Equal(t, team.FinalSummaryFallbackLeadSessionError, result.SummaryFallbackReason)
	assert.Equal(t, "prompt_preflight", result.SummaryErrorType)

	failedEvents := handler.getRuntimeEventBus().Query(runtimeevents.QueryFilter{
		TeamID:    teamID,
		EventType: "team.summary.failed",
		Limit:     10,
	})
	require.NotEmpty(t, failedEvents)
	assert.Equal(t, "prompt_preflight", failedEvents[0].Payload["error_type"])
	assert.Equal(t, team.FinalSummaryFallbackLeadSessionError, failedEvents[0].Payload["fallback_reason"])

	summaryEvents := handler.getRuntimeEventBus().Query(runtimeevents.QueryFilter{
		TeamID:    teamID,
		EventType: "team.summary",
		Limit:     10,
	})
	require.NotEmpty(t, summaryEvents)
	assert.Equal(t, team.FinalSummarySourceFallback, summaryEvents[0].Payload["summary_source"])
	assert.Equal(t, true, summaryEvents[0].Payload["used_fallback"])
	assert.Equal(t, team.FinalSummaryFallbackLeadSessionError, summaryEvents[0].Payload["fallback_reason"])
	assert.Equal(t, "prompt_preflight", summaryEvents[0].Payload["error_type"])
}

func TestGetTeamOrchestratorDoesNotStartLoopsAsSideEffect(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{
		DSN: "file:skills-team-orchestrator-side-effect-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.CreateTeam(context.Background(), team.Team{
		Status: team.TeamStatusActive,
	})
	require.NoError(t, err)

	handler := &Handler{
		teamStore:        store,
		teamOrchestrator: team.NewOrchestrator(store, nil, nil),
	}
	handler.SetSessionManager(chat.NewSessionManager(chat.NewInMemoryStorage(), nil))

	orchestrator := handler.getTeamOrchestrator()
	require.NotNil(t, orchestrator)
	time.Sleep(100 * time.Millisecond)

	loopCount := handler.teamLifecycleService().LoopCount()
	require.Zero(t, loopCount, "getTeamOrchestrator should not start team loops")
}

func TestSyncTeamLifecycleLoopsCompletesTeamWhenTeammateReportsOutcomeViaBroker(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{
		DSN: "file:skills-team-sync-loop-outcome-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	teammateSession, err := sessionManager.Create(context.Background(), "teammate-user")
	require.NoError(t, err)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-team-sync-loop-model",
		MaxRetries:   0,
	})
	provider := &runtimeCommandSequenceProvider{
		name: "test-team-sync-loop-model",
		responses: []*llm.LLMResponse{
			{
				Content: "Reporting completion through the team tool.",
				Model:   "test-team-sync-loop-model",
				ToolCalls: []types.ToolCall{
					{
						ID:   "tool_report_outcome_1",
						Name: toolbroker.ToolReportTaskOutcome,
						Args: map[string]interface{}{
							"task_status": "done",
							"summary":     "auto team ok",
						},
					},
				},
			},
			{
				Content: "```json\n{\"task_status\":\"done\",\"summary\":\"auto team ok\"}\n```",
				Model:   "test-team-sync-loop-model",
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)
	handler.SetSessionManager(sessionManager)
	handler.SetLLMRuntime(runtime)
	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Agent.DefaultModel = provider.Name()
	handler.SetRuntimeConfig(runtimeConfig, "")
	handler.SetTeamOrchestrator(team.NewOrchestrator(store, nil, nil))
	handler.teamOrchestrator.TickInterval = 10 * time.Millisecond

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		Status: team.TeamStatusActive,
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(context.Background(), team.Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: teammateSession.ID,
		State:     team.TeammateStateIdle,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(context.Background(), team.Task{
		TeamID: teamID,
		Title:  "final task",
		Goal:   "finish the auto team flow",
		Status: team.TaskStatusReady,
	})
	require.NoError(t, err)

	handler.teamLifecycleService().SyncLoops()
	t.Cleanup(func() { handler.teamLifecycleService().StopAllLoops() })

	if !assert.Eventually(t, func() bool {
		teamRecord, loadErr := store.GetTeam(context.Background(), teamID)
		return loadErr == nil && teamRecord != nil && teamRecord.Status == team.TeamStatusDone
	}, 5*time.Second, 20*time.Millisecond) {
		teamRecord, _ := store.GetTeam(context.Background(), teamID)
		taskRecord, _ := store.GetTask(context.Background(), taskID)
		sessionRecord, _ := sessionManager.Get(context.Background(), teammateSession.ID)
		historyCount := 0
		history := []types.Message(nil)
		if sessionRecord != nil {
			history = sessionRecord.GetMessages()
			historyCount = len(history)
		}
		runtimeBusEvents := handler.getRuntimeEventBus().Query(runtimeevents.QueryFilter{
			TeamID: teamID,
			Limit:  20,
		})
		t.Fatalf("team did not converge to done; team=%+v task=%+v session_history=%d history=%v runtime_events=%v",
			teamRecord,
			taskRecord,
			historyCount,
			history,
			runtimeBusEvents,
		)
	}

	var task *team.Task
	assert.Eventually(t, func() bool {
		loadedTask, loadErr := store.GetTask(context.Background(), taskID)
		if loadErr != nil || loadedTask == nil {
			return false
		}
		task = loadedTask
		return task.Status == team.TaskStatusDone && task.Summary == "auto team ok"
	}, 5*time.Second, 20*time.Millisecond, "expected task outcome to be persisted after orchestrator completion")
	require.NotNil(t, task)

	assert.Eventually(t, func() bool {
		mate, loadErr := store.GetTeammate(context.Background(), "mate-1")
		return loadErr == nil && mate != nil && mate.State == team.TeammateStateIdle
	}, 5*time.Second, 20*time.Millisecond, "expected teammate to return to idle after orchestrator cleanup")

	assert.Eventually(t, func() bool {
		events, listErr := store.ListTeamEvents(context.Background(), team.TeamEventFilter{TeamID: teamID})
		if listErr != nil {
			return false
		}
		for _, event := range events {
			if event.Type == "team.completed" && event.Payload["status"] == "done" {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "expected persisted team.completed event")

	assert.Eventually(t, func() bool {
		session, loadErr := sessionManager.Get(context.Background(), teammateSession.ID)
		if loadErr != nil || session == nil {
			return false
		}
		messages := session.GetMessages()
		if len(messages) == 0 {
			return false
		}
		last := messages[len(messages)-1]
		return last.Role == "assistant" && strings.Contains(last.Content, `"summary":"auto team ok"`)
	}, 5*time.Second, 20*time.Millisecond)

	assert.Eventually(t, func() bool {
		runtimeBusEvents := handler.getRuntimeEventBus().Query(runtimeevents.QueryFilter{
			TeamID:    teamID,
			EventType: "team.completed",
			Limit:     10,
		})
		return len(runtimeBusEvents) > 0
	}, 5*time.Second, 20*time.Millisecond)
}
