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
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
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

func TestLocalTeamLifecycleService_RunSettledWaitsForDoneSummaryEvent(t *testing.T) {
	ctx := context.Background()
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	teamID, err := teamStore.CreateTeam(ctx, team.Team{ID: "summary-wait-team"})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if err := teamStore.UpdateTeamStatus(ctx, teamID, team.TeamStatusDone); err != nil {
		t.Fatalf("UpdateTeamStatus: %v", err)
	}

	host := &localChatRuntimeHost{
		TeamStore: teamStore,
		Orchestrator: &team.Orchestrator{
			LeadPlanner: &team.LeadPlanner{},
		},
	}
	lifecycle := newLocalTeamLifecycleService(host)

	settled, err := lifecycle.RunSettled(ctx, teamID)
	if err != nil {
		t.Fatalf("RunSettled without summary: %v", err)
	}
	if settled {
		t.Fatal("expected done team with configured lead planner to wait for team.summary event")
	}

	if _, err := teamStore.AppendTeamEvent(ctx, team.TeamEvent{
		Type:   "team.summary",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"summary": "done",
		},
	}); err != nil {
		t.Fatalf("AppendTeamEvent: %v", err)
	}

	settled, err = lifecycle.RunSettled(ctx, teamID)
	if err != nil {
		t.Fatalf("RunSettled with summary: %v", err)
	}
	if !settled {
		t.Fatal("expected done team to settle after team.summary event is persisted")
	}
}

func TestLocalChatRuntimeHost_TeamLifecyclePersistsParentMailbox(t *testing.T) {
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	host := &localChatRuntimeHost{
		EventStore: runtimeStore,
		EventBus:   runtimeevents.NewBusWithRetention(16),
		BaseSession: &ChatSession{
			RuntimeSession: &runtimechat.Session{ID: "root-session"},
		},
	}

	host.dispatchTeamLifecycleEvent(team.TeamEvent{
		Type:   "team.completed",
		TeamID: "team-1",
		Payload: map[string]interface{}{
			"status": "done",
		},
	}, true)

	events, err := runtimeStore.ListEvents(context.Background(), "root-session", 0, 10)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected team completed event and mailbox mirror, got %#v", events)
	}
	if events[0].Type != "team.completed" || events[0].Payload["status"] != "done" {
		t.Fatalf("unexpected lifecycle event: %#v", events[0])
	}
	if events[1].Type != runtimechat.EventMailboxReceived || events[1].Payload["kind"] != team.TeamLifecycleMailboxKind {
		t.Fatalf("unexpected lifecycle mailbox event: %#v", events[1])
	}
	messages, err := runtimeStore.ListMailbox(context.Background(), "root-session", 0, 10)
	if err != nil {
		t.Fatalf("ListMailbox: %v", err)
	}
	if len(messages) != 1 || messages[0].Kind != team.TeamLifecycleMailboxKind || messages[0].Metadata["event_type"] != "team.completed" {
		t.Fatalf("unexpected lifecycle mailbox substrate rows: %#v", messages)
	}
	if messages[0].Metadata["message_type"] != team.TeamLifecycleControlMessageType || messages[0].Metadata["status"] != "done" {
		t.Fatalf("unexpected lifecycle mailbox metadata: %#v", messages[0].Metadata)
	}
	controlMessages, err := runtimeStore.ListAgentControlMailbox(context.Background(), "root-session", 0, 10)
	if err != nil {
		t.Fatalf("ListAgentControlMailbox: %v", err)
	}
	if len(controlMessages) != 1 || controlMessages[0].Kind != team.TeamLifecycleMailboxKind || controlMessages[0].Metadata["message_type"] != team.TeamLifecycleControlMessageType {
		t.Fatalf("unexpected lifecycle agent-control rows: %#v", controlMessages)
	}
}

