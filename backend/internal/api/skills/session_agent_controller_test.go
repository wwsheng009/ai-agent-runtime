package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func TestSessionAgentController_PathTargetsAndCloseSubtree(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Agents.MaxDepth = 2
	handler.SetRuntimeConfig(cfg, "")

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-path")
	require.NoError(t, err)

	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-path-parent"})
	require.NoError(t, err)
	_, err = controller.Spawn(ctx, "api-path-parent", toolbroker.SpawnAgentArgs{ID: "api-path-child"})
	require.NoError(t, err)
	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-path-sibling"})
	require.NoError(t, err)

	messageResult, err := controller.SendMessage(ctx, rootSession.ID, toolbroker.AgentMessageArgs{
		Target:  "/root/api-path-parent/api-path-child",
		Message: "hello by path",
	})
	require.NoError(t, err)
	assert.Equal(t, "api-path-child", messageResult.TargetSessionID)

	waitResult, err := controller.Wait(ctx, toolbroker.WaitAgentArgs{
		ID:        "/root/api-path-parent/api-path-child",
		TimeoutMs: 100,
	})
	require.NoError(t, err)
	assert.Equal(t, "api-path-child", waitResult.MatchedSessionID)

	result, err := controller.Close(ctx, "/root/api-path-parent")
	require.NoError(t, err)
	assert.Equal(t, 2, result.ClosedCount)
	assert.Equal(t, []string{"api-path-parent", "api-path-child"}, result.ClosedSessionIDs)

	parent, err := sessionManager.Get(ctx, "api-path-parent")
	require.NoError(t, err)
	child, err := sessionManager.Get(ctx, "api-path-child")
	require.NoError(t, err)
	sibling, err := sessionManager.Get(ctx, "api-path-sibling")
	require.NoError(t, err)
	assert.Equal(t, chat.StateClosed, parent.State)
	assert.Equal(t, chat.StateClosed, child.State)
	assert.NotEqual(t, chat.StateClosed, sibling.State)

	listResult, err := controller.List(ctx, rootSession.ID, toolbroker.ListAgentsArgs{IncludeClosed: true})
	require.NoError(t, err)
	assert.Equal(t, 3, listResult.Count)
}

func TestSessionAgentControllerSpawnPersistsRouteContext(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	enabled := true
	handler.SetAICLIConfig(&agentconfig.Config{
		AICLI: &agentconfig.AICLIConfig{
			Subagents: &agentconfig.AICLISubagentsConfig{
				Routing: &agentconfig.AICLISubagentRoutingConfig{
					Enabled:           &enabled,
					DefaultDifficulty: "normal",
					Levels: map[string]agentconfig.AICLISubagentRouteProfile{
						"hard": {
							Provider:        "codex",
							Model:           "gpt-5.4",
							ReasoningEffort: "high",
						},
					},
				},
			},
		},
	})

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-route")
	require.NoError(t, err)

	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	result, err := controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{
		ID:                  "api-route-child",
		AgentType:           "worker",
		Difficulty:          "hard",
		DifficultyRationale: "policy-sensitive",
		PermissionMode:      "bypass_permissions",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "codex", result.Provider)
	assert.Equal(t, "gpt-5.4", result.Model)
	assert.Equal(t, "high", result.ReasoningEffort)
	assert.Equal(t, "hard", result.Difficulty)
	assert.Equal(t, "bypass_permissions", result.PermissionMode)
	assert.Equal(t, "explicit", result.DifficultySource)
	assert.Equal(t, "policy-sensitive", result.DifficultyRationale)
	assert.Equal(t, "difficulty_level", result.RouteSource)
	assert.Empty(t, result.RouteWarnings)

	child, err := sessionManager.Get(ctx, "api-route-child")
	require.NoError(t, err)
	assert.Equal(t, "codex", agentcontrol.ContextString(child, sessionmeta.ProviderName))
	assert.Equal(t, "gpt-5.4", agentcontrol.ContextString(child, toolbroker.AgentSessionContextRequestedModel))
	assert.Equal(t, "gpt-5.4", agentcontrol.ContextString(child, sessionmeta.Model))
	assert.Equal(t, "high", agentcontrol.ContextString(child, sessionmeta.ReasoningEffort))
	assert.Equal(t, "hard", agentcontrol.ContextString(child, toolbroker.AgentSessionContextDifficulty))
	assert.Equal(t, "bypass_permissions", agentcontrol.ContextString(child, toolbroker.AgentSessionContextPermissionMode))
}

