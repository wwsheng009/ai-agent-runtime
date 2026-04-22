package commands

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fatih/color"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
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
