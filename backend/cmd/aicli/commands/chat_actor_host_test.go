package commands

import (
	"context"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestLocalChatRuntimeHost_MirrorsTeamSummaryIntoBaseSession(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	provider := &autoStartLocalOrchestrationProvider{teammateDelay: 50 * time.Millisecond}
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	session := &ChatSession{
		ProviderName:     "test-provider",
		PermissionMode:   runtimepolicy.ModeDefault,
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
		NoInteractive:    true,
		RequestTimeout:   2 * time.Second,
	}
	host.BaseSession = session

	_, err = session.ChatExecutor.Execute(context.Background(), session, "Create an auto-start team and let the planner finish the task")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := host.waitForTeamTerminal(waitCtx, "team-auto"); err != nil {
		t.Fatalf("waitForTeamTerminal: %v", err)
	}

	reloaded, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("manager.Get: %v", err)
	}
	var foundSummary bool
	for _, message := range reloaded.History {
		if message.Role == "assistant" && strings.Contains(message.Content, "auto lead summary") {
			foundSummary = true
			break
		}
	}
	if !foundSummary {
		t.Fatalf("expected mirrored team summary in base session history, got %+v", reloaded.History)
	}
}

func TestBuildLocalChatAgent_PropagatesReasoningEffortToAgentOptions(t *testing.T) {
	session := &ChatSession{
		ReasoningEffort: "medium",
		Stream:          true,
	}
	host := &localChatRuntimeHost{
		Bootstrap: &runtimebootstrap.Manager{},
	}

	apiAgent := buildLocalChatAgent(session, host, nil, "", "", "")
	if apiAgent == nil {
		t.Fatal("expected agent")
	}

	cfg := apiAgent.GetConfig()
	if cfg == nil || cfg.Options == nil {
		t.Fatal("expected agent options")
	}
	if got := cfg.Options["reasoning_effort"]; got != "medium" {
		t.Fatalf("expected reasoning_effort=medium, got %#v", got)
	}
}

func TestLocalActorRegistry_EnforcesAgentLimitsAndListsChildren(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.RuntimeConfig.Agents.MaxThreads = 1
	host.RuntimeConfig.Agents.MaxDepth = 1
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	registry := host.ActorRegistry

	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "child-1"}); err != nil {
		t.Fatalf("first spawn failed: %v", err)
	}
	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "child-2"}); err == nil || !strings.Contains(err.Error(), "thread limit") {
		t.Fatalf("expected max thread error, got %v", err)
	}
	if _, err := registry.Close(context.Background(), "child-1"); err != nil {
		t.Fatalf("close child-1: %v", err)
	}
	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "child-2"}); err != nil {
		t.Fatalf("spawn after close failed: %v", err)
	}
	if _, err := registry.Spawn(context.Background(), "child-2", toolbroker.SpawnAgentArgs{ID: "grandchild-1"}); err == nil || !strings.Contains(err.Error(), "depth limit") {
		t.Fatalf("expected max depth error, got %v", err)
	}

	list, err := registry.List(context.Background(), rootSession.ID, toolbroker.ListAgentsArgs{})
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	if list.Count != 1 || list.Agents[0].SessionID != "child-2" || list.Agents[0].Path != "/root/child-2" || list.Agents[0].Depth != 1 {
		t.Fatalf("unexpected active list: %#v", list)
	}
	list, err = registry.List(context.Background(), rootSession.ID, toolbroker.ListAgentsArgs{IncludeClosed: true})
	if err != nil {
		t.Fatalf("list closed agents: %v", err)
	}
	if list.Count != 2 {
		t.Fatalf("expected active and closed child agents, got %#v", list)
	}
}