func TestSessionAgentControllerSpawnIgnoresProviderAndReasoningWhenRoutingDisabled(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-route-disabled")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	result, err := controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{
		ID:              "api-route-disabled-child",
		Provider:        "untrusted-provider",
		Model:           "legacy-child-model",
		ReasoningEffort: "high",
		Difficulty:      "hard",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Empty(t, result.Provider)
	assert.Equal(t, "legacy-child-model", result.Model)
	assert.Empty(t, result.ReasoningEffort)
	assert.Equal(t, "hard", result.Difficulty)
	assert.Equal(t, "explicit", result.DifficultySource)
	assert.Equal(t, "disabled", result.RouteSource)

	child, err := sessionManager.Get(ctx, "api-route-disabled-child")
	require.NoError(t, err)
	assert.Empty(t, agentcontrol.ContextString(child, sessionmeta.ProviderName))
	assert.Equal(t, "legacy-child-model", agentcontrol.ContextString(child, toolbroker.AgentSessionContextRequestedModel))
	assert.Empty(t, agentcontrol.ContextString(child, sessionmeta.ReasoningEffort))
}

func TestSessionAgentController_WritesAndReadsAgentRegistry(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Agents.MaxDepth = 2
	handler.SetRuntimeConfig(cfg, "")
	enabled := true
	handler.SetAICLIConfig(&agentconfig.Config{
		AICLI: &agentconfig.AICLIConfig{
			Subagents: &agentconfig.AICLISubagentsConfig{
				Routing: &agentconfig.AICLISubagentRoutingConfig{
					Enabled:           &enabled,
					DefaultDifficulty: "normal",
					Levels: map[string]agentconfig.AICLISubagentRouteProfile{
						"hard": {
							Provider:        "remote",
							Model:           "strong-model",
							ReasoningEffort: "high",
						},
					},
				},
			},
		},
	})
	store, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: t.TempDir() + "/agents.db",
	})
	require.NoError(t, err)
	defer store.Close()
	handler.SetAgentControlAgentStore(store)

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-registry")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-registry-child", AgentType: "worker", Difficulty: "hard"})
	require.NoError(t, err)
	records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		RootSessionID: rootSession.ID,
		IncludeClosed: true,
	})
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.Equal(t, agentcontrol.AgentTypeRoot, records[0].AgentType)
	assert.Equal(t, "api-registry-child", records[1].SessionID)
	assert.Equal(t, "/root/api-registry-child", records[1].AgentPath)
	assert.Equal(t, agentcontrol.WorkflowSpawnAgent, records[1].Workflow)

	require.NoError(t, sessionManager.GetStorage().Delete(ctx, "api-registry-child"))
	listResult, err := controller.List(ctx, rootSession.ID, toolbroker.ListAgentsArgs{})
	require.NoError(t, err)
	require.Equal(t, 1, listResult.Count)
	agent := listResult.Agents[0]
	assert.Equal(t, "api-registry-child", agent.SessionID)
	assert.Equal(t, "/root/api-registry-child", agent.Path)
	assert.Equal(t, "worker", agent.AgentType)
	assert.Equal(t, "remote", agent.Provider)
	assert.Equal(t, "strong-model", agent.Model)
	assert.Equal(t, "high", agent.ReasoningEffort)
	assert.Equal(t, "hard", agent.Difficulty)
	assert.Equal(t, "difficulty_level", agent.RouteSource)
	assert.False(t, agent.Exists)
	assert.Equal(t, "missing", agent.Status)
}

func TestSessionAgentController_RegistryCloseUsesSegmentSafePath(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Agents.MaxDepth = 2
	handler.SetRuntimeConfig(cfg, "")
	store, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: t.TempDir() + "/agents.db",
	})
	require.NoError(t, err)
	defer store.Close()
	handler.SetAgentControlAgentStore(store)

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-registry-close")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)
	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-close-a"})
	require.NoError(t, err)
	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-close-a2"})
	require.NoError(t, err)
	_, err = controller.Spawn(ctx, "api-close-a", toolbroker.SpawnAgentArgs{ID: "api-close-child"})
	require.NoError(t, err)

	result, err := controller.Close(ctx, "/root/api-close-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"api-close-a", "api-close-child"}, result.ClosedSessionIDs)

	sibling, err := sessionManager.Get(ctx, "api-close-a2")
	require.NoError(t, err)
	assert.NotEqual(t, chat.StateClosed, sibling.State)
	active, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{RootSessionID: rootSession.ID})
	require.NoError(t, err)
	var activeSessions []string
	for _, record := range active {
		if record.SessionID != "" {
			activeSessions = append(activeSessions, record.SessionID)
		}
	}
	assert.Contains(t, activeSessions, "api-close-a2")
	assert.NotContains(t, activeSessions, "api-close-a")
	assert.NotContains(t, activeSessions, "api-close-child")
}

