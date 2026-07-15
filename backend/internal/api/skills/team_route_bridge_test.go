package skills

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

type teamRouteBridgeProvider struct {
	name     string
	content  string
	requests []*llm.LLMRequest
}

func (p *teamRouteBridgeProvider) Name() string {
	return p.name
}

func (p *teamRouteBridgeProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.requests = append(p.requests, &llm.LLMRequest{
		Provider:        req.Provider,
		Model:           req.Model,
		ReasoningEffort: req.ReasoningEffort,
	})
	return &llm.LLMResponse{
		Content: p.content,
		Model:   req.Model,
	}, nil
}

func (p *teamRouteBridgeProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	p.requests = append(p.requests, &llm.LLMRequest{
		Provider:        req.Provider,
		Model:           req.Model,
		ReasoningEffort: req.ReasoningEffort,
	})
	ch := make(chan llm.StreamChunk, 1)
	ch <- llm.StreamChunk{Type: llm.EventTypeText, Content: p.content, Done: true}
	close(ch)
	return ch, nil
}

func (p *teamRouteBridgeProvider) CountTokens(text string) int {
	return len(text)
}

func (p *teamRouteBridgeProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{SupportsTools: true}
}

func (p *teamRouteBridgeProvider) CheckHealth(ctx context.Context) error {
	return nil
}

func TestChatRouteOverrideFromTaskExecutionRoute(t *testing.T) {
	assert.Nil(t, chatRouteOverrideFromTaskExecutionRoute(nil))
	assert.Nil(t, chatRouteOverrideFromTaskExecutionRoute(&team.TaskExecutionRoute{
		Difficulty: "hard",
		Source:     "difficulty_level",
	}))
	assert.Nil(t, chatRouteOverrideFromTaskExecutionRoute(&team.TaskExecutionRoute{
		Provider: "parent-provider",
		Model:    "parent-model",
		Source:   "disabled",
	}))

	override := chatRouteOverrideFromTaskExecutionRoute(&team.TaskExecutionRoute{
		Provider:        " routed-provider ",
		Model:           " routed-model ",
		ReasoningEffort: " high ",
	})
	require.NotNil(t, override)
	assert.Equal(t, "routed-provider", override.Provider)
	assert.Equal(t, "routed-model", override.Model)
	assert.Equal(t, "high", override.ReasoningEffort)
}

func TestAPITeamTaskRouteResolverResolvesWriterRoute(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	handler.SetSessionManager(sessionManager)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "base-provider",
		DefaultModel:    "base-model",
		MaxRetries:      0,
	})
	require.NoError(t, runtime.RegisterProvider("base-provider", &teamRouteBridgeProvider{name: "base-provider"}))
	require.NoError(t, runtime.RegisterProvider("writer-provider", &teamRouteBridgeProvider{name: "writer-provider"}))
	handler.SetLLMRuntime(runtime)
	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Agent.DefaultProvider = "base-provider"
	runtimeConfig.Agent.DefaultModel = "base-model"
	handler.SetRuntimeConfig(runtimeConfig, "")

	enabled := true
	handler.SetAICLIConfig(&agentconfig.Config{
		AICLI: &agentconfig.AICLIConfig{
			Subagents: &agentconfig.AICLISubagentsConfig{
				Routing: &agentconfig.AICLISubagentRoutingConfig{
					Enabled:           &enabled,
					DefaultDifficulty: "normal",
					Levels: map[string]agentconfig.AICLISubagentRouteProfile{
						"hard": {Provider: "level-provider", Model: "level-model"},
					},
					Roles: map[string]map[string]agentconfig.AICLISubagentRouteProfile{
						"writer": {
							"hard": {
								Provider:        "writer-provider",
								Model:           "writer-model",
								ReasoningEffort: "high",
							},
						},
					},
				},
			},
		},
	})

	lead, err := sessionManager.Create(ctx, "team-lead")
	require.NoError(t, err)
	resolution, err := (&apiTeamTaskRouteResolver{handler: handler}).ResolveTaskRoute(ctx, team.TaskRouteRequest{
		Team:     team.Team{ID: "team-1", LeadSessionID: lead.ID},
		Teammate: team.Teammate{ID: "mate-1", SessionID: "mate-session"},
		Task: team.Task{
			ID:                  "task-1",
			Title:               "Implement routing",
			Difficulty:          "hard",
			DifficultyRationale: "provider-sensitive change",
			WritePaths:          []string{"backend/internal/api/skills"},
		},
		Attempt:   2,
		SessionID: "mate-session",
	})
	require.NoError(t, err)
	require.NotNil(t, resolution)
	require.NotNil(t, resolution.Route)
	assert.False(t, resolution.Disabled)
	assert.False(t, resolution.Strict)
	assert.Equal(t, "hard", resolution.Route.Difficulty)
	assert.Equal(t, "explicit", resolution.Route.DifficultySource)
	assert.Equal(t, "provider-sensitive change", resolution.Route.DifficultyRationale)
	assert.Equal(t, "writer-provider", resolution.Route.Provider)
	assert.Equal(t, "writer-model", resolution.Route.Model)
	assert.Equal(t, "high", resolution.Route.ReasoningEffort)
	assert.Equal(t, "role_override", resolution.Route.Source)
	assert.Equal(t, 2, resolution.Route.Attempt)
	assert.False(t, resolution.Route.ResolvedAt.IsZero())
}