func TestLocalChatRuntimeHost_TaskLifecyclePersistsTeammateMailbox(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-1",
		LeadSessionID: "root-session",
		Status:        team.TeamStatusActive,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := store.UpsertTeammate(context.Background(), team.Teammate{
		ID:        "mate-1",
		TeamID:    "team-1",
		SessionID: "mate-session",
		State:     team.TeammateStateBusy,
	}); err != nil {
		t.Fatalf("UpsertTeammate: %v", err)
	}

	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	host := &localChatRuntimeHost{
		EventStore: runtimeStore,
		TeamStore:  store,
		BaseSession: &ChatSession{
			RuntimeSession: &runtimechat.Session{ID: "root-session"},
		},
	}

	host.dispatchTeamLifecycleEvent(team.TeamEvent{
		Type:   "task.completed",
		TeamID: "team-1",
		Payload: map[string]interface{}{
			"task_id":  "task-1",
			"assignee": "mate-1",
			"summary":  "done",
		},
	}, true)

	parentMessages, err := runtimeStore.ListMailbox(context.Background(), "root-session", 0, 10)
	if err != nil {
		t.Fatalf("ListMailbox parent: %v", err)
	}
	if len(parentMessages) != 0 {
		t.Fatalf("expected task lifecycle not to mirror into parent mailbox, got %#v", parentMessages)
	}
	messages, err := runtimeStore.ListMailbox(context.Background(), "mate-session", 0, 10)
	if err != nil {
		t.Fatalf("ListMailbox teammate: %v", err)
	}
	if len(messages) != 1 || messages[0].Kind != team.TaskLifecycleMailboxKind || messages[0].Metadata["event_type"] != "task.completed" {
		t.Fatalf("unexpected task lifecycle mailbox row: %#v", messages)
	}
	if messages[0].Metadata["message_type"] != team.TaskLifecycleControlMessageType || messages[0].Metadata["control_action"] != team.TaskLifecycleControlAction {
		t.Fatalf("unexpected task lifecycle metadata: %#v", messages[0].Metadata)
	}
	controlMessages, err := runtimeStore.ListAgentControlMailbox(context.Background(), "mate-session", 0, 10)
	if err != nil {
		t.Fatalf("ListAgentControlMailbox teammate: %v", err)
	}
	if len(controlMessages) != 1 || controlMessages[0].Kind != team.TaskLifecycleMailboxKind || controlMessages[0].Metadata["message_type"] != team.TaskLifecycleControlMessageType {
		t.Fatalf("unexpected task lifecycle agent-control rows: %#v", controlMessages)
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
	assignee := "member-1"
	if _, err := teamStore.CreateTask(context.Background(), team.Task{
		ID:       "task-docs",
		TeamID:   teamID,
		Title:    "Review docs",
		Status:   team.TaskStatusRunning,
		Assignee: &assignee,
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
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
	if agent.TeamID != teamID || agent.TeammateID != "member-1" || agent.CurrentTaskID != "task-docs" || agent.CurrentTaskStatus != string(team.TaskStatusRunning) {
		t.Fatalf("unexpected teammate task projection: %#v", agent)
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

func TestLocalActorRegistry_CloseAgentPathClosesSubtree(t *testing.T) {
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
	host.RuntimeConfig.Agents.MaxDepth = 2
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	registry := host.ActorRegistry

	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "close-parent"}); err != nil {
		t.Fatalf("spawn parent: %v", err)
	}
	if _, err := registry.Spawn(context.Background(), "close-parent", toolbroker.SpawnAgentArgs{ID: "close-child"}); err != nil {
		t.Fatalf("spawn child: %v", err)
	}
	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "close-sibling"}); err != nil {
		t.Fatalf("spawn sibling: %v", err)
	}

	result, err := registry.Close(context.Background(), "/root/close-parent")
	if err != nil {
		t.Fatalf("close path: %v", err)
	}
	if result.ClosedCount != 2 {
		t.Fatalf("expected close subtree to close parent and child, got %#v", result)
	}
	if !reflect.DeepEqual(result.ClosedSessionIDs, []string{"close-parent", "close-child"}) {
		t.Fatalf("unexpected closed sessions: %#v", result.ClosedSessionIDs)
	}

	parent, err := manager.Get(context.Background(), "close-parent")
	if err != nil {
		t.Fatalf("load close-parent: %v", err)
	}
	child, err := manager.Get(context.Background(), "close-child")
	if err != nil {
		t.Fatalf("load close-child: %v", err)
	}
	sibling, err := manager.Get(context.Background(), "close-sibling")
	if err != nil {
		t.Fatalf("load close-sibling: %v", err)
	}
	if parent.State != runtimechat.StateClosed || child.State != runtimechat.StateClosed {
		t.Fatalf("expected parent and child closed, got parent=%s child=%s", parent.State, child.State)
	}
	if sibling.State == runtimechat.StateClosed {
		t.Fatalf("expected sibling to stay open, got %s", sibling.State)
	}

	list, err := registry.List(context.Background(), rootSession.ID, toolbroker.ListAgentsArgs{IncludeClosed: true})
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	if list.Count != 3 {
		t.Fatalf("expected closed subtree plus open sibling in list, got %#v", list)
	}
}

func TestLocalActorRegistry_AgentPathTargetsResolveToSession(t *testing.T) {
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

	provider := runtimellm.NewMockProvider("mock", 0)
	provider.SetResponse("read status", "status ok")
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

	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	registry := host.ActorRegistry

	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "path-target"}); err != nil {
		t.Fatalf("spawn path target: %v", err)
	}
	messageResult, err := registry.SendMessage(context.Background(), rootSession.ID, toolbroker.AgentMessageArgs{
		Target:  "/root/path-target",
		Message: "hello by path",
	})
	if err != nil {
		t.Fatalf("send message by path: %v", err)
	}
	if messageResult.TargetSessionID != "path-target" {
		t.Fatalf("expected path to resolve to session id, got %#v", messageResult)
	}

	waitResult, err := registry.Wait(context.Background(), toolbroker.WaitAgentArgs{ID: "/root/path-target", TimeoutMs: 100})
	if err != nil {
		t.Fatalf("wait by path: %v", err)
	}
	if waitResult.MatchedSessionID != "path-target" {
		t.Fatalf("expected wait path to resolve, got %#v", waitResult)
	}

	if _, err := registry.Resume(context.Background(), "/root/path-target"); err != nil {
		t.Fatalf("resume by path: %v", err)
	}
}