func TestSessionAgentController_RegistrySpawnLimitUsesDurableStore(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Agents.MaxThreads = 1
	handler.SetRuntimeConfig(cfg, "")
	store, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: t.TempDir() + "/agents.db",
	})
	require.NoError(t, err)
	defer store.Close()
	handler.SetAgentControlAgentStore(store)

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-registry-limit")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)
	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-limit-child-1"})
	require.NoError(t, err)
	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-limit-child-2"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "agent spawn thread limit reached")
	_, err = sessionManager.Get(ctx, "api-limit-child-2")
	require.Error(t, err)
}

func TestSessionAgentController_ProjectsTeamTeammatesIntoAgentList(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	require.NoError(t, err)
	defer teamStore.Close()
	handler.teamStore = teamStore

	rootSession, err := sessionManager.Create(ctx, "user-team-agent-projection")
	require.NoError(t, err)
	teamID, err := teamStore.CreateTeam(ctx, team.Team{
		ID:            "team-alpha",
		LeadSessionID: rootSession.ID,
	})
	require.NoError(t, err)
	_, err = teamStore.UpsertTeammate(ctx, team.Teammate{
		ID:        "member-1",
		TeamID:    teamID,
		Name:      "Documentation Reviewer",
		Profile:   "documentation-reviewer",
		SessionID: "api-mate-session",
		State:     team.TeammateStateIdle,
	})
	require.NoError(t, err)
	assignee := "member-1"
	_, err = teamStore.CreateTask(ctx, team.Task{
		ID:       "api-task-docs",
		TeamID:   teamID,
		Title:    "Review docs",
		Status:   team.TaskStatusReady,
		Assignee: &assignee,
	})
	require.NoError(t, err)

	mateSession := chat.NewSession(rootSession.UserID)
	mateSession.ID = "api-mate-session"
	require.NoError(t, sessionManager.GetStorage().Save(ctx, mateSession))

	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)
	listResult, err := controller.List(ctx, rootSession.ID, toolbroker.ListAgentsArgs{PathPrefix: "/root/teams/team-alpha"})
	require.NoError(t, err)
	require.Equal(t, 1, listResult.Count)
	agent := listResult.Agents[0]
	assert.Equal(t, "api-mate-session", agent.SessionID)
	assert.Equal(t, rootSession.ID, agent.ParentSessionID)
	assert.Equal(t, "/root/teams/team-alpha/member-1", agent.Path)
	assert.Equal(t, 1, agent.Depth)
	assert.Equal(t, "documentation-reviewer", agent.AgentType)
	assert.Equal(t, teamID, agent.TeamID)
	assert.Equal(t, "member-1", agent.TeammateID)
	assert.Equal(t, "api-task-docs", agent.CurrentTaskID)
	assert.Equal(t, string(team.TaskStatusReady), agent.CurrentTaskStatus)

	reloaded, err := sessionManager.Get(ctx, "api-mate-session")
	require.NoError(t, err)
	parent, ok := reloaded.GetContext(toolbroker.AgentSessionContextParentSessionID)
	require.True(t, ok)
	assert.Equal(t, rootSession.ID, parent)
	path, ok := reloaded.GetContext(toolbroker.AgentSessionContextPath)
	require.True(t, ok)
	assert.Equal(t, "/root/teams/team-alpha/member-1", path)
}

func TestSessionAgentController_ProjectsTeamTeammatesIntoAgentRegistry(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	store, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: t.TempDir() + "/agents.db",
	})
	require.NoError(t, err)
	defer store.Close()
	handler.SetAgentControlAgentStore(store)

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	require.NoError(t, err)
	defer teamStore.Close()
	handler.teamStore = teamStore

	rootSession, err := sessionManager.Create(ctx, "user-team-agent-registry")
	require.NoError(t, err)
	teamID, err := teamStore.CreateTeam(ctx, team.Team{
		ID:            "team-registry",
		LeadSessionID: rootSession.ID,
	})
	require.NoError(t, err)
	_, err = teamStore.UpsertTeammate(ctx, team.Teammate{
		ID:        "member-registry",
		TeamID:    teamID,
		Name:      "Registry Reviewer",
		Profile:   "documentation-reviewer",
		SessionID: "api-mate-registry-session",
		State:     team.TeammateStateIdle,
	})
	require.NoError(t, err)
	mateSession := chat.NewSession(rootSession.UserID)
	mateSession.ID = "api-mate-registry-session"
	require.NoError(t, sessionManager.GetStorage().Save(ctx, mateSession))

	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)
	listResult, err := controller.List(ctx, rootSession.ID, toolbroker.ListAgentsArgs{PathPrefix: "/root/teams/team-registry"})
	require.NoError(t, err)
	require.Equal(t, 1, listResult.Count)

	records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		RootSessionID: rootSession.ID,
		PathPrefix:    "/root/teams/team-registry",
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	record := records[0]
	assert.Equal(t, "api-mate-registry-session", record.SessionID)
	assert.Equal(t, agentcontrol.WorkflowSpawnTeam, record.Workflow)
	assert.Equal(t, "team-registry", record.TeamID)
	assert.Equal(t, "member-registry", record.TeammateID)
	assert.Equal(t, "/root/teams/team-registry/member-registry", record.AgentPath)
}

