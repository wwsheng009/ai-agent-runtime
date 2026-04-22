package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fatih/color"
	"github.com/joho/godotenv"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

func TestAICLIChatActorExecutor_LiveAutoStartShowsTeamProgressTimeline(t *testing.T) {
	if os.Getenv("LIVE_AICLI_TEAM_PROGRESS_TEST") != "1" {
		t.Skip("set LIVE_AICLI_TEAM_PROGRESS_TEST=1 to enable live aicli team progress smoke")
	}

	root := findCommandsRepoRoot(t)
	configPath := strings.TrimSpace(os.Getenv("LIVE_AICLI_TEAM_PROGRESS_CONFIG"))
	if configPath == "" {
		configPath = filepath.Join(root, "configs", "config.yaml")
	}
	envPath := strings.TrimSpace(os.Getenv("LIVE_AICLI_TEAM_PROGRESS_ENV"))
	if envPath == "" {
		envPath = filepath.Join(root, "configs", ".env")
	}
	if _, err := os.Stat(envPath); err == nil {
		if loadErr := godotenv.Overload(envPath); loadErr != nil {
			t.Fatalf("godotenv.Overload: %v", loadErr)
		}
	}

	cfgManager, err := config.NewManager(configPath)
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	cfg := cfgManager.Config()
	if cfg == nil {
		t.Fatal("expected config")
	}
	if cfg.AICLI != nil && cfg.AICLI.MCP != nil {
		cfg.AICLI.MCP.AutoConnect = false
	}

	providerName := strings.TrimSpace(os.Getenv("LIVE_AICLI_TEAM_PROGRESS_PROVIDER"))
	if providerName == "" {
		providerName = selectLiveChatProvider(cfg)
	}
	if providerName == "" {
		t.Skip("no suitable live provider found in config")
	}
	provider, ok := cfg.Providers.Items[providerName]
	if !ok || !provider.Enabled {
		t.Skipf("provider %q not found or disabled", providerName)
	}
	if len(provider.GetAllAPIKeys()) == 0 {
		t.Skipf("provider %q has no configured api keys", providerName)
	}

	model := strings.TrimSpace(os.Getenv("LIVE_AICLI_TEAM_PROGRESS_MODEL"))
	if model == "" {
		model = strings.TrimSpace(provider.DefaultModel)
	}
	if model == "" && len(provider.SupportedModels) > 0 {
		model = strings.TrimSpace(provider.SupportedModels[0])
	}
	if model == "" {
		t.Skipf("provider %q has no default/supported model", providerName)
	}

	sessionRoot := t.TempDir()
	opts := &chatCommandOptions{
		ProviderFlag:           providerName,
		ProviderChanged:        true,
		ModelFlag:              model,
		ModelChanged:           true,
		StreamFlag:             false,
		StreamChanged:          true,
		NoInteractive:          true,
		LogDir:                 filepath.Join(sessionRoot, "logs"),
		SessionDirFlag:         filepath.Join(sessionRoot, "sessions"),
		ReasoningEffortFlag:    "medium",
		ReasoningEffortChanged: true,
		PermissionMode:         runtimepolicy.ModeBypassPermissions,
		OutputFormat:           "text",
	}

	persistenceState, err := prepareChatPersistence(opts)
	if err != nil {
		t.Fatalf("prepareChatPersistence: %v", err)
	}
	if persistenceState.runtimeSessionManager != nil {
		t.Cleanup(persistenceState.runtimeSessionManager.Stop)
	}

	runtimeState, details, err := prepareChatRuntimeState(cfg, opts, nil)
	if err != nil {
		t.Fatalf("prepareChatRuntimeState: %v details=%v", err, details)
	}

	session, cleanup, err := bootstrapChatSession(cfg, opts, nil, persistenceState, runtimeState)
	if err != nil {
		t.Fatalf("bootstrapChatSession: %v", err)
	}
	if cleanup != nil {
		t.Cleanup(cleanup)
	}
	if session.LocalRuntimeHost == nil {
		t.Fatal("expected local runtime host")
	}

	session.NoInteractive = false
	session.JSONOutput = false
	session.OutputFormat = "interactive"

	var (
		linesMu sync.Mutex
		lines   []string
	)
	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(line string) {
		linesMu.Lock()
		defer linesMu.Unlock()
		lines = append(lines, line)
	}
	session.RuntimeEventBridge = bridge

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	teamID := "team-live-" + suffix
	taskID := "task-live-" + suffix
	prompt := fmt.Sprintf(`Use the real runtime team tools only and do not simulate. Call spawn_team now with exact args {"team_id":"%s","tasks":[{"id":"%s","assignee":"planner","goal":"Do not edit files. Finish immediately and end with a structured done outcome whose summary is auto smoke finished.","title":"Immediate structured completion","deliverables":["structured done outcome"]}],"teammates":[{"id":"planner","name":"Planner","profile":"planner"},{"id":"executor","name":"Executor","profile":"executor"}],"auto_start":true}. After the tool succeeds, reply briefly with the created team_id and task_id.`, teamID, taskID)

	response, err := session.ChatExecutor.Execute(context.Background(), session, prompt)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(response, teamID) || !strings.Contains(response, taskID) {
		t.Fatalf("expected team and task ids in response, got %q", response)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := session.LocalRuntimeHost.waitForTeamTerminal(waitCtx, teamID); err != nil {
		t.Fatalf("waitForTeamTerminal: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		snapshot := func() []string {
			linesMu.Lock()
			defer linesMu.Unlock()
			cloned := make([]string, len(lines))
			copy(cloned, lines)
			return cloned
		}()
		if containsAllChatTimelineLines(snapshot,
			fmt.Sprintf("[progress] planner -> lead %s Started task: Immediate structured completion", taskID),
			fmt.Sprintf("[team] completed %s status=done", teamID),
		) && containsChatTimelinePrefix(snapshot, fmt.Sprintf("[team summary] %s ", teamID)) {
			break
		}
		if time.Now().After(deadline) {
			teamEvents, listErr := session.LocalRuntimeHost.TeamStore.ListTeamEvents(context.Background(), team.TeamEventFilter{TeamID: teamID})
			reloaded, loadErr := session.SessionManager.Get(context.Background(), session.RuntimeSession.ID)
			t.Fatalf("timed out waiting for live timeline lines, got %v; team events=%v; team events err=%v; session history=%v; session err=%v", snapshot, teamEvents, listErr, reloaded.History, loadErr)
		}
		time.Sleep(200 * time.Millisecond)
	}

	teamRecord, err := session.LocalRuntimeHost.TeamStore.GetTeam(context.Background(), teamID)
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if teamRecord == nil || teamRecord.Status != team.TeamStatusDone {
		t.Fatalf("expected live team done, got %+v", teamRecord)
	}
}

func TestAICLIChatActorExecutor_LiveDocsPromptCreatesTeamAndUsesLocalReadFlow(t *testing.T) {
	if os.Getenv("LIVE_AICLI_DOCS_TEAM_REGRESSION") != "1" {
		t.Skip("set LIVE_AICLI_DOCS_TEAM_REGRESSION=1 to enable live docs team regression")
	}

	root := findCommandsRepoRoot(t)
	configPath := strings.TrimSpace(os.Getenv("LIVE_AICLI_DOCS_TEAM_REGRESSION_CONFIG"))
	if configPath == "" {
		configPath = filepath.Join(root, "configs", "config.yaml")
	}
	envPath := strings.TrimSpace(os.Getenv("LIVE_AICLI_DOCS_TEAM_REGRESSION_ENV"))
	if envPath == "" {
		envPath = filepath.Join(root, "configs", ".env")
	}
	if _, err := os.Stat(envPath); err == nil {
		if loadErr := godotenv.Overload(envPath); loadErr != nil {
			t.Fatalf("godotenv.Overload: %v", loadErr)
		}
	}

	cfgManager, err := config.NewManager(configPath)
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	cfg := cfgManager.Config()
	if cfg == nil {
		t.Fatal("expected config")
	}
	if cfg.AICLI != nil && cfg.AICLI.MCP != nil {
		cfg.AICLI.MCP.AutoConnect = false
	}

	providerName := strings.TrimSpace(os.Getenv("LIVE_AICLI_DOCS_TEAM_REGRESSION_PROVIDER"))
	if providerName == "" {
		providerName = selectLiveChatProvider(cfg)
	}
	if providerName == "" {
		t.Skip("no suitable live provider found in config")
	}
	provider, ok := cfg.Providers.Items[providerName]
	if !ok || !provider.Enabled {
		t.Skipf("provider %q not found or disabled", providerName)
	}
	if len(provider.GetAllAPIKeys()) == 0 {
		t.Skipf("provider %q has no configured api keys", providerName)
	}

	model := strings.TrimSpace(os.Getenv("LIVE_AICLI_DOCS_TEAM_REGRESSION_MODEL"))
	if model == "" {
		model = strings.TrimSpace(provider.DefaultModel)
	}
	if model == "" && len(provider.SupportedModels) > 0 {
		model = strings.TrimSpace(provider.SupportedModels[0])
	}
	if model == "" {
		t.Skipf("provider %q has no default/supported model", providerName)
	}

	sessionRoot := t.TempDir()
	opts := &chatCommandOptions{
		ProviderFlag:           providerName,
		ProviderChanged:        true,
		ModelFlag:              model,
		ModelChanged:           true,
		StreamFlag:             true,
		StreamChanged:          true,
		NoInteractive:          true,
		LogDir:                 filepath.Join(sessionRoot, "logs"),
		SessionDirFlag:         filepath.Join(sessionRoot, "sessions"),
		ReasoningEffortFlag:    "medium",
		ReasoningEffortChanged: true,
		PermissionMode:         runtimepolicy.ModeBypassPermissions,
		OutputFormat:           "text",
	}

	persistenceState, err := prepareChatPersistence(opts)
	if err != nil {
		t.Fatalf("prepareChatPersistence: %v", err)
	}
	if persistenceState.runtimeSessionManager != nil {
		t.Cleanup(persistenceState.runtimeSessionManager.Stop)
	}

	runtimeState, details, err := prepareChatRuntimeState(cfg, opts, nil)
	if err != nil {
		t.Fatalf("prepareChatRuntimeState: %v details=%v", err, details)
	}

	session, cleanupSession, err := buildChatSession(cfg, opts, nil, persistenceState, runtimeState)
	if err != nil {
		t.Fatalf("buildChatSession: %v", err)
	}
	session.ProfileRoot = root
	if cleanupSession != nil {
		t.Cleanup(cleanupSession)
	}
	if err := restoreChatPersistenceState(session, persistenceState, opts); err != nil {
		t.Fatalf("restoreChatPersistenceState: %v", err)
	}
	_, cleanupCapabilities, err := initializeChatCapabilities(cfg, opts, session)
	if err != nil {
		t.Fatalf("initializeChatCapabilities: %v", err)
	}
	if cleanupCapabilities != nil {
		t.Cleanup(cleanupCapabilities)
	}
	if session.LocalRuntimeHost == nil {
		t.Fatal("expected local runtime host")
	}

	session.NoInteractive = false
	session.JSONOutput = false
	session.OutputFormat = "interactive"

	var (
		linesMu sync.Mutex
		lines   []string
	)
	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(line string) {
		linesMu.Lock()
		defer linesMu.Unlock()
		lines = append(lines, line)
	}
	session.RuntimeEventBridge = bridge

	response, err := session.ChatExecutor.Execute(context.Background(), session, "创建几个团队成员来探索docs目录的文档")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if strings.TrimSpace(response) == "" {
		t.Fatal("expected non-empty actor response for docs prompt")
	}
	for _, forbidden := range []string{
		"如果你愿意",
		"下一步可以继续",
		"你要我继续哪一种",
	} {
		if strings.Contains(response, forbidden) {
			t.Fatalf("expected docs auto-start response to avoid premature follow-up choice %q, got %q", forbidden, response)
		}
	}
	if !shouldDisplayFinalResponse(session, response) {
		t.Fatalf("expected stream fallback response to be displayable, got %q", response)
	}
	if session.ActiveTeam == nil || strings.TrimSpace(session.ActiveTeam.TeamID) == "" {
		t.Fatalf("expected active team binding after docs prompt, got %+v", session.ActiveTeam)
	}

	deadline := time.Now().Add(90 * time.Second)
	for {
		snapshot := func() []string {
			linesMu.Lock()
			defer linesMu.Unlock()
			cloned := make([]string, len(lines))
			copy(cloned, lines)
			return cloned
		}()
		if containsAnyChatTimelineLine(snapshot, "[tool] ls", "[tool] view", "[tool] glob", "[tool] grep") {
			break
		}
		if time.Now().After(deadline) {
			teamEvents, listErr := session.LocalRuntimeHost.TeamStore.ListTeamEvents(context.Background(), team.TeamEventFilter{TeamID: session.ActiveTeam.TeamID})
			reloaded, loadErr := session.SessionManager.Get(context.Background(), session.RuntimeSession.ID)
			t.Fatalf("timed out waiting for live docs timeline, got %v; team events=%v; team events err=%v; session history=%v; session err=%v", snapshot, teamEvents, listErr, reloaded.History, loadErr)
		}
		time.Sleep(250 * time.Millisecond)
	}

	teamRecord, err := session.LocalRuntimeHost.TeamStore.GetTeam(context.Background(), session.ActiveTeam.TeamID)
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if teamRecord == nil {
		t.Fatalf("expected persisted team record for %s", session.ActiveTeam.TeamID)
	}
	if teamRecord.LeadSessionID != session.RuntimeSession.ID {
		t.Fatalf("expected lead session %q, got %+v", session.RuntimeSession.ID, teamRecord)
	}

	teammates, err := session.LocalRuntimeHost.TeamStore.ListTeammates(context.Background(), session.ActiveTeam.TeamID)
	if err != nil {
		t.Fatalf("ListTeammates: %v", err)
	}
	if len(teammates) < 2 {
		t.Fatalf("expected multiple teammates, got %+v", teammates)
	}
	seenSessionIDs := map[string]struct{}{}
	for _, mate := range teammates {
		if strings.TrimSpace(mate.SessionID) == "" || strings.EqualFold(strings.TrimSpace(mate.SessionID), "current") {
			t.Fatalf("expected resolved teammate session id, got %+v", mate)
		}
		if _, exists := seenSessionIDs[mate.SessionID]; exists {
			t.Fatalf("expected unique teammate session ids, got %+v", teammates)
		}
		seenSessionIDs[mate.SessionID] = struct{}{}
	}

	tasks, err := session.LocalRuntimeHost.TeamStore.ListTasks(context.Background(), team.TaskFilter{TeamID: session.ActiveTeam.TeamID})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	foundDocsReadPath := false
	for _, task := range tasks {
		for _, readPath := range task.ReadPaths {
			if strings.Contains(strings.ToLower(readPath), "docs") {
				foundDocsReadPath = true
				break
			}
		}
		if foundDocsReadPath {
			break
		}
	}
	if !foundDocsReadPath {
		t.Fatalf("expected at least one docs read path in live tasks, got %+v", tasks)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	if err := session.LocalRuntimeHost.waitForTeamTerminal(waitCtx, session.ActiveTeam.TeamID); err != nil {
		t.Fatalf("waitForTeamTerminal(%s): %v", session.ActiveTeam.TeamID, err)
	}

	summaryDeadline := time.Now().Add(45 * time.Second)
	for {
		snapshot := func() []string {
			linesMu.Lock()
			defer linesMu.Unlock()
			cloned := make([]string, len(lines))
			copy(cloned, lines)
			return cloned
		}()
		if containsChatTimelinePrefix(snapshot, fmt.Sprintf("[team summary] %s ", session.ActiveTeam.TeamID)) {
			break
		}
		if time.Now().After(summaryDeadline) {
			reloaded, loadErr := session.SessionManager.Get(context.Background(), session.RuntimeSession.ID)
			teamEvents, listErr := session.LocalRuntimeHost.TeamStore.ListTeamEvents(context.Background(), team.TeamEventFilter{TeamID: session.ActiveTeam.TeamID})
			t.Fatalf("timed out waiting for docs team summary; timeline=%v; team events=%v; team events err=%v; history=%v; history err=%v",
				snapshot, teamEvents, listErr, reloaded.History, loadErr)
		}
		time.Sleep(250 * time.Millisecond)
	}

	reloaded, err := session.SessionManager.Get(context.Background(), session.RuntimeSession.ID)
	if err != nil {
		t.Fatalf("reload runtime session: %v", err)
	}
	if reloaded == nil {
		t.Fatal("expected persisted runtime session after docs team completion")
	}
	if !historyContainsAssistantMessage(reloaded.History, response) {
		t.Fatalf("expected initial assistant response to persist, got %+v", reloaded.History)
	}
	foundFollowupSummary := false
	for _, message := range reloaded.History {
		if strings.TrimSpace(message.Role) != "assistant" {
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content == "" || content == strings.TrimSpace(response) {
			continue
		}
		foundFollowupSummary = true
		break
	}
	if !foundFollowupSummary {
		t.Fatalf("expected a follow-up assistant summary after docs team completion, got %+v", reloaded.History)
	}
}

func TestAICLIChatActorExecutor_LiveDocsPromptClearsPromptBeforeAsyncTimeline(t *testing.T) {
	if os.Getenv("LIVE_AICLI_PROMPT_INTERACTION_TEST") != "1" {
		t.Skip("set LIVE_AICLI_PROMPT_INTERACTION_TEST=1 to enable live prompt interaction smoke")
	}

	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
	ui.SetTheme(ui.ThemeAuto)

	root := findCommandsRepoRoot(t)
	configPath := strings.TrimSpace(os.Getenv("LIVE_AICLI_PROMPT_INTERACTION_CONFIG"))
	if configPath == "" {
		configPath = filepath.Join(root, "configs", "config.yaml")
	}
	envPath := strings.TrimSpace(os.Getenv("LIVE_AICLI_PROMPT_INTERACTION_ENV"))
	if envPath == "" {
		envPath = filepath.Join(root, "configs", ".env")
	}
	if _, err := os.Stat(envPath); err == nil {
		if loadErr := godotenv.Overload(envPath); loadErr != nil {
			t.Fatalf("godotenv.Overload: %v", loadErr)
		}
	}

	cfgManager, err := config.NewManager(configPath)
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	cfg := cfgManager.Config()
	if cfg == nil {
		t.Fatal("expected config")
	}
	if cfg.AICLI != nil && cfg.AICLI.MCP != nil {
		cfg.AICLI.MCP.AutoConnect = false
	}

	providerName := strings.TrimSpace(os.Getenv("LIVE_AICLI_PROMPT_INTERACTION_PROVIDER"))
	if providerName == "" {
		providerName = selectLiveChatProvider(cfg)
	}
	if providerName == "" {
		t.Skip("no suitable live provider found in config")
	}
	provider, ok := cfg.Providers.Items[providerName]
	if !ok || !provider.Enabled {
		t.Skipf("provider %q not found or disabled", providerName)
	}
	if len(provider.GetAllAPIKeys()) == 0 {
		t.Skipf("provider %q has no configured api keys", providerName)
	}

	model := strings.TrimSpace(os.Getenv("LIVE_AICLI_PROMPT_INTERACTION_MODEL"))
	if model == "" {
		model = strings.TrimSpace(provider.DefaultModel)
	}
	if model == "" && len(provider.SupportedModels) > 0 {
		model = strings.TrimSpace(provider.SupportedModels[0])
	}
	if model == "" {
		t.Skipf("provider %q has no default/supported model", providerName)
	}

	sessionRoot := t.TempDir()
	opts := &chatCommandOptions{
		ProviderFlag:           providerName,
		ProviderChanged:        true,
		ModelFlag:              model,
		ModelChanged:           true,
		StreamFlag:             true,
		StreamChanged:          true,
		NoInteractive:          true,
		LogDir:                 filepath.Join(sessionRoot, "logs"),
		SessionDirFlag:         filepath.Join(sessionRoot, "sessions"),
		ReasoningEffortFlag:    "medium",
		ReasoningEffortChanged: true,
		PermissionMode:         runtimepolicy.ModeBypassPermissions,
		OutputFormat:           "text",
	}

	persistenceState, err := prepareChatPersistence(opts)
	if err != nil {
		t.Fatalf("prepareChatPersistence: %v", err)
	}
	if persistenceState.runtimeSessionManager != nil {
		t.Cleanup(persistenceState.runtimeSessionManager.Stop)
	}

	runtimeState, details, err := prepareChatRuntimeState(cfg, opts, nil)
	if err != nil {
		t.Fatalf("prepareChatRuntimeState: %v details=%v", err, details)
	}

	session, cleanupSession, err := buildChatSession(cfg, opts, nil, persistenceState, runtimeState)
	if err != nil {
		t.Fatalf("buildChatSession: %v", err)
	}
	session.ProfileRoot = root
	if cleanupSession != nil {
		t.Cleanup(cleanupSession)
	}
	if err := restoreChatPersistenceState(session, persistenceState, opts); err != nil {
		t.Fatalf("restoreChatPersistenceState: %v", err)
	}
	_, cleanupCapabilities, err := initializeChatCapabilities(cfg, opts, session)
	if err != nil {
		t.Fatalf("initializeChatCapabilities: %v", err)
	}
	if cleanupCapabilities != nil {
		t.Cleanup(cleanupCapabilities)
	}
	if session.LocalRuntimeHost == nil {
		t.Fatal("expected local runtime host")
	}

	session.NoInteractive = false
	session.JSONOutput = false
	session.OutputFormat = "interactive"
	session.Interaction = newChatInteractionCoordinator(session)
	capture := &terminalCaptureWriter{}
	session.Interaction.SetWriter(capture)
	session.Interaction.promptAdvanceFn = func() bool { return false }

	response, err := session.ChatExecutor.Execute(context.Background(), session, "创建几个团队成员来探索docs目录的文档")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if strings.TrimSpace(response) == "" {
		t.Fatal("expected non-empty actor response for docs prompt")
	}
	if session.ActiveTeam == nil || strings.TrimSpace(session.ActiveTeam.TeamID) == "" {
		t.Fatalf("expected active team binding after docs prompt, got %+v", session.ActiveTeam)
	}

	session.Interaction.PrintPrompt()

	waitCtx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	if err := session.LocalRuntimeHost.waitForTeamTerminal(waitCtx, session.ActiveTeam.TeamID); err != nil {
		t.Fatalf("waitForTeamTerminal(%s): %v", session.ActiveTeam.TeamID, err)
	}

	deadline := time.Now().Add(45 * time.Second)
	for {
		rendered := capture.String()
		if (strings.Contains(rendered, "• Running ") || strings.Contains(rendered, "• Ran ") || strings.Contains(rendered, "[task] ") || strings.Contains(rendered, "[team summary] ")) &&
			strings.Contains(rendered, "你>") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for async timeline in captured terminal output: %q", rendered)
		}
		time.Sleep(250 * time.Millisecond)
	}

	lines := strings.Split(capture.String(), "\n")
	lastAsyncIndex := -1
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "[thinking]") ||
			strings.Contains(trimmed, "• Running ") ||
			strings.Contains(trimmed, "• Ran ") ||
			strings.Contains(trimmed, "[task]") ||
			strings.Contains(trimmed, "[team]") ||
			strings.Contains(trimmed, "[team summary]") {
			lastAsyncIndex = index
		}
	}
	if lastAsyncIndex < 0 {
		t.Fatalf("expected async timeline lines in captured terminal output, got %q", capture.String())
	}
	promptCount := 0
	for index, line := range lines {
		if !strings.Contains(line, "你>") {
			continue
		}
		promptCount++
		if index < lastAsyncIndex {
			t.Fatalf("expected no visible prompt before async timeline settled, got lines=%v", lines)
		}
	}
	if promptCount == 0 {
		t.Fatalf("expected prompt to appear after async timeline settled, got lines=%v", lines)
	}
}