func TestLocalActorRegistry_WaitUsesEventStoreWakeup(t *testing.T) {
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
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	registry := host.ActorRegistry

	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "event-wait-child"}); err != nil {
		t.Fatalf("spawn event wait child: %v", err)
	}
	if err := host.RuntimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "event-wait-child",
		Status:    runtimechat.SessionRunning,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save running state: %v", err)
	}

	resultCh := make(chan *toolbroker.AgentWaitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, waitErr := registry.Wait(context.Background(), toolbroker.WaitAgentArgs{ID: "event-wait-child", TimeoutMs: 2000})
		if waitErr != nil {
			errCh <- waitErr
			return
		}
		resultCh <- result
	}()

	time.Sleep(100 * time.Millisecond)
	if err := host.RuntimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "event-wait-child",
		Status:    runtimechat.SessionIdle,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save idle state: %v", err)
	}
	if _, err := host.EventStore.AppendEvent(context.Background(), runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: "event-wait-child",
		Payload:   map[string]interface{}{"success": true},
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	select {
	case waitErr := <-errCh:
		t.Fatalf("wait failed: %v", waitErr)
	case result := <-resultCh:
		if result == nil || result.MatchedSessionID != "event-wait-child" || result.ReadyCount != 1 {
			t.Fatalf("unexpected wait result: %#v", result)
		}
	case <-time.After(450 * time.Millisecond):
		t.Fatal("wait did not wake from event store append")
	}
}

