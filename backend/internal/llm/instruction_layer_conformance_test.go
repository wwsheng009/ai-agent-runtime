package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/providercompat"
	"github.com/wwsheng009/ai-agent-runtime/internal/prompt"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Instruction-layer conformance suite (plan §4.12).
//
// One canonical fixture is pushed through RuntimeMessagesToProtocolMessages and
// every protocol adapter. The contract under test:
//
//  1. content and order are preserved end to end;
//  2. turn instructions never land on an assistant/model role;
//  3. session instructions stay in the leading native instruction slot
//     (Anthropic system / Gemini systemInstruction / Codex instructions /
//     OpenAI system) and their text is not duplicated into history;
//  4. the internal instruction metadata never leaks onto the wire.
const (
	conformanceSessionInstruction = "SESSION-INSTRUCTION: keep the session rules stable."
	conformanceTurnInstruction    = "TURN-INSTRUCTION: use the mentioned skill this turn."
	conformanceUserText           = "USER-TURN: perform the task."
	conformanceAssistantText      = "ASSISTANT-TURN: acknowledged."
)

// instructionLayerFixture mirrors the canonical shape from plan §4.12:
// leading session instruction + user + turn instruction (history tail) +
// assistant.
func instructionLayerFixture(t *testing.T) []types.Message {
	t.Helper()
	session := prompt.NewInstructionMessage(
		prompt.InstructionScopeSession,
		prompt.InstructionSourceSkillsCatalog,
		conformanceSessionInstruction,
	)
	turn := prompt.NewInstructionMessage(
		prompt.InstructionScopeTurn,
		prompt.InstructionSourceSkillInstructions,
		conformanceTurnInstruction,
	)
	if session == nil || turn == nil {
		t.Fatal("failed to build instruction-layer fixture")
	}
	return []types.Message{
		*session,
		*types.NewUserMessage(conformanceUserText),
		*turn,
		*types.NewAssistantMessage(conformanceAssistantText),
	}
}

func protocolFixture(t *testing.T, protocol string) []map[string]interface{} {
	t.Helper()
	messages := RuntimeMessagesToProtocolMessages(instructionLayerFixture(t), protocol)
	if len(messages) == 0 {
		t.Fatal("expected protocol messages for the instruction-layer fixture")
	}
	return messages
}

func wireText(value interface{}) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(payload)
}

func wireRole(message map[string]interface{}) string {
	role, _ := message["role"].(string)
	return strings.ToLower(strings.TrimSpace(role))
}

func protocolMessageIndexContaining(messages []map[string]interface{}, fragment string) int {
	for index, message := range messages {
		if strings.Contains(wireText(message), fragment) {
			return index
		}
	}
	return -1
}

// wireRequest serializes the adapter request and decodes it again so the
// assertions below inspect exactly the wire shape an upstream would receive.
func wireRequest(t *testing.T, request map[string]interface{}) map[string]interface{} {
	t.Helper()
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal adapter request: %v", err)
	}
	decoded := map[string]interface{}{}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode adapter request: %v", err)
	}
	return decoded
}

func wireArrayField(t *testing.T, container map[string]interface{}, key string) []interface{} {
	t.Helper()
	items, ok := container[key].([]interface{})
	if !ok {
		t.Fatalf("expected %q array on the wire, got %#v", key, container[key])
	}
	return items
}

func findWireMessage(t *testing.T, messages []interface{}, fragment string) (map[string]interface{}, int) {
	t.Helper()
	for index, raw := range messages {
		message, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if strings.Contains(wireText(message), fragment) {
			return message, index
		}
	}
	t.Fatalf("fragment %q not found in wire messages: %#v", fragment, messages)
	return nil, -1
}