func TestSessionAgentController_SendMessagePersistsMailboxWithoutTargetActor(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-mailbox")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-mailbox-child"})
	require.NoError(t, err)
	handler.getSessionHub().Stop("api-mailbox-child")

	result, err := controller.SendMessage(ctx, rootSession.ID, toolbroker.AgentMessageArgs{
		Target:  "/root/api-mailbox-child",
		Message: "durable api hello",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "api-mailbox-child", result.TargetSessionID)
	assert.True(t, result.Delivered)
	assert.False(t, result.Triggered)
	if _, ok := handler.getSessionHub().Get("api-mailbox-child"); ok {
		t.Fatal("send_message should persist mailbox event without starting target actor")
	}

	events, err := handler.getSessionEventStore().ListEvents(ctx, "api-mailbox-child", 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	event := events[0]
	assert.Equal(t, chat.EventMailboxReceived, event.Type)
	assert.Equal(t, "agent_message", event.Payload["kind"])
	assert.Equal(t, "durable api hello", event.Payload["body"])
	metadata, ok := event.Payload["metadata"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "api-mailbox-child", metadata["target_session_id"])
	assert.Equal(t, false, metadata["trigger_turn"])
	assert.Equal(t, toolbroker.AgentMailboxMessageType, metadata["message_type"])
	assert.Equal(t, toolbroker.AgentMailboxMessageAction, metadata["control_action"])
	assert.Equal(t, toolbroker.AgentMailboxWorkflow, metadata["workflow"])
	assert.Equal(t, toolbroker.AgentMailboxMessageKind, metadata["mailbox_kind"])
}

func TestSessionAgentController_FollowupTaskPersistsMailboxWhenTargetBusy(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-busy-followup")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-busy-followup-child"})
	require.NoError(t, err)
	_, err = handler.getSessionHub().GetOrCreate("api-busy-followup-child")
	require.NoError(t, err)
	require.NoError(t, handler.getSessionRuntimeStore().SaveState(ctx, &chat.RuntimeState{
		SessionID: "api-busy-followup-child",
		Status:    chat.SessionRunning,
		UpdatedAt: time.Now().UTC(),
	}))

	result, err := controller.FollowupTask(ctx, rootSession.ID, toolbroker.AgentMessageArgs{
		Target:  "/root/api-busy-followup-child",
		Message: "queue api while busy",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "api-busy-followup-child", result.TargetSessionID)
	assert.True(t, result.Delivered)
	assert.False(t, result.Triggered)

	events, err := handler.getSessionEventStore().ListEvents(ctx, "api-busy-followup-child", 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	event := events[0]
	assert.Equal(t, chat.EventMailboxReceived, event.Type)
	assert.Equal(t, "followup_task", event.Payload["kind"])
	assert.Equal(t, "queue api while busy", event.Payload["body"])
	metadata, ok := event.Payload["metadata"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "api-busy-followup-child", metadata["target_session_id"])
	assert.Equal(t, true, metadata["trigger_turn"])
	assert.Equal(t, toolbroker.AgentMailboxFollowupMessageType, metadata["message_type"])
	assert.Equal(t, toolbroker.AgentMailboxFollowupAction, metadata["control_action"])
	assert.Equal(t, toolbroker.AgentMailboxWorkflow, metadata["workflow"])
	assert.Equal(t, toolbroker.AgentMailboxFollowupKind, metadata["mailbox_kind"])
}

func TestSessionAgentController_WaitUsesRuntimeEventWakeup(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	provider := newSessionAgentBlockingProvider("test-session-agent-event-model")
	defer provider.releaseCall()
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: provider.Name(),
		MaxRetries:   0,
	})
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))
	handler.SetLLMRuntime(runtime)

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-event")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-event-child"})
	require.NoError(t, err)
	_, err = controller.SendInput(ctx, toolbroker.SendAgentInputArgs{
		ID:      "/root/api-event-child",
		Message: "finish after release",
	})
	require.NoError(t, err)

	select {
	case <-provider.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("child agent did not enter provider call")
	}

	resultCh := make(chan *toolbroker.AgentWaitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, waitErr := controller.Wait(context.Background(), toolbroker.WaitAgentArgs{
			ID:        "/root/api-event-child",
			TimeoutMs: 2000,
		})
		if waitErr != nil {
			errCh <- waitErr
			return
		}
		resultCh <- result
	}()

	time.Sleep(100 * time.Millisecond)
	provider.releaseCall()

	select {
	case waitErr := <-errCh:
		t.Fatalf("wait failed: %v", waitErr)
	case result := <-resultCh:
		require.NotNil(t, result)
		assert.Equal(t, "api-event-child", result.MatchedSessionID)
		assert.Equal(t, 1, result.ReadyCount)
	case <-time.After(450 * time.Millisecond):
		t.Fatal("wait did not wake from runtime event")
	}
}

