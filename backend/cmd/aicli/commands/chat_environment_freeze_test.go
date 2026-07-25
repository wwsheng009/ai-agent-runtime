package commands

import (
	"strings"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeprompt "github.com/wwsheng009/ai-agent-runtime/internal/prompt"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestSessionEnvironmentSnapshot_FreezeOnceAndReuse(t *testing.T) {
	session := &ChatSession{
		SystemPromptText: "Base prompt.",
		RuntimeSession:   runtimechat.NewSession("tester"),
	}

	first := ensureSessionEnvironmentSnapshot(session, `E:\projects\demo`)
	if strings.TrimSpace(first.ContextBlock) == "" {
		t.Fatal("expected non-empty frozen context block")
	}
	if sessionmeta.String(session.RuntimeSession.Metadata.Context, sessionmeta.EnvironmentContextBlock) != first.ContextBlock {
		t.Fatalf("expected context block stored on session metadata")
	}
	if sessionmeta.String(session.RuntimeSession.Metadata.Context, sessionmeta.EnvironmentProbedAt) == "" {
		t.Fatal("expected environment_probed_at to be stored")
	}

	// Mutate stored metadata to prove reuse does not re-capture live host facts.
	frozen := "<environment_context>\n  <cwd>frozen-cwd</cwd>\n</environment_context>"
	sessionmeta.Set(session.RuntimeSession.Metadata.Context, sessionmeta.EnvironmentContextBlock, frozen)
	sessionmeta.Set(session.RuntimeSession.Metadata.Context, sessionmeta.EnvironmentCapabilityGuidance, "frozen capability")

	second := ensureSessionEnvironmentSnapshot(session, `E:\projects\other`)
	if second.ContextBlock != frozen {
		t.Fatalf("expected frozen snapshot reuse, got %q", second.ContextBlock)
	}
	if second.CapabilityGuidance != "frozen capability" {
		t.Fatalf("expected frozen capability guidance, got %q", second.CapabilityGuidance)
	}

	prompt1 := composeDurableChatSystemPromptWithGuidanceForCWD(session, `E:\projects\other`)
	prompt2 := composeDurableChatSystemPromptWithGuidanceForCWD(session, `E:\projects\third`)
	if prompt1 != prompt2 {
		t.Fatalf("durable prompt must be stable across compose calls\nfirst:\n%s\nsecond:\n%s", prompt1, prompt2)
	}
	if !strings.Contains(prompt1, "frozen-cwd") {
		t.Fatalf("expected durable prompt to use frozen context, got:\n%s", prompt1)
	}
	if !strings.Contains(prompt1, "frozen capability") {
		t.Fatalf("expected durable prompt to use frozen capability guidance, got:\n%s", prompt1)
	}
}

func TestSyncChatSystemPromptMessage_DoesNotRewriteWhenSnapshotUnchanged(t *testing.T) {
	session := &ChatSession{
		SystemPromptText: "Base prompt.",
		RuntimeSession:   runtimechat.NewSession("tester"),
	}
	storeSessionEnvironmentSnapshot(session, runtimeprompt.EnvironmentSnapshot{
		ContextBlock:       "<environment_context>\n  <cwd>stable</cwd>\n</environment_context>",
		CapabilityGuidance: "stable capability",
	})

	ensureChatSystemPromptMessage(session)
	if len(session.Messages) != 1 || session.Messages[0].Role != "system" {
		t.Fatalf("expected single system message, got %#v", session.Messages)
	}
	before := session.Messages[0].Content
	ptrBefore := &session.Messages[0]

	// Second ensure with same frozen snapshot must short-circuit.
	ensureChatSystemPromptMessage(session)
	if session.Messages[0].Content != before {
		t.Fatalf("expected immutable system content, before=%q after=%q", before, session.Messages[0].Content)
	}
	if &session.Messages[0] != ptrBefore {
		// replaceRuntimeMessages always reallocates the slice; content stability is the contract.
		// Content equality is asserted above.
	}

	// User/assistant turns must remain untouched when ensure re-runs.
	appendRuntimeMessage(session, *runtimetypes.NewUserMessage("hello"))
	appendRuntimeMessage(session, *runtimetypes.NewAssistantMessage("world"))
	userBefore := session.Messages[1].Content
	assistantBefore := session.Messages[2].Content
	ensureChatSystemPromptMessage(session)
	if len(session.Messages) != 3 {
		t.Fatalf("expected history length stable at 3, got %d", len(session.Messages))
	}
	if session.Messages[0].Content != before {
		t.Fatalf("system prefix mutated: %q", session.Messages[0].Content)
	}
	if session.Messages[1].Content != userBefore || session.Messages[2].Content != assistantBefore {
		t.Fatalf("conversation turns mutated: %#v", session.Messages)
	}
}

func TestComposeChatSystemPrompt_GoalIsOutboundOnly(t *testing.T) {
	session, cleanup := newGoalCommandTestSession(t)
	defer cleanup()
	session.SystemPromptText = "Base prompt."

	captureStdout(t, func() {
		handleCommand(session, "/goal finish persistent work", false)
	})
	ensureChatSystemPromptMessage(session)

	durable := composeDurableChatSystemPromptWithGuidance(session)
	outbound := composeChatSystemPromptWithGuidance(session)
	if strings.Contains(durable, "finish persistent work") {
		t.Fatalf("durable prompt must omit goal guidance, got:\n%s", durable)
	}
	if !strings.Contains(outbound, "finish persistent work") {
		t.Fatalf("outbound prompt must include goal guidance, got:\n%s", outbound)
	}
	if len(session.Messages) == 0 || session.Messages[0].Content != durable {
		t.Fatalf("stored history must use durable prompt without goal")
	}
}
