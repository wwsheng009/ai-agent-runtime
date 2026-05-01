package commands

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestPrepareChatPersistence_UsesProvidedSessionDir(t *testing.T) {
	dir := t.TempDir()
	state, err := prepareChatPersistence(&chatCommandOptions{
		SessionDirFlag:           dir,
		SessionFeaturesRequested: true,
	})
	if err != nil {
		t.Fatalf("prepareChatPersistence: %v", err)
	}
	if state == nil || state.runtimeSessionManager == nil {
		t.Fatal("expected runtime session manager")
	}
	if state.resolvedSessionDir != dir {
		t.Fatalf("expected session dir %q, got %q", dir, state.resolvedSessionDir)
	}
	if state.sessionUserID == "" {
		t.Fatal("expected resolved session user id")
	}
	if state.loadedRuntimeSession != nil {
		t.Fatalf("expected no loaded runtime session, got %+v", state.loadedRuntimeSession)
	}
}

func TestBuildChatSession_NoInteractive(t *testing.T) {
	cfg := &config.Config{}
	opts := &chatCommandOptions{
		NoInteractive: true,
		OutputFormat:  "json",
		JSONEnvelope:  true,
		DisableTools:  true,
		Message:       "hello",
	}
	runtimeState := &chatRuntimeState{
		providerName:    "codex_ee",
		provider:        config.Provider{Enabled: true, Protocol: "codex", BaseURL: "https://example.com"},
		adapter:         &adapter.CodexAdapter{},
		modelName:       "gpt-5.2-code",
		reasoningEffort: "medium",
		shouldStream:    false,
		baseURL:         "https://example.com/v1/responses",
		retryCfg:        defaultRetryConfig(),
		requestTimeout:  30 * time.Second,
	}

	session, cleanup, err := buildChatSession(cfg, opts, nil, &chatPersistenceState{sessionUserID: "tester", resolvedSessionDir: t.TempDir()}, runtimeState)
	if err != nil {
		t.Fatalf("buildChatSession: %v", err)
	}
	if cleanup == nil {
		t.Fatal("expected cleanup function")
	}
	defer cleanup()

	if session.ProviderName != "codex_ee" || session.Model != "gpt-5.2-code" {
		t.Fatalf("unexpected session identity: %+v", session)
	}
	if !session.NoInteractive || !session.JSONOutput || !session.JSONEnvelope {
		t.Fatalf("unexpected session output flags: %+v", session)
	}
	if session.KeyHandler != nil || session.Layout != nil || session.InputBox != nil {
		t.Fatalf("expected no interactive UI artifacts, got %+v", session)
	}
	if session.Logger == nil || session.FunctionCatalog == nil || session.FunctionRegistry == nil {
		t.Fatalf("expected logger and function catalog to be initialized: %+v", session)
	}
}

func TestBuildChatFinalCleanup_ClearsScreenAndStopsPromptRedraw(t *testing.T) {
	cleanupCalls := 0

	output := captureStdout(t, func() {
		session := &ChatSession{}
		session.Layout = ui.NewLayout(ui.LayoutSimple)
		session.Interaction = newChatInteractionCoordinator(session)
		session.Interaction.promptDelay = 10 * time.Millisecond
		session.Interaction.SchedulePromptRedraw()

		finalCleanup := buildChatFinalCleanup(session, func() {
			cleanupCalls++
		})

		finalCleanup()
		time.Sleep(40 * time.Millisecond)
		finalCleanup()
	})

	if cleanupCalls != 1 {
		t.Fatalf("expected cleanup session to run once, got %d", cleanupCalls)
	}
	if !strings.Contains(output, "\x1b[2J") {
		t.Fatalf("expected final cleanup to clear the screen, got %q", output)
	}
	if strings.Contains(output, ui.UserPromptText(0)) {
		t.Fatalf("expected prompt redraw to be suppressed after shutdown, got %q", output)
	}
}

func TestShouldInitializeChatInteractiveUI_DisabledForJSONAndLegacyMode(t *testing.T) {
	if shouldInitializeChatInteractiveUI(&chatCommandOptions{OutputFormat: "json"}) {
		t.Fatal("expected JSON output to disable interactive UI")
	}

	t.Setenv("AICLI_TUI", "legacy")
	if shouldInitializeChatInteractiveUI(&chatCommandOptions{OutputFormat: "interactive"}) {
		t.Fatal("expected AICLI_TUI=legacy to disable interactive UI")
	}
}

