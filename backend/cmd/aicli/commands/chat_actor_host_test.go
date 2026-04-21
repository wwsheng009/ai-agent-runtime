package commands

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
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

func TestBuildLocalChatAgent_UsesSignalsWorkspaceContextForActorChat(t *testing.T) {
	session := &ChatSession{}
	host := &localChatRuntimeHost{
		Bootstrap: &runtimebootstrap.Manager{},
	}

	apiAgent := buildLocalChatAgent(session, host, nil, t.TempDir(), "", "")
	if apiAgent == nil {
		t.Fatal("expected agent")
	}

	cfg := apiAgent.GetConfig()
	if cfg == nil || cfg.Options == nil {
		t.Fatal("expected agent options")
	}
	if got := cfg.Options["context_workspace_mode"]; got != "signals" {
		t.Fatalf("expected context_workspace_mode=signals, got %#v", got)
	}
	if got := cfg.Options["context_min_workspace_query_length"]; got != 4 {
		t.Fatalf("expected context_min_workspace_query_length=4, got %#v", got)
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