func assertFixtureRolesAndOrder(t *testing.T, messages []map[string]interface{}, sessionRole, turnRole string) {
	t.Helper()
	// Order is asserted on the concatenated wire text so it stays valid when a
	// sanitizer merges consecutive same-role messages (e.g. Anthropic folds the
	// user turn and the appended turn instruction into one user message while
	// keeping the original text order inside its content blocks).
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		parts = append(parts, wireText(message))
	}
	wireStream := strings.Join(parts, "\n")
	sessionOffset := strings.Index(wireStream, conformanceSessionInstruction)
	userOffset := strings.Index(wireStream, conformanceUserText)
	turnOffset := strings.Index(wireStream, conformanceTurnInstruction)
	if sessionOffset < 0 || userOffset < 0 || turnOffset < 0 {
		t.Fatalf("fixture fragments missing from protocol messages: %#v", messages)
	}
	if !(sessionOffset < userOffset && userOffset < turnOffset) {
		t.Fatalf(
			"expected session<user<turn order, got offsets %d,%d,%d",
			sessionOffset, userOffset, turnOffset,
		)
	}
	if assistantOffset := strings.Index(wireStream, conformanceAssistantText); assistantOffset >= 0 && assistantOffset < turnOffset {
		t.Fatalf("assistant fragment must follow the turn instruction, got offset %d", assistantOffset)
	}

	sessionMessage, _ := findWireMessageOrNone(protocolMessagesAsInterfaces(messages), conformanceSessionInstruction)
	turnMessage, _ := findWireMessageOrNone(protocolMessagesAsInterfaces(messages), conformanceTurnInstruction)
	if sessionMessage == nil || turnMessage == nil {
		t.Fatalf("fixture fragments missing from protocol messages: %#v", messages)
	}
	if got := wireRole(sessionMessage); got != sessionRole {
		t.Fatalf("session instruction role = %q, want %q: %#v", got, sessionRole, sessionMessage)
	}
	if got := wireRole(turnMessage); got != turnRole {
		t.Fatalf("turn instruction role = %q, want %q: %#v", got, turnRole, turnMessage)
	}
	for _, message := range []map[string]interface{}{sessionMessage, turnMessage} {
		for _, key := range []string{types.MetaInstructionScope, types.MetaInstructionSource} {
			if _, exists := message[key]; exists {
				t.Fatalf("internal instruction metadata %q leaked onto protocol message: %#v", key, message)
			}
		}
	}
}

func protocolMessagesAsInterfaces(messages []map[string]interface{}) []interface{} {
	result := make([]interface{}, 0, len(messages))
	for _, message := range messages {
		result = append(result, message)
	}
	return result
}

func TestInstructionLayerConformance_ProtocolRolePlanning(t *testing.T) {
	cases := []struct {
		name        string
		protocol    string
		sessionRole string
		turnRole    string
	}{
		{name: "anthropic", protocol: "anthropic", sessionRole: "system", turnRole: "user"},
		{name: "gemini", protocol: "gemini", sessionRole: "system", turnRole: "user"},
		{name: "codex", protocol: "codex", sessionRole: "system", turnRole: "developer"},
		{name: "openai", protocol: "openai", sessionRole: "system", turnRole: "system"},
		{name: "default", protocol: "", sessionRole: "system", turnRole: "system"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			assertFixtureRolesAndOrder(t, protocolFixture(t, testCase.protocol), testCase.sessionRole, testCase.turnRole)
		})
	}
}