func TestRestoreChatPersistenceState_LoadedSession(t *testing.T) {
	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.AddMessage(*runtimetypes.NewUserMessage("hello"))
	if runtimeSession.Metadata.Context == nil {
		runtimeSession.Metadata.Context = make(map[string]interface{})
	}
	runtimeSession.Metadata.Context[chatRuntimeContextTokenCount] = 987

	session := &ChatSession{}
	err := restoreChatPersistenceState(session, &chatPersistenceState{
		loadedRuntimeSession: runtimeSession,
	}, &chatCommandOptions{
		SessionTitleFlag: "restored title",
	})
	if err != nil {
		t.Fatalf("restoreChatPersistenceState: %v", err)
	}
	if session.RuntimeSession == nil {
		t.Fatal("expected runtime session to be restored")
	}
	if len(session.Messages) != 2 {
		t.Fatalf("expected restored messages, got %d", len(session.Messages))
	}
	if session.Messages[0].Role != "system" {
		t.Fatalf("expected restored system prompt to be prepended, got %#v", session.Messages[0])
	}
	if got := session.Messages[0].Content; got != composeChatSystemPromptWithGuidance(session) {
		t.Fatalf("expected restored system prompt to include runtime guidance, got %#v", got)
	}
	if session.RuntimeSession.Metadata.Title != "restored title" {
		t.Fatalf("expected updated session title, got %q", session.RuntimeSession.Metadata.Title)
	}
	if session.TokenCount != 987 {
		t.Fatalf("expected restored token count 987, got %d", session.TokenCount)
	}
}

func TestSyncAndRestoreChatTokenCountRoundTrip(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	session := &ChatSession{
		ProviderName:   "codex_ee",
		Provider:       config.Provider{Protocol: "codex"},
		Model:          "gpt-5.2-code",
		SessionManager: manager,
		RuntimeSession: runtimeSession,
		TokenCount:     1234,
	}

	if err := syncRuntimeSessionFromChat(session); err != nil {
		t.Fatalf("syncRuntimeSessionFromChat: %v", err)
	}

	cloned, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("manager.Get: %v", err)
	}
	if got, ok := runtimeSessionContextInt(cloned, chatRuntimeContextTokenCount); !ok || got != 1234 {
		t.Fatalf("expected persisted token count 1234, got ok=%v value=%d", ok, got)
	}

	restored := &ChatSession{}
	if err := restoreChatStateFromRuntimeSession(restored, cloned); err != nil {
		t.Fatalf("restoreChatStateFromRuntimeSession: %v", err)
	}
	if restored.TokenCount != 1234 {
		t.Fatalf("expected restored token count 1234, got %d", restored.TokenCount)
	}
}

func TestBootstrapChatSession_CreatesRuntimeConversation(t *testing.T) {
	cfg := &config.Config{}
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	session, cleanup, err := bootstrapChatSession(cfg, &chatCommandOptions{
		NoInteractive:            true,
		OutputFormat:             "json",
		DisableTools:             true,
		SessionTitleFlag:         "new conversation",
		SessionFeaturesRequested: true,
	}, nil, &chatPersistenceState{
		runtimeSessionManager: manager,
		sessionUserID:         userID,
		resolvedSessionDir:    dir,
	}, &chatRuntimeState{
		providerName:    "codex_ee",
		provider:        config.Provider{Enabled: true, Protocol: "codex", BaseURL: "https://example.com"},
		adapter:         &adapter.CodexAdapter{},
		modelName:       "gpt-5.2-code",
		reasoningEffort: "medium",
		shouldStream:    false,
		baseURL:         "https://example.com/v1/responses",
		retryCfg:        defaultRetryConfig(),
		requestTimeout:  15 * time.Second,
	})
	if err != nil {
		t.Fatalf("bootstrapChatSession: %v", err)
	}
	if cleanup == nil {
		t.Fatal("expected cleanup function")
	}
	defer cleanup()

	if session.RuntimeSession == nil {
		t.Fatal("expected runtime session to be created")
	}
	if session.RuntimeSession.Metadata.Title != "new conversation" {
		t.Fatalf("expected session title to be persisted, got %q", session.RuntimeSession.Metadata.Title)
	}
	if session.SessionManager == nil || session.SessionUserID != userID {
		t.Fatalf("expected persistence metadata, got %+v", session)
	}
}

