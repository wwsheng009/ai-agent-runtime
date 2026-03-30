package skills

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

