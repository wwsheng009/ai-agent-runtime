package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fatih/color"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func TestBuildChatSessionInfo_IncludesEndpointHostAndOperationalMetadata(t *testing.T) {
	session := &ChatSession{
		ProviderName: "nvidia",
		Provider: config.Provider{
			Enabled:  true,
			Protocol: "openai",
			BaseURL:  "https://integrate.api.nvidia.com",
			APIKeys:  []string{"key-1", "key-2"},
		},
		Adapter:        &adapter.OpenAIAdapter{},
		Model:          "gpt-4.1-mini",
		Stream:         true,
		BaseURL:        "https://integrate.api.nvidia.com/v1/chat/completions",
		RequestTimeout: 45 * time.Second,
	}

	info := buildChatSessionInfo(session)
	if info.ProviderName != "nvidia" || info.Protocol != "openai" || info.ModelName != "gpt-4.1-mini" {
		t.Fatalf("unexpected session identity info: %+v", info)
	}
	if info.EndpointURL != "https://integrate.api.nvidia.com/v1/chat/completions" {
		t.Fatalf("expected endpoint url to be preserved, got %q", info.EndpointURL)
	}
	if info.Host != "integrate.api.nvidia.com" {
		t.Fatalf("expected host to be extracted, got %q", info.Host)
	}
	if info.KeyCount != 2 {
		t.Fatalf("expected api key count 2, got %d", info.KeyCount)
	}
	if info.Timeout != "45s" {
		t.Fatalf("expected timeout 45s, got %q", info.Timeout)
	}
	if !info.IsStream {
		t.Fatal("expected stream session info")
	}
}

func TestBuildChatSessionInfo_FallsBackToResolvedEndpoint(t *testing.T) {
	session := &ChatSession{
		ProviderName: "alpha",
		Provider: config.Provider{
			Enabled:  true,
			Protocol: "openai",
			BaseURL:  "https://api.example.com",
			APIPath:  "/gateway",
		},
		Adapter: &adapter.OpenAIAdapter{},
		Model:   "gpt-4.1",
	}

	info := buildChatSessionInfo(session)
	if info.EndpointURL != "https://api.example.com/gateway/v1/chat/completions" {
		t.Fatalf("unexpected fallback endpoint: %q", info.EndpointURL)
	}
	if info.Host != "api.example.com" {
		t.Fatalf("unexpected fallback host: %q", info.Host)
	}
}

func TestBuildChatSessionInfo_UsesConfiguredReasoningCapability(t *testing.T) {
	session := &ChatSession{
		ProviderName: "deepseek",
		Provider: config.Provider{
			Enabled:  true,
			Protocol: "openai",
			BaseURL:  "https://api.deepseek.com",
			ModelCapabilities: map[string]config.ModelCapabilitySpec{
				"deepseek-v4-pro": {
					ReasoningModel:   true,
					ReasoningEfforts: []string{"high", "max"},
				},
			},
		},
		Model: "deepseek-v4-pro",
	}

	info := buildChatSessionInfo(session)
	if !info.ReasoningEnabled {
		t.Fatal("expected configured reasoning capability to be reflected in session info")
	}
}