func TestBootstrapChatSession_UsesActorExecutorByDefault(t *testing.T) {
	cfg := &config.Config{}
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	session, cleanup, err := bootstrapChatSession(cfg, &chatCommandOptions{
		NoInteractive:            true,
		OutputFormat:             "json",
		DisableTools:             true,
		SessionFeaturesRequested: true,
	}, nil, &chatPersistenceState{
		runtimeSessionManager: manager,
		sessionUserID:         userID,
		resolvedSessionDir:    dir,
	}, &chatRuntimeState{
		providerName:    "codex_ee",
		provider:        config.Provider{Enabled: true, Protocol: "codex", BaseURL: "https://example.com"},
		adapter:         &adapter.CodexAdapter{},
		modelName:       "gpt-5.2-code",
		reasoningEffort: "medium",
		shouldStream:    false,
		baseURL:         "https://example.com/v1/responses",
		retryCfg:        defaultRetryConfig(),
		requestTimeout:  15 * time.Second,
	})
	if err != nil {
		t.Fatalf("bootstrapChatSession: %v", err)
	}
	if cleanup == nil {
		t.Fatal("expected cleanup function")
	}
	defer cleanup()

	if session.ChatExecutor == nil {
		t.Fatal("expected chat executor")
	}
	if got := reflect.TypeOf(session.ChatExecutor).String(); got != "*commands.aicliActorChatExecutor" {
		t.Fatalf("expected actor executor, got %s", got)
	}

	sessionValue := reflect.ValueOf(session).Elem()
	hostField := sessionValue.FieldByName("LocalRuntimeHost")
	if !hostField.IsValid() {
		t.Fatalf("expected ChatSession to expose LocalRuntimeHost")
	}
	if hostField.IsNil() {
		t.Fatal("expected LocalRuntimeHost to be initialized")
	}

	hostValue := hostField.Elem()
	sessionHub := hostValue.FieldByName("SessionHub")
	if !sessionHub.IsValid() || sessionHub.IsNil() {
		t.Fatal("expected local runtime host to include SessionHub")
	}
}

func TestBuildLocalChatToolPolicy_AllowsBrokerTeamTools(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	policy := buildLocalChatToolPolicy(&ChatSession{}, stubLocalChatToolSurface{
		tools: []runtimeskill.ToolInfo{
			{Name: "bash"},
			{Name: "edit"},
		},
	}, &toolbroker.Broker{TeamStore: store})
	if policy == nil {
		t.Fatal("expected tool policy")
	}
	if !policy.AllowlistEnabled {
		t.Fatal("expected allowlist policy")
	}

	for _, name := range []string{"bash", "edit", toolbroker.ToolSpawnTeam, toolbroker.ToolReadTaskSpec} {
		if !policy.AllowedTools[name] {
			t.Fatalf("expected %q to be allowed, got %#v", name, policy.AllowedTools)
		}
	}
}

func TestLoadLocalChatRuntimeConfig_DefaultsTeamStorePathToSessionRuntimeDir(t *testing.T) {
	session := &ChatSession{
		SessionDir: t.TempDir(),
		Model:      "gpt-5.2-codex",
	}

	cfg, err := loadLocalChatRuntimeConfig(&config.Config{}, session)
	if err != nil {
		t.Fatalf("loadLocalChatRuntimeConfig: %v", err)
	}

	expected := filepath.Join(session.SessionDir, "runtime", "team_store.sqlite")
	if cfg.Team.StorePath != expected {
		t.Fatalf("expected team store path %q, got %q", expected, cfg.Team.StorePath)
	}
}