func TestInstructionLayerConformance_AnthropicAdapter(t *testing.T) {
	request := (&adapter.AnthropicAdapter{}).BuildRequest(adapter.RequestConfig{
		Model:     "claude-conformance",
		MaxTokens: 1024,
		Messages:  protocolFixture(t, "anthropic"),
	})
	decoded := wireRequest(t, request)

	system, _ := decoded["system"].(string)
	if !strings.Contains(system, conformanceSessionInstruction) {
		t.Fatalf("leading session instruction must land in top-level system, got %#v", decoded["system"])
	}
	if strings.Contains(system, conformanceTurnInstruction) {
		t.Fatalf("turn instruction must not land in top-level system, got %#v", decoded["system"])
	}

	messages := wireArrayField(t, decoded, "messages")
	if _, index := findWireMessageOrNone(messages, conformanceSessionInstruction); index >= 0 {
		t.Fatalf("session instruction must not be duplicated into anthropic messages")
	}
	turnMessage, _ := findWireMessage(t, messages, conformanceTurnInstruction)
	if role := wireRole(turnMessage); role != "user" {
		t.Fatalf("turn instruction must be an anthropic user message, got role %q: %#v", role, turnMessage)
	}
	for _, raw := range messages {
		message, ok := raw.(map[string]interface{})
		if !ok || wireRole(message) != "assistant" {
			continue
		}
		if strings.Contains(wireText(message), conformanceTurnInstruction) {
			t.Fatalf("assistant message must not carry the turn instruction: %#v", message)
		}
	}
	// The trailing assistant prefill is intentionally trimmed by the existing
	// Anthropic sanitizer (trimAnthropicAssistantPrefill) before this adapter
	// runs. That pre-existing rule is out of scope for the instruction-layer
	// contract, so only the instruction fragments are asserted here.
}

func TestInstructionLayerConformance_GeminiAdapter(t *testing.T) {
	request := (&adapter.GeminiAdapter{}).BuildRequest(adapter.RequestConfig{
		Model:    "gemini-conformance",
		Messages: protocolFixture(t, "gemini"),
	})
	decoded := wireRequest(t, request)

	systemInstruction, ok := decoded["systemInstruction"].(map[string]interface{})
	if !ok {
		t.Fatalf("leading session instruction must land in systemInstruction, got %#v", decoded["systemInstruction"])
	}
	systemText := wireText(systemInstruction)
	if !strings.Contains(systemText, conformanceSessionInstruction) {
		t.Fatalf("systemInstruction must contain the session instruction, got %#v", systemInstruction)
	}
	if strings.Contains(systemText, conformanceTurnInstruction) {
		t.Fatalf("turn instruction must not land in systemInstruction, got %#v", systemInstruction)
	}

	contents := wireArrayField(t, decoded, "contents")
	assistantAsModel := false
	for index, raw := range contents {
		content, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("contents[%d] is not an object: %#v", index, raw)
		}
		role := wireRole(content)
		text := wireText(content)
		if strings.Contains(text, conformanceSessionInstruction) {
			t.Fatalf("session instruction leaked into contents[%d]: %#v", index, content)
		}
		if strings.Contains(text, conformanceTurnInstruction) && role != "user" {
			t.Fatalf("turn instruction must be user parts, got role %q at contents[%d]: %#v", role, index, content)
		}
		if role == "model" && strings.Contains(text, conformanceAssistantText) {
			assistantAsModel = true
		}
	}
	if !assistantAsModel {
		t.Fatalf("assistant content must stay a model turn in contents: %#v", contents)
	}
}

func TestInstructionLayerConformance_CodexAdapter(t *testing.T) {
	request := (&adapter.CodexAdapter{}).BuildRequest(adapter.RequestConfig{
		Model:    "codex-conformance",
		Messages: protocolFixture(t, "codex"),
	})
	decoded := wireRequest(t, request)

	instructions, _ := decoded["instructions"].(string)
	if !strings.Contains(instructions, conformanceSessionInstruction) {
		t.Fatalf("leading session instruction must land in top-level instructions, got %#v", decoded["instructions"])
	}
	if strings.Contains(instructions, conformanceTurnInstruction) {
		t.Fatalf("turn instruction must not land in top-level instructions, got %#v", decoded["instructions"])
	}

	input := wireArrayField(t, decoded, "input")
	if _, index := findWireMessageOrNone(input, conformanceSessionInstruction); index >= 0 {
		t.Fatalf("session instruction must not be duplicated into codex input")
	}
	turnItem, _ := findWireMessage(t, input, conformanceTurnInstruction)
	if role := wireRole(turnItem); role != "developer" {
		t.Fatalf("turn instruction must be a codex developer item, got role %q: %#v", role, turnItem)
	}
	if !strings.Contains(wireText(decoded), conformanceAssistantText) {
		t.Fatalf("assistant fragment lost from codex request: %#v", decoded)
	}
}