func TestAICLIChatActorExecutor_LiveDocsTranscriptUsesObservedListingForTeamTasks(t *testing.T) {
	if os.Getenv("LIVE_AICLI_DOCS_TRANSCRIPT_REGRESSION") != "1" {
		t.Skip("set LIVE_AICLI_DOCS_TRANSCRIPT_REGRESSION=1 to enable live docs transcript regression")
	}

	root := findCommandsRepoRoot(t)
	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("os.Chdir(%s): %v", root, err)
	}
	defer func() {
		_ = os.Chdir(previousWD)
	}()

	configPath := strings.TrimSpace(os.Getenv("LIVE_AICLI_DOCS_TRANSCRIPT_CONFIG"))
	if configPath == "" {
		configPath = filepath.Join(root, "configs", "config.yaml")
	}
	envPath := strings.TrimSpace(os.Getenv("LIVE_AICLI_DOCS_TRANSCRIPT_ENV"))
	if envPath == "" {
		envPath = filepath.Join(root, "configs", ".env")
	}
	if _, err := os.Stat(envPath); err == nil {
		if loadErr := godotenv.Overload(envPath); loadErr != nil {
			t.Fatalf("godotenv.Overload: %v", loadErr)
		}
	}

	cfgManager, err := config.NewManager(configPath)
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	cfg := cfgManager.Config()
	if cfg == nil {
		t.Fatal("expected config")
	}
	if cfg.AICLI != nil && cfg.AICLI.MCP != nil {
		cfg.AICLI.MCP.AutoConnect = false
	}

	providerName := strings.TrimSpace(os.Getenv("LIVE_AICLI_DOCS_TRANSCRIPT_PROVIDER"))
	if providerName == "" {
		providerName = selectLiveChatProvider(cfg)
	}
	if providerName == "" {
		t.Skip("no suitable live provider found in config")
	}
	provider, ok := cfg.Providers.Items[providerName]
	if !ok || !provider.Enabled {
		t.Skipf("provider %q not found or disabled", providerName)
	}
	if len(provider.GetAllAPIKeys()) == 0 {
		t.Skipf("provider %q has no configured api keys", providerName)
	}

	model := strings.TrimSpace(os.Getenv("LIVE_AICLI_DOCS_TRANSCRIPT_MODEL"))
	if model == "" {
		model = strings.TrimSpace(provider.DefaultModel)
	}
	if model == "" && len(provider.SupportedModels) > 0 {
		model = strings.TrimSpace(provider.SupportedModels[0])
	}
	if model == "" {
		t.Skipf("provider %q has no default/supported model", providerName)
	}

	sessionRoot := t.TempDir()
	opts := &chatCommandOptions{
		ProviderFlag:           providerName,
		ProviderChanged:        true,
		ModelFlag:              model,
		ModelChanged:           true,
		StreamFlag:             true,
		StreamChanged:          true,
		NoInteractive:          true,
		LogDir:                 filepath.Join(sessionRoot, "logs"),
		SessionDirFlag:         filepath.Join(sessionRoot, "sessions"),
		ReasoningEffortFlag:    "medium",
		ReasoningEffortChanged: true,
		PermissionMode:         runtimepolicy.ModeBypassPermissions,
		OutputFormat:           "text",
	}

	persistenceState, err := prepareChatPersistence(opts)
	if err != nil {
		t.Fatalf("prepareChatPersistence: %v", err)
	}
	if persistenceState.runtimeSessionManager != nil {
		t.Cleanup(persistenceState.runtimeSessionManager.Stop)
	}

	runtimeState, details, err := prepareChatRuntimeState(cfg, opts, nil)
	if err != nil {
		t.Fatalf("prepareChatRuntimeState: %v details=%v", err, details)
	}

	session, cleanupSession, err := buildChatSession(cfg, opts, nil, persistenceState, runtimeState)
	if err != nil {
		t.Fatalf("buildChatSession: %v", err)
	}
	session.ProfileRoot = root
	if cleanupSession != nil {
		t.Cleanup(cleanupSession)
	}
	if err := restoreChatPersistenceState(session, persistenceState, opts); err != nil {
		t.Fatalf("restoreChatPersistenceState: %v", err)
	}
	_, cleanupCapabilities, err := initializeChatCapabilities(cfg, opts, session)
	if err != nil {
		t.Fatalf("initializeChatCapabilities: %v", err)
	}
	if cleanupCapabilities != nil {
		t.Cleanup(cleanupCapabilities)
	}
	if session.LocalRuntimeHost == nil {
		t.Fatal("expected local runtime host")
	}

	session.NoInteractive = false
	session.JSONOutput = false
	session.OutputFormat = "interactive"

	var (
		linesMu sync.Mutex
		lines   []string
	)
	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(line string) {
		linesMu.Lock()
		defer linesMu.Unlock()
		lines = append(lines, line)
	}
	session.RuntimeEventBridge = bridge

	firstResponse, err := session.ChatExecutor.Execute(context.Background(), session, "查看docs目录的文件")
	if err != nil {
		t.Fatalf("first Execute failed: %v", err)
	}
	if strings.TrimSpace(firstResponse) == "" {
		t.Fatal("expected non-empty docs listing response")
	}

	secondResponse, err := session.ChatExecutor.Execute(context.Background(), session, "创建几个team member来探索文件列表")
	if err != nil {
		t.Fatalf("second Execute failed: %v", err)
	}
	if strings.TrimSpace(secondResponse) == "" {
		t.Fatal("expected non-empty follow-up response")
	}
	if session.ActiveTeam == nil || strings.TrimSpace(session.ActiveTeam.TeamID) == "" {
		t.Fatalf("expected active team after transcript follow-up, got %+v", session.ActiveTeam)
	}

	deadline := time.Now().Add(45 * time.Second)
	for {
		tasks, listErr := session.LocalRuntimeHost.TeamStore.ListTasks(context.Background(), team.TaskFilter{TeamID: session.ActiveTeam.TeamID})
		if listErr == nil && len(tasks) > 0 {
			break
		}
		if time.Now().After(deadline) {
			linesMu.Lock()
			snapshot := append([]string(nil), lines...)
			linesMu.Unlock()
			reloaded, loadErr := session.SessionManager.Get(context.Background(), session.RuntimeSession.ID)
			t.Fatalf("timed out waiting for transcript team tasks; timeline=%v; session history=%v; session err=%v", snapshot, reloaded.History, loadErr)
		}
		time.Sleep(250 * time.Millisecond)
	}

	tasks, err := session.LocalRuntimeHost.TeamStore.ListTasks(context.Background(), team.TaskFilter{TeamID: session.ActiveTeam.TeamID})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("expected transcript flow to create at least one team task")
	}

	for _, task := range tasks {
		if len(task.ReadPaths) == 0 {
			continue
		}
		for _, readPath := range task.ReadPaths {
			absolute := filepath.Join(root, filepath.FromSlash(strings.TrimSpace(readPath)))
			info, statErr := os.Stat(absolute)
			if statErr != nil {
				t.Fatalf("expected live transcript task path %q to exist for task %+v: %v", readPath, task, statErr)
			}
			if !info.IsDir() && !strings.HasSuffix(strings.ToLower(info.Name()), ".md") {
				t.Fatalf("expected transcript task path %q to target docs content, got %+v", readPath, task)
			}
		}
	}

	linesMu.Lock()
	snapshot := append([]string(nil), lines...)
	linesMu.Unlock()
	for _, line := range snapshot {
		normalized := strings.ToLower(strings.TrimSpace(line))
		if strings.Contains(normalized, "docs/agents") || strings.Contains(normalized, "docs/anthropic") || strings.Contains(normalized, "does not exist in the current workspace") {
			t.Fatalf("expected no ghost docs paths in transcript timeline, got %v", snapshot)
		}
	}

	events, err := session.LocalRuntimeHost.TeamStore.ListTeamEvents(context.Background(), team.TeamEventFilter{TeamID: session.ActiveTeam.TeamID})
	if err != nil {
		t.Fatalf("ListTeamEvents: %v", err)
	}
	for _, event := range events {
		if event.Type != "task.blocked" {
			continue
		}
		summary, _ := event.Payload["summary"].(string)
		lower := strings.ToLower(summary)
		if strings.Contains(lower, "does not exist") || strings.Contains(lower, "missing") {
			t.Fatalf("expected no missing-path team block in transcript flow, got %+v", events)
		}
	}
}

func selectLiveChatProvider(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	preferred := []string{cfg.Providers.DefaultProvider, "CODEX_03", "codex_03", "nvidia", "deepseek", "bigmodel"}
	seen := make(map[string]struct{}, len(preferred))
	for _, name := range preferred {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		provider, ok := cfg.Providers.Items[name]
		if !ok || !provider.Enabled || len(provider.GetAllAPIKeys()) == 0 {
			continue
		}
		return name
	}
	for name, provider := range cfg.Providers.Items {
		if !provider.Enabled || len(provider.GetAllAPIKeys()) == 0 {
			continue
		}
		return name
	}
	return ""
}

func findCommandsRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("failed to locate repository root")
		}
	}
}