func TestRestoreAmbientTeamBindingFromRuntimeStore(t *testing.T) {
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	require.NoError(t, runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "lead-session",
		Status:    runtimechat.SessionIdle,
		AmbientRunMeta: &team.RunMeta{
			Team: &team.TeamRunMeta{
				TeamID:  "team-restore",
				AgentID: "lead",
			},
		},
	}))

	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "lead-session"},
		PermissionMode:   runtimepolicy.ModeDefault,
		LocalRuntimeHost: &localChatRuntimeHost{RuntimeStore: runtimeStore},
	}

	if !restoreAmbientTeamBindingFromRuntimeStore(session) {
		t.Fatal("expected ambient binding to restore")
	}
	if session.ActiveTeam == nil || session.ActiveTeam.TeamID != "team-restore" || session.ActiveTeam.AgentID != "lead" {
		t.Fatalf("unexpected restored active team: %+v", session.ActiveTeam)
	}
}

func TestRestoreLocalRuntimeHostTeamState_ReplaysTerminalTeamStateWithoutDuplicatingRuntimeEvents(t *testing.T) {
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

	const (
		teamID            = "team-restore"
		teammateID        = "mate-1"
		teammateSessionID = "mate-session"
		summaryText       = "restored lead summary"
	)
	if _, err := teamStore.CreateTeam(context.Background(), team.Team{
		ID:            teamID,
		LeadSessionID: runtimeSession.ID,
		Status:        team.TeamStatusDone,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := teamStore.UpsertTeammate(context.Background(), team.Teammate{
		ID:        teammateID,
		TeamID:    teamID,
		SessionID: teammateSessionID,
		State:     team.TeammateStateIdle,
	}); err != nil {
		t.Fatalf("UpsertTeammate: %v", err)
	}
	if _, err := teamStore.AppendTeamEvent(context.Background(), team.TeamEvent{
		Type:   "team.completed",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"status": string(team.TeamStatusDone),
		},
	}); err != nil {
		t.Fatalf("AppendTeamEvent completed: %v", err)
	}
	if _, err := teamStore.AppendTeamEvent(context.Background(), team.TeamEvent{
		Type:   "team.summary",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"summary": summaryText,
		},
	}); err != nil {
		t.Fatalf("AppendTeamEvent summary: %v", err)
	}

	sessionStore := manager.GetStorage()
	teammateSession := runtimechat.NewSession(userID)
	teammateSession.ID = teammateSessionID
	require.NoError(t, sessionStore.Save(context.Background(), teammateSession))

	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	require.NoError(t, runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: runtimeSession.ID,
		Status:    runtimechat.SessionIdle,
		AmbientRunMeta: &team.RunMeta{
			Team: &team.TeamRunMeta{
				TeamID:  teamID,
				AgentID: "lead",
			},
		},
	}))

	session := &ChatSession{
		RuntimeSession:   runtimeSession,
		SessionManager:   manager,
		SessionUserID:    userID,
		SessionDir:       dir,
		PermissionMode:   runtimepolicy.ModeDefault,
		LocalRuntimeHost: &localChatRuntimeHost{},
	}
	host := &localChatRuntimeHost{
		RuntimeStore: runtimeStore,
		EventStore:   runtimeStore,
		EventBus:     runtimeevents.NewBusWithRetention(16),
		SessionStore: sessionStore,
		SessionUser:  userID,
		TeamStore:    teamStore,
		BaseSession:  session,
	}
	host.TeamLifecycle = newLocalTeamLifecycleService(host)
	host.SessionHub = buildCleanupTestSessionHub(t, host, sessionStore)
	host.ActorRegistry = newLocalActorRegistry(host)
	session.LocalRuntimeHost = host

	if _, err := host.SessionHub.GetOrCreate(runtimeSession.ID); err != nil {
		t.Fatalf("GetOrCreate lead: %v", err)
	}
	if _, err := host.SessionHub.GetOrCreate(teammateSessionID); err != nil {
		t.Fatalf("GetOrCreate teammate: %v", err)
	}

	restoreLocalRuntimeHostTeamState(session)

	if session.ActiveTeam == nil || session.ActiveTeam.TeamID != teamID || session.ActiveTeam.AgentID != "lead" {
		t.Fatalf("expected restored active team binding, got %+v", session.ActiveTeam)
	}

	reloaded, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("manager.Get lead: %v", err)
	}
	foundSummary := false
	for _, message := range reloaded.History {
		if strings.TrimSpace(message.Role) == "assistant" && strings.TrimSpace(message.Content) == summaryText {
			foundSummary = true
			break
		}
	}
	if !foundSummary {
		t.Fatalf("expected mirrored stored terminal summary, got %+v", reloaded.History)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, exists := host.SessionHub.Get(teammateSessionID)
		closedSession, loadErr := manager.Get(context.Background(), teammateSessionID)
		if !exists && loadErr == nil && closedSession != nil && closedSession.State == runtimechat.StateClosed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected teammate cleanup after restore replay, exists=%v session=%+v err=%v", exists, closedSession, loadErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	state, err := runtimeStore.LoadState(context.Background(), runtimeSession.ID)
	require.NoError(t, err)
	if state == nil || state.AmbientRunMeta != nil {
		t.Fatalf("expected ambient team state to be cleared after restore replay, got %+v", state)
	}

	events, err := runtimeSessionEvents(runtimeStore, runtimeSession.ID)
	require.NoError(t, err)
	for _, event := range events {
		if event.Type == "team.completed" || event.Type == "team.summary" {
			t.Fatalf("expected restore replay to avoid duplicating runtime events, got %+v", events)
		}
	}
}