func TestAPITeamTaskRouteResolverPrefersIndependentTeamRouting(t *testing.T) {
	enabled := true
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetAICLIConfig(&agentconfig.Config{AICLI: &agentconfig.AICLIConfig{
		Subagents: &agentconfig.AICLISubagentsConfig{Routing: &agentconfig.AICLISubagentRoutingConfig{
			Enabled: &enabled,
			Levels: map[string]agentconfig.AICLISubagentRouteProfile{
				"hard": {Provider: "child-provider", Model: "child-model"},
			},
		}},
		Teams: &agentconfig.AICLITeamsConfig{Routing: &agentconfig.AICLISubagentRoutingConfig{
			Enabled: &enabled,
			Levels: map[string]agentconfig.AICLISubagentRouteProfile{
				"hard": {Provider: "team-provider", Model: "team-model"},
			},
		}},
	}})

	resolution, err := (&apiTeamTaskRouteResolver{handler: handler}).ResolveTaskRoute(context.Background(), team.TaskRouteRequest{
		Task: team.Task{ID: "task-hard", Difficulty: "hard"},
	})
	require.NoError(t, err)
	require.NotNil(t, resolution)
	require.NotNil(t, resolution.Route)
	assert.Equal(t, "team-provider", resolution.Route.Provider)
	assert.Equal(t, "team-model", resolution.Route.Model)
}

func TestAPITeamTaskRouteResolverDisabledAndStrictModes(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		handler := NewHandler(skill.NewRegistry(nil), nil, nil)
		resolution, err := (&apiTeamTaskRouteResolver{handler: handler}).ResolveTaskRoute(context.Background(), team.TaskRouteRequest{
			Task: team.Task{ID: "task-disabled", Difficulty: "hard"},
		})
		require.NoError(t, err)
		require.NotNil(t, resolution)
		require.NotNil(t, resolution.Route)
		assert.True(t, resolution.Disabled)
		assert.False(t, resolution.Strict)
		assert.Equal(t, "disabled", resolution.Route.Source)
	})

	t.Run("strict invalid difficulty", func(t *testing.T) {
		enabled := true
		handler := NewHandler(skill.NewRegistry(nil), nil, nil)
		handler.SetAICLIConfig(&agentconfig.Config{
			AICLI: &agentconfig.AICLIConfig{
				Subagents: &agentconfig.AICLISubagentsConfig{
					Routing: &agentconfig.AICLISubagentRoutingConfig{
						Enabled:           &enabled,
						CompatibilityMode: "strict",
						DefaultDifficulty: "normal",
					},
				},
			},
		})

		resolution, err := (&apiTeamTaskRouteResolver{handler: handler}).ResolveTaskRoute(context.Background(), team.TaskRouteRequest{
			Task: team.Task{ID: "task-strict", Difficulty: "impossible"},
		})
		require.Error(t, err)
		require.NotNil(t, resolution)
		assert.True(t, resolution.Strict)
		assert.Contains(t, err.Error(), "invalid subagent difficulty")
	})
}

type stubTeamTaskRouteResolver struct{}

func (r *stubTeamTaskRouteResolver) ResolveTaskRoute(ctx context.Context, request team.TaskRouteRequest) (*team.TaskRouteResolution, error) {
	return nil, nil
}