func TestLocalActorRegistry_ListIncludesTeamTeammateSessions(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	teamID, err := teamStore.CreateTeam(context.Background(), team.Team{
		ID:            "team-alpha",
		LeadSessionID: rootSession.ID,
		Status:        team.TeamStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := teamStore.UpsertTeammate(context.Background(), team.Teammate{
		ID:        "member-1",
		TeamID:    teamID,
		Name:      "Documentation Reviewer",
		Profile:   "documentation-reviewer",
		SessionID: "mate-session",
		State:     team.TeammateStateIdle,
	}); err != nil {
		t.Fatalf("UpsertTeammate: %v", err)
	}

	mateSession := runtimechat.NewSession(userID)
	mateSession.ID = "mate-session"
	if err := manager.GetStorage().Save(context.Background(), mateSession); err != nil {
		t.Fatalf("Save mate session: %v", err)
	}

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	registry := host.ActorRegistry

	list, err := registry.List(context.Background(), rootSession.ID, toolbroker.ListAgentsArgs{})
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	if list.Count != 1 {
		t.Fatalf("expected team teammate in agent list, got %#v", list)
	}
	agent := list.Agents[0]
	if agent.SessionID != "mate-session" || agent.ParentSessionID != rootSession.ID {
		t.Fatalf("unexpected teammate identity: %#v", agent)
	}
	if agent.Path != "/root/teams/team-alpha/member-1" || agent.Depth != 1 || agent.AgentType != "documentation-reviewer" {
		t.Fatalf("unexpected teammate metadata: %#v", agent)
	}

	filtered, err := registry.List(context.Background(), rootSession.ID, toolbroker.ListAgentsArgs{PathPrefix: "/root/teams/team-alpha"})
	if err != nil {
		t.Fatalf("list agents with path prefix: %v", err)
	}
	if filtered.Count != 1 || filtered.Agents[0].SessionID != "mate-session" {
		t.Fatalf("expected path-prefix filtered teammate, got %#v", filtered)
	}

	reloaded, err := manager.Get(context.Background(), "mate-session")
	if err != nil {
		t.Fatalf("reload mate session: %v", err)
	}
	if parent, ok := reloaded.GetContext(toolbroker.AgentSessionContextParentSessionID); !ok || parent != rootSession.ID {
		t.Fatalf("expected persisted teammate parent context, got %#v", reloaded.Metadata.Context)
	}
}

func TestLocalActorRegistry_UsesConfiguredDefaultForkTurns(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	rootSession.AddMessage(*runtimetypes.NewUserMessage("first parent turn"))
	rootSession.AddMessage(*runtimetypes.NewAssistantMessage("last parent turn"))
	if err := manager.Update(context.Background(), rootSession); err != nil {
		t.Fatalf("manager.Update: %v", err)
	}

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.RuntimeConfig.Agents.DefaultForkTurns = "1"
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	registry := host.ActorRegistry

	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "fork-default-child"}); err != nil {
		t.Fatalf("spawn with default fork_turns failed: %v", err)
	}
	child, err := manager.Get(context.Background(), "fork-default-child")
	if err != nil {
		t.Fatalf("load forked child: %v", err)
	}
	messages := child.GetMessages()
	if len(messages) != 1 || messages[0].Content != "last parent turn" {
		t.Fatalf("expected default fork_turns=1 to copy last parent turn, got %#v", messages)
	}

	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "fork-none-child", ForkTurns: "none"}); err != nil {
		t.Fatalf("spawn with explicit fork_turns=none failed: %v", err)
	}
	child, err = manager.Get(context.Background(), "fork-none-child")
	if err != nil {
		t.Fatalf("load non-forked child: %v", err)
	}
	if messages := child.GetMessages(); len(messages) != 0 {
		t.Fatalf("expected explicit fork_turns=none to override default, got %#v", messages)
	}
}

func TestBuildLocalChatAgent_DisablesWorkspaceContextByDefaultForActorChat(t *testing.T) {
	session := &ChatSession{}
	host := &localChatRuntimeHost{
		Bootstrap: &runtimebootstrap.Manager{},
	}

	apiAgent := buildLocalChatAgent(session, host, nil, t.TempDir(), "", "")
	if apiAgent == nil {
		t.Fatal("expected agent")
	}

	cfg := apiAgent.GetConfig()
	if cfg == nil {
		t.Fatal("expected agent config")
	}
	if cfg.Options != nil {
		if got := cfg.Options["workspace_path"]; got != nil {
			t.Fatalf("expected workspace_path to be disabled by default, got %#v", got)
		}
		if got := cfg.Options["context_workspace_mode"]; got != nil {
			t.Fatalf("expected context_workspace_mode to be disabled by default, got %#v", got)
		}
	}
}