func TestPrintSessionInfo_RendersProviderEndpointDetails(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{
		ProviderName: "nvidia",
		Provider: config.Provider{
			Enabled:  true,
			Protocol: "openai",
			BaseURL:  "https://integrate.api.nvidia.com",
			APIKeys:  []string{"key-1", "key-2"},
		},
		Adapter:        &adapter.OpenAIAdapter{},
		Model:          "gpt-4.1-mini",
		BaseURL:        "https://integrate.api.nvidia.com/v1/chat/completions",
		RequestTimeout: 45 * time.Second,
	}

	output := captureStdout(t, func() {
		printSessionInfo(session)
	})

	for _, expected := range []string{
		"Endpoint:",
		"https://integrate.api.nvidia.com/v1/chat/completions",
		"Host:",
		"integrate.api.nvidia.com",
		"Auth Keys:",
		"Timeout:",
		"45s",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestPrintSessionInfo_AlignsFollowupMetadataRows(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{
		ProviderName:      "codex_ee",
		Provider:          config.Provider{Enabled: true, Protocol: "codex", BaseURL: "https://example.com"},
		Adapter:           &adapter.CodexAdapter{},
		Model:             "gpt-5.2-codex",
		BaseURL:           "https://example.com/v1/responses",
		RequestTimeout:    5 * time.Minute,
		MCPEnabled:        true,
		MCPStatus:         &MCPStatus{Enabled: true, ToolCount: 13, MCPCount: 2},
		ReasoningEffort:   "medium",
		PermissionMode:    "default",
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
		DisableTools:      false,
		RuntimeSession:    &runtimechat.Session{ID: "session-1", State: runtimechat.StateActive},
		LocalRuntimeHost:  &localChatRuntimeHost{},
		InputQueue: &chatInputQueue{
			lines: make(chan chatQueuedInput, 4),
			errs:  make(chan error, 1),
		},
	}
	session.InputQueue.lines <- chatQueuedInput{Text: "queued-1\n", Source: "stdin"}
	session.InputQueue.lines <- chatQueuedInput{Text: "queued-2\n", Source: "stdin"}
	session.queuedInputDrain = true

	output := captureStdout(t, func() {
		printSessionInfo(session)
		printCurrentRuntimeSession(session)
	})

	for _, expected := range []string{
		"MCP:               已启用 (13 个工具, 2 个 MCP 服务器)",
		"Reasoning Effort:  medium",
		"Permission Mode:   default",
		"Approval Reuse:    session_readonly_shell",
		"Queued Input:      2 pending (draining)",
		"Session:           session-1 [active]",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected aligned metadata row %q, got:\n%s", expected, output)
		}
	}
}

func TestPrintSessionInfo_RendersExplicitReasoningCapability(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{
		ProviderName: "deepseek",
		Provider: config.Provider{
			Enabled:  true,
			Protocol: "openai",
			BaseURL:  "https://api.deepseek.com",
			ModelCapabilities: map[string]config.ModelCapabilitySpec{
				"deepseek-v4-pro": {
					ReasoningModel:   true,
					ReasoningEfforts: []string{"high", "max"},
				},
			},
		},
		Model: "deepseek-v4-pro",
	}

	output := captureStdout(t, func() {
		printSessionInfo(session)
	})

	if !strings.Contains(output, "Reasoning:") {
		t.Fatalf("expected output to contain explicit reasoning label, got:\n%s", output)
	}
	if strings.Contains(output, "推理模型") || strings.Contains(output, "禁用 temperature") {
		t.Fatalf("expected output to avoid semantic reasoning-model description, got:\n%s", output)
	}
}

func TestCurrentRuntimeSessionPathAndStoreSummary_CustomDir(t *testing.T) {
	sessionDir := t.TempDir()
	session := &ChatSession{
		SessionDir: sessionDir,
		RuntimeSession: &runtimechat.Session{
			ID:    "session-1",
			State: runtimechat.StateActive,
		},
	}

	expectedPath := filepath.Join(sessionDir, "session-1.json")
	if got := currentRuntimeSessionPath(session); got != expectedPath {
		t.Fatalf("expected session path %q, got %q", expectedPath, got)
	}

	summary := currentRuntimeSessionStoreSummary(session)
	if !strings.Contains(summary, sessionDir) {
		t.Fatalf("expected store summary to include custom dir %q, got %q", sessionDir, summary)
	}
	if !strings.Contains(summary, "(custom; default ") {
		t.Fatalf("expected custom store summary, got %q", summary)
	}
}

func TestCurrentRuntimeSessionStoreSummary_DefaultDir(t *testing.T) {
	session := &ChatSession{
		SessionDir: resolveDefaultChatSessionDir(),
		RuntimeSession: &runtimechat.Session{
			ID:    "session-1",
			State: runtimechat.StateActive,
		},
	}

	summary := currentRuntimeSessionStoreSummary(session)
	if !strings.Contains(summary, "(default)") {
		t.Fatalf("expected default store summary, got %q", summary)
	}
}

func TestPrintCurrentRuntimeSession_IncludesSessionPathAndStore(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
	ui.SetTheme(ui.ThemeAuto)

	sessionDir := t.TempDir()
	logger := NewChatLogger("codex_ee", "codex", "gpt-5.2-code", false, "https://example.com")
	if err := logger.SetLogDir(t.TempDir()); err != nil {
		t.Fatalf("set log dir: %v", err)
	}
	runtimeCapture := &chatRuntimeHTTPCapture{}
	runtimeCapture.SetArtifactDir(logger.RuntimeHTTPArtifactDir())
	requestPath := filepath.Join(logger.RuntimeHTTPArtifactDir(), "001_request_gateway_client.json")
	responsePath := filepath.Join(logger.RuntimeHTTPArtifactDir(), "001_response_gateway_client.json")
	runtimeCapture.RecordArtifactPath("request", requestPath)
	runtimeCapture.RecordArtifactPath("response", responsePath)
	session := &ChatSession{
		SessionDir:         sessionDir,
		Logger:             logger,
		runtimeHTTPCapture: runtimeCapture,
		RuntimeSession: &runtimechat.Session{
			ID:    "session-1",
			State: runtimechat.StateActive,
		},
	}

	output := captureStdout(t, func() {
		printCurrentRuntimeSession(session)
	})

	for _, expected := range []string{
		"Session:           session-1 [active]",
		"Session File:      " + filepath.Join(sessionDir, "session-1.json"),
		"Session Store:     " + sessionDir + " (custom; default ",
		"Chat Log File:     " + logger.SessionLogPath(),
		"Debug Log File:    " + logger.DebugLogPath(),
		"HTTP Artifact Dir: " + logger.RuntimeHTTPArtifactDir(),
		"Last HTTP Req:     " + requestPath,
		"Last HTTP Resp:    " + responsePath,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestPrintCurrentRuntimeSession_ResolvesRelativePathsToAbsolute(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
	ui.SetTheme(ui.ThemeAuto)

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	tempWD := t.TempDir()
	if err := os.Chdir(tempWD); err != nil {
		t.Fatalf("chdir temp wd: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()

	logger := NewChatLogger("codex_ee", "codex", "gpt-5.2-code", false, "https://example.com")
	if err := logger.SetLogDir("chat-logs"); err != nil {
		t.Fatalf("set relative log dir: %v", err)
	}
	runtimeCapture := &chatRuntimeHTTPCapture{}
	requestPath := filepath.Join(logger.RuntimeHTTPArtifactDir(), "001_request_gateway_client.json")
	responsePath := filepath.Join(logger.RuntimeHTTPArtifactDir(), "001_response_gateway_client.json")
	runtimeCapture.RecordArtifactPath("request", requestPath)
	runtimeCapture.RecordArtifactPath("response", responsePath)

	session := &ChatSession{
		SessionDir:         "sessions",
		Logger:             logger,
		runtimeHTTPCapture: runtimeCapture,
		RuntimeSession: &runtimechat.Session{
			ID:    "session-1",
			State: runtimechat.StateActive,
		},
	}

	output := captureStdout(t, func() {
		printCurrentRuntimeSession(session)
	})

	for _, expected := range []string{
		"Session File:      " + resolveAbsoluteChatPath(filepath.Join("sessions", "session-1.json")),
		"Session Store:     " + resolveAbsoluteChatPath("sessions") + " (custom; default ",
		"Chat Log File:     " + resolveAbsoluteChatPath(logger.SessionLogPath()),
		"Debug Log File:    " + resolveAbsoluteChatPath(logger.DebugLogPath()),
		"HTTP Artifact Dir: " + resolveAbsoluteChatPath(logger.RuntimeHTTPArtifactDir()),
		"Last HTTP Req:     " + resolveAbsoluteChatPath(requestPath),
		"Last HTTP Resp:    " + resolveAbsoluteChatPath(responsePath),
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestHandleCommand_DebugPrintsSessionArtifactsAndRuntimeState(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
	ui.SetTheme(ui.ThemeAuto)

	baseDir := t.TempDir()
	sessionDir := filepath.Join(baseDir, "sessions")
	logDir := filepath.Join(baseDir, "chat-logs")
	runtimeConfigPath := filepath.Join(baseDir, "runtime.yaml")
	mcpConfigPath := filepath.Join(baseDir, "mcp.yaml")
	skillsDir := filepath.Join(baseDir, "skills")
	profileRoot := filepath.Join(baseDir, "workspace")

	logger := NewChatLogger("codex_ee", "codex", "gpt-5.2-code", false, "https://example.com")
	if err := logger.SetLogDir(logDir); err != nil {
		t.Fatalf("set log dir: %v", err)
	}
	runtimeCapture := &chatRuntimeHTTPCapture{}
	runtimeCapture.SetArtifactDir(logger.RuntimeHTTPArtifactDir())
	requestPath := filepath.Join(logger.RuntimeHTTPArtifactDir(), "001_request_gateway_client.json")
	responsePath := filepath.Join(logger.RuntimeHTTPArtifactDir(), "001_response_gateway_client.json")
	runtimeCapture.RecordArtifactPath("request", requestPath)
	runtimeCapture.RecordArtifactPath("response", responsePath)

	queue := &chatInputQueue{
		lines: make(chan chatQueuedInput, 4),
		errs:  make(chan error, 1),
	}

	session := &ChatSession{
		ProviderName:               "codex_ee",
		Provider:                   config.Provider{Enabled: true, Protocol: "openai", BaseURL: "https://example.com", APIKeys: []string{"key-1"}},
		Model:                      "gpt-5.2-code",
		ReasoningEffort:            "medium",
		HTTPDebug:                  true,
		Stream:                     true,
		NoInteractive:              true,
		JSONOutput:                 true,
		JSONEnvelope:               true,
		MCPEnabled:                 true,
		MCPStatus:                  &MCPStatus{Enabled: true, ToolCount: 7, MCPCount: 2},
		SkillsDebug:                true,
		OutputFormat:               "json",
		ProfileName:                "debug-profile",
		ProfileAgent:               "agent-x",
		ProfileRoot:                profileRoot,
		RuntimeConfigPath:          runtimeConfigPath,
		MCPConfigPath:              mcpConfigPath,
		ResolvedSkillDirs:          []string{skillsDir},
		PermissionMode:             "default",
		ApprovalReuseMode:          chatApprovalReuseTeamReadOnlyShell,
		SelectedAgentTarget:        "/root/debug-child",
		SessionDir:                 sessionDir,
		InputQueue:                 queue,
		RuntimeSession:             &runtimechat.Session{ID: "session-1", State: runtimechat.StateActive},
		Logger:                     logger,
		runtimeHTTPCapture:         runtimeCapture,
		lastLocalShellArtifactPath: filepath.Join(logger.LocalShellArtifactDir(), "001_git.txt"),
		Interaction: &chatInteractionCoordinator{
			promptVisible:       true,
			promptPasteActive:   true,
			thinkingActive:      true,
			streamingActive:     true,
			reasoningActive:     true,
			completeBlockOutput: true,
		},
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/debug", false); quit {
			t.Fatal("expected debug command not to exit")
		}
	})

	expectedFragments := []string{
		"Session:           session-1 [active]",
		"Session File:      " + filepath.Join(sessionDir, "session-1.json"),
		"Session Store:     " + sessionDir,
		"Chat Log File:     " + logger.SessionLogPath(),
		"Debug Log File:    " + logger.DebugLogPath(),
		fmt.Sprintf("%-18s %s", "HTTP Artifact Dir:", logger.RuntimeHTTPArtifactDir()),
		fmt.Sprintf("%-18s %s", "Shell Artifact Dir:", logger.LocalShellArtifactDir()),
		fmt.Sprintf("%-18s %s", "Generated Image Artifact Dir:", filepath.Join(logger.SessionDirPath(), "generated-images")),
		fmt.Sprintf("%-18s %s", "Last HTTP Req:", requestPath),
		fmt.Sprintf("%-18s %s", "Last HTTP Resp:", responsePath),
		fmt.Sprintf("%-18s %s", "Last Shell Out:", filepath.Join(logger.LocalShellArtifactDir(), "001_git.txt")),
		fmt.Sprintf("%-18s %s", "Profile Root:", profileRoot),
		fmt.Sprintf("%-18s %s", "Runtime Config Path:", runtimeConfigPath),
		fmt.Sprintf("%-18s %s", "MCP Config Path:", mcpConfigPath),
		fmt.Sprintf("%-18s %s", "Resolved Skill Dirs:", skillsDir),
		fmt.Sprintf("%-18s %s", "Output Format:", "json"),
		fmt.Sprintf("%-18s %s", "No Interactive:", "on"),
		fmt.Sprintf("%-18s %s", "JSON Output:", "on"),
		fmt.Sprintf("%-18s %s", "JSON Envelope:", "on"),
		fmt.Sprintf("%-18s %s", "MCP Enabled:", "on"),
		fmt.Sprintf("%-18s %s", "Skills Debug:", "on"),
		fmt.Sprintf("%-18s %s", "Permission Mode:", "default"),
		fmt.Sprintf("%-18s %s", "Approval Reuse:", "team_readonly_shell"),
		fmt.Sprintf("%-18s %s", "Queued Input:", "0 pending"),
		fmt.Sprintf("%-18s %s", "Interaction:", "prompt_visible=true prompt_paste_active=true thinking_active=true streaming_active=true reasoning_active=true complete_block_output=true shutdown=false"),
		fmt.Sprintf("%-18s %s", "Agent Target:", "/root/debug-child"),
		fmt.Sprintf("%-18s %s", "Surface:", "<none>"),
	}
	for _, expected := range expectedFragments {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestChatDebugAgentGraphLinesListsLocalAgents(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	host := &localChatRuntimeHost{
		SessionStore: manager.GetStorage(),
		SessionUser:  userID,
		BaseSession: &ChatSession{
			RuntimeSession: rootSession,
			SessionUserID:  userID,
		},
	}
	host.ActorRegistry = newLocalActorRegistry(host)
	session := &ChatSession{
		RuntimeSession:      rootSession,
		SessionUserID:       userID,
		LocalRuntimeHost:    host,
		SelectedAgentTarget: "/root/debug-worker",
	}

	worker := runtimechat.NewSession(userID)
	worker.ID = "debug-worker"
	worker.SetContext(toolbroker.AgentSessionContextParentSessionID, rootSession.ID)
	worker.SetContext(toolbroker.AgentSessionContextRootSessionID, rootSession.ID)
	worker.SetContext(toolbroker.AgentSessionContextPath, "/root/debug-worker")
	worker.SetContext(toolbroker.AgentSessionContextDepth, 1)
	worker.SetContext(toolbroker.AgentSessionContextAgentType, "worker")
	if err := manager.GetStorage().Save(context.Background(), worker); err != nil {
		t.Fatalf("save worker: %v", err)
	}

	lines := chatDebugAgentGraphLines(session)
	output := strings.Join(lines, "\n")
	for _, expected := range []string{
		"count=1",
		"selected=/root/debug-worker",
		"/root/debug-worker",
		"status=idle",
		"session=debug-worker",
		"parent=" + rootSession.ID,
		"depth=1",
		"type=worker",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected agent graph to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestChatAgentTargetLinesListsAvailableTargets(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	host := &localChatRuntimeHost{
		SessionStore: manager.GetStorage(),
		SessionUser:  userID,
		BaseSession: &ChatSession{
			RuntimeSession: rootSession,
			SessionUserID:  userID,
		},
	}
	host.ActorRegistry = newLocalActorRegistry(host)
	session := &ChatSession{
		RuntimeSession:      rootSession,
		SessionUserID:       userID,
		LocalRuntimeHost:    host,
		SelectedAgentTarget: "/root/target-worker",
	}

	worker := runtimechat.NewSession(userID)
	worker.ID = "target-worker"
	worker.SetContext(toolbroker.AgentSessionContextParentSessionID, rootSession.ID)
	worker.SetContext(toolbroker.AgentSessionContextRootSessionID, rootSession.ID)
	worker.SetContext(toolbroker.AgentSessionContextPath, "/root/target-worker")
	worker.SetContext(toolbroker.AgentSessionContextDepth, 1)
	worker.SetContext(toolbroker.AgentSessionContextAgentType, "worker")
	if err := manager.GetStorage().Save(context.Background(), worker); err != nil {
		t.Fatalf("save worker: %v", err)
	}

	output := strings.Join(chatAgentTargetLines(session), "\n")
	for _, expected := range []string{
		"Selected Agent Target: /root/target-worker",
		"Agent Targets:",
		"[1] * /root/target-worker",
		"status=idle",
		"session=target-worker",
		"type=worker",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected target lines to contain %q, got:\n%s", expected, output)
		}
	}

	commandOutput := captureStdout(t, func() {
		if quit := handleCommand(session, "/agents target", false); quit {
			t.Fatal("agents target command should not quit")
		}
	})
	if !strings.Contains(commandOutput, "Agent Targets:") || !strings.Contains(commandOutput, "[1] * /root/target-worker") {
		t.Fatalf("expected agents target command to list available targets, got:\n%s", commandOutput)
	}
}

func TestChatDebugMailboxLinesListsPendingTeamMessages(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	const teamID = "debug-mailbox-team"
	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            teamID,
		LeadSessionID: "debug-root",
		Status:        team.TeamStatusActive,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	taskID := "task-1"
	messageID, err := store.InsertMail(context.Background(), team.MailMessage{
		TeamID:    teamID,
		FromAgent: "worker",
		ToAgent:   "lead",
		TaskID:    &taskID,
		Kind:      "progress",
		Body:      "Started task and waiting for review.",
	})
	if err != nil {
		t.Fatalf("InsertMail: %v", err)
	}

	session := &ChatSession{
		ActiveTeam:       &chatTeamBinding{TeamID: teamID, AgentID: "lead"},
		LocalRuntimeHost: &localChatRuntimeHost{TeamStore: store},
	}
	lines := chatDebugMailboxLines(session)
	output := strings.Join(lines, "\n")
	for _, expected := range []string{
		"team=" + teamID,
		"agent=lead",
		"unread=1",
		messageID,
		"kind=progress",
		"from=worker",
		"to=lead",
		"task=task-1",
		"body=Started task and waiting for review.",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected mailbox debug to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestChatAgentPickerResolvesByNumberPathAndSession(t *testing.T) {
	agents := []toolbroker.AgentStatusResult{
		{ID: "agent-1", SessionID: "session-1", Path: "/root/agent-1", Status: "idle"},
		{ID: "agent-2", SessionID: "session-2", Path: "/root/agent-2", Status: "running"},
	}
	if got := resolveChatAgentPickerChoice("2", agents); got == nil || got.SessionID != "session-2" {
		t.Fatalf("expected numeric choice to resolve second agent, got %#v", got)
	}
	if got := resolveChatAgentPickerChoice("/root/agent-1", agents); got == nil || got.SessionID != "session-1" {
		t.Fatalf("expected path choice to resolve first agent, got %#v", got)
	}
	if got := resolveChatAgentPickerChoice("session-2", agents); got == nil || got.Path != "/root/agent-2" {
		t.Fatalf("expected session choice to resolve second agent, got %#v", got)
	}
	if got := resolveChatAgentPickerChoice("missing", agents); got != nil {
		t.Fatalf("expected missing choice to return nil, got %#v", got)
	}
}

func TestChatAgentPickerPopupLinesIncludeAgentDetails(t *testing.T) {
	lines := chatAgentPickerPopupLines([]toolbroker.AgentStatusResult{
		{ID: "agent-1", SessionID: "session-1", Path: "/root/agent-1", Status: "idle", AgentType: "worker", TeamID: "team-1", TeammateID: "member-1", CurrentTaskID: "task-1", CurrentTaskStatus: "running"},
	}, "")
	output := strings.Join(lines, "\n")
	for _, expected := range []string{
		"Agent Picker:",
		"[1] /root/agent-1",
		"status=idle",
		"session=session-1",
		"type=worker",
		"team=team-1",
		"teammate=member-1",
		"task=task-1",
		"task_status=running",
		"输入编号",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected picker lines to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestParseChatAgentMessageCommandPreservesMessageSpaces(t *testing.T) {
	target, message := parseChatAgentMessageCommand("send /root/agent-1 review docs and report")
	if target != "/root/agent-1" || message != "review docs and report" {
		t.Fatalf("unexpected parsed command: target=%q message=%q", target, message)
	}
	target, message = parseChatAgentMessageCommand("followup session-2 continue the task")
	if target != "session-2" || message != "continue the task" {
		t.Fatalf("unexpected parsed followup command: target=%q message=%q", target, message)
	}
}

func TestChatTimelineLinesListsActiveTeamEvents(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	const teamID = "timeline-team"
	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            teamID,
		LeadSessionID: "timeline-root",
		Status:        team.TeamStatusActive,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	_, _ = store.AppendTeamEvent(context.Background(), team.TeamEvent{
		Type:   "task.completed",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"task_id":  "task-1",
			"assignee": "worker",
			"summary":  "finished docs review",
		},
	})
	_, _ = store.AppendTeamEvent(context.Background(), team.TeamEvent{
		Type:   "team.completed",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"status": "done",
		},
	})

	session := &ChatSession{
		ActiveTeam:       &chatTeamBinding{TeamID: teamID, AgentID: "lead"},
		LocalRuntimeHost: &localChatRuntimeHost{TeamStore: store},
	}
	lines := chatTimelineLines(session, 10)
	output := strings.Join(lines, "\n")
	for _, expected := range []string{
		"team=" + teamID,
		"events=2",
		"#1 task.completed",
		"task=task-1",
		"assignee=worker",
		"summary=finished docs review",
		"#2 team.completed",
		"status=done",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected timeline to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestChatTimelineCommandLinesListsExplicitTeamEvents(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	const teamID = "timeline-explicit-team"
	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            teamID,
		LeadSessionID: "timeline-root",
		Status:        team.TeamStatusActive,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	_, _ = store.AppendTeamEvent(context.Background(), team.TeamEvent{
		Type:   "task.completed",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"task_id": "explicit-task",
			"summary": "explicit team finished",
		},
	})

	session := &ChatSession{
		LocalRuntimeHost: &localChatRuntimeHost{TeamStore: store},
	}
	output := strings.Join(chatTimelineCommandLines(session, "/timeline "+teamID+" 5"), "\n")
	for _, expected := range []string{
		"team=" + teamID,
		"events=1",
		"#1 task.completed",
		"task=explicit-task",
		"summary=explicit team finished",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected explicit timeline to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestChatTimelineLinesShowsRecentEventsInSequenceOrder(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	const teamID = "timeline-recent-team"
	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            teamID,
		LeadSessionID: "timeline-root",
		Status:        team.TeamStatusActive,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	for _, name := range []string{"first", "second", "third"} {
		_, _ = store.AppendTeamEvent(context.Background(), team.TeamEvent{
			Type:   "task.completed",
			TeamID: teamID,
			Payload: map[string]interface{}{
				"task_id": name,
			},
		})
	}

	session := &ChatSession{
		ActiveTeam:       &chatTeamBinding{TeamID: teamID, AgentID: "lead"},
		LocalRuntimeHost: &localChatRuntimeHost{TeamStore: store},
	}
	lines := chatTimelineLines(session, 2)
	output := strings.Join(lines, "\n")
	if strings.Contains(output, "task=first") {
		t.Fatalf("expected recent timeline to hide oldest event, got:\n%s", output)
	}
	second := strings.Index(output, "#2 task.completed task=second")
	third := strings.Index(output, "#3 task.completed task=third")
	if second < 0 || third < 0 || second > third {
		t.Fatalf("expected recent timeline in ascending seq order, got:\n%s", output)
	}
	if !strings.Contains(output, "events=3 shown=2") {
		t.Fatalf("expected total/shown counts, got:\n%s", output)
	}
}

func TestChatTimelineLinesIncludesTaskDispatchDetails(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	const teamID = "timeline-dispatch-team"
	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            teamID,
		LeadSessionID: "timeline-root",
		Status:        team.TeamStatusActive,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	request := team.TaskTriggerRequest{
		SessionID: "team-1__member-1",
		TeamID:    teamID,
		AgentID:   "member-1",
		TaskID:    "task-1",
		Prompt:    "review docs",
	}
	if _, err := team.AppendTaskDispatchRequested(context.Background(), store, request); err != nil {
		t.Fatalf("AppendTaskDispatchRequested: %v", err)
	}
	if _, err := team.AppendTaskDispatchCompleted(context.Background(), store, request, &team.SessionResult{
		Success: true,
		TraceID: "trace-1",
		Steps:   2,
		Output:  "done",
	}, nil); err != nil {
		t.Fatalf("AppendTaskDispatchCompleted: %v", err)
	}

	session := &ChatSession{
		ActiveTeam:       &chatTeamBinding{TeamID: teamID, AgentID: "lead"},
		LocalRuntimeHost: &localChatRuntimeHost{TeamStore: store},
	}
	output := strings.Join(chatTimelineLines(session, 10), "\n")
	for _, expected := range []string{
		"#1 " + team.TaskDispatchRequestedEvent,
		"task=task-1",
		"session=team-1__member-1",
		"assignee=member-1",
		"via=agent_control.trigger_task",
		"#2 " + team.TaskDispatchCompletedEvent,
		"success=true",
		"trace=trace-1",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected dispatch timeline to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestChatTimelineCommandLinesFiltersEventRows(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	const teamID = "timeline-filter-team"
	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            teamID,
		LeadSessionID: "timeline-root",
		Status:        team.TeamStatusActive,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	for _, event := range []team.TeamEvent{
		{
			Type:   "task.completed",
			TeamID: teamID,
			Payload: map[string]interface{}{
				"task_id":  "keep-task",
				"assignee": "member-a",
				"summary":  "kept event",
			},
		},
		{
			Type:   "task.completed",
			TeamID: teamID,
			Payload: map[string]interface{}{
				"task_id":  "skip-task",
				"assignee": "member-b",
				"summary":  "hidden event",
			},
		},
		{
			Type:   "team.completed",
			TeamID: teamID,
			Payload: map[string]interface{}{
				"status": "done",
			},
		},
	} {
		if _, err := store.AppendTeamEvent(context.Background(), event); err != nil {
			t.Fatalf("AppendTeamEvent: %v", err)
		}
	}

	session := &ChatSession{
		LocalRuntimeHost: &localChatRuntimeHost{TeamStore: store},
	}
	output := strings.Join(chatTimelineCommandLines(session, "/timeline "+teamID+" filter=task=keep-task 10"), "\n")
	if !strings.Contains(output, "team="+teamID+" events=3 shown=3") {
		t.Fatalf("expected filtered timeline to keep header context, got:\n%s", output)
	}
	if !strings.Contains(output, "task=keep-task") || !strings.Contains(output, "summary=kept event") {
		t.Fatalf("expected filtered timeline to keep matching event, got:\n%s", output)
	}
	for _, hidden := range []string{"task=skip-task", "hidden event", "team.completed"} {
		if strings.Contains(output, hidden) {
			t.Fatalf("expected filtered timeline to hide %q, got:\n%s", hidden, output)
		}
	}
}

func TestChatCollabLinesListsParentMailboxEvents(t *testing.T) {
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "collab-root", State: runtimechat.StateActive},
		LocalRuntimeHost: &localChatRuntimeHost{
			EventStore: runtimeStore,
		},
	}
	_, err := runtimeStore.AppendEvent(context.Background(), runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "collab-root",
		Payload:   map[string]interface{}{"content": "not collab"},
	})
	if err != nil {
		t.Fatalf("append non-collab event: %v", err)
	}
	for _, body := range []string{"first", "second", "third"} {
		message := toolbroker.BuildAgentMailboxMessage("child-1", "parent", body, false)
		if _, _, err := runtimeStore.AppendMailbox(context.Background(), "collab-root", message); err != nil {
			t.Fatalf("append mailbox: %v", err)
		}
	}

	output := strings.Join(chatCollabLines(session, 2), "\n")
	if strings.Contains(output, "not collab") || strings.Contains(output, "body=first") {
		t.Fatalf("expected collab lines to filter non-collab and old events, got:\n%s", output)
	}
	for _, expected := range []string{
		"session=collab-root events=3 shown=2 source=agent_control+mailbox control_events=3",
		"mailbox_received",
		"from=child-1",
		"to=parent",
		"kind=agent_message",
		"msg=agent_control.agent_message",
		"action=agent.message",
		"workflow=spawn_agent",
		"delivery=session_mailbox",
		"mailbox=agent_message",
		"target=parent",
		"body=second",
		"body=third",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected collab output to contain %q, got:\n%s", expected, output)
		}
	}
	if strings.Count(output, "mailbox_received") != 2 {
		t.Fatalf("expected collab output to list mailbox substrate rows without session-event mirror duplicates, got:\n%s", output)
	}
}

func TestHandleCommand_CollabPrintsParentMailboxTimeline(t *testing.T) {
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "collab-command-root", State: runtimechat.StateActive},
		LocalRuntimeHost: &localChatRuntimeHost{
			EventStore: runtimeStore,
		},
	}
	if _, _, err := runtimeStore.AppendMailbox(context.Background(), "collab-command-root", team.MailMessage{
		FromAgent: "child-1",
		ToAgent:   "parent",
		Kind:      "agent_message",
		Body:      "command collab hello",
	}); err != nil {
		t.Fatalf("append mailbox: %v", err)
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/collab 5", false); quit {
			t.Fatal("collab command should not quit")
		}
	})
	if !strings.Contains(output, "Parent Mailbox Timeline:") || !strings.Contains(output, "command collab hello") {
		t.Fatalf("expected collab command output, got:\n%s", output)
	}
}

func TestHandleCommand_CollabPrintsSelectedAgentMailboxTimeline(t *testing.T) {
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
	session := &ChatSession{
		RuntimeSession:      &runtimechat.Session{ID: "collab-selected-root", State: runtimechat.StateActive},
		SelectedAgentTarget: "collab-selected-child",
		LocalRuntimeHost:    &localChatRuntimeHost{EventStore: runtimeStore},
	}
	if _, _, err := runtimeStore.AppendMailbox(context.Background(), "collab-selected-child", toolbroker.BuildAgentMailboxMessage(
		"parent",
		"collab-selected-child",
		"selected collab hello",
		false,
	)); err != nil {
		t.Fatalf("append selected mailbox: %v", err)
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/collab selected 5", false); quit {
			t.Fatal("collab command should not quit")
		}
	})
	for _, expected := range []string{
		"Agent Mailbox Timeline:",
		"session=collab-selected-child",
		"selected collab hello",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected selected collab output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestHandleCommand_CollabAllAggregatesParentAndAgentMailboxes(t *testing.T) {
	ctx := context.Background()
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
	sessionStore := runtimechat.NewInMemoryStorage()
	root := runtimechat.NewSession("collab-user")
	root.ID = "collab-all-root"
	root.State = runtimechat.StateActive
	if err := sessionStore.Save(ctx, root); err != nil {
		t.Fatalf("save root session: %v", err)
	}
	child := runtimechat.NewSession("collab-user")
	child.ID = "collab-all-child"
	child.SetContext(toolbroker.AgentSessionContextParentSessionID, root.ID)
	child.SetContext(toolbroker.AgentSessionContextRootSessionID, root.ID)
	child.SetContext(toolbroker.AgentSessionContextPath, "/root/collab-all-child")
	child.SetContext(toolbroker.AgentSessionContextDepth, 1)
	if err := sessionStore.Save(ctx, child); err != nil {
		t.Fatalf("save child session: %v", err)
	}
	host := &localChatRuntimeHost{
		EventStore:   runtimeStore,
		SessionStore: sessionStore,
		SessionUser:  "collab-user",
	}
	host.ActorRegistry = newLocalActorRegistry(host)
	session := &ChatSession{
		RuntimeSession:   root,
		SessionUserID:    "collab-user",
		LocalRuntimeHost: host,
	}
	if _, _, err := runtimeStore.AppendMailbox(ctx, root.ID, toolbroker.BuildAgentMailboxMessage(
		child.ID,
		"parent",
		"parent aggregate hello",
		false,
	)); err != nil {
		t.Fatalf("append parent mailbox: %v", err)
	}
	if _, _, err := runtimeStore.AppendMailbox(ctx, child.ID, toolbroker.BuildAgentMailboxMessage(
		"parent",
		child.ID,
		"child aggregate hello",
		false,
	)); err != nil {
		t.Fatalf("append child mailbox: %v", err)
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/collab all 5", false); quit {
			t.Fatal("collab command should not quit")
		}
	})
	for _, expected := range []string{
		"All Mailbox Timelines:",
		"targets=2",
		"target=parent session=collab-all-root",
		"target=/root/collab-all-child session=collab-all-child",
		"parent aggregate hello",
		"child aggregate hello",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected collab all output to contain %q, got:\n%s", expected, output)
		}
	}

	filtered := captureStdout(t, func() {
		if quit := handleCommand(session, "/collab all filter=body=child 5", false); quit {
			t.Fatal("collab command should not quit")
		}
	})
	if strings.Contains(filtered, "parent aggregate hello") {
		t.Fatalf("expected filtered collab output to hide parent mailbox event, got:\n%s", filtered)
	}
	if !strings.Contains(filtered, "child aggregate hello") {
		t.Fatalf("expected filtered collab output to keep child mailbox event, got:\n%s", filtered)
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _, _ = runtimeStore.AppendMailbox(ctx, child.ID, toolbroker.BuildAgentMailboxMessage(
			"parent",
			child.ID,
			"follow child update",
			false,
		))
	}()
	followed := captureStdout(t, func() {
		if quit := handleCommand(session, "/collab follow all filter=body=follow timeout=500ms 5", false); quit {
			t.Fatal("collab command should not quit")
		}
	})
	for _, expected := range []string{
		"follow=waiting",
		"follow=update session=collab-all-child",
		"Follow Update:",
		"follow child update",
	} {
		if !strings.Contains(followed, expected) {
			t.Fatalf("expected followed collab output to contain %q, got:\n%s", expected, followed)
		}
	}
}

func TestHandleCommand_AgentsPanelShowsUnifiedMultiAgentView(t *testing.T) {
	ctx := context.Background()
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
	sessionStore := runtimechat.NewInMemoryStorage()
	root := runtimechat.NewSession("panel-user")
	root.ID = "panel-root"
	root.State = runtimechat.StateActive
	require.NoError(t, sessionStore.Save(ctx, root))
	child := runtimechat.NewSession("panel-user")
	child.ID = "panel-child"
	child.SetContext(toolbroker.AgentSessionContextParentSessionID, root.ID)
	child.SetContext(toolbroker.AgentSessionContextRootSessionID, root.ID)
	child.SetContext(toolbroker.AgentSessionContextPath, "/root/panel-child")
	child.SetContext(toolbroker.AgentSessionContextDepth, 1)
	require.NoError(t, sessionStore.Save(ctx, child))
	host := &localChatRuntimeHost{
		EventStore:   runtimeStore,
		SessionStore: sessionStore,
		SessionUser:  "panel-user",
	}
	host.ActorRegistry = newLocalActorRegistry(host)
	session := &ChatSession{
		RuntimeSession:      root,
		SessionUserID:       "panel-user",
		SelectedAgentTarget: "/root/panel-child",
		LocalRuntimeHost:    host,
	}
	_, _, err := runtimeStore.AppendMailbox(ctx, child.ID, toolbroker.BuildAgentMailboxMessage(
		"parent",
		child.ID,
		"panel mailbox hello",
		false,
	))
	require.NoError(t, err)

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/agents panel 5", false); quit {
			t.Fatal("agents panel command should not quit")
		}
	})
	for _, expected := range []string{
		"Agent Control Panel:",
		"selected=/root/panel-child",
		"parent_session=panel-root",
		"Agents:",
		"/root/panel-child",
		"Mailbox:",
		"panel mailbox hello",
		"Timeline:",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected agents panel output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestHandleCommand_AgentsPanelShowsRegistryServiceMode(t *testing.T) {
	ctx := context.Background()
	registry, err := agentcontrol.NewRegistryService(ctx, agentcontrol.RegistryServiceConfig{
		StorePath: filepath.Join(t.TempDir(), "agent-control.sqlite"),
	})
	require.NoError(t, err)
	defer registry.Close()

	root := runtimechat.NewSession("panel-user")
	root.ID = "panel-root"
	host := &localChatRuntimeHost{
		EventStore:         runtimechat.NewInMemoryRuntimeStore(32),
		SessionUser:        "panel-user",
		AgentControl:       registry,
		AgentRegistryStore: registry.AgentStore,
	}
	session := &ChatSession{
		RuntimeSession:   root,
		SessionUserID:    "panel-user",
		LocalRuntimeHost: host,
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/agents panel 5", false); quit {
			t.Fatal("agents panel command should not quit")
		}
	})
	for _, expected := range []string{
		"service=on",
		"service_health=ok",
		"mode=single_sqlite",
		"shared_db=true",
		"agents=durable",
		"runtime_projection=local_only:global_writer_not_configured@runtime_in_memory",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected agents panel output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestHandleCommand_DebugShowsAgentControlRegistryServiceMode(t *testing.T) {
	ctx := context.Background()
	registry, err := agentcontrol.NewRegistryService(ctx, agentcontrol.RegistryServiceConfig{
		StorePath: filepath.Join(t.TempDir(), "agent-control.sqlite"),
	})
	require.NoError(t, err)
	defer registry.Close()

	root := runtimechat.NewSession("debug-user")
	root.ID = "debug-agent-control-root"
	host := &localChatRuntimeHost{
		EventStore:         runtimechat.NewInMemoryRuntimeStore(32),
		SessionUser:        "debug-user",
		AgentControl:       registry,
		AgentRegistryStore: registry.AgentStore,
	}
	session := &ChatSession{
		RuntimeSession:   root,
		SessionUserID:    "debug-user",
		LocalRuntimeHost: host,
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/debug", false); quit {
			t.Fatal("debug command should not quit")
		}
	})
	for _, expected := range []string{
		"AgentControl Registry:",
		"service=on",
		"service_health=ok",
		"mode=single_sqlite",
		"shared_db=true",
		"agents=durable",
		"runtime_projection=local_only:global_writer_not_configured@runtime_in_memory",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected debug output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestHandleCommand_CollabAndPanelUseCompletionMailboxWithoutDisplayMirror(t *testing.T) {
	ctx := context.Background()
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
	sessionStore := runtimechat.NewInMemoryStorage()
	root := runtimechat.NewSession("collab-user")
	root.ID = "completion-root"
	require.NoError(t, sessionStore.Save(ctx, root))
	child := runtimechat.NewSession("collab-user")
	child.ID = "completion-child"
	child.SetContext(toolbroker.AgentSessionContextParentSessionID, root.ID)
	child.SetContext(toolbroker.AgentSessionContextRootSessionID, root.ID)
	child.SetContext(toolbroker.AgentSessionContextPath, "/root/completion-child")
	child.SetContext(toolbroker.AgentSessionContextDepth, 1)
	require.NoError(t, sessionStore.Save(ctx, child))
	host := &localChatRuntimeHost{
		EventStore:   runtimeStore,
		SessionStore: sessionStore,
		SessionUser:  "collab-user",
	}
	host.ActorRegistry = newLocalActorRegistry(host)
	session := &ChatSession{
		RuntimeSession:      root,
		SessionUserID:       "collab-user",
		SelectedAgentTarget: "/root/completion-child",
		LocalRuntimeHost:    host,
	}

	completion := toolbroker.BuildSubagentCompletionMailboxMessage(root.ID, child.ID, "/root/completion-child", "worker", runtimechat.EventSessionEnd, map[string]interface{}{
		"status": "done",
	})
	_, _, err := runtimeStore.AppendAgentControlMailbox(ctx, root.ID, completion)
	require.NoError(t, err)
	events, err := runtimeStore.ListEvents(ctx, root.ID, 0, 20)
	require.NoError(t, err)
	for _, event := range events {
		if event.Type == "subagent.completed" {
			t.Fatalf("test setup should not write display mirror event, got %#v", event)
		}
	}

	collabOutput := captureStdout(t, func() {
		if quit := handleCommand(session, "/collab 5", false); quit {
			t.Fatal("collab command should not quit")
		}
	})
	for _, expected := range []string{
		"Parent Mailbox Timeline:",
		"source=agent_control+mailbox",
		"kind=subagent.completed",
		"action=agent.completed",
		"completion-child",
	} {
		if !strings.Contains(collabOutput, expected) {
			t.Fatalf("expected collab output to contain %q without display mirror, got:\n%s", expected, collabOutput)
		}
	}

	panelOutput := captureStdout(t, func() {
		if quit := handleCommand(session, "/agents panel 5", false); quit {
			t.Fatal("agents panel command should not quit")
		}
	})
	for _, expected := range []string{
		"Agent Control Panel:",
		"Mailbox:",
		"kind=subagent.completed",
		"action=agent.completed",
		"completion-child",
	} {
		if !strings.Contains(panelOutput, expected) {
			t.Fatalf("expected panel output to contain %q without display mirror, got:\n%s", expected, panelOutput)
		}
	}

	result, err := host.ActorRegistry.ReadEvents(ctx, toolbroker.ReadAgentEventsArgs{
		SessionID:   root.ID,
		MailboxOnly: true,
		Limit:       5,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	if len(result.Events) != 1 {
		t.Fatalf("expected one mailbox event, got %#v", result.Events)
	}
	if result.Events[0].Type != runtimechat.EventMailboxReceived || result.Events[0].Payload["kind"] != toolbroker.SubagentCompletionMailboxKind {
		t.Fatalf("expected mailbox completion event, got %#v", result.Events[0])
	}
}

func TestHandleCommand_AgentsPanelFollowWaitsForMailboxUpdate(t *testing.T) {
	ctx := context.Background()
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
	sessionStore := runtimechat.NewInMemoryStorage()
	sessionManager := runtimechat.NewSessionManager(sessionStore, nil)
	root := runtimechat.NewSession("panel-user")
	root.ID = "panel-follow-root"
	require.NoError(t, sessionStore.Save(ctx, root))
	host := &localChatRuntimeHost{
		EventStore:   runtimeStore,
		SessionStore: sessionStore,
		SessionUser:  "panel-user",
	}
	host.ActorRegistry = newLocalActorRegistry(host)
	session := &ChatSession{
		RuntimeSession:   root,
		SessionManager:   sessionManager,
		SessionUserID:    "panel-user",
		LocalRuntimeHost: host,
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _, _ = runtimeStore.AppendMailbox(ctx, root.ID, toolbroker.BuildAgentMailboxMessage(
			"child",
			"parent",
			"panel follow mailbox update",
			false,
		))
	}()
	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/agents panel follow timeout=500ms 5", false); quit {
			t.Fatal("agents panel follow command should not quit")
		}
	})
	for _, expected := range []string{
		"Agent Control Panel:",
		"Panel Follow:",
		"follow=waiting",
		"follow=update session=panel-follow-root",
		"panel follow mailbox update",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected agents panel follow output to contain %q, got:\n%s", expected, output)
		}
	}
	if session.SelectedAgentTarget != "" {
		t.Fatalf("panel follow should not change selected target, got %q", session.SelectedAgentTarget)
	}
}

func TestHandleCommand_AgentsPanelCanSwitchTarget(t *testing.T) {
	ctx := context.Background()
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
	sessionStore := runtimechat.NewInMemoryStorage()
	sessionManager := runtimechat.NewSessionManager(sessionStore, nil)
	root := runtimechat.NewSession("panel-user")
	root.ID = "panel-nav-root"
	require.NoError(t, sessionStore.Save(ctx, root))
	first := runtimechat.NewSession("panel-user")
	first.ID = "panel-nav-first"
	first.SetContext(toolbroker.AgentSessionContextParentSessionID, root.ID)
	first.SetContext(toolbroker.AgentSessionContextRootSessionID, root.ID)
	first.SetContext(toolbroker.AgentSessionContextPath, "/root/panel-nav-first")
	first.SetContext(toolbroker.AgentSessionContextDepth, 1)
	require.NoError(t, sessionStore.Save(ctx, first))
	second := runtimechat.NewSession("panel-user")
	second.ID = "panel-nav-second"
	second.SetContext(toolbroker.AgentSessionContextParentSessionID, root.ID)
	second.SetContext(toolbroker.AgentSessionContextRootSessionID, root.ID)
	second.SetContext(toolbroker.AgentSessionContextPath, "/root/panel-nav-second")
	second.SetContext(toolbroker.AgentSessionContextDepth, 1)
	require.NoError(t, sessionStore.Save(ctx, second))
	host := &localChatRuntimeHost{
		EventStore:   runtimeStore,
		SessionStore: sessionStore,
		SessionUser:  "panel-user",
	}
	host.ActorRegistry = newLocalActorRegistry(host)
	session := &ChatSession{
		RuntimeSession:   root,
		SessionManager:   sessionManager,
		SessionUserID:    "panel-user",
		LocalRuntimeHost: host,
	}
	host.BaseSession = session

	targetOutput := captureStdout(t, func() {
		if quit := handleCommand(session, "/agents panel target /root/panel-nav-second 5", false); quit {
			t.Fatal("agents panel target command should not quit")
		}
	})
	if session.SelectedAgentTarget != "/root/panel-nav-second" {
		t.Fatalf("expected selected target to switch, got %q", session.SelectedAgentTarget)
	}
	if !strings.Contains(targetOutput, "selected=/root/panel-nav-second") {
		t.Fatalf("expected panel target output to show selected target, got:\n%s", targetOutput)
	}
	stored, err := sessionManager.Get(ctx, root.ID)
	require.NoError(t, err)
	if got := runtimeSessionContextString(stored, chatRuntimeContextSelectedAgent); got != "/root/panel-nav-second" {
		t.Fatalf("expected selected target to persist, got %q", got)
	}

	nextOutput := captureStdout(t, func() {
		if quit := handleCommand(session, "/agents panel next 5", false); quit {
			t.Fatal("agents panel next command should not quit")
		}
	})
	if session.SelectedAgentTarget != "/root/panel-nav-first" {
		t.Fatalf("expected selected target to wrap to first, got %q\n%s", session.SelectedAgentTarget, nextOutput)
	}
	prevOutput := captureStdout(t, func() {
		if quit := handleCommand(session, "/agents panel prev 5", false); quit {
			t.Fatal("agents panel prev command should not quit")
		}
	})
	if session.SelectedAgentTarget != "/root/panel-nav-second" {
		t.Fatalf("expected selected target to move back to second, got %q\n%s", session.SelectedAgentTarget, prevOutput)
	}
}

func TestChatAgentPanelModalControllerNavigatesAndSelectsTarget(t *testing.T) {
	ctx := context.Background()
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
	sessionStore := runtimechat.NewInMemoryStorage()
	sessionManager := runtimechat.NewSessionManager(sessionStore, nil)
	root := runtimechat.NewSession("panel-user")
	root.ID = "panel-modal-root"
	require.NoError(t, sessionStore.Save(ctx, root))
	first := runtimechat.NewSession("panel-user")
	first.ID = "panel-modal-first"
	first.SetContext(toolbroker.AgentSessionContextParentSessionID, root.ID)
	first.SetContext(toolbroker.AgentSessionContextRootSessionID, root.ID)
	first.SetContext(toolbroker.AgentSessionContextPath, "/root/panel-modal-first")
	first.SetContext(toolbroker.AgentSessionContextDepth, 1)
	require.NoError(t, sessionStore.Save(ctx, first))
	second := runtimechat.NewSession("panel-user")
	second.ID = "panel-modal-second"
	second.SetContext(toolbroker.AgentSessionContextParentSessionID, root.ID)
	second.SetContext(toolbroker.AgentSessionContextRootSessionID, root.ID)
	second.SetContext(toolbroker.AgentSessionContextPath, "/root/panel-modal-second")
	second.SetContext(toolbroker.AgentSessionContextDepth, 1)
	require.NoError(t, sessionStore.Save(ctx, second))
	host := &localChatRuntimeHost{
		EventStore:   runtimeStore,
		SessionStore: sessionStore,
		SessionUser:  "panel-user",
	}
	host.ActorRegistry = newLocalActorRegistry(host)
	session := &ChatSession{
		RuntimeSession:   root,
		SessionManager:   sessionManager,
		SessionUserID:    "panel-user",
		LocalRuntimeHost: host,
	}
	host.BaseSession = session
	state := newChatAgentPanelModalState(5)
	controller := newChatAgentPanelModalController(session, &state, "Agent Panel> ")

	controller.Navigate(1)
	if state.Cursor != 1 {
		t.Fatalf("expected down navigation to select second row, got cursor=%d", state.Cursor)
	}
	controller.MovePane(1)
	if state.Pane != chatAgentPanelPaneMailbox {
		t.Fatalf("expected right navigation to focus mailbox pane, got %s", state.Pane.String())
	}
	controller.MovePane(1)
	if state.Pane != chatAgentPanelPaneTimeline {
		t.Fatalf("expected right navigation to focus timeline pane, got %s", state.Pane.String())
	}
	controller.Select()
	if session.SelectedAgentTarget != "/root/panel-modal-second" {
		t.Fatalf("expected modal enter to select second target, got %q", session.SelectedAgentTarget)
	}
	stored, err := sessionManager.Get(ctx, root.ID)
	require.NoError(t, err)
	if got := runtimeSessionContextString(stored, chatRuntimeContextSelectedAgent); got != "/root/panel-modal-second" {
		t.Fatalf("expected modal selection to persist, got %q", got)
	}
	lines := chatAgentPanelModalLines(session, &state)
	joined := strings.Join(lines, "\n")
	for _, expected := range []string{
		"mode=follow pane=timeline cursor=2",
		"selected=/root/panel-modal-second",
		">* [2] /root/panel-modal-second",
		"Timeline: <focused>",
		"Enter 设为 target",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected modal lines to contain %q, got:\n%s", expected, joined)
		}
	}
}