func TestSessionAgentController_WaitUsesEventStoreWakeup(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-event-store-wait")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-event-store-wait-child"})
	require.NoError(t, err)
	require.NoError(t, handler.getSessionRuntimeStore().SaveState(ctx, &chat.RuntimeState{
		SessionID: "api-event-store-wait-child",
		Status:    chat.SessionRunning,
		UpdatedAt: time.Now().UTC(),
	}))

	resultCh := make(chan *toolbroker.AgentWaitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, waitErr := controller.Wait(context.Background(), toolbroker.WaitAgentArgs{
			ID:        "/root/api-event-store-wait-child",
			TimeoutMs: 2000,
		})
		if waitErr != nil {
			errCh <- waitErr
			return
		}
		resultCh <- result
	}()

	time.Sleep(100 * time.Millisecond)
	require.NoError(t, handler.getSessionRuntimeStore().SaveState(ctx, &chat.RuntimeState{
		SessionID: "api-event-store-wait-child",
		Status:    chat.SessionIdle,
		UpdatedAt: time.Now().UTC(),
	}))
	_, err = handler.getSessionEventStore().AppendEvent(ctx, runtimeevents.Event{
		Type:      chat.EventSessionEnd,
		SessionID: "api-event-store-wait-child",
		Payload:   map[string]interface{}{"success": true},
	})
	require.NoError(t, err)

	select {
	case readErr := <-errCh:
		t.Fatalf("wait failed: %v", readErr)
	case result := <-resultCh:
		require.NotNil(t, result)
		assert.Equal(t, "api-event-store-wait-child", result.MatchedSessionID)
		assert.Equal(t, 1, result.ReadyCount)
	case <-time.After(450 * time.Millisecond):
		t.Fatal("wait_agent did not wake from event store append")
	}
}

func TestSessionAgentController_ReadEventsUsesEventStoreWakeup(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-read-event")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-read-event-child"})
	require.NoError(t, err)

	resultCh := make(chan *toolbroker.AgentEventsResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, readErr := controller.ReadEvents(context.Background(), toolbroker.ReadAgentEventsArgs{
			ID:       "/root/api-read-event-child",
			AfterSeq: 0,
			Limit:    20,
			WaitMs:   2000,
		})
		if readErr != nil {
			errCh <- readErr
			return
		}
		resultCh <- result
	}()

	time.Sleep(100 * time.Millisecond)
	store := handler.getSessionEventStore()
	require.NotNil(t, store)
	event := runtimeevents.Event{
		Type:      chat.EventAssistantMessage,
		SessionID: "api-read-event-child",
		Payload:   map[string]interface{}{"content": "event read done"},
	}
	_, err = store.AppendEvent(ctx, event)
	require.NoError(t, err)

	select {
	case readErr := <-errCh:
		t.Fatalf("read events failed: %v", readErr)
	case result := <-resultCh:
		require.NotNil(t, result)
		assert.Equal(t, "api-read-event-child", result.SessionID)
		assert.Equal(t, 1, result.Count)
		if assert.Len(t, result.Events, 1) {
			assert.Equal(t, chat.EventAssistantMessage, result.Events[0].Type)
		}
	case <-time.After(450 * time.Millisecond):
		t.Fatal("read_agent_events did not wake from event store append")
	}
}

func TestSessionAgentController_WaitWithoutTargetUsesParentMailbox(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-parent-mailbox")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	resultCh := make(chan *toolbroker.AgentWaitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, waitErr := controller.Wait(context.Background(), toolbroker.WaitAgentArgs{
			SessionID:   rootSession.ID,
			MailboxOnly: true,
			TimeoutMs:   2000,
		})
		if waitErr != nil {
			errCh <- waitErr
			return
		}
		resultCh <- result
	}()

	time.Sleep(100 * time.Millisecond)
	err = controller.deliverAgentMailboxEvent(ctx, rootSession.ID, team.MailMessage{
		FromAgent: "child-1",
		ToAgent:   "parent",
		Kind:      "agent_message",
		Body:      "api parent mailbox hello",
	})
	require.NoError(t, err)

	select {
	case waitErr := <-errCh:
		t.Fatalf("wait failed: %v", waitErr)
	case result := <-resultCh:
		require.NotNil(t, result)
		require.NotNil(t, result.Event)
		assert.Equal(t, chat.EventMailboxReceived, result.Event.Type)
		assert.Equal(t, int64(1), result.LatestSeq)
		assert.Equal(t, int64(1), result.Event.Seq)
		assert.Equal(t, int64(1), result.Event.Payload["mailbox_seq"])
		assert.Equal(t, "api parent mailbox hello", result.Event.Payload["body"])
	case <-time.After(450 * time.Millisecond):
		t.Fatal("wait_agent did not wake from parent mailbox event")
	}
}