func TestLocalActorRegistry_ReadEventsUsesEventStoreWakeup(t *testing.T) {
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
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	registry := host.ActorRegistry

	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "event-read-child"}); err != nil {
		t.Fatalf("spawn event read child: %v", err)
	}

	resultCh := make(chan *toolbroker.AgentEventsResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, readErr := registry.ReadEvents(context.Background(), toolbroker.ReadAgentEventsArgs{
			ID:       "/root/event-read-child",
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
	event := runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "event-read-child",
		Payload:   map[string]interface{}{"content": "event read done"},
	}
	if _, err := host.EventStore.AppendEvent(context.Background(), event); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	select {
	case readErr := <-errCh:
		t.Fatalf("read events failed: %v", readErr)
	case result := <-resultCh:
		if result == nil || result.SessionID != "event-read-child" || result.Count != 1 {
			t.Fatalf("unexpected read result: %#v", result)
		}
		if len(result.Events) != 1 || result.Events[0].Type != runtimechat.EventAssistantMessage {
			t.Fatalf("unexpected events: %#v", result.Events)
		}
	case <-time.After(450 * time.Millisecond):
		t.Fatal("read_agent_events did not wake from event store append")
	}
}

func TestLocalActorRegistry_WaitWithoutTargetUsesParentMailbox(t *testing.T) {
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

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.BaseSession = &ChatSession{RuntimeSession: rootSession, SessionUserID: userID}
	registry := host.ActorRegistry

	resultCh := make(chan *toolbroker.AgentWaitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, waitErr := registry.Wait(context.Background(), toolbroker.WaitAgentArgs{
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
	if err := registry.deliverAgentMailboxEvent(context.Background(), rootSession.ID, team.MailMessage{
		FromAgent: "child-1",
		ToAgent:   "parent",
		Kind:      "agent_message",
		Body:      "parent mailbox hello",
	}); err != nil {
		t.Fatalf("deliverAgentMailboxEvent: %v", err)
	}

	select {
	case waitErr := <-errCh:
		t.Fatalf("wait failed: %v", waitErr)
	case result := <-resultCh:
		if result == nil || result.Event == nil || result.Event.Type != runtimechat.EventMailboxReceived {
			t.Fatalf("unexpected mailbox wait result: %#v", result)
		}
		if result.LatestSeq != 1 || result.Event.Seq != 1 || result.Event.Payload["mailbox_seq"] != int64(1) {
			t.Fatalf("expected mailbox substrate sequence 1, got result=%#v payload=%#v", result, result.Event.Payload)
		}
		if result.Event.Payload["body"] != "parent mailbox hello" {
			t.Fatalf("unexpected mailbox event payload: %#v", result.Event.Payload)
		}
	case <-time.After(450 * time.Millisecond):
		t.Fatal("wait_agent did not wake from parent mailbox event")
	}
}

func TestLocalActorRegistry_ReadEventsWithoutTargetUsesParentMailbox(t *testing.T) {
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

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.BaseSession = &ChatSession{RuntimeSession: rootSession, SessionUserID: userID}
	registry := host.ActorRegistry

	resultCh := make(chan *toolbroker.AgentEventsResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, readErr := registry.ReadEvents(context.Background(), toolbroker.ReadAgentEventsArgs{
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
	if _, err := host.EventStore.AppendEvent(context.Background(), runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: rootSession.ID,
		Payload:   map[string]interface{}{"content": "not mailbox"},
	}); err != nil {
		t.Fatalf("AppendEvent non-mailbox: %v", err)
	}
	if err := registry.deliverAgentMailboxEvent(context.Background(), rootSession.ID, team.MailMessage{
		FromAgent: "child-1",
		ToAgent:   "parent",
		Kind:      "agent_message",
		Body:      "parent mailbox event read hello",
	}); err != nil {
		t.Fatalf("deliverAgentMailboxEvent: %v", err)
	}

	select {
	case readErr := <-errCh:
		t.Fatalf("read failed: %v", readErr)
	case result := <-resultCh:
		if result == nil || result.SessionID != rootSession.ID || result.Count != 1 {
			t.Fatalf("unexpected mailbox read result: %#v", result)
		}
		if len(result.Events) != 1 || result.Events[0].Type != runtimechat.EventMailboxReceived {
			t.Fatalf("unexpected mailbox events: %#v", result.Events)
		}
		if result.LatestSeq != 1 || result.Events[0].Seq != 1 || result.Events[0].Payload["mailbox_seq"] != int64(1) {
			t.Fatalf("expected mailbox substrate sequence 1, got result=%#v payload=%#v", result, result.Events[0].Payload)
		}
		if result.Events[0].Payload["body"] != "parent mailbox event read hello" {
			t.Fatalf("unexpected mailbox payload: %#v", result.Events[0].Payload)
		}
	case <-time.After(450 * time.Millisecond):
		t.Fatal("read_agent_events did not wake from parent mailbox event")
	}
}

func TestLocalActorRegistry_ReadEventsWithoutTargetMergesAgentControlMailbox(t *testing.T) {
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

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.BaseSession = &ChatSession{RuntimeSession: rootSession, SessionUserID: userID}
	registry := host.ActorRegistry

	if err := registry.deliverAgentMailboxEvent(context.Background(), rootSession.ID, team.MailMessage{
		FromAgent: "child-1",
		ToAgent:   "parent",
		Kind:      "info",
		Body:      "legacy mailbox",
	}); err != nil {
		t.Fatalf("deliver legacy mailbox: %v", err)
	}
	controlMessage := toolbroker.BuildAgentMailboxMessage("child-2", "parent", "control mailbox", false)
	if err := registry.deliverAgentMailboxEvent(context.Background(), rootSession.ID, controlMessage); err != nil {
		t.Fatalf("deliver control mailbox: %v", err)
	}

	result, err := registry.ReadEvents(context.Background(), toolbroker.ReadAgentEventsArgs{
		SessionID:   rootSession.ID,
		MailboxOnly: true,
		AfterSeq:    0,
		Limit:       20,
	})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if result == nil || result.Count != 2 || result.LatestSeq != 2 {
		t.Fatalf("unexpected merged mailbox result: %#v", result)
	}
	if result.Events[0].Payload["body"] != "legacy mailbox" || result.Events[1].Payload["body"] != "control mailbox" {
		t.Fatalf("unexpected merged event order: %#v", result.Events)
	}
	if result.Events[1].Payload["metadata"] == nil {
		t.Fatalf("expected control mailbox metadata, got %#v", result.Events[1].Payload)
	}
}

func TestLocalActorRegistry_SendMessagePersistsMailboxWithoutTargetActor(t *testing.T) {
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

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.BaseSession = &ChatSession{RuntimeSession: rootSession, SessionUserID: userID}
	registry := host.ActorRegistry
	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "mailbox-child"}); err != nil {
		t.Fatalf("spawn mailbox child: %v", err)
	}
	host.SessionHub.Stop("mailbox-child")

	result, err := registry.SendMessage(context.Background(), rootSession.ID, toolbroker.AgentMessageArgs{
		Target:  "/root/mailbox-child",
		Message: "durable hello",
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if result == nil || result.TargetSessionID != "mailbox-child" || !result.Delivered || result.Triggered {
		t.Fatalf("unexpected send result: %#v", result)
	}
	if _, ok := host.SessionHub.Get("mailbox-child"); ok {
		t.Fatal("send_message should persist mailbox event without starting target actor")
	}

	events, err := host.EventStore.ListEvents(context.Background(), "mailbox-child", 0, 10)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected durable mailbox event, got %#v", events)
	}
	event := events[0]
	if event.Type != runtimechat.EventMailboxReceived || event.Payload["kind"] != "agent_message" || event.Payload["body"] != "durable hello" {
		t.Fatalf("unexpected mailbox event: %#v", event)
	}
	metadata, ok := event.Payload["metadata"].(map[string]interface{})
	if !ok || metadata["target_session_id"] != "mailbox-child" || metadata["trigger_turn"] != false {
		t.Fatalf("unexpected mailbox metadata: %#v", event.Payload)
	}
	if metadata["message_type"] != toolbroker.AgentMailboxMessageType ||
		metadata["control_action"] != toolbroker.AgentMailboxMessageAction ||
		metadata["workflow"] != toolbroker.AgentMailboxWorkflow ||
		metadata["mailbox_kind"] != toolbroker.AgentMailboxMessageKind {
		t.Fatalf("expected agent-control mailbox metadata, got %#v", metadata)
	}
}

func TestHandleCommand_AgentsSendPersistsMailboxMessage(t *testing.T) {
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

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	session := &ChatSession{
		RuntimeSession:   rootSession,
		SessionUserID:    userID,
		LocalRuntimeHost: host,
	}
	host.BaseSession = session
	if _, err := host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "send-child"}); err != nil {
		t.Fatalf("spawn send child: %v", err)
	}
	host.SessionHub.Stop("send-child")

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/agents send /root/send-child please inspect docs", false); quit {
			t.Fatal("agents send command should not quit")
		}
	})
	if !strings.Contains(output, "Agent Message: sent target=send-child mode=delivered") {
		t.Fatalf("unexpected agents send output:\n%s", output)
	}
	messages, err := host.EventStore.(runtimechat.MailboxReaderStore).ListMailbox(context.Background(), "send-child", 0, 10)
	if err != nil {
		t.Fatalf("ListMailbox: %v", err)
	}
	if len(messages) != 1 || messages[0].Kind != "agent_message" || messages[0].Body != "please inspect docs" {
		t.Fatalf("unexpected child mailbox messages: %#v", messages)
	}
	if _, ok := host.SessionHub.Get("send-child"); ok {
		t.Fatal("agents send should persist mailbox without starting stopped target actor")
	}
}

