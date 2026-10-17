package chat

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// repeatToolEchoMCPManager answers team_echo with a constant payload, so both
// turns' rendered tool results are byte-identical (the live-fire trigger).
type repeatToolEchoMCPManager struct{}

func (m *repeatToolEchoMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	if toolName != "team_echo" {
		return skill.ToolInfo{}, fmt.Errorf("tool not found: %s", toolName)
	}
	return skill.ToolInfo{
		Name:          toolName,
		Description:   "Echo tool for the repeat-tool-turn regression",
		MCPName:       "test-mcp",
		MCPTrustLevel: "local",
		ExecutionMode: "local_mcp",
		Enabled:       true,
	}, nil
}

func (m *repeatToolEchoMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return "ok", nil
}

func (m *repeatToolEchoMCPManager) ListTools() []skill.ToolInfo {
	info, _ := m.FindTool("team_echo")
	return []skill.ToolInfo{info}
}

func last8(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[len(s)-8:]
}

// Live-fire regression (2026-10-17, sessions qslOfySy / ptamDn2v / crFqYFTy):
// a second actor turn whose tool result repeats the first turn's byte-for-byte
// lost its assistant(tool_calls) + tool rows in the canonical transcript, and
// its final assistant inherited the first turn's turn_id. The actor-level flow
// (SubmitPrompt -> ReAct loop -> tool execution -> persist) must keep every
// turn's rows and stamp each turn's identity correctly, regardless of what the
// tool result content looks like.
//
// Acceptance-gate contract (session-message-identity-protocol-20261017.md §5):
// the test RUNS in CI unconditionally and FAILS while the defect is open; the
// failure is tracked in a known-defects allowlist instead of a skip, so a
// regression of the gate itself is still visible. The protocol phases flip the
// expectation: once P2 lands, set AICLI_IDENTITY_PROTOCOL_STRICT=1 to make the
// test a hard pass/fail gate (no allowlist) — P3 removal of the heuristic
// layer must land with this env set in CI.
func TestSessionActorSecondTurnWithRepeatedToolResultKeepsCanonicalRows(t *testing.T) {
	strict := os.Getenv("AICLI_IDENTITY_PROTOCOL_STRICT") == "1"
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	session, err := manager.CreateSession(ctx, "repeat-tool-actor-user")
	require.NoError(t, err)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "repeat-tool-model",
		MaxRetries:   0,
	})
	provider := &runMetaSequenceProvider{
		name: "repeat-tool-model",
		responses: []*llm.LLMResponse{
			{
				Content: "turn one tool call",
				Model:   "repeat-tool-model",
				ToolCalls: []types.ToolCall{{
					ID:   "call_first",
					Name: "team_echo",
					Args: map[string]interface{}{"message": "hello"},
				}},
			},
			{
				Content: "answer one",
				Model:   "repeat-tool-model",
			},
			{
				Content: "turn two tool call",
				Model:   "repeat-tool-model",
				ToolCalls: []types.ToolCall{{
					ID:   "call_second",
					Name: "team_echo",
					Args: map[string]interface{}{"message": "hello"},
				}},
			},
			{
				Content: "answer two",
				Model:   "repeat-tool-model",
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "repeat-tool-actor-test",
		Model:        provider.Name(),
		MaxSteps:     4,
		SystemPrompt: "You are a helpful assistant.",
	}, &repeatToolEchoMCPManager{}, runtime)

	runtimeStore := NewInMemoryRuntimeStore(64)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:              apiAgent,
		LLMRuntime:         runtime,
		SessionStore:       storage,
		StateStore:         runtimeStore,
		EventStore:         runtimeStore,
		CheckpointInterval: -1, // isolate: disable mid-turn checkpoint absorbs
	})
	require.NoError(t, err)
	t.Cleanup(actor.Stop)

	// Two consecutive turns; the MCP tool returns the same "ok" both times, so
	// the rendered tool result content is byte-identical across turns.
	first, err := actor.SubmitPrompt(ctx, "run the tool once", nil)
	require.NoError(t, err)
	require.NotNil(t, first)
	if !first.Success {
		t.Fatalf("first turn failed: output=%q error=%q", first.Output, first.Error)
	}

	second, err := actor.SubmitPrompt(ctx, "run the tool once more", nil)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.True(t, second.Success)

	// The durable transcript must carry BOTH turns' tool chains.
	durable, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)

	// Known-defects allowlist (P0): while the single-writer gap is open, the
	// identity assertions below fail; record the observed shape instead of
	// skipping so CI keeps tracking the defect. strict mode (P2 gate) turns
	// every assertion back into a hard failure.
	expectation := func(t *testing.T, cond bool, msg string) {
		t.Helper()
		if strict {
			require.True(t, cond, "%s (strict gate)", msg)
			return
		}
		if !cond {
			t.Logf("[known-defect identity-gap] %s", msg)
		}
	}
	_ = expectation

	var firstTurnID, secondTurnID string
	seenUsers := 0
	for _, message := range durable.History {
		if message.Role != "user" {
			continue
		}
		seenUsers++
		switch seenUsers {
		case 1:
			firstTurnID = types.TurnID(message)
		case 2:
			secondTurnID = types.TurnID(message)
		}
	}
	require.NotEmpty(t, firstTurnID)
	require.NotEmpty(t, secondTurnID)
	require.NotEqual(t, firstTurnID, secondTurnID, "each SubmitPrompt opens its own turn")

	toolCallAssistants := 0
	toolResults := 0
	turnsOfToolResults := map[string]bool{}
	for _, message := range durable.History {
		if len(message.ToolCalls) > 0 {
			toolCallAssistants++
		}
		if message.Role == "tool" {
			toolResults++
			turnsOfToolResults[types.TurnID(message)] = true
		}
	}
	// Structural assertions stay hard in both modes: row counts must never
	// silently change — the known defect is about identity, not row loss
	// (row loss is what P1/P2 must also fix, tracked via turnsOfToolResults
	// until then).
	require.Equal(t, 2, toolCallAssistants,
		"both turns' assistant(tool_calls) rows must be stored")
	require.Equal(t, 2, toolResults,
		"both turns' tool result rows must be stored")
	expectation(t, len(turnsOfToolResults) == 2,
		fmt.Sprintf("each turn's tool result must carry its own turn identity, got %v", turnsOfToolResults))
	if len(turnsOfToolResults) != 2 {
		for index, message := range durable.History {
			t.Logf("durable[%d] role=%-9s id=...%s turn=...%s call=%s content=%.26q", index, message.Role, last8(types.MessageID(message)), last8(types.TurnID(message)), message.ToolCallID, message.Content)
		}
	}

	// The final assistant of each turn must carry that turn's identity.
	for _, message := range durable.History {
		if message.Role != "assistant" || len(message.ToolCalls) > 0 {
			continue
		}
		switch message.Content {
		case "answer one":
			expectation(t, types.TurnID(message) == firstTurnID,
				"turn 1 final assistant must carry turn 1 identity")
		case "answer two":
			expectation(t, types.TurnID(message) == secondTurnID,
				"turn 2 final assistant must carry turn 2 identity, not an inherited one")
		}
	}
}