func TestSessionAgentController_ReadEventsWithoutTargetUsesParentMailbox(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-parent-events")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	resultCh := make(chan *toolbroker.AgentEventsResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, readErr := controller.ReadEvents(context.Background(), toolbroker.ReadAgentEventsArgs{
			SessionID:   rootSession.ID,
			MailboxOnly: true,
			AfterSeq:    0,
			Limit:       20,
			WaitMs:      2000,
		})
		if readErr != nil {
			errCh <- readErr
			return
		}
		resultCh <- result
	}()

	time.Sleep(100 * time.Millisecond)
	store := handler.getSessionEventStore()
	require.NotNil(t, store)
	_, err = store.AppendEvent(ctx, runtimeevents.Event{
		Type:      chat.EventAssistantMessage,
		SessionID: rootSession.ID,
		Payload:   map[string]interface{}{"content": "not mailbox"},
	})
	require.NoError(t, err)
	err = controller.deliverAgentMailboxEvent(ctx, rootSession.ID, team.MailMessage{
		FromAgent: "child-1",
		ToAgent:   "parent",
		Kind:      "agent_message",
		Body:      "api parent mailbox event read hello",
	})
	require.NoError(t, err)

	select {
	case readErr := <-errCh:
		t.Fatalf("read failed: %v", readErr)
	case result := <-resultCh:
		require.NotNil(t, result)
		assert.Equal(t, rootSession.ID, result.SessionID)
		assert.Equal(t, 1, result.Count)
		assert.Equal(t, int64(1), result.LatestSeq)
		if assert.Len(t, result.Events, 1) {
			assert.Equal(t, chat.EventMailboxReceived, result.Events[0].Type)
			assert.Equal(t, int64(1), result.Events[0].Seq)
			assert.Equal(t, int64(1), result.Events[0].Payload["mailbox_seq"])
			assert.Equal(t, "api parent mailbox event read hello", result.Events[0].Payload["body"])
		}
	case <-time.After(450 * time.Millisecond):
		t.Fatal("read_agent_events did not wake from parent mailbox event")
	}
}

func TestSessionAgentController_ReadEventsWithoutTargetMergesAgentControlMailbox(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-parent-merged-events")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	err = controller.deliverAgentMailboxEvent(ctx, rootSession.ID, team.MailMessage{
		FromAgent: "child-1",
		ToAgent:   "parent",
		Kind:      "info",
		Body:      "api legacy mailbox",
	})
	require.NoError(t, err)
	err = controller.deliverAgentMailboxEvent(ctx, rootSession.ID, toolbroker.BuildAgentMailboxMessage("child-2", "parent", "api control mailbox", false))
	require.NoError(t, err)

	result, err := controller.ReadEvents(ctx, toolbroker.ReadAgentEventsArgs{
		SessionID:   rootSession.ID,
		MailboxOnly: true,
		AfterSeq:    0,
		Limit:       20,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, rootSession.ID, result.SessionID)
	assert.Equal(t, 2, result.Count)
	assert.Equal(t, int64(2), result.LatestSeq)
	if assert.Len(t, result.Events, 2) {
		assert.Equal(t, "api legacy mailbox", result.Events[0].Payload["body"])
		assert.Equal(t, "api control mailbox", result.Events[1].Payload["body"])
		assert.NotNil(t, result.Events[1].Payload["metadata"])
	}
}