func TestBuildLocalChatAgent_UsesSignalsWorkspaceContextWhenWorkspaceEnabled(t *testing.T) {
	session := &ChatSession{}
	host := &localChatRuntimeHost{
		Bootstrap: &runtimebootstrap.Manager{},
	}
	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Workspace.Enabled = true

	apiAgent := buildLocalChatAgent(session, host, runtimeConfig, t.TempDir(), "", "")
	if apiAgent == nil {
		t.Fatal("expected agent")
	}

	cfg := apiAgent.GetConfig()
	if cfg == nil || cfg.Options == nil {
		t.Fatal("expected agent options")
	}
	if got := cfg.Options["workspace_path"]; got == nil {
		t.Fatal("expected workspace_path when workspace is enabled")
	}
	if got := cfg.Options["context_workspace_mode"]; got != contextmgr.WorkspaceModeSignals {
		t.Fatalf("expected context_workspace_mode=signals, got %#v", got)
	}
	if got := cfg.Options["context_min_workspace_query_length"]; got != 4 {
		t.Fatalf("expected context_min_workspace_query_length=4, got %#v", got)
	}
}

func TestBuildLocalChatAgent_UsesConfiguredWorkspaceModeForActorChat(t *testing.T) {
	session := &ChatSession{}
	host := &localChatRuntimeHost{
		Bootstrap: &runtimebootstrap.Manager{},
	}
	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Context.WorkspaceMode = contextmgr.WorkspaceModeBroad

	apiAgent := buildLocalChatAgent(session, host, runtimeConfig, t.TempDir(), "", "")
	if apiAgent == nil {
		t.Fatal("expected agent")
	}

	cfg := apiAgent.GetConfig()
	if cfg == nil || cfg.Options == nil {
		t.Fatal("expected agent options")
	}
	if got := cfg.Options["workspace_path"]; got == nil {
		t.Fatal("expected workspace_path when workspace mode is configured")
	}
	if got := cfg.Options["context_workspace_mode"]; got != contextmgr.WorkspaceModeBroad {
		t.Fatalf("expected context_workspace_mode=broad, got %#v", got)
	}
}

func TestBuildLocalChatAgent_PropagatesRuntimeWorkspaceOptions(t *testing.T) {
	session := &ChatSession{}
	host := &localChatRuntimeHost{
		Bootstrap: &runtimebootstrap.Manager{},
	}
	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Workspace.Include = []string{"*.go", "*.ts"}
	runtimeConfig.Workspace.Exclude = []string{"node_modules", "vendor", ".git"}
	runtimeConfig.Workspace.MaxFileSize = 1234
	runtimeConfig.Workspace.MaxChunkSize = 321
	runtimeConfig.Workspace.ChunkOverlap = 12

	apiAgent := buildLocalChatAgent(session, host, runtimeConfig, t.TempDir(), "", "")
	if apiAgent == nil {
		t.Fatal("expected agent")
	}

	cfg := apiAgent.GetConfig()
	if cfg == nil || cfg.Options == nil {
		t.Fatal("expected agent options")
	}
	if got := cfg.Options["workspace_max_file_size"]; got != int64(1234) {
		t.Fatalf("expected workspace_max_file_size=1234, got %#v", got)
	}
	if got := cfg.Options["workspace_max_chunk_size"]; got != 321 {
		t.Fatalf("expected workspace_max_chunk_size=321, got %#v", got)
	}
	if got := cfg.Options["workspace_chunk_overlap"]; got != 12 {
		t.Fatalf("expected workspace_chunk_overlap=12, got %#v", got)
	}
	if got := cfg.Options["workspace_include"]; !reflect.DeepEqual(got, []string{"*.go", "*.ts"}) {
		t.Fatalf("expected workspace_include to be propagated, got %#v", got)
	}
	if got := cfg.Options["workspace_exclude"]; !reflect.DeepEqual(got, []string{"node_modules", "vendor", ".git"}) {
		t.Fatalf("expected workspace_exclude to be propagated, got %#v", got)
	}
}