func TestHandleCommand_AgentsTargetProvidesDefaultSendTarget(t *testing.T) {
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

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	session := &ChatSession{
		RuntimeSession:   rootSession,
		SessionManager:   manager,
		SessionUserID:    userID,
		LocalRuntimeHost: host,
	}
	host.BaseSession = session
	if _, err := host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "target-child"}); err != nil {
		t.Fatalf("spawn target child: %v", err)
	}
	host.SessionHub.Stop("target-child")

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/agents target /root/target-child", false); quit {
			t.Fatal("agents target command should not quit")
		}
	})
	if !strings.Contains(output, "Selected Agent Target: /root/target-child") {
		t.Fatalf("unexpected agents target output:\n%s", output)
	}
	if session.SelectedAgentTarget != "/root/target-child" {
		t.Fatalf("expected selected target to be set, got %q", session.SelectedAgentTarget)
	}
	stored, err := manager.Get(context.Background(), rootSession.ID)
	if err != nil {
		t.Fatalf("manager.Get: %v", err)
	}
	if got := runtimeSessionContextString(stored, chatRuntimeContextSelectedAgent); got != "/root/target-child" {
		t.Fatalf("expected selected target context, got %q", got)
	}

	output = captureStdout(t, func() {
		if quit := handleCommand(session, "/agents send inspect selected target", false); quit {
			t.Fatal("agents send command should not quit")
		}
	})
	if !strings.Contains(output, "Agent Message: sent target=target-child mode=delivered") {
		t.Fatalf("unexpected agents send output:\n%s", output)
	}
	messages, err := host.EventStore.(runtimechat.MailboxReaderStore).ListMailbox(context.Background(), "target-child", 0, 10)
	if err != nil {
		t.Fatalf("ListMailbox: %v", err)
	}
	if len(messages) != 1 || messages[0].Body != "inspect selected target" {
		t.Fatalf("unexpected mailbox messages: %#v", messages)
	}
}