func TestComposeLocalChatSystemPrompt_IncludesWorkspaceGuidance(t *testing.T) {
	session := &ChatSession{SystemPromptText: "Base prompt."}
	got := composeLocalChatSystemPrompt(session, `E:\projects\ai\ai-gateway`)

	for _, want := range []string{
		"Base prompt.",
		"Shell guidance:",
		"Detected user shell:",
		"File editing guidance:",
		"Prefer the dedicated file tools for workspace mutations.",
		"Current workspace root: E:\\projects\\ai\\ai-gateway",
		`Interpret "当前目录", ".", and relative paths as relative to the current workspace root unless the user explicitly says otherwise.`,
		"If the user asks to inspect or search the current workspace, do that directly instead of asking which current directory they mean.",
		"When planning file or directory work, only use paths that you directly confirmed from tool output in the current workspace. Do not invent sibling directories or extrapolate missing paths from naming patterns.",
		"Team-only tools such as read_task_spec, read_task_context, send_team_message, read_mailbox_digest, report_task_outcome, and block_current_task require an active team run. Only call them after spawn_team has created the team run or when the current chat is already bound to an active team task.",
		`When calling team tools, leave teammate session_id unset unless you truly need a fixed explicit session. Never use session_id="current" for teammates.`,
		"When calling spawn_team from the current chat, do not set lead_session_id unless the user explicitly asked for a different lead session. The current session will be used automatically.",
		"When you call spawn_team with auto_start=true, treat the delegated work as already in progress. Do not ask the user to choose the next step while the team is running; instead briefly state that the team is working in the background and that you will summarize when it finishes.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected system prompt to contain %q, got %q", want, got)
		}
	}
	gotLower := strings.ToLower(got)
	if strings.Contains(gotLower, "powershell") || strings.Contains(gotLower, "pwsh") {
		if !strings.Contains(got, "Select-Object -First 200") {
			t.Fatalf("expected PowerShell guidance to mention Select-Object -First 200, got %q", got)
		}
	}
}

func TestResolveLocalWorkspacePath_ResolvesRelativeRuntimeRootAgainstCurrentDir(t *testing.T) {
	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	workspaceRoot := t.TempDir()
	if err := os.Chdir(workspaceRoot); err != nil {
		t.Fatalf("os.Chdir: %v", err)
	}
	defer func() {
		_ = os.Chdir(previousWD)
	}()

	got := resolveLocalWorkspacePath(&runtimecfg.RuntimeConfig{
		Workspace: runtimecfg.WorkspaceConfig{Root: "."},
	}, &ChatSession{})

	if got != workspaceRoot {
		t.Fatalf("expected workspace root %q, got %q", workspaceRoot, got)
	}
}

type stubLocalChatToolSurface struct {
	tools []runtimeskill.ToolInfo
}

func (s stubLocalChatToolSurface) FindTool(toolName string) (runtimeskill.ToolInfo, error) {
	for _, tool := range s.tools {
		if tool.Name == toolName {
			return tool, nil
		}
	}
	return runtimeskill.ToolInfo{}, nil
}

func (s stubLocalChatToolSurface) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return nil, nil
}

func (s stubLocalChatToolSurface) ListTools() []runtimeskill.ToolInfo {
	return append([]runtimeskill.ToolInfo(nil), s.tools...)
}