func TestSessionAgentController_MirrorsChildCompletionToParentEvents(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	hookPayloads := make(chan map[string]interface{}, 2)
	hookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode hook payload: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		hookPayloads <- payload
		_, _ = w.Write([]byte(`{"action":"continue"}`))
	}))
	defer hookServer.Close()
	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Hooks = []runtimehooks.HookConfig{
		{
			ID:    "api-spawn-agent-start",
			Event: runtimehooks.EventSubagentStart,
			Exec:  runtimehooks.ExecConfig{Type: "http", URL: hookServer.URL},
		},
		{
			ID:    "api-spawn-agent-stop",
			Event: runtimehooks.EventSubagentStop,
			Exec:  runtimehooks.ExecConfig{Type: "http", URL: hookServer.URL},
		},
	}
	handler.SetRuntimeConfig(runtimeConfig, "")
	enabled := true
	handler.SetAICLIConfig(&agentconfig.Config{
		AICLI: &agentconfig.AICLIConfig{
			Subagents: &agentconfig.AICLISubagentsConfig{
				Routing: &agentconfig.AICLISubagentRoutingConfig{
					Enabled:                        &enabled,
					AllowExplicitProviderOverride:  true,
					AllowExplicitModelOverride:     true,
					AllowExplicitReasoningOverride: true,
				},
			},
		},
	})

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-completion")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{
		ID:                  "api-completion-child",
		AgentType:           "worker",
		Provider:            "codex",
		Model:               "gpt-5.4",
		ReasoningEffort:     "high",
		Difficulty:          "hard",
		DifficultySource:    "explicit",
		DifficultyRationale: "completion audit",
		RouteSource:         "explicit_override",
	})
	require.NoError(t, err)
	startHook := waitAPIHookPayload(t, hookPayloads)
	assert.Equal(t, "api-completion-child", startHook["session_id"])
	assert.Equal(t, rootSession.ID, startHook["parent_session_id"])
	assert.Equal(t, "/root/api-completion-child", startHook["path"])
	assert.Equal(t, "worker", startHook["agent_type"])
	assert.Equal(t, "hard", startHook["difficulty"])
	assert.Equal(t, "codex", startHook["route_provider"])
	assert.Equal(t, "gpt-5.4", startHook["route_model"])
	assert.Equal(t, "high", startHook["route_reasoning_effort"])
	assert.Equal(t, "explicit_override", startHook["route_source"])

	childEnd := runtimeevents.Event{
		Type:      chat.EventSessionEnd,
		SessionID: "api-completion-child",
		TraceID:   "trace-api-child-complete",
		Payload: map[string]interface{}{
			"success":            true,
			"steps":              5,
			"seq":                int64(55),
			"usage_total_tokens": 1200,
		},
	}
	handler.getRuntimeEventBus().Publish(childEnd)
	stopHook := waitAPIHookPayload(t, hookPayloads)
	assert.Equal(t, "api-completion-child", stopHook["session_id"])
	assert.Equal(t, chat.EventSessionEnd, stopHook["source_event_type"])
	assert.Equal(t, string(chat.SessionIdle), stopHook["status"])
	assert.Equal(t, true, stopHook["success"])
	assert.Equal(t, "worker", stopHook["agent_type"])
	assert.Equal(t, "hard", stopHook["difficulty"])
	assert.Equal(t, "gpt-5.4", stopHook["route_model"])
	assert.Equal(t, float64(5), stopHook["steps"])
	assert.Equal(t, float64(1200), stopHook["usage_total_tokens"])

	store := handler.getSessionEventStore()
	require.NotNil(t, store)
	events, err := store.ListEvents(ctx, rootSession.ID, 0, 20)
	require.NoError(t, err)
	require.Len(t, events, 2)
	mailboxEvent := events[0]
	assert.Equal(t, chat.EventMailboxReceived, mailboxEvent.Type)
	assert.Equal(t, rootSession.ID, mailboxEvent.SessionID)
	assert.Equal(t, "subagent.completed", mailboxEvent.Payload["kind"])
	assert.Equal(t, "api-completion-child", mailboxEvent.Payload["from_agent"])
	assert.Equal(t, "parent", mailboxEvent.Payload["to_agent"])
	metadata, ok := mailboxEvent.Payload["metadata"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "api-completion-child", metadata["session_id"])
	assert.Equal(t, "/root/api-completion-child", metadata["path"])
	assert.Equal(t, "worker", metadata["agent_type"])
	assert.Equal(t, int64(55), metadata["event_seq"])
	assert.Equal(t, agentcontrol.MessageTypeSubagentCompleted, metadata["message_type"])
	assert.Equal(t, agentcontrol.ActionAgentCompleted, metadata["control_action"])
	assert.Equal(t, agentcontrol.WorkflowSpawnAgent, metadata["workflow"])
	assert.Equal(t, agentcontrol.DeliverySessionMailbox, metadata["mailbox_delivery"])
	assert.Equal(t, agentcontrol.MailboxKindSubagentCompleted, metadata["mailbox_kind"])
	assert.Equal(t, "hard", metadata["difficulty"])
	assert.Equal(t, "explicit", metadata["difficulty_source"])
	assert.Equal(t, "completion audit", metadata["difficulty_rationale"])
	assert.Equal(t, "codex", metadata["route_provider"])
	assert.Equal(t, "gpt-5.4", metadata["route_model"])
	assert.Equal(t, "high", metadata["route_reasoning_effort"])
	assert.Equal(t, "explicit_override", metadata["route_source"])
	assert.Equal(t, 1200, metadata["usage_total_tokens"])
	event := events[1]
	assert.Equal(t, "subagent.completed", event.Type)
	assert.Equal(t, rootSession.ID, event.SessionID)
	assert.Equal(t, "api-completion-child", event.Payload["session_id"])
	assert.Equal(t, "/root/api-completion-child", event.Payload["path"])
	assert.Equal(t, "worker", event.Payload["agent_type"])
	assert.Equal(t, string(chat.SessionIdle), event.Payload["status"])
	assert.Equal(t, true, event.Payload["success"])
	assert.Equal(t, 5, event.Payload["steps"])
	assert.Equal(t, int64(55), event.Payload["source_event_seq"])
	assert.Equal(t, true, event.Payload["display_mirror"])
	assert.Equal(t, toolbroker.SubagentCompletionMirrorSource, event.Payload["mirror_source"])
	assert.Equal(t, "delivered", event.Payload["mailbox_delivery_status"])
	assert.Equal(t, agentcontrol.MessageTypeSubagentCompleted, event.Payload["message_type"])
	assert.Equal(t, agentcontrol.ActionAgentCompleted, event.Payload["control_action"])
	assert.Equal(t, "hard", event.Payload["difficulty"])
	assert.Equal(t, "explicit", event.Payload["difficulty_source"])
	assert.Equal(t, "completion audit", event.Payload["difficulty_rationale"])
	assert.Equal(t, "codex", event.Payload["route_provider"])
	assert.Equal(t, "gpt-5.4", event.Payload["route_model"])
	assert.Equal(t, "high", event.Payload["route_reasoning_effort"])
	assert.Equal(t, "explicit_override", event.Payload["route_source"])
	assert.Equal(t, 1200, event.Payload["usage_total_tokens"])
}

