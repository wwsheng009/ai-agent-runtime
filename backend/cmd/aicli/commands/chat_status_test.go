package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

func TestHandleCommand_StatusPrintsSessionSummaryAndDoesNotEnterChatFlow(t *testing.T) {
	baseDir := t.TempDir()
	workspaceRoot := filepath.Join(baseDir, "status-workspace")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "AGENTS.md"), []byte("Stay within the workspace boundary."), 0o644); err != nil {
		t.Fatalf("write agents md: %v", err)
	}

	logger := NewChatLogger("OpenAI-go-away", "openai", "gpt-5.4-mini", false, "http://localhost:8080/v1")
	scope := aicliLogScope{TurnID: "turn-1", RequestID: "req-1"}
	logger.LogResponse(scope, map[string]interface{}{
		"usage": map[string]interface{}{
			"input_tokens":  21,
			"output_tokens": 13,
			"total_tokens":  34,
		},
	}, nil, false, nil, 12)

	oldVersion := chatStatusVersion
	oldBuildTime := chatStatusBuildTime
	SetChatStatusBuildInfo("v0.128.0", "unknown")
	t.Cleanup(func() {
		chatStatusVersion = oldVersion
		chatStatusBuildTime = oldBuildTime
	})

	session := &ChatSession{
		ProviderName:    "OpenAI-go-away",
		Provider:        config.Provider{Enabled: true, Protocol: "openai", BaseURL: "http://localhost:8080"},
		Model:           "gpt-5.4-mini",
		ReasoningEffort: "xhigh",
		BaseURL:         "http://localhost:8080/v1",
		PermissionMode:  runtimepolicy.ModeBypassPermissions,
		ProfileRoot:     workspaceRoot,
		ProfileContext:  map[string]interface{}{"collaboration_mode": "default"},
		RuntimeSession:  &runtimechat.Session{ID: "019de76b-2481-7130-b902-f6166e6d2b96", State: runtimechat.StateActive},
		Logger:          logger,
		TokenCount:      2137949,
		Messages:        nil,
	}

	beforeMessages := len(session.Messages)
	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/status", false); quit {
			t.Fatal("expected status command not to exit")
		}
	})

	if got := len(session.Messages); got != beforeMessages {
		t.Fatalf("expected status command to avoid mutating chat messages, before=%d after=%d", beforeMessages, got)
	}

	expectedFragments := []string{
		">_ AI CLI (v0.128.0)",
		"Model:",
		"gpt-5.4-mini (reasoning xhigh)",
		"Model provider:",
		"OpenAI-go-away - http://localhost:8080/v1",
		"Directory:",
		"status-workspace",
		"Permissions:",
		"Full Access",
		"Agents.md:",
		"AGENTS.md",
		"Collaboration mode:",
		"Default",
		"Session:",
		"019de76b-2481-7130-b902-f6166e6d2b96",
		"Context used:",
		"2137949 / 256000 (835%)",
		"Token count:",
		"2.1m",
		"Token usage:",
		"34 total (21 input + 13 output)",
		"Limits:",
		"data not available yet",
	}
	for _, expected := range expectedFragments {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected output to contain %q, got:\n%s", expected, output)
		}
	}
	if !strings.Contains(output, "╭") || !strings.Contains(output, "╰") {
		t.Fatalf("expected boxed status output, got:\n%s", output)
	}
}

func TestBuildChatStatusTokenCountValue_FormatsCumulativeCount(t *testing.T) {
	session := &ChatSession{TokenCount: 2137949}

	if got := buildChatStatusTokenCountValue(session); got != "2.1m" {
		t.Fatalf("expected compact cumulative token count, got %q", got)
	}
}

func TestBuildChatStatusContextUsedValue_UsesContextWindow(t *testing.T) {
	session := &ChatSession{
		TokenCount:              90000,
		ContextWindowTokenCount: 100000,
	}

	if got := buildChatStatusContextUsedValue(session); got != "90000 / 100000 (90%)" {
		t.Fatalf("expected context usage percentage, got %q", got)
	}
}