func TestRunChatAgentPanelModalInterruptMarksSessionInterrupted(t *testing.T) {
	session := &ChatSession{}
	err := handleChatAgentPanelModalInputError(session, ui.ErrInteractiveInputInterrupted)
	if err != io.EOF {
		t.Fatalf("expected interrupt to return io.EOF, got %v", err)
	}
	if !session.IsInterrupted() {
		t.Fatal("expected panel modal interrupt to mark session interrupted")
	}
}

func TestChatAgentPanelModalWatchesTaskWake(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer teamStore.Close()

	session := &ChatSession{
		ActiveTeam: &chatTeamBinding{TeamID: "panel-task-wake-team"},
		LocalRuntimeHost: &localChatRuntimeHost{
			TeamStore: teamStore,
		},
	}
	updates := watchChatAgentPanelModalUpdates(ctx, session)
	taskID, err := teamStore.CreateTask(ctx, team.Task{
		ID:     "panel-task-wake-task",
		TeamID: "panel-task-wake-team",
		Title:  "wake panel",
		Goal:   "wake panel",
		Status: team.TaskStatusPending,
	})
	require.NoError(t, err)
	require.NoError(t, teamStore.UpdateTaskStatus(ctx, taskID, team.TaskStatusRunning, "wake panel"))

	select {
	case <-updates:
	case <-time.After(time.Second):
		t.Fatal("expected panel modal update from task wake")
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	originalStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = writer

	defer func() {
		os.Stdout = originalStdout
	}()

	fn()

	_ = writer.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	_ = reader.Close()

	return string(data)
}