func TestInstructionLayerConformance_OpenAIAdapter(t *testing.T) {
	request := (&adapter.OpenAIAdapter{}).BuildRequest(adapter.RequestConfig{
		Model:    "openai-conformance",
		Messages: protocolFixture(t, "openai"),
	})
	decoded := wireRequest(t, request)

	messages := wireArrayField(t, decoded, "messages")
	sessionMessage, _ := findWireMessage(t, messages, conformanceSessionInstruction)
	if role := wireRole(sessionMessage); role != "system" {
		t.Fatalf("session instruction must be an openai system message, got role %q: %#v", role, sessionMessage)
	}
	turnMessage, _ := findWireMessage(t, messages, conformanceTurnInstruction)
	if role := wireRole(turnMessage); role != "system" {
		t.Fatalf("turn instruction must stay an openai system message, got role %q: %#v", role, turnMessage)
	}
	if !strings.Contains(wireText(decoded), conformanceAssistantText) {
		t.Fatalf("assistant fragment lost from openai request: %#v", decoded)
	}
}

// TestInstructionLayerConformance_AnthropicCompatibleNormalizer pins the
// providercompat chain to the same contract: residual (non-leading) instruction
// messages become user text blocks for Anthropic-compatible upstreams, while
// the leading session prefix is left for the adapter's top-level system fold.
func TestInstructionLayerConformance_AnthropicCompatibleNormalizer(t *testing.T) {
	messages := protocolFixture(t, "codex")
	turnIndex := protocolMessageIndexContaining(messages, conformanceTurnInstruction)
	sessionIndex := protocolMessageIndexContaining(messages, conformanceSessionInstruction)
	if turnIndex < 0 || sessionIndex < 0 {
		t.Fatalf("fixture fragments missing: %#v", messages)
	}
	if role := wireRole(messages[turnIndex]); role != "developer" {
		t.Fatalf("fixture precondition: codex turn role = %q, want developer", role)
	}

	normalized := providercompat.NormalizeAnthropicCompatibleMessages(providercompat.Context{
		ProviderName: "opencode-console-go",
		Protocol:     "anthropic",
		Profile:      agentconfig.CompatibilityProfileOpenCodeConsoleGo,
	}, messages)

	if role := wireRole(messages[turnIndex]); role != "developer" {
		t.Fatalf("normalizer must not mutate canonical history, got role %q", role)
	}
	normalizedTurn := normalized[protocolMessageIndexContaining(normalized, conformanceTurnInstruction)]
	if role := wireRole(normalizedTurn); role != "user" {
		t.Fatalf("non-leading instruction must normalize to user, got role %q: %#v", role, normalizedTurn)
	}
	if !strings.Contains(wireText(normalizedTurn), conformanceTurnInstruction) {
		t.Fatalf("normalizer lost the instruction text: %#v", normalizedTurn)
	}
	normalizedSession := normalized[protocolMessageIndexContaining(normalized, conformanceSessionInstruction)]
	if role := wireRole(normalizedSession); role != "system" {
		t.Fatalf("leading session instruction must stay system for the adapter fold, got role %q", role)
	}
}

// findWireMessageOrNone is the non-fatal variant of findWireMessage.
func findWireMessageOrNone(messages []interface{}, fragment string) (map[string]interface{}, int) {
	for index, raw := range messages {
		message, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if strings.Contains(wireText(message), fragment) {
			return message, index
		}
	}
	return nil, -1
}