func waitAPIHookPayload(t *testing.T, payloads <-chan map[string]interface{}) map[string]interface{} {
	t.Helper()
	select {
	case payload := <-payloads:
		return payload
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for API hook payload")
		return nil
	}
}

func TestSessionAgentController_PersistsCompletionMailboxWithoutParentActor(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(chat.NewSessionManager(chat.NewInMemoryStorage(), nil))
	defer handler.getSessionHub().StopAll()

	controller := &sessionAgentController{handler: handler}
	controller.deliverSubagentCompletionMailbox(ctx, "parent-session", "api-child-session", "/root/api-child-session", "worker", chat.EventSessionEnd, map[string]interface{}{
		"status":  string(chat.SessionIdle),
		"success": true,
		"seq":     int64(11),
	})

	store := handler.getSessionEventStore()
	require.NotNil(t, store)
	events, err := store.ListEvents(ctx, "parent-session", 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	event := events[0]
	assert.Equal(t, chat.EventMailboxReceived, event.Type)
	assert.Equal(t, "subagent.completed", event.Payload["kind"])
	assert.Equal(t, "api-child-session", event.Payload["from_agent"])
	metadata, ok := event.Payload["metadata"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "api-child-session", metadata["session_id"])
	assert.Equal(t, int64(11), metadata["event_seq"])
	assert.Equal(t, agentcontrol.MessageTypeSubagentCompleted, metadata["message_type"])
	assert.Equal(t, agentcontrol.ActionAgentCompleted, metadata["control_action"])
	assert.Equal(t, agentcontrol.MailboxKindSubagentCompleted, metadata["mailbox_kind"])
}

type sessionAgentBlockingProvider struct {
	name        string
	entered     chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
}

func newSessionAgentBlockingProvider(name string) *sessionAgentBlockingProvider {
	return &sessionAgentBlockingProvider{
		name:    name,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (p *sessionAgentBlockingProvider) Name() string {
	return p.name
}

func (p *sessionAgentBlockingProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.enterOnce.Do(func() {
		close(p.entered)
	})
	select {
	case <-p.release:
		return &llm.LLMResponse{
			Content: "event done",
			Model:   p.name,
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *sessionAgentBlockingProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	return nil, nil
}

func (p *sessionAgentBlockingProvider) CountTokens(text string) int {
	return len(text) / 4
}

func (p *sessionAgentBlockingProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsJSONMode:  true,
	}
}

func (p *sessionAgentBlockingProvider) CheckHealth(ctx context.Context) error {
	return nil
}

func (p *sessionAgentBlockingProvider) releaseCall() {
	p.releaseOnce.Do(func() {
		close(p.release)
	})
}

var _ llm.Provider = (*sessionAgentBlockingProvider)(nil)