func TestBuildLocalChatAgent_UsesProviderMaxTokensLimitAsDefault(t *testing.T) {
	session := &ChatSession{
		Provider: agentconfig.Provider{
			MaxTokensLimit: 10000,
		},
	}
	host := &localChatRuntimeHost{
		Bootstrap: &runtimebootstrap.Manager{},
	}

	apiAgent := buildLocalChatAgent(session, host, nil, "", "", "")
	if apiAgent == nil {
		t.Fatal("expected agent")
	}

	cfg := apiAgent.GetConfig()
	if cfg == nil {
		t.Fatal("expected agent config")
	}
	if cfg.DefaultMaxTokens != 10000 {
		t.Fatalf("expected DefaultMaxTokens=10000, got %d", cfg.DefaultMaxTokens)
	}
}

func TestBuildLocalChatLoopConfig_PropagatesReasoningEffort(t *testing.T) {
	config := buildLocalChatLoopConfig(nil, &ChatSession{
		ReasoningEffort: "high",
	})
	if config == nil {
		t.Fatal("expected loop config")
	}
	if got := config.ReasoningEffort; got != "high" {
		t.Fatalf("expected loop reasoning_effort=high, got %#v", got)
	}
}

func TestBuildLocalChatLoopConfig_PropagatesParallelToolConfig(t *testing.T) {
	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Agent.EnableParallelTools = true
	runtimeConfig.Agent.MaxParallelToolCalls = 4

	config := buildLocalChatLoopConfig(runtimeConfig, &ChatSession{})
	if config == nil {
		t.Fatal("expected loop config")
	}
	if !config.EnableParallelTools {
		t.Fatal("expected parallel tools to be enabled")
	}
	if config.MaxParallelToolCalls != 4 {
		t.Fatalf("expected MaxParallelToolCalls=4, got %d", config.MaxParallelToolCalls)
	}
}

func TestLocalTeamLifecycleService_SyncLoopsFiltersToBaseLeadSession(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-current",
		LeadSessionID: "current-session",
		Status:        team.TeamStatusActive,
	}); err != nil {
		t.Fatalf("CreateTeam current: %v", err)
	}
	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-old",
		LeadSessionID: "old-session",
		Status:        team.TeamStatusActive,
	}); err != nil {
		t.Fatalf("CreateTeam old: %v", err)
	}

	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = "current-session"
	host := &localChatRuntimeHost{
		TeamStore: store,
		BaseSession: &ChatSession{
			RuntimeSession: runtimeSession,
		},
		Orchestrator: team.NewOrchestrator(store, nil, nil),
	}
	host.Orchestrator.TickInterval = time.Hour
	lifecycle := newLocalTeamLifecycleService(host)
	host.TeamLifecycle = lifecycle
	defer lifecycle.StopLoops()

	lifecycle.SyncLoops()

	if !lifecycle.hasTeamLoop("team-current") {
		t.Fatal("expected current lead team loop to start")
	}
	if lifecycle.hasTeamLoop("team-old") {
		t.Fatal("expected old lead team loop to stay stopped")
	}
}

func TestLocalChatRuntimeHost_DispatchTeamLifecycleEventUsesTeamLeadSession(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-old",
		LeadSessionID: "old-session",
		Status:        team.TeamStatusActive,
	}); err != nil {
		t.Fatalf("CreateTeam old: %v", err)
	}
	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-current",
		LeadSessionID: "current-session",
		Status:        team.TeamStatusActive,
	}); err != nil {
		t.Fatalf("CreateTeam current: %v", err)
	}

	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = "current-session"
	eventStore := runtimechat.NewInMemoryRuntimeStore(16)
	lifecycle := &recordingTeamLifecycleService{}
	host := &localChatRuntimeHost{
		EventBus:      runtimeevents.NewBusWithRetention(16),
		EventStore:    eventStore,
		TeamStore:     store,
		TeamLifecycle: lifecycle,
		BaseSession: &ChatSession{
			RuntimeSession: runtimeSession,
		},
	}

	host.dispatchTeamLifecycleEvent(team.TeamEvent{
		Type:   "task.failed",
		TeamID: "team-old",
		Payload: map[string]interface{}{
			"task_id": "task-old",
			"summary": "stale event",
		},
	}, true)

	oldEvents, err := eventStore.ListEvents(context.Background(), "old-session", 0, 10)
	if err != nil {
		t.Fatalf("ListEvents old: %v", err)
	}
	if len(oldEvents) != 1 || oldEvents[0].SessionID != "old-session" {
		t.Fatalf("expected stale event persisted to old lead session, got %+v", oldEvents)
	}
	currentEvents, err := eventStore.ListEvents(context.Background(), "current-session", 0, 10)
	if err != nil {
		t.Fatalf("ListEvents current: %v", err)
	}
	if len(currentEvents) != 0 {
		t.Fatalf("expected no stale event in current session, got %+v", currentEvents)
	}
	if len(lifecycle.applied) != 0 {
		t.Fatalf("expected foreign team event not to apply to current lifecycle, got %+v", lifecycle.applied)
	}
	if recent := host.EventBus.Recent(10); len(recent) != 0 {
		t.Fatalf("expected foreign team event not to publish to current bus, got %+v", recent)
	}

	host.dispatchTeamLifecycleEvent(team.TeamEvent{
		Type:   "task.completed",
		TeamID: "team-current",
		Payload: map[string]interface{}{
			"task_id": "task-current",
			"summary": "done",
		},
	}, true)

	currentEvents, err = eventStore.ListEvents(context.Background(), "current-session", 0, 10)
	if err != nil {
		t.Fatalf("ListEvents current after dispatch: %v", err)
	}
	if len(currentEvents) != 1 || currentEvents[0].SessionID != "current-session" {
		t.Fatalf("expected current team event in current session, got %+v", currentEvents)
	}
	if len(lifecycle.applied) != 1 || lifecycle.applied[0].Type != "task.completed" {
		t.Fatalf("expected current team event applied once, got %+v", lifecycle.applied)
	}
}

