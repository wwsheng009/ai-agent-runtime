package skills

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/workspace"
)

func TestEnsureSessionEnvironmentSnapshot_FreezeOnce(t *testing.T) {
	session := chat.NewSession("user-1")
	first := ensureSessionEnvironmentSnapshot(session, `E:\projects\demo`)
	if strings.TrimSpace(first.ContextBlock) == "" {
		t.Fatal("expected non-empty context block")
	}
	stored := sessionmeta.String(session.Metadata.Context, sessionmeta.EnvironmentContextBlock)
	if stored != first.ContextBlock {
		t.Fatalf("expected stored context block, got %q", stored)
	}

	sessionmeta.Set(session.Metadata.Context, sessionmeta.EnvironmentContextBlock, "frozen-block")
	sessionmeta.Set(session.Metadata.Context, sessionmeta.EnvironmentCapabilityGuidance, "frozen-cap")
	second := ensureSessionEnvironmentSnapshot(session, `E:\projects\other`)
	if second.ContextBlock != "frozen-block" {
		t.Fatalf("expected freeze reuse, got %q", second.ContextBlock)
	}
	if second.CapabilityGuidance != "frozen-cap" {
		t.Fatalf("expected frozen capability, got %q", second.CapabilityGuidance)
	}
}

func TestBuildAgentContextMessages_UsesFrozenEnvironmentBlock(t *testing.T) {
	contextValues := map[string]interface{}{
		"workspace_path":                      `E:\projects\demo`,
		sessionmeta.EnvironmentContextBlock:   "<environment_context>\n  <cwd>frozen</cwd>\n</environment_context>",
		sessionmeta.EnvironmentCapabilityGuidance: "Measured capability guidance (frozen).",
		"current_date":                        "2099-01-01",
		"timezone":                            "FROZEN",
		"os":                                  "frozen-os",
		"shell":                               "frozen-shell",
	}
	messages := buildAgentContextMessages(contextValues, &workspace.WorkspaceContext{Summary: "summary"})
	if len(messages) == 0 {
		t.Fatal("expected context messages")
	}
	foundEnv := false
	foundShell := false
	for _, message := range messages {
		if strings.Contains(message.Content, "Environment context:") && strings.Contains(message.Content, "frozen") {
			foundEnv = true
		}
		if strings.Contains(message.Content, "Shell guidance:") && strings.Contains(message.Content, "Measured capability guidance (frozen).") {
			foundShell = true
		}
		if strings.Contains(message.Content, "Runtime context summary:") {
			if strings.Contains(message.Content, sessionmeta.EnvironmentContextBlock) {
				t.Fatalf("runtime summary must not leak freeze keys: %s", message.Content)
			}
			if strings.Contains(message.Content, `"current_date"`) ||
				strings.Contains(message.Content, `"timezone"`) ||
				strings.Contains(message.Content, `"os"`) ||
				strings.Contains(message.Content, `"shell"`) {
				t.Fatalf("runtime summary must not leak environment fact keys: %s", message.Content)
			}
		}
	}
	if !foundEnv {
		t.Fatalf("expected frozen environment context message, got %#v", messages)
	}
	if !foundShell {
		t.Fatalf("expected shell guidance with frozen capability, got %#v", messages)
	}
}

func TestStripLeadingContextMessages_RemovesEphemeralPrefix(t *testing.T) {
	contextMessages := []types.Message{
		*types.NewSystemMessage("Environment context:\n<environment_context>frozen</environment_context>"),
		*types.NewSystemMessage("Shell guidance:\n- prefer toolkit grep"),
	}
	history := []types.Message{
		*types.NewUserMessage("hello"),
		*types.NewAssistantMessage("hi"),
	}
	prepared := prependContextMessages(history, contextMessages)
	if len(prepared) != 4 {
		t.Fatalf("expected 4 prepared messages, got %d", len(prepared))
	}
	// Simulate ReAct appending the new turn.
	prepared = append(prepared, *types.NewUserMessage("hello"), *types.NewAssistantMessage("done"))

	durable := stripLeadingContextMessages(prepared, contextMessages)
	if len(durable) != 4 {
		t.Fatalf("expected 4 durable messages after strip, got %d: %#v", len(durable), durable)
	}
	if durable[0].Role != "user" || durable[0].Content != "hello" {
		t.Fatalf("unexpected first durable message: %#v", durable[0])
	}
	if durable[3].Role != "assistant" || durable[3].Content != "done" {
		t.Fatalf("unexpected last durable message: %#v", durable[3])
	}

	// Mismatched prefix must not strip arbitrary history.
	mismatch := stripLeadingContextMessages(history, contextMessages)
	if len(mismatch) != 2 {
		t.Fatalf("expected mismatch to leave history intact, got %d", len(mismatch))
	}
}