func TestLocalActorRegistry_FollowupTaskPersistsMailboxWhenTargetBusy(t *testing.T) {
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

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.BaseSession = &ChatSession{RuntimeSession: rootSession, SessionUserID: userID}
	registry := host.ActorRegistry
	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "busy-followup-child"}); err != nil {
		t.Fatalf("spawn busy followup child: %v", err)
	}
	if _, err := host.SessionHub.GetOrCreate("busy-followup-child"); err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if err := host.RuntimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "busy-followup-child",
		Status:    runtimechat.SessionRunning,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	result, err := registry.FollowupTask(context.Background(), rootSession.ID, toolbroker.AgentMessageArgs{
		Target:  "/root/busy-followup-child",
		Message: "queue while busy",
	})
	if err != nil {
		t.Fatalf("FollowupTask: %v", err)
	}
	if result == nil || result.TargetSessionID != "busy-followup-child" || !result.Delivered || result.Triggered {
		t.Fatalf("unexpected followup result: %#v", result)
	}

	events, err := host.EventStore.ListEvents(context.Background(), "busy-followup-child", 0, 10)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected durable busy followup mailbox event, got %#v", events)
	}
	event := events[0]
	if event.Type != runtimechat.EventMailboxReceived || event.Payload["kind"] != "followup_task" || event.Payload["body"] != "queue while busy" {
		t.Fatalf("unexpected followup mailbox event: %#v", event)
	}
	metadata, ok := event.Payload["metadata"].(map[string]interface{})
	if !ok || metadata["target_session_id"] != "busy-followup-child" || metadata["trigger_turn"] != true {
		t.Fatalf("unexpected followup metadata: %#v", event.Payload)
	}
	if metadata["message_type"] != toolbroker.AgentMailboxFollowupMessageType ||
		metadata["control_action"] != toolbroker.AgentMailboxFollowupAction ||
		metadata["workflow"] != toolbroker.AgentMailboxWorkflow ||
		metadata["mailbox_kind"] != toolbroker.AgentMailboxFollowupKind {
		t.Fatalf("expected agent-control followup metadata, got %#v", metadata)
	}
}