func TestLocalChatRuntimeHost_TeamCompletedClosesNonLeadTeammateSessions(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	const (
		teamID               = "team-cleanup"
		leadSessionID        = "lead-session"
		startedMateSessionID = "mate-started-session"
		idleMateSessionID    = "mate-idle-session"
	)
	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            teamID,
		LeadSessionID: leadSessionID,
		Status:        team.TeamStatusDone,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := store.UpsertTeammate(context.Background(), team.Teammate{
		ID:        "mate-started",
		TeamID:    teamID,
		SessionID: startedMateSessionID,
		State:     team.TeammateStateBusy,
	}); err != nil {
		t.Fatalf("UpsertTeammate started: %v", err)
	}
	if _, err := store.UpsertTeammate(context.Background(), team.Teammate{
		ID:        "mate-idle",
		TeamID:    teamID,
		SessionID: idleMateSessionID,
		State:     team.TeammateStateIdle,
	}); err != nil {
		t.Fatalf("UpsertTeammate idle: %v", err)
	}

	sessionStore := runtimechat.NewInMemoryStorage()
	for _, sessionID := range []string{leadSessionID, startedMateSessionID, idleMateSessionID} {
		session := runtimechat.NewSession("tester")
		session.ID = sessionID
		if err := sessionStore.Save(context.Background(), session); err != nil {
			t.Fatalf("sessionStore.Save(%s): %v", sessionID, err)
		}
	}

	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(16),
		SessionStore: sessionStore,
		TeamStore:    store,
	}
	host.SessionHub = buildCleanupTestSessionHub(t, host, sessionStore)
	host.ActorRegistry = newLocalActorRegistry(host)

	leadActor, err := host.SessionHub.GetOrCreate(leadSessionID)
	if err != nil {
		t.Fatalf("GetOrCreate lead: %v", err)
	}
	leadActor.Start()
	startedMateActor, err := host.SessionHub.GetOrCreate(startedMateSessionID)
	if err != nil {
		t.Fatalf("GetOrCreate started mate: %v", err)
	}
	startedMateActor.Start()
	if _, err := host.SessionHub.GetOrCreate(idleMateSessionID); err != nil {
		t.Fatalf("GetOrCreate idle mate: %v", err)
	}

	host.publishTeamLifecycleEvent(team.TeamEvent{
		Type:   "team.completed",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"status": string(team.TeamStatusDone),
		},
	})
	host.publishTeamLifecycleEvent(team.TeamEvent{
		Type:   "team.completed",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"status": string(team.TeamStatusDone),
		},
	})

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, startedExists := host.SessionHub.Get(startedMateSessionID)
		_, idleExists := host.SessionHub.Get(idleMateSessionID)
		startedSession, startedErr := sessionStore.Load(context.Background(), startedMateSessionID)
		idleSession, idleErr := sessionStore.Load(context.Background(), idleMateSessionID)
		if !startedExists && !idleExists &&
			startedErr == nil && startedSession != nil && startedSession.State == runtimechat.StateClosed &&
			idleErr == nil && idleSession != nil && idleSession.State == runtimechat.StateClosed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for teammate cleanup: startedExists=%v idleExists=%v startedState=%v idleState=%v startedErr=%v idleErr=%v",
				startedExists, idleExists,
				sessionStateString(startedSession), sessionStateString(idleSession),
				startedErr, idleErr,
			)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, ok := host.SessionHub.Get(leadSessionID); !ok {
		t.Fatal("expected lead actor to remain active in session hub")
	}
	leadSession, err := sessionStore.Load(context.Background(), leadSessionID)
	if err != nil {
		t.Fatalf("sessionStore.Load(lead): %v", err)
	}
	if leadSession.State == runtimechat.StateClosed {
		t.Fatalf("expected lead session to remain open, got state %s", leadSession.State)
	}

	teammates, err := store.ListTeammates(context.Background(), teamID)
	if err != nil {
		t.Fatalf("ListTeammates: %v", err)
	}
	if len(teammates) != 2 {
		t.Fatalf("expected two teammates, got %+v", teammates)
	}
	states := map[string]team.TeammateState{}
	for _, teammate := range teammates {
		states[teammate.ID] = teammate.State
	}
	if states["mate-started"] != team.TeammateStateBusy {
		t.Fatalf("expected started teammate state to remain busy, got %+v", teammates)
	}
	if states["mate-idle"] != team.TeammateStateIdle {
		t.Fatalf("expected idle teammate state to remain idle, got %+v", teammates)
	}
}

