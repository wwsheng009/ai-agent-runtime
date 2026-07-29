package commands

import (
	"context"
	"fmt"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
	if info.SupportsFast {
		t.Fatal("expected non-codex protocol to omit Fast support")
	}
	if info.IsFast {
		t.Fatal("expected IsFast false when Fast is unsupported")
	}
}

func TestBuildChatSessionInfo_IncludesFastForCodexProtocol(t *testing.T) {
	session := &ChatSession{
		ProviderName: "codex_ee",
		Provider: config.Provider{
			Enabled:  true,
			Protocol: "codex",
			BaseURL:  "https://example.com",
		},
		Adapter:  &adapter.CodexAdapter{},
		Model:    "gpt-5.2-codex",
		FastMode: true,
		Stream:   false,
	}

	info := buildChatSessionInfo(session)
	if !info.SupportsFast {
		t.Fatal("expected codex protocol to support Fast")
	}
	if !info.IsFast {
		t.Fatal("expected IsFast true when FastMode enabled on codex")
	}

	session.FastMode = false
	info = buildChatSessionInfo(session)
	if !info.SupportsFast || info.IsFast {
		t.Fatalf("expected SupportsFast with IsFast=false, got %+v", info)
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
	t.Setenv("NO_COLOR", "1")
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

func TestPrintSessionInfo_RendersCompactLineage(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = "session-compact-1"
	runtimeSession.Metadata.Title = "检查登录流程为什么失败 · compact #2"
	runtimeSession.Metadata.Context = map[string]interface{}{
		runtimechat.ContextCompactGeneration:    2,
		runtimechat.ContextCompactRootTitle:     "检查登录流程为什么失败",
		runtimechat.ContextCompactRootSessionID: "session-compact-1",
	}
	session := &ChatSession{
		ProviderName:   "openai",
		Provider:       config.Provider{Enabled: true, Protocol: "openai", BaseURL: "https://example.com"},
		Model:          "gpt-4.1",
		RuntimeSession: runtimeSession,
	}

	output := captureStdout(t, func() {
		printSessionInfo(session)
	})
	for _, expected := range []string{
		"Compact Gen:       #2",
		"Compact Root:      检查登录流程为什么失败",
		"Compact Root ID:   session-compact-1",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected compact lineage row %q, got:\n%s", expected, output)
		}
	}

	// printCurrentRuntimeSession reuses the same lineage printer (including Root ID).
	currentOutput := captureStdout(t, func() {
		printCurrentRuntimeSession(session)
	})
	for _, expected := range []string{
		"Title:",
		"检查登录流程为什么失败 · compact #2",
		"Compact Gen:       #2",
		"Compact Root:      检查登录流程为什么失败",
		"Compact Root ID:   session-compact-1",
	} {
		if !strings.Contains(currentOutput, expected) {
			t.Fatalf("expected current-session compact lineage row %q, got:\n%s", expected, currentOutput)
		}
	}
}

func TestPrintSessionInfo_AlignsFollowupMetadataRows(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
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

func TestFormatChatAgentSourceLine(t *testing.T) {
	if got := formatChatAgentSourceLine(nil); got != "" {
		t.Fatalf("expected empty line for nil session, got %q", got)
	}
	if got := formatChatAgentSourceLine(&ChatSession{}); got != "" {
		t.Fatalf("expected empty line when source/path unset, got %q", got)
	}
	if got := formatChatAgentSourceLine(&ChatSession{AgentSource: "builtin"}); got != "builtin" {
		t.Fatalf("expected source-only line, got %q", got)
	}
	if got := formatChatAgentSourceLine(&ChatSession{
		AgentSource:     "builtin",
		AgentSourcePath: "builtin:explore",
	}); got != "builtin · builtin:explore" {
		t.Fatalf("expected builtin path preserved, got %q", got)
	}

	path := filepath.Join(t.TempDir(), "agents", "explore.md")
	got := formatChatAgentSourceLine(&ChatSession{
		AgentSource:     "project",
		AgentSourcePath: path,
	})
	want := "project · " + resolveAbsoluteChatPath(path)
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestPrintSessionInfo_IncludesAgentSource(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	agentPath := filepath.Join(t.TempDir(), ".agents", "agents", "general.md")
	session := &ChatSession{
		ProviderName:    "openai",
		Provider:        config.Provider{Enabled: true, Protocol: "openai", BaseURL: "https://example.com"},
		Model:           "gpt-4.1",
		AgentSource:     "project",
		AgentSourcePath: agentPath,
	}

	output := captureStdout(t, func() {
		printSessionInfo(session)
	})
	expected := fmt.Sprintf("%-18s %s", "Agent Source:", "project · "+resolveAbsoluteChatPath(agentPath))
	if !strings.Contains(output, expected) {
		t.Fatalf("expected agent source row %q, got:\n%s", expected, output)
	}
}

func TestPrintSessionInfo_RendersExplicitReasoningCapability(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
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

	expectedPath := resolveAbsoluteChatPath(fileSessionJSONPath(sessionDir, "session-1", time.Time{}))
	if got := currentRuntimeSessionPath(session); got != expectedPath {
		t.Fatalf("expected session path %q, got %q", expectedPath, got)
	}

	summary := currentRuntimeSessionStoreSummary(session)
	if !strings.Contains(summary, sessionDir) {
		t.Fatalf("expected store summary to include custom dir %q, got %q", sessionDir, summary)
	}
	if !strings.Contains(summary, "(file; custom; default ") {
		t.Fatalf("expected custom store summary, got %q", summary)
	}
}

func TestResolveFileSessionJSONPathPrefersExistingLegacyFlatFile(t *testing.T) {
	sessionDir := t.TempDir()
	legacyPath := filepath.Join(sessionDir, "session-legacy.json")
	if err := os.WriteFile(legacyPath, []byte(`{"id":"session-legacy"}`), 0o644); err != nil {
		t.Fatalf("write legacy session file: %v", err)
	}

	got := resolveFileSessionJSONPath(sessionDir, "session-legacy", time.Time{})
	if got != resolveAbsoluteChatPath(legacyPath) {
		t.Fatalf("expected existing legacy path %q, got %q", legacyPath, got)
	}
}

func TestResolveFileSessionJSONPathUsesDatedPathWhenMissing(t *testing.T) {
	sessionDir := t.TempDir()
	createdAt := time.Date(2026, 7, 25, 12, 0, 0, 0, time.Local)
	want := resolveAbsoluteChatPath(fileSessionJSONPath(sessionDir, "session-missing", createdAt))
	got := resolveFileSessionJSONPath(sessionDir, "session-missing", createdAt)
	if got != want {
		t.Fatalf("expected dated path %q, got %q", want, got)
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
	if !strings.Contains(summary, "(file; default)") {
		t.Fatalf("expected default store summary, got %q", summary)
	}
}

func TestCurrentRuntimeSessionArtifactRootUsesSessionIDWithSQLiteStore(t *testing.T) {
	sessionDir := t.TempDir()
	manager, _, _, err := newChatSessionManager(sessionDir)
	if err != nil {
		t.Fatalf("create sqlite session manager: %v", err)
	}
	t.Cleanup(manager.Stop)
	session := &ChatSession{
		SessionDir:     sessionDir,
		SessionManager: manager,
		RuntimeSession: &runtimechat.Session{ID: "session-1", State: runtimechat.StateActive},
	}
	wantRoot := filepath.Join(sessionDir, "session-1.artifacts")
	if got := currentRuntimeSessionArtifactRoot(session); got != wantRoot {
		t.Fatalf("expected per-session artifact root %q, got %q", wantRoot, got)
	}
	if got := currentRuntimeHTTPArtifactDir(session); got != filepath.Join(wantRoot, "runtime-http") {
		t.Fatalf("unexpected runtime HTTP artifact dir: %q", got)
	}
	if got := currentLocalShellArtifactDir(session); got != filepath.Join(wantRoot, "local-shell") {
		t.Fatalf("unexpected local shell artifact dir: %q", got)
	}
}

func TestPrintCurrentRuntimeSession_IncludesSessionPathAndStore(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
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
		"Session File:      " + resolveAbsoluteChatPath(fileSessionJSONPath(sessionDir, "session-1", time.Time{})),
		"Session Store:     " + sessionDir + " (file; custom; default ",
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
	t.Setenv("NO_COLOR", "1")
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
		"Session File:      " + resolveAbsoluteChatPath(fileSessionJSONPath("sessions", "session-1", time.Time{})),
		"Session Store:     " + resolveAbsoluteChatPath("sessions") + " (file; custom; default ",
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
	t.Setenv("NO_COLOR", "1")
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
	enabled := true

	agentConfigPath := filepath.Join(profileRoot, "agents", "agent-x", "agent.yaml")
	session := &ChatSession{
		ProviderName:        "codex_ee",
		Provider:            config.Provider{Enabled: true, Protocol: "openai", BaseURL: "https://example.com", APIKeys: []string{"key-1"}},
		Model:               "gpt-5.2-code",
		ReasoningEffort:     "medium",
		HTTPDebug:           true,
		Stream:              true,
		NoInteractive:       true,
		JSONOutput:          true,
		JSONEnvelope:        true,
		MCPEnabled:          true,
		MCPStatus:           &MCPStatus{Enabled: true, ToolCount: 7, MCPCount: 2},
		SkillsDebug:         true,
		OutputFormat:        "json",
		ProfileName:         "debug-profile",
		ProfileAgent:        "agent-x",
		ProfileRoot:         profileRoot,
		AgentSource:         "profile",
		AgentSourcePath:     agentConfigPath,
		RuntimeConfigPath:   runtimeConfigPath,
		MCPConfigPath:       mcpConfigPath,
		ResolvedSkillDirs:   []string{skillsDir},
		PermissionMode:      "default",
		ApprovalReuseMode:   chatApprovalReuseTeamReadOnlyShell,
		SelectedAgentTarget: "/root/debug-child",
		SessionDir:          sessionDir,
		InputQueue:          queue,
		RuntimeSession:      &runtimechat.Session{ID: "session-1", State: runtimechat.StateActive, Metadata: runtimechat.SessionMetadata{Summary: "session debug summary"}},
		Logger:              logger,
		Config: &config.Config{
			AICLI: &config.AICLIConfig{
				Subagents: &config.AICLISubagentsConfig{
					Routing: &config.AICLISubagentRoutingConfig{
						Enabled:           &enabled,
						DefaultDifficulty: "normal",
						Levels: map[string]config.AICLISubagentRouteProfile{
							"hard": {
								Provider:        "strong",
								Model:           "strong-model",
								ReasoningEffort: "high",
							},
						},
					},
				},
				Teams: &config.AICLITeamsConfig{
					Routing: &config.AICLISubagentRoutingConfig{
						Enabled:           &enabled,
						DefaultDifficulty: "expert",
						Levels: map[string]config.AICLISubagentRouteProfile{
							"expert": {Provider: "team", Model: "team-model"},
						},
					},
				},
			},
		},
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
		if quit := handleCommand(session, "/debug display", false); quit {
			t.Fatal("expected debug command not to exit")
		}
	})

	expectedFragments := []string{
		"Session:           session-1 [active]",
		fmt.Sprintf("%-18s %s", "Summary:", "session debug summary"),
		"Session File:      " + resolveAbsoluteChatPath(fileSessionJSONPath(sessionDir, "session-1", time.Time{})),
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
		fmt.Sprintf("%-18s %s", "Agent Source:", "profile · "+resolveAbsoluteChatPath(agentConfigPath)),
		fmt.Sprintf("%-18s %s", "Runtime Config Path:", runtimeConfigPath),
		fmt.Sprintf("%-18s %s", "MCP Config Path:", mcpConfigPath),
		fmt.Sprintf("%-18s %s", "Resolved Skill Dirs:", skillsDir),
		fmt.Sprintf("%-18s %s", "Output Format:", "json"),
		fmt.Sprintf("%-18s %s", "No Interactive:", "on"),
		fmt.Sprintf("%-18s %s", "JSON Output:", "on"),
		fmt.Sprintf("%-18s %s", "JSON Envelope:", "on"),
		fmt.Sprintf("%-18s %s", "MCP Enabled:", "on"),
		fmt.Sprintf("%-18s %s", "Debug Mode:", "off"),
		fmt.Sprintf("%-18s %s", "Skills Debug:", "on"),
		fmt.Sprintf("%-18s %s", "Permission Mode:", "default"),
		fmt.Sprintf("%-18s %s", "Approval Reuse:", "team_readonly_shell"),
		fmt.Sprintf("%-18s %s", "Queued Input:", "0 pending"),
		fmt.Sprintf("%-18s %s", "Interaction:", "prompt_visible=true prompt_paste_active=true thinking_active=true streaming_active=true reasoning_active=true complete_block_output=true shutdown=false"),
		fmt.Sprintf("%-18s %s", "Agent Target:", "/root/debug-child"),
		fmt.Sprintf("%-18s %s", "Surface:", "<none>"),
		"Subagent Routing:",
		fmt.Sprintf("%-18s %s", "Routing Enabled:", "on"),
		fmt.Sprintf("%-18s %s", "Default Difficulty:", "normal"),
		"hard: provider=strong model=strong-model reasoning_effort=high",
	}
	for _, expected := range expectedFragments {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestHandleCommand_DebugModeTogglesStatusAndPersists(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("create runtime session: %v", err)
	}
	session := &ChatSession{
		SessionManager: manager,
		RuntimeSession: runtimeSession,
		SessionUserID:  userID,
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/debug", false); quit {
			t.Fatal("debug status command should not quit")
		}
	})
	if !strings.Contains(output, fmt.Sprintf("%-18s %s", "Debug Mode:", "off")) || !strings.Contains(output, "/debug display") {
		t.Fatalf("expected /debug to show status and usage, got:\n%s", output)
	}

	output = captureStdout(t, func() {
		if quit := handleCommand(session, "/debug on", false); quit {
			t.Fatal("debug on command should not quit")
		}
	})
	if !session.DebugMode || !strings.Contains(output, fmt.Sprintf("%-18s %s", "Debug Mode:", "on")) {
		t.Fatalf("expected debug mode on, session=%+v output=%q", session, output)
	}
	stored, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if got, ok := runtimeSessionContextBool(stored, chatRuntimeContextDebugMode); !ok || !got {
		t.Fatalf("expected persisted debug mode=true, got value=%t ok=%t context=%#v", got, ok, stored.Metadata.Context)
	}

	output = captureStdout(t, func() {
		if quit := handleCommand(session, "/debug off", false); quit {
			t.Fatal("debug off command should not quit")
		}
	})
	if session.DebugMode || !strings.Contains(output, fmt.Sprintf("%-18s %s", "Debug Mode:", "off")) {
		t.Fatalf("expected debug mode off, session=%+v output=%q", session, output)
	}
}

func TestHandleCommand_DebugRoutingPrintsSubagentAndTeamRoutingSummary(t *testing.T) {
	enabled := true
	session := &ChatSession{
		Config: &config.Config{
			AICLI: &config.AICLIConfig{
				Subagents: &config.AICLISubagentsConfig{
					Routing: &config.AICLISubagentRoutingConfig{
						Enabled:                        &enabled,
						CompatibilityMode:              "strict",
						DefaultDifficulty:              "normal",
						AllowExplicitProviderOverride:  true,
						AllowExplicitModelOverride:     true,
						AllowExplicitReasoningOverride: true,
						MaxExpertConcurrency:           2,
						Levels: map[string]config.AICLISubagentRouteProfile{
							"hard": {
								Provider:        "strong",
								Model:           "strong-model",
								ReasoningEffort: "high",
								MaxTokens:       12000,
							},
						},
					},
				},
				Teams: &config.AICLITeamsConfig{
					Routing: &config.AICLISubagentRoutingConfig{
						Enabled:           &enabled,
						DefaultDifficulty: "expert",
						Levels: map[string]config.AICLISubagentRouteProfile{
							"expert": {Provider: "team", Model: "team-model"},
						},
					},
				},
			},
		},
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/debug routing", false); quit {
			t.Fatal("debug routing command should not quit")
		}
	})
	for _, expected := range []string{
		"Subagent Routing:",
		fmt.Sprintf("%-18s %s", "Routing Enabled:", "on"),
		fmt.Sprintf("%-18s %s", "Compatibility:", "strict"),
		fmt.Sprintf("%-18s %s", "Default Difficulty:", "normal"),
		fmt.Sprintf("%-18s %s", "Reasoning Override:", "on"),
		fmt.Sprintf("%-18s %s", "Expert Limit:", "2"),
		"hard: provider=strong model=strong-model reasoning_effort=high max_tokens=12000",
		"Team Routing:",
		fmt.Sprintf("%-18s %s", "Routing Source:", "team_independent"),
		"expert: provider=team model=team-model",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected routing output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestHandleCommand_AgentsRoutingTestPrintsDryRunDecision(t *testing.T) {
	enabled := true
	session := &ChatSession{
		ProviderName: "parent",
		Model:        "parent-model",
		Config: &config.Config{
			Providers: config.ProvidersConfig{
				DefaultProvider: "parent",
				Items: map[string]config.Provider{
					"parent": {
						Enabled:      true,
						DefaultModel: "parent-model",
					},
					"strong": {
						Enabled:         true,
						DefaultModel:    "strong-default",
						SupportedModels: []string{"strong-model"},
						ModelCapabilities: map[string]config.ModelCapabilitySpec{
							"strong-model": {
								ReasoningModel:   true,
								ReasoningEfforts: []string{"high"},
							},
						},
					},
				},
			},
			AICLI: &config.AICLIConfig{
				Subagents: &config.AICLISubagentsConfig{
					Routing: &config.AICLISubagentRoutingConfig{
						Enabled:           &enabled,
						DefaultDifficulty: "normal",
						Levels: map[string]config.AICLISubagentRouteProfile{
							"hard": {
								Provider:        "strong",
								Model:           "strong-model",
								ReasoningEffort: "high",
							},
						},
					},
				},
			},
		},
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/agents routing test --role writer --difficulty hard --goal migration --read-only=false", false); quit {
			t.Fatal("agents routing command should not quit")
		}
	})
	for _, expected := range []string{
		"Subagent Route Dry Run",
		"Scope:           subagent",
		"Routing source:  subagent",
		"Routing enabled: true",
		"Role:          writer",
		"Difficulty:    hard",
		"Provider:      strong",
		"Model:         strong-model",
		"Reasoning:     high",
		"Source:        difficulty_level",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected routing dry-run output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestHandleCommand_AgentsRoutingTestSpawnTeamWritePathPreview(t *testing.T) {
	enabled := true
	session := &ChatSession{
		ProviderName: "parent",
		Model:        "parent-model",
		Config: &config.Config{
			Providers: config.ProvidersConfig{
				DefaultProvider: "parent",
				Items: map[string]config.Provider{
					"parent": {Enabled: true, DefaultModel: "parent-model"},
					"strong": {Enabled: true, DefaultModel: "strong-model"},
				},
			},
			AICLI: &config.AICLIConfig{
				Subagents: &config.AICLISubagentsConfig{
					Routing: &config.AICLISubagentRoutingConfig{
						Enabled: &enabled,
						Roles: map[string]map[string]config.AICLISubagentRouteProfile{
							"writer": {
								"hard": {Provider: "strong", Model: "strong-model", ReasoningEffort: "high"},
							},
						},
					},
				},
			},
		},
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/agents routing test --workflow spawn_team --team-id team-1 --teammate member-1 --task task-1 --difficulty hard --write-path src/foo.go", false); quit {
			t.Fatal("agents routing command should not quit")
		}
	})
	for _, expected := range []string{
		"Team Route Dry Run",
		"Scope:           team",
		"Routing source:  subagent_inherited",
		"Workflow:      spawn_team",
		"Team task:     team=team-1 teammate=member-1 task=task-1",
		"Role:          writer",
		"Read only:     false",
		"Write paths:   src/foo.go",
		"Provider:      strong",
		"Model:         strong-model",
		"Reasoning:     high",
		"Source:        role_override",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected routing dry-run output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestHandleCommand_AgentsRoutingTestDefaultsToWritableSpawnAgentPreview(t *testing.T) {
	enabled := true
	session := &ChatSession{
		ProviderName: "parent",
		Model:        "parent-model",
		Config: &config.Config{
			Providers: config.ProvidersConfig{
				DefaultProvider: "parent",
				Items: map[string]config.Provider{
					"parent": {Enabled: true, DefaultModel: "parent-model"},
				},
			},
			AICLI: &config.AICLIConfig{
				Subagents: &config.AICLISubagentsConfig{
					Routing: &config.AICLISubagentRoutingConfig{
						Enabled:           &enabled,
						DefaultDifficulty: "easy",
						Levels: map[string]config.AICLISubagentRouteProfile{
							"easy":   {Provider: "parent", Model: "easy-model"},
							"normal": {Provider: "parent", Model: "normal-model"},
						},
					},
				},
			},
		},
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/agents routing test --role writer --goal update", false); quit {
			t.Fatal("agents routing command should not quit")
		}
	})
	for _, expected := range []string{
		"Read only:     false",
		"Difficulty:    normal (inferred)",
		"Model:         normal-model",
		"difficulty_promoted_by_heuristic",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected routing dry-run output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestHandleCommand_AgentsRoutingTestPrintsInvalidDifficultyWarning(t *testing.T) {
	enabled := true
	session := &ChatSession{
		ProviderName: "parent",
		Model:        "parent-model",
		Config: &config.Config{
			Providers: config.ProvidersConfig{
				DefaultProvider: "parent",
				Items: map[string]config.Provider{
					"parent": {Enabled: true, DefaultModel: "parent-model"},
				},
			},
			AICLI: &config.AICLIConfig{
				Subagents: &config.AICLISubagentsConfig{
					Routing: &config.AICLISubagentRoutingConfig{
						Enabled:           &enabled,
						DefaultDifficulty: "normal",
						Levels: map[string]config.AICLISubagentRouteProfile{
							"normal": {Provider: "parent", Model: "parent-model"},
						},
					},
				},
			},
		},
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/agents routing test --difficulty 复杂 --goal inspect --read-only=true", false); quit {
			t.Fatal("agents routing command should not quit")
		}
	})
	for _, expected := range []string{
		"Difficulty:    normal (default)",
		"difficulty_invalid_defaulted",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected routing dry-run output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestHandleCommand_AgentsRoutingTestPrintsProviderFallbackWarning(t *testing.T) {
	enabled := true
	session := &ChatSession{
		ProviderName: "parent",
		Model:        "parent-model",
		Config: &config.Config{
			Providers: config.ProvidersConfig{
				DefaultProvider: "parent",
				Items: map[string]config.Provider{
					"parent": {Enabled: true, DefaultModel: "parent-model"},
				},
			},
			AICLI: &config.AICLIConfig{
				Subagents: &config.AICLISubagentsConfig{
					Routing: &config.AICLISubagentRoutingConfig{
						Enabled: &enabled,
						Levels: map[string]config.AICLISubagentRouteProfile{
							"hard": {Provider: "missing-provider", Model: "missing-model"},
						},
					},
				},
			},
		},
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/agents routing test --difficulty hard --goal inspect --read-only=true", false); quit {
			t.Fatal("agents routing command should not quit")
		}
	})
	for _, expected := range []string{
		"Provider:      parent",
		"Model:         parent-model",
		"Source:        fallback",
		"provider_unresolved",
		"provider_fallback_parent",
		"model_fallback_parent",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected routing dry-run output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestHandleCommand_AgentsRoutingTestDisabledPreservesLegacyModelOverride(t *testing.T) {
	disabled := false
	session := &ChatSession{
		ProviderName: "parent",
		Model:        "parent-model",
		Config: &config.Config{
			Providers: config.ProvidersConfig{
				DefaultProvider: "parent",
				Items: map[string]config.Provider{
					"parent": {Enabled: true, DefaultModel: "parent-model"},
				},
			},
			AICLI: &config.AICLIConfig{
				Subagents: &config.AICLISubagentsConfig{
					Routing: &config.AICLISubagentRoutingConfig{
						Enabled:           &disabled,
						DefaultDifficulty: "not-a-real-difficulty",
					},
				},
			},
		},
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/agents routing test --provider ignored --model child-model --reasoning-effort high", false); quit {
			t.Fatal("agents routing command should not quit")
		}
	})
	for _, expected := range []string{
		"Routing enabled: false",
		"Provider:      parent",
		"Model:         child-model",
		"Reasoning:     -",
		"Source:        disabled",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected routing dry-run output to contain %q, got:\n%s", expected, output)
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

func TestChatAgentPickerItemsUsesDurableRegistryWithoutSessionList(t *testing.T) {
	ctx := context.Background()
	registry, err := agentcontrol.NewRegistryService(ctx, agentcontrol.RegistryServiceConfig{
		StorePath: filepath.Join(t.TempDir(), "agent-control.sqlite"),
	})
	require.NoError(t, err)
	defer registry.Close()

	sessionStore := &countingSessionStorage{InMemoryStorage: runtimechat.NewInMemoryStorage()}
	root := runtimechat.NewSession("picker-user")
	root.ID = "picker-root"
	require.NoError(t, sessionStore.Save(ctx, root))
	_, err = registry.AgentStore.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:         "picker-worker",
		RootSessionID:   root.ID,
		ParentAgentID:   localRootAgentID(root.ID),
		ParentSessionID: root.ID,
		SessionID:       "picker-worker-session",
		AgentPath:       "/root/picker-worker",
		Depth:           1,
		AgentType:       "worker",
		Status:          agentcontrol.AgentStatusActive,
	})
	require.NoError(t, err)
	_, err = registry.AgentStore.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:         "picker-closed-worker",
		RootSessionID:   root.ID,
		ParentAgentID:   localRootAgentID(root.ID),
		ParentSessionID: root.ID,
		SessionID:       "picker-closed-session",
		AgentPath:       "/root/picker-closed-worker",
		Depth:           1,
		AgentType:       "worker",
		Status:          agentcontrol.AgentStatusClosed,
	})
	require.NoError(t, err)

	host := &localChatRuntimeHost{
		SessionStore:       sessionStore,
		SessionUser:        "picker-user",
		AgentRegistryStore: registry.AgentStore,
	}
	host.ActorRegistry = newLocalActorRegistry(host)
	session := &ChatSession{
		RuntimeSession:   root,
		SessionUserID:    "picker-user",
		LocalRuntimeHost: host,
	}

	agents, err := chatAgentPickerItems(session)
	require.NoError(t, err)
	if len(agents) != 1 {
		t.Fatalf("expected one picker agent from durable registry, got %#v", agents)
	}
	if agents[0].Path != "/root/picker-worker" || agents[0].SessionID != "picker-worker-session" || agents[0].AgentType != "worker" {
		t.Fatalf("unexpected picker agent: %#v", agents[0])
	}
	if got := atomic.LoadInt32(&sessionStore.listCalls); got != 0 {
		t.Fatalf("expected picker fast path not to list all sessions, got list calls=%d", got)
	}

	graphAgents, err := chatAgentGraphItems(session)
	require.NoError(t, err)
	if len(graphAgents) != 2 {
		t.Fatalf("expected graph to include active and closed durable agents, got %#v", graphAgents)
	}
	if got := atomic.LoadInt32(&sessionStore.listCalls); got != 0 {
		t.Fatalf("expected graph fast path not to list all sessions, got list calls=%d", got)
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
		{
			ID:                "agent-1",
			SessionID:         "session-1",
			Path:              "/root/agent-1",
			Status:            "idle",
			AgentType:         "worker",
			Difficulty:        "hard",
			DifficultySource:  "explicit",
			Provider:          "remote",
			Model:             "strong-model",
			ReasoningEffort:   "high",
			RouteSource:       "difficulty_level",
			RouteWarnings:     []string{"provider_fallback_parent"},
			TeamID:            "team-1",
			TeammateID:        "member-1",
			CurrentTaskID:     "task-1",
			CurrentTaskStatus: "running",
		},
	}, "")
	output := strings.Join(lines, "\n")
	for _, expected := range []string{
		"Agent Picker:",
		"[1] /root/agent-1",
		"status=idle",
		"session=session-1",
		"type=worker",
		"difficulty=hard",
		"difficulty_source=explicit",
		"provider=remote",
		"model=strong-model",
		"reasoning=high",
		"route_source=difficulty_level",
		"warnings=provider_fallback_parent",
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
	if _, err := team.AppendTaskDispatchStarted(context.Background(), store, request); err != nil {
		t.Fatalf("AppendTaskDispatchStarted: %v", err)
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
		"#2 " + team.TaskDispatchStartedEvent,
		"#3 " + team.TaskDispatchCompletedEvent,
		"success=true",
		"trace=trace-1",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected dispatch timeline to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestChatTimelineLinesIncludesTaskRouteDetails(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	const teamID = "timeline-route-team"
	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            teamID,
		LeadSessionID: "timeline-root",
		Status:        team.TeamStatusActive,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := team.AppendTaskRouteResolved(context.Background(), store, team.TaskRouteAudit{
		TeamID:    teamID,
		AgentID:   "member-1",
		TaskID:    "task-1",
		SessionID: "session-1",
		Route: &team.TaskExecutionRoute{
			Difficulty:      team.TaskDifficultyHard,
			Provider:        "openai",
			Model:           "gpt-test",
			ReasoningEffort: "high",
			Source:          "difficulty_level",
			Warnings:        []string{"provider_fallback_parent"},
			FallbackUsed:    true,
			FallbackReason:  "provider_fallback_parent",
			Attempt:         2,
		},
		RecordedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AppendTaskRouteResolved: %v", err)
	}

	session := &ChatSession{
		ActiveTeam:       &chatTeamBinding{TeamID: teamID, AgentID: "lead"},
		LocalRuntimeHost: &localChatRuntimeHost{TeamStore: store},
	}
	output := strings.Join(chatTimelineLines(session, 10), "\n")
	for _, expected := range []string{
		"#1 " + team.TaskRouteResolvedEvent,
		"task=task-1",
		"session=session-1",
		"assignee=member-1",
		"via=agent_control.trigger_task",
		"difficulty=hard",
		"provider=openai",
		"model=gpt-test",
		"reasoning=high",
		"route_source=difficulty_level",
		"fallback_used=true",
		"fallback_reason=provider_fallback_parent",
		"warnings=provider_fallback_parent",
		"attempt=2",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected route timeline to contain %q, got:\n%s", expected, output)
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

func TestChatCollabMailboxLineIncludesRouteMetadata(t *testing.T) {
	message := toolbroker.BuildSubagentCompletionMailboxMessage("parent", "child", "/root/child", "worker", runtimechat.EventSessionEnd, map[string]interface{}{
		"status":                 "idle",
		"difficulty":             "hard",
		"difficulty_source":      "explicit",
		"route_provider":         "remote",
		"route_model":            "strong-model",
		"route_reasoning_effort": "high",
		"route_source":           "difficulty_level",
		"route_warnings":         []string{"provider_fallback_parent"},
		"usage_total_tokens":     1200,
	})
	message.Seq = 7

	line := chatCollabMailboxLine(message)
	for _, expected := range []string{
		"kind=subagent.completed",
		"difficulty=hard",
		"difficulty_source=explicit",
		"provider=remote",
		"model=strong-model",
		"reasoning=high",
		"route_source=difficulty_level",
		"usage_total_tokens=1200",
		"warnings=provider_fallback_parent",
	} {
		if !strings.Contains(line, expected) {
			t.Fatalf("expected mailbox line to contain %q, got:\n%s", expected, line)
		}
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
		if quit := handleCommand(session, "/agents panel full 5", false); quit {
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

func TestHandleCommand_AgentsPanelDefaultsToCompactSummary(t *testing.T) {
	session := &ChatSession{
		RuntimeSession:      &runtimechat.Session{ID: "panel-summary-root", State: runtimechat.StateActive},
		SelectedAgentTarget: "/root/selected-worker",
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/agents panel", false); quit {
			t.Fatal("agents panel summary command should not quit")
		}
	})
	for _, expected := range []string{
		"Agent Control Panel:",
		"selected=/root/selected-worker",
		"parent_session=panel-summary-root",
		"/agents panel full",
		"/agents panel follow",
		"/agents panel close",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected compact panel summary to contain %q, got:\n%s", expected, output)
		}
	}
	for _, unexpected := range []string{"Mailbox:", "Timeline:"} {
		if strings.Contains(output, unexpected) {
			t.Fatalf("did not expect compact panel summary to render %q; use /agents panel full for details:\n%s", unexpected, output)
		}
	}
}

func TestChatAgentPanelUsesBoundedRecentWindowAndHandlesNoAgents(t *testing.T) {
	store := &recordingPanelRuntimeStore{
		eventSeq:   1000,
		mailboxSeq: 1000,
		controlSeq: 1000,
	}
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "panel-bounded-root", State: runtimechat.StateActive},
		LocalRuntimeHost: &localChatRuntimeHost{
			EventStore: store,
		},
	}

	output := strings.Join(chatAgentPanelLines(session, 8), "\n")
	for _, expected := range []string{
		"Agent Control Panel:",
		"selected=<none>",
		"Agents:",
		"  <none>",
		"Mailbox:",
		"panel bounded mailbox",
		"Timeline:",
		"  <none>",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected bounded panel output to contain %q, got:\n%s", expected, output)
		}
	}
	if store.controlAfterSeq != 936 || store.controlLimit != 64 {
		t.Fatalf("expected bounded control mailbox read window, got after=%d limit=%d", store.controlAfterSeq, store.controlLimit)
	}
	if store.mailboxAfterSeq != 936 || store.mailboxLimit != 64 {
		t.Fatalf("expected bounded mailbox read window, got after=%d limit=%d", store.mailboxAfterSeq, store.mailboxLimit)
	}
	if store.eventAfterSeq != 936 || store.eventLimit != 64 {
		t.Fatalf("expected bounded event read window, got after=%d limit=%d", store.eventAfterSeq, store.eventLimit)
	}
}

func TestChatAgentPanelTimelineUsesBoundedRecentWindow(t *testing.T) {
	store := &recordingPanelTeamStore{lastSeq: 1000}
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "panel-timeline-root", State: runtimechat.StateActive},
		ActiveTeam:     &chatTeamBinding{TeamID: "panel-team"},
		LocalRuntimeHost: &localChatRuntimeHost{
			TeamStore: store,
		},
	}

	output := strings.Join(chatAgentPanelLines(session, 8), "\n")
	for _, expected := range []string{
		"Timeline:",
		"team=panel-team",
		"recent panel team event",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected bounded panel timeline output to contain %q, got:\n%s", expected, output)
		}
	}
	if store.afterSeq != 936 || store.limit != 64 {
		t.Fatalf("expected bounded team event read window, got after=%d limit=%d", store.afterSeq, store.limit)
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
		if quit := handleCommand(session, "/agents panel full 5", false); quit {
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
		if quit := handleCommand(session, "/debug display", false); quit {
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
		if quit := handleCommand(session, "/agents panel full 5", false); quit {
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
		"mode=follow view=timeline agent_cursor=2",
		"selected=/root/panel-modal-second",
		">* [2] /root/panel-modal-second",
		"Timeline:",
		"Enter 设为 target",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected modal lines to contain %q, got:\n%s", expected, joined)
		}
	}
}

func TestRunChatAgentPanelModalInterruptOnlyClosesPanel(t *testing.T) {
	session := &ChatSession{}
	err := normalizeChatAgentPanelComposerReadError(session, ui.ErrInteractiveInputInterrupted)
	if err != io.EOF {
		t.Fatalf("expected interrupt to return io.EOF, got %v", err)
	}
	if session.IsInterrupted() {
		t.Fatal("expected panel modal interrupt to leave the chat session running")
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

	type readResult struct {
		data []byte
		err  error
	}
	readDone := make(chan readResult, 1)
	go func() {
		data, err := io.ReadAll(reader)
		readDone <- readResult{data: data, err: err}
	}()

	writerClosed := false
	defer func() {
		os.Stdout = originalStdout
		if !writerClosed {
			_ = writer.Close()
		}
		_ = reader.Close()
	}()

	fn()

	os.Stdout = originalStdout
	writerClosed = true
	_ = writer.Close()
	result := <-readDone
	if result.err != nil {
		t.Fatalf("read stdout: %v", result.err)
	}

	return string(result.data)
}

type recordingPanelRuntimeStore struct {
	eventSeq   int64
	mailboxSeq int64
	controlSeq int64

	eventAfterSeq   int64
	eventLimit      int
	mailboxAfterSeq int64
	mailboxLimit    int
	controlAfterSeq int64
	controlLimit    int
}

func (s *recordingPanelRuntimeStore) AppendEvent(context.Context, runtimeevents.Event) (int64, error) {
	return 0, nil
}

func (s *recordingPanelRuntimeStore) ListEvents(_ context.Context, sessionID string, afterSeq int64, limit int) ([]runtimeevents.Event, error) {
	s.eventAfterSeq = afterSeq
	s.eventLimit = limit
	return []runtimeevents.Event{{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: sessionID,
		Payload:   map[string]interface{}{"summary": "recent panel event"},
	}}, nil
}

func (s *recordingPanelRuntimeStore) ListMailbox(_ context.Context, sessionID string, afterSeq int64, limit int) ([]team.MailMessage, error) {
	s.mailboxAfterSeq = afterSeq
	s.mailboxLimit = limit
	return []team.MailMessage{toolbroker.BuildAgentMailboxMessage("child-1", "parent", "panel bounded mailbox", false)}, nil
}

func (s *recordingPanelRuntimeStore) ListAgentControlMailbox(_ context.Context, sessionID string, afterSeq int64, limit int) ([]team.MailMessage, error) {
	s.controlAfterSeq = afterSeq
	s.controlLimit = limit
	return []team.MailMessage{toolbroker.BuildAgentMailboxMessage("child-1", "parent", "panel bounded control mailbox", false)}, nil
}

func (s *recordingPanelRuntimeStore) LastEventSeq(context.Context, string) (int64, error) {
	return s.eventSeq, nil
}

func (s *recordingPanelRuntimeStore) LastMailboxSeq(context.Context, string) (int64, error) {
	return s.mailboxSeq, nil
}

func (s *recordingPanelRuntimeStore) LastAgentControlMailboxSeq(context.Context, string) (int64, error) {
	return s.controlSeq, nil
}

type recordingPanelTeamStore struct {
	team.Store
	lastSeq  int64
	afterSeq int64
	limit    int
}

func (s *recordingPanelTeamStore) ListTeamEvents(_ context.Context, filter team.TeamEventFilter) ([]team.TeamEventRecord, error) {
	s.afterSeq = filter.AfterSeq
	s.limit = filter.Limit
	return []team.TeamEventRecord{{
		Seq: filter.AfterSeq + 1,
		TeamEvent: team.TeamEvent{
			Type:   "task.completed",
			TeamID: filter.TeamID,
			Payload: map[string]interface{}{
				"task_id": "recent-task",
				"summary": "recent panel team event",
			},
		},
	}}, nil
}

func (s *recordingPanelTeamStore) LastTeamEventSeq(context.Context, string) (int64, error) {
	return s.lastSeq, nil
}