func TestLocalActorRegistry_MirrorsChildCompletionToParentEvents(t *testing.T) {
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
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	registry := host.ActorRegistry

	_, err = registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{
		ID:        "completion-child",
		AgentType: "worker",
	})
	if err != nil {
		t.Fatalf("spawn completion child: %v", err)
	}
	childEnd := runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: "completion-child",
		TraceID:   "trace-child-complete",
		Payload: map[string]interface{}{
			"success": true,
			"steps":   3,
			"seq":     int64(44),
		},
	}
	host.EventBus.Publish(childEnd)

	events, err := host.EventStore.ListEvents(context.Background(), rootSession.ID, 0, 20)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected parent completion event and mailbox event, got %#v", events)
	}
	mailboxEvent := events[0]
	if mailboxEvent.Type != runtimechat.EventMailboxReceived || mailboxEvent.SessionID != rootSession.ID {
		t.Fatalf("unexpected completion mailbox event: %#v", mailboxEvent)
	}
	if mailboxEvent.Payload["kind"] != "subagent.completed" || mailboxEvent.Payload["from_agent"] != "completion-child" || mailboxEvent.Payload["to_agent"] != "parent" {
		t.Fatalf("unexpected completion mailbox payload: %#v", mailboxEvent.Payload)
	}
	metadata, ok := mailboxEvent.Payload["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected completion mailbox metadata, got %#v", mailboxEvent.Payload)
	}
	if metadata["session_id"] != "completion-child" ||
		metadata["path"] != "/root/completion-child" ||
		metadata["agent_type"] != "worker" ||
		metadata["event_seq"] != int64(44) ||
		metadata["message_type"] != agentcontrol.MessageTypeSubagentCompleted ||
		metadata["control_action"] != agentcontrol.ActionAgentCompleted ||
		metadata["workflow"] != agentcontrol.WorkflowSpawnAgent ||
		metadata["mailbox_delivery"] != agentcontrol.DeliverySessionMailbox ||
		metadata["mailbox_kind"] != agentcontrol.MailboxKindSubagentCompleted {
		t.Fatalf("unexpected completion mailbox metadata: %#v", metadata)
	}
	event := events[1]
	if event.Type != "subagent.completed" || event.SessionID != rootSession.ID {
		t.Fatalf("unexpected mirrored event: %#v", event)
	}
	if event.Payload["session_id"] != "completion-child" || event.Payload["path"] != "/root/completion-child" || event.Payload["agent_type"] != "worker" {
		t.Fatalf("unexpected mirrored payload: %#v", event.Payload)
	}
	if event.Payload["status"] != string(runtimechat.SessionIdle) || event.Payload["success"] != true {
		t.Fatalf("unexpected completion status payload: %#v", event.Payload)
	}
	if event.Payload["source_event_seq"] != int64(44) {
		t.Fatalf("expected source event seq on display mirror, got %#v", event.Payload)
	}
	if event.Payload["display_mirror"] != true ||
		event.Payload["mirror_source"] != toolbroker.SubagentCompletionMirrorSource ||
		event.Payload["mailbox_delivery_status"] != "delivered" ||
		event.Payload["message_type"] != agentcontrol.MessageTypeSubagentCompleted ||
		event.Payload["control_action"] != agentcontrol.ActionAgentCompleted {
		t.Fatalf("expected display mirror metadata, got %#v", event.Payload)
	}
}