func TestLocalTeamLifecycleService_DelaysTeammateCleanupUntilRuntimeIdle(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	const (
		teamID        = "team-cleanup-waits"
		leadSessionID = "lead-session"
		mateSessionID = "mate-session"
	)
	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            teamID,
		LeadSessionID: leadSessionID,
		Status:        team.TeamStatusDone,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := store.UpsertTeammate(context.Background(), team.Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: mateSessionID,
		State:     team.TeammateStateIdle,
	}); err != nil {
		t.Fatalf("UpsertTeammate: %v", err)
	}

	sessionStore := runtimechat.NewInMemoryStorage()
	for _, sessionID := range []string{leadSessionID, mateSessionID} {
		session := runtimechat.NewSession("tester")
		session.ID = sessionID
		if err := sessionStore.Save(context.Background(), session); err != nil {
			t.Fatalf("sessionStore.Save(%s): %v", sessionID, err)
		}
	}

	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	if err := runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: mateSessionID,
		Status:    runtimechat.SessionRunning,
	}); err != nil {
		t.Fatalf("SaveState running: %v", err)
	}

	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(16),
		RuntimeStore: runtimeStore,
		SessionStore: sessionStore,
		TeamStore:    store,
	}
	host.SessionHub = buildCleanupTestSessionHub(t, host, sessionStore)
	host.ActorRegistry = newLocalActorRegistry(host)
	lifecycle := newLocalTeamLifecycleService(host)
	host.TeamLifecycle = lifecycle

	if _, err := host.SessionHub.GetOrCreate(mateSessionID); err != nil {
		t.Fatalf("GetOrCreate mate: %v", err)
	}

	lifecycle.closeTerminalTeammatesAsync(teamID)
	time.Sleep(250 * time.Millisecond)

	if _, exists := host.SessionHub.Get(mateSessionID); !exists {
		t.Fatal("expected running teammate actor to stay open while runtime state is running")
	}
	mateSession, err := sessionStore.Load(context.Background(), mateSessionID)
	if err != nil {
		t.Fatalf("sessionStore.Load mate: %v", err)
	}
	if mateSession.State == runtimechat.StateClosed {
		t.Fatalf("expected running teammate session to stay open, got %s", mateSession.State)
	}

	if err := runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: mateSessionID,
		Status:    runtimechat.SessionIdle,
	}); err != nil {
		t.Fatalf("SaveState idle: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, exists := host.SessionHub.Get(mateSessionID)
		closedSession, loadErr := sessionStore.Load(context.Background(), mateSessionID)
		if !exists && loadErr == nil && closedSession != nil && closedSession.State == runtimechat.StateClosed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for cleanup after idle: exists=%v session=%+v err=%v", exists, closedSession, loadErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLocalChatRuntimeHost_ReplayedTerminalEventClosesNonLeadTeammateSessions(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	const (
		teamID        = "team-replay-cleanup"
		leadSessionID = "lead-session"
		mateSessionID = "mate-session"
	)
	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            teamID,
		LeadSessionID: leadSessionID,
		Status:        team.TeamStatusDone,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := store.UpsertTeammate(context.Background(), team.Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: mateSessionID,
		State:     team.TeammateStateIdle,
	}); err != nil {
		t.Fatalf("UpsertTeammate: %v", err)
	}
	if _, err := store.AppendTeamEvent(context.Background(), team.TeamEvent{
		Type:   "team.completed",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"status": string(team.TeamStatusDone),
		},
	}); err != nil {
		t.Fatalf("AppendTeamEvent: %v", err)
	}

	sessionStore := runtimechat.NewInMemoryStorage()
	for _, sessionID := range []string{leadSessionID, mateSessionID} {
		session := runtimechat.NewSession("tester")
		session.ID = sessionID
		if err := sessionStore.Save(context.Background(), session); err != nil {
			t.Fatalf("sessionStore.Save(%s): %v", sessionID, err)
		}
	}

	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(16),
		SessionStore: sessionStore,
		TeamStore:    store,
	}
	host.SessionHub = buildCleanupTestSessionHub(t, host, sessionStore)
	host.ActorRegistry = newLocalActorRegistry(host)

	if _, err := host.SessionHub.GetOrCreate(leadSessionID); err != nil {
		t.Fatalf("GetOrCreate lead: %v", err)
	}
	if _, err := host.SessionHub.GetOrCreate(mateSessionID); err != nil {
		t.Fatalf("GetOrCreate mate: %v", err)
	}

	host.replayStoredTerminalTeamLifecycleEvents(teamID)

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, mateExists := host.SessionHub.Get(mateSessionID)
		mateSession, mateErr := sessionStore.Load(context.Background(), mateSessionID)
		if !mateExists && mateErr == nil && mateSession != nil && mateSession.State == runtimechat.StateClosed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for replayed teammate cleanup: mateExists=%v mateState=%v mateErr=%v",
				mateExists, sessionStateString(mateSession), mateErr,
			)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, ok := host.SessionHub.Get(leadSessionID); !ok {
		t.Fatal("expected lead actor to remain active after replayed cleanup")
	}
	leadSession, err := sessionStore.Load(context.Background(), leadSessionID)
	if err != nil {
		t.Fatalf("sessionStore.Load(lead): %v", err)
	}
	if leadSession.State == runtimechat.StateClosed {
		t.Fatalf("expected lead session to remain open, got state %s", leadSession.State)
	}
}