func TestGetTeamOrchestratorInjectsTeamTaskRouteResolver(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{
		DSN: "file:skills-team-route-resolver-injection?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)
	t.Cleanup(func() { handler.getSessionHub().StopAll() })

	orchestrator := handler.getTeamOrchestrator()
	require.NotNil(t, orchestrator)
	require.NotNil(t, orchestrator.Runner)
	require.NotNil(t, orchestrator.Runner.RouteResolver)
	_, ok := orchestrator.Runner.RouteResolver.(*apiTeamTaskRouteResolver)
	assert.True(t, ok)
}

func TestGetTeamOrchestratorPreservesInjectedTeamTaskRouteResolver(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{
		DSN: "file:skills-team-route-resolver-preserve?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	custom := &stubTeamTaskRouteResolver{}
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)
	handler.SetTeamOrchestrator(&team.Orchestrator{
		Runner: &team.TeammateRunner{RouteResolver: custom},
	})
	t.Cleanup(func() { handler.getSessionHub().StopAll() })

	orchestrator := handler.getTeamOrchestrator()
	require.NotNil(t, orchestrator)
	require.NotNil(t, orchestrator.Runner)
	assert.Same(t, custom, orchestrator.Runner.RouteResolver)
}

func TestAPITeamTaskRouteAuditSinkPublishesRouteResolvedEvent(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{
		DSN: "file:skills-team-route-audit-sink?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	teamID, err := store.CreateTeam(ctx, team.Team{ID: "team-route-audit"})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, team.Task{
		ID:     "task-route-audit",
		TeamID: teamID,
		Title:  "Route audit",
		Status: team.TaskStatusRunning,
	})
	require.NoError(t, err)

	events := team.NewTeamEventBus()
	var published []team.TeamEvent
	events.Subscribe(team.TaskRouteResolvedEvent, func(event team.TeamEvent) {
		published = append(published, event)
	})
	sink := newAPITeamTaskRouteAuditSink(store, events)
	require.NotNil(t, sink)
	err = sink.RecordTaskRouteAudit(ctx, team.TaskRouteAudit{
		TeamID:    teamID,
		AgentID:   "mate-1",
		TaskID:    taskID,
		SessionID: "session-1",
		Route: &team.TaskExecutionRoute{
			Difficulty:      team.TaskDifficultyHard,
			Provider:        "remote-strong",
			Model:           "strong-model",
			ReasoningEffort: "high",
			Source:          "difficulty_level",
			Attempt:         1,
		},
	})
	require.NoError(t, err)
	require.Len(t, published, 1)
	assert.Equal(t, team.TaskRouteResolvedEvent, published[0].Type)
	assert.Equal(t, "remote-strong", published[0].Payload["route_provider"])

	records, err := store.ListAgentControlTaskRecords(ctx, agentcontrol.TaskFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		TeamID:   teamID,
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "remote-strong", records[0].RouteProvider)
	assert.Equal(t, "strong-model", records[0].RouteModel)
}

func TestSessionActorClientTriggerTaskAppliesTaskRouteOverride(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{
		DSN: "file:skills-trigger-task-route-override?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.CreateTeam(ctx, team.Team{ID: "team-route"})
	require.NoError(t, err)

	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	session, err := sessionManager.Create(ctx, "team-route-user")
	require.NoError(t, err)

	baseProvider := &teamRouteBridgeProvider{name: "base-provider", content: "base reply"}
	routeProvider := &teamRouteBridgeProvider{name: "route-provider", content: "route reply"}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "base-provider",
		DefaultModel:    "base-model",
		MaxRetries:      0,
	})
	require.NoError(t, runtime.RegisterProvider(baseProvider.Name(), baseProvider))
	require.NoError(t, runtime.RegisterProvider(routeProvider.Name(), routeProvider))

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)
	handler.SetSessionManager(sessionManager)
	handler.SetLLMRuntime(runtime)
	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Agent.MaxMaxSteps = 1
	runtimeConfig.Agent.DefaultProvider = "base-provider"
	runtimeConfig.Agent.DefaultModel = "base-model"
	handler.SetRuntimeConfig(runtimeConfig, "")
	t.Cleanup(func() { handler.getSessionHub().StopAll() })

	client := &sessionActorClient{
		hub:        handler.getSessionHub(),
		store:      store,
		eventStore: handler.getSessionEventStore(),
		eventBus:   handler.getRuntimeEventBus(),
	}
	runMeta := &team.RunMeta{
		PermissionMode: "bypass_permissions",
		Team: &team.TeamRunMeta{
			TeamID:        "team-route",
			AgentID:       "mate-route",
			CurrentTaskID: "task-route",
		},
	}
	result, err := client.TriggerTask(ctx, team.TaskTriggerRequest{
		SessionID:  session.ID,
		TeamID:     "team-route",
		AgentID:    "mate-route",
		TaskID:     "task-route",
		Difficulty: "hard",
		Route: &team.TaskExecutionRoute{
			Difficulty:      "hard",
			Provider:        "route-provider",
			Model:           "route-model",
			ReasoningEffort: "high",
			Source:          "difficulty_level",
		},
		Prompt:  "use the task route",
		RunMeta: runMeta,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "route reply", result.Output)
	assert.Equal(t, "bypass_permissions", runMeta.PermissionMode)

	require.Len(t, routeProvider.requests, 1)
	assert.Equal(t, "route-provider", routeProvider.requests[0].Provider)
	assert.Equal(t, "route-model", routeProvider.requests[0].Model)
	assert.Equal(t, "high", routeProvider.requests[0].ReasoningEffort)
	assert.Empty(t, baseProvider.requests)
}