func TestLocalActorRegistry_PersistsCompletionMailboxWithoutParentActor(t *testing.T) {
	eventStore := runtimechat.NewInMemoryRuntimeStore(16)
	host := &localChatRuntimeHost{
		EventStore: eventStore,
		EventBus:   runtimeevents.NewBusWithRetention(16),
	}
	registry := newLocalActorRegistry(host)

	registry.deliverSubagentCompletionMailbox(context.Background(), "parent-session", "child-session", "/root/child-session", "worker", runtimechat.EventSessionEnd, map[string]interface{}{
		"status":  string(runtimechat.SessionIdle),
		"success": true,
		"seq":     int64(7),
	})

	events, err := eventStore.ListEvents(context.Background(), "parent-session", 0, 10)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected durable mailbox event without parent actor, got %#v", events)
	}
	event := events[0]
	if event.Type != runtimechat.EventMailboxReceived || event.Payload["kind"] != "subagent.completed" {
		t.Fatalf("unexpected mailbox event: %#v", event)
	}
	metadata, ok := event.Payload["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected mailbox metadata, got %#v", event.Payload)
	}
	if metadata["session_id"] != "child-session" ||
		metadata["event_seq"] != int64(7) ||
		metadata["message_type"] != agentcontrol.MessageTypeSubagentCompleted ||
		metadata["control_action"] != agentcontrol.ActionAgentCompleted ||
		metadata["mailbox_kind"] != agentcontrol.MailboxKindSubagentCompleted {
		t.Fatalf("unexpected mailbox metadata: %#v", metadata)
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

func TestLocalTeamLifecycleService_SyncLoopsRepairsActiveTeamWithoutTeammates(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-repair",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusActive,
		MaxTeammates:  2,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	for _, id := range []string{"task-a", "task-b", "task-c"} {
		if _, err := store.CreateTask(context.Background(), team.Task{
			ID:     id,
			TeamID: teamID,
			Title:  id,
			Status: team.TaskStatusPending,
		}); err != nil {
			t.Fatalf("CreateTask %s: %v", id, err)
		}
	}

	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = "lead-session"
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

	teammates, err := store.ListTeammates(context.Background(), teamID)
	if err != nil {
		t.Fatalf("ListTeammates: %v", err)
	}
	if len(teammates) != 2 {
		t.Fatalf("expected repaired teammate records capped by max_teammates, got %+v", teammates)
	}
	if teammates[0].ID != "mate-1" || teammates[0].SessionID != "team-repair__mate_1" {
		t.Fatalf("unexpected first repaired teammate: %+v", teammates[0])
	}
	if teammates[1].ID != "mate-2" || teammates[1].SessionID != "team-repair__mate_2" {
		t.Fatalf("unexpected second repaired teammate: %+v", teammates[1])
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