func TestLocalChatRuntimeHost_DelegatesToConfiguredTeamLifecycleService(t *testing.T) {
	lifecycle := &recordingTeamLifecycleService{runSettledResult: true}
	host := &localChatRuntimeHost{
		EventBus:      runtimeevents.NewBusWithRetention(8),
		TeamLifecycle: lifecycle,
		SessionStore:  runtimechat.NewInMemoryStorage(),
	}

	host.publishTeamLifecycleEvent(team.TeamEvent{
		Type:   "team.completed",
		TeamID: "team-1",
		Payload: map[string]interface{}{
			"status": string(team.TeamStatusDone),
		},
	})
	host.replayStoredTerminalTeamLifecycleEvents("team-2")
	if err := host.waitForTeamTerminal(context.Background(), "team-3"); err != nil {
		t.Fatalf("waitForTeamTerminal: %v", err)
	}
	settled, err := host.teamRunSettled(context.Background(), "team-4")
	if err != nil {
		t.Fatalf("teamRunSettled: %v", err)
	}
	if !settled {
		t.Fatal("expected delegated runSettled result")
	}
	host.syncTeamLifecycleLoops()
	host.stopTeamLifecycleLoops()

	if len(lifecycle.applied) != 1 {
		t.Fatalf("expected one delegated runtime event, got %+v", lifecycle.applied)
	}
	if lifecycle.applied[0].Type != "team.completed" {
		t.Fatalf("unexpected delegated event: %+v", lifecycle.applied[0])
	}
	if got := payloadStringValue(lifecycle.applied[0].Payload["team_id"]); got != "team-1" {
		t.Fatalf("expected delegated team id, got %q", got)
	}
	if len(lifecycle.replayedTeamIDs) != 1 || lifecycle.replayedTeamIDs[0] != "team-2" {
		t.Fatalf("expected replay delegation for team-2, got %+v", lifecycle.replayedTeamIDs)
	}
	if len(lifecycle.waitedTeamIDs) != 1 || lifecycle.waitedTeamIDs[0] != "team-3" {
		t.Fatalf("expected wait delegation for team-3, got %+v", lifecycle.waitedTeamIDs)
	}
	if len(lifecycle.settledTeamIDs) != 1 || lifecycle.settledTeamIDs[0] != "team-4" {
		t.Fatalf("expected runSettled delegation for team-4, got %+v", lifecycle.settledTeamIDs)
	}
	if lifecycle.syncCalls != 1 {
		t.Fatalf("expected sync delegation once, got %d", lifecycle.syncCalls)
	}
	if lifecycle.stopCalls != 1 {
		t.Fatalf("expected stop delegation once, got %d", lifecycle.stopCalls)
	}
}

func buildCleanupTestSessionHub(t *testing.T, host *localChatRuntimeHost, sessionStore runtimechat.SessionStorage) *runtimechat.SessionHub {
	t.Helper()

	provider := runtimellm.NewMockProvider("mock", 0)
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
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
		apiAgent := agent.NewAgentWithLLM(&agent.Config{
			Name:     "cleanup-test",
			Provider: "mock",
			Model:    "mock-model",
			MaxSteps: 1,
		}, nil, llmRuntime)
		apiAgent.SetToolExecutionPolicy(runtimepolicy.NewToolExecutionPolicy([]string{}, false))
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        apiAgent,
			LLMRuntime:   llmRuntime,
			SessionStore: sessionStore,
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})
}

func sessionStateString(session *runtimechat.Session) string {
	if session == nil {
		return "<nil>"
	}
	return string(session.State)
}

type recordingTeamLifecycleService struct {
	applied          []runtimeevents.Event
	replayedTeamIDs  []string
	waitedTeamIDs    []string
	settledTeamIDs   []string
	pendingTeamIDs   []string
	syncCalls        int
	stopCalls        int
	runSettledResult bool
	runSettledErr    error
	waitErr          error
	pendingResult    bool
}

func (c *recordingTeamLifecycleService) Apply(event runtimeevents.Event) {
	c.applied = append(c.applied, event)
}

func (c *recordingTeamLifecycleService) PublishStoredTerminalEvents(teamID string) {
	c.replayedTeamIDs = append(c.replayedTeamIDs, teamID)
}

func (c *recordingTeamLifecycleService) WaitForTerminal(ctx context.Context, teamID string) error {
	c.waitedTeamIDs = append(c.waitedTeamIDs, teamID)
	return c.waitErr
}

func (c *recordingTeamLifecycleService) RunSettled(ctx context.Context, teamID string) (bool, error) {
	c.settledTeamIDs = append(c.settledTeamIDs, teamID)
	return c.runSettledResult, c.runSettledErr
}

func (c *recordingTeamLifecycleService) Pending(ctx context.Context, teamID string) bool {
	c.pendingTeamIDs = append(c.pendingTeamIDs, teamID)
	return c.pendingResult
}

func (c *recordingTeamLifecycleService) SyncLoops() {
	c.syncCalls++
}

func (c *recordingTeamLifecycleService) StopLoops() {
	c.stopCalls++
}

func TestFindGitRoot(t *testing.T) {
	// The repo itself has a .git at the root.
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// testFile is backend/cmd/aicli/commands/chat_actor_host_test.go
	// Walk up to the git root (E:\projects\ai\ai-agent-runtime)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", "..", "..", ".."))

	got := findGitRoot(filepath.Dir(testFile))
	if filepath.Clean(got) != repoRoot {
		t.Fatalf("findGitRoot from test dir = %q, want %q", got, repoRoot)
	}

	got = findGitRoot(filepath.Join(t.TempDir(), "a", "b"))
	if got != "" {
		t.Fatalf("findGitRoot in temp dir = %q, want empty", got)
	}
}
