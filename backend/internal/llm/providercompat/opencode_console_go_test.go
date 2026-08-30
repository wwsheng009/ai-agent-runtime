package providercompat

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

const openCodeProfile = agentconfig.CompatibilityProfileOpenCodeConsoleGo

func TestOpenCodeConsoleGo_DeveloperRoleToSystem(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "developer", "content": "be terse"},
		{"role": "user", "content": "hi"},
	}
	got := NormalizeOpenAICompatibleMessages(Context{
		Protocol: "openai",
		Profile:  openCodeProfile,
	}, messages)

	if got[0]["role"] != "system" {
		t.Fatalf("expected developer role rewritten to system, got %#v", got[0]["role"])
	}
	if got[0]["content"] != "be terse" {
		t.Fatalf("expected content preserved, got %#v", got[0]["content"])
	}
	if got[1]["role"] != "user" {
		t.Fatalf("expected user role untouched, got %#v", got[1]["role"])
	}
	// The canonical history must not be mutated in place.
	if messages[0]["role"] != "developer" {
		t.Fatalf("expected original message left intact, got %#v", messages[0]["role"])
	}
}

// TestOpenCodeConsoleGo_DeveloperRoleProjectedWithoutProfile locks the
// default OpenAI-compatible wire: even without an explicit profile, outgoing
// developer instructions are projected to system because strict gateways
// (system/user/assistant/tool only) reject developer with HTTP 400. Canonical
// history must stay untouched.
func TestOpenCodeConsoleGo_DeveloperRoleProjectedWithoutProfile(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "developer", "content": "be terse"},
	}
	got := NormalizeOpenAICompatibleMessages(Context{Protocol: "openai"}, messages)
	if got[0]["role"] != "system" {
		t.Fatalf("expected developer projected to system without profile, got %#v", got[0]["role"])
	}
	if messages[0]["role"] != "developer" {
		t.Fatalf("expected original message left intact, got %#v", messages[0]["role"])
	}
}

func TestOpenCodeConsoleGo_DeveloperRoleIgnoredForNonOpenAIProtocol(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "developer", "content": "be terse"},
	}
	got := NormalizeOpenAICompatibleMessages(Context{
		Protocol: "codex",
		Profile:  openCodeProfile,
	}, messages)
	if got[0]["role"] != "developer" {
		t.Fatalf("expected developer role untouched for codex protocol, got %#v", got[0]["role"])
	}
}

func TestOpenCodeConsoleGo_FlattenAssistantOutputText(t *testing.T) {
	body := map[string]interface{}{
		"input": []map[string]interface{}{
			{
				"type": "message",
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "output_text", "text": "Hello, "},
					{"type": "output_text", "text": "world"},
				},
			},
		},
	}
	normalized := PrepareRequestBody(Context{
		Protocol: "codex",
		Profile:  openCodeProfile,
	}, body)

	input := normalized["input"].([]map[string]interface{})
	if got := input[0]["content"]; got != "Hello, world" {
		t.Fatalf("expected assistant content flattened to string, got %#v", got)
	}
	// Original body must be preserved for canonical replay.
	orig := body["input"].([]map[string]interface{})
	if _, ok := orig[0]["content"].([]map[string]interface{}); !ok {
		t.Fatalf("expected original body content array preserved, got %#v", orig[0]["content"])
	}
}

func TestOpenCodeConsoleGo_FlattenAssistantOutputTextAnySlice(t *testing.T) {
	body := map[string]interface{}{
		"input": []interface{}{
			map[string]interface{}{
				"type": "message",
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{"type": "output_text", "text": "a"},
					map[string]interface{}{"type": "output_text", "text": "b"},
				},
			},
		},
	}
	normalized := PrepareRequestBody(Context{
		Protocol: "codex",
		Profile:  openCodeProfile,
	}, body)
	input := normalized["input"].([]interface{})
	item := input[0].(map[string]interface{})
	if got := item["content"]; got != "ab" {
		t.Fatalf("expected flattened content 'ab', got %#v", got)
	}
}

func TestOpenCodeConsoleGo_PreserveMixedAssistantContent(t *testing.T) {
	content := []map[string]interface{}{
		{"type": "output_text", "text": "text part"},
		{"type": "refusal", "refusal": "no"},
	}
	body := map[string]interface{}{
		"input": []map[string]interface{}{
			{"type": "message", "role": "assistant", "content": content},
		},
	}
	normalized := PrepareRequestBody(Context{
		Protocol: "codex",
		Profile:  openCodeProfile,
	}, body)
	input := normalized["input"].([]map[string]interface{})
	if _, ok := input[0]["content"].([]map[string]interface{}); !ok {
		t.Fatalf("expected non-text assistant content left as array, got %#v", input[0]["content"])
	}
}

func TestOpenCodeConsoleGo_NormalizeResponsesToolCallItemIDs(t *testing.T) {
	body := map[string]interface{}{
		"input": []map[string]interface{}{
			{
				"type":      "function_call",
				"id":        "call_1",
				"call_id":   "call_1",
				"name":      "ls",
				"arguments": "{}",
			},
			{
				"type":    "function_call_output",
				"id":      "call_1",
				"call_id": "call_1",
				"output":  "ok",
			},
			{
				"type":    "custom_tool_call",
				"id":      "call_patch_1",
				"call_id": "call_patch_1",
				"name":    "apply_patch",
				"input":   "patch",
			},
		},
	}
	normalized := PrepareRequestBody(Context{
		Protocol: "codex",
		Profile:  openCodeProfile,
	}, body)
	input := normalized["input"].([]map[string]interface{})
	if input[0]["id"] != "fc_1" || input[0]["call_id"] != "call_1" {
		t.Fatalf("unexpected function_call item: %#v", input[0])
	}
	if input[1]["id"] != "fc_1" || input[1]["call_id"] != "call_1" {
		t.Fatalf("unexpected function_call_output item: %#v", input[1])
	}
	if input[2]["id"] != "ctc_patch_1" || input[2]["call_id"] != "call_patch_1" {
		t.Fatalf("unexpected custom_tool_call item: %#v", input[2])
	}
	// Canonical body must not be mutated.
	orig := body["input"].([]map[string]interface{})
	if orig[0]["id"] != "call_1" || orig[2]["id"] != "call_patch_1" {
		t.Fatalf("expected original body preserved, got %#v", orig)
	}
}

func TestOpenCodeConsoleGo_NormalizeResponsesToolCallItemIDsAnySlice(t *testing.T) {
	body := map[string]interface{}{
		"input": []interface{}{
			map[string]interface{}{
				"type":      "function_call",
				"id":        "call_2",
				"call_id":   "call_2",
				"name":      "bash",
				"arguments": "{}",
			},
		},
	}
	normalized := PrepareRequestBody(Context{
		Protocol: "codex",
		Profile:  openCodeProfile,
	}, body)
	input := normalized["input"].([]interface{})
	item := input[0].(map[string]interface{})
	if item["id"] != "fc_2" || item["call_id"] != "call_2" {
		t.Fatalf("unexpected function_call item: %#v", item)
	}
}

func TestOpenCodeConsoleGo_PreserveUserContent(t *testing.T) {
	body := map[string]interface{}{
		"input": []map[string]interface{}{
			{
				"type": "message",
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "input_text", "text": "keep"},
				},
			},
		},
	}
	normalized := PrepareRequestBody(Context{
		Protocol: "codex",
		Profile:  openCodeProfile,
	}, body)
	input := normalized["input"].([]map[string]interface{})
	if _, ok := input[0]["content"].([]map[string]interface{}); !ok {
		t.Fatalf("expected user content array untouched, got %#v", input[0]["content"])
	}
}

func TestOpenCodeConsoleGo_NoProfileLeavesResponsesBodyUnchanged(t *testing.T) {
	body := map[string]interface{}{
		"input": []map[string]interface{}{
			{
				"type": "message",
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "output_text", "text": "x"},
				},
			},
		},
	}
	normalized := PrepareRequestBody(Context{Protocol: "codex"}, body)
	input := normalized["input"].([]map[string]interface{})
	if _, ok := input[0]["content"].([]map[string]interface{}); !ok {
		t.Fatalf("expected content array preserved without profile, got %#v", input[0]["content"])
	}
}

// TestOpenCodeConsoleGo_AllDeveloperMessagesConverted locks the "runtime-only,
// never persisted" contract for the chat/completions dialect: every developer
// instruction message (fact ledger, goal, todo state, ...) in the history must
// be rewritten to system at send time, while the canonical history stays
// developer untouched so other providers can replay the same history.
func TestOpenCodeConsoleGo_AllDeveloperMessagesConverted(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "system", "content": "base prompt"},
		{"role": "developer", "content": "fact ledger A"},
		{"role": "developer", "content": "active goal B"},
		{"role": "developer", "content": "todo state C"},
		{"role": "user", "content": "hello"},
	}
	got := NormalizeOpenAICompatibleMessages(Context{
		Protocol: "openai",
		Profile:  openCodeProfile,
	}, messages)

	// Every developer message is projected to system at send time; system and
	// user messages keep their roles.
	for index := 0; index < len(got); index++ {
		role, _ := got[index]["role"].(string)
		switch index {
		case 0:
			if role != "system" {
				t.Fatalf("expected system message %d preserved, got %#v", index, role)
			}
		case 1, 2, 3:
			if role != "system" {
				t.Fatalf("expected developer message %d rewritten to system, got %#v", index, role)
			}
		case 4:
			if role != "user" {
				t.Fatalf("expected user message %d preserved, got %#v", index, role)
			}
		}
	}
	// The canonical history must remain developer for every injected stage.
	for index := 1; index <= 3; index++ {
		if role, _ := messages[index]["role"].(string); role != "developer" {
			t.Fatalf("expected canonical message %d to stay developer, got %#v", index, role)
		}
	}
}

// TestOpenCodeConsoleGo_ReversibleAcrossProfiles proves the transform is a
// send-time projection, not a rewrite: the same developer history projects to
// system under every OpenAI-compatible wire dialect (explicit OpenCode profile
// and default/standard alike, since strict gateways reject developer). The
// canonical history itself never changes, so switching providers stays
// lossless.
func TestOpenCodeConsoleGo_ReversibleAcrossProfiles(t *testing.T) {
	history := []map[string]interface{}{
		{"role": "developer", "content": "be terse"},
		{"role": "user", "content": "hi"},
	}
	opencode := NormalizeOpenAICompatibleMessages(Context{
		Protocol: "openai",
		Profile:  openCodeProfile,
	}, history)
	if role, _ := opencode[0]["role"].(string); role != "system" {
		t.Fatalf("expected opencode profile to project developer as system, got %#v", role)
	}
	standard := NormalizeOpenAICompatibleMessages(Context{
		Protocol: "openai",
		Profile:  agentconfig.CompatibilityProfileStandard,
	}, history)
	if role, _ := standard[0]["role"].(string); role != "system" {
		t.Fatalf("expected standard/default profile to project developer as system, got %#v", role)
	}
	if role, _ := history[0]["role"].(string); role != "developer" {
		t.Fatalf("expected canonical history untouched by both projections, got %#v", role)
	}
}

// TestOpenCodeConsoleGo_ChainWithDeepSeekModel verifies the OpenCode adapter
// still applies when a deepseek model id also matches the DeepSeek adapter
// (registry runs both); the deepseek adapter owns capabilities/reasoning replay
// only and must not interfere with the developer rewrite.
func TestOpenCodeConsoleGo_ChainWithDeepSeekModel(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "developer", "content": "fact ledger"},
		{"role": "user", "content": "hi"},
	}
	got := NormalizeOpenAICompatibleMessages(Context{
		ProviderName: "opencode.ai",
		BaseURL:      "https://opencode.ai/zen/go",
		Protocol:     "openai",
		Profile:      openCodeProfile,
		Model:        "deepseek-v4-flash",
	}, messages)
	if role, _ := got[0]["role"].(string); role != "system" {
		t.Fatalf("expected developer rewritten to system despite deepseek model match, got %#v", role)
	}
	if role, _ := messages[0]["role"].(string); role != "developer" {
		t.Fatalf("expected canonical history untouched, got %#v", role)
	}
}

// TestOpenCodeConsoleGo_FlattenAllAssistantMessages locks the responses
// dialect: every assistant message in a multi-turn input must be flattened to
// string content at send time, while developer/user content shapes are kept
// and the original body is preserved for canonical replay.
func TestOpenCodeConsoleGo_FlattenAllAssistantMessages(t *testing.T) {
	body := map[string]interface{}{
		"input": []map[string]interface{}{
			{
				"type": "message",
				"role": "developer",
				"content": []map[string]interface{}{
					{"type": "input_text", "text": "be terse"},
				},
			},
			{
				"type": "message",
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "input_text", "text": "first"},
				},
			},
			{
				"type": "message",
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "output_text", "text": "one "},
					{"type": "output_text", "text": "two"},
				},
			},
			{
				"type": "message",
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "input_text", "text": "second"},
				},
			},
			{
				"type": "message",
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "output_text", "text": "done"},
				},
			},
		},
	}
	normalized := PrepareRequestBody(Context{
		Protocol: "codex",
		Profile:  openCodeProfile,
	}, body)
	input := normalized["input"].([]map[string]interface{})
	if got := input[2]["content"]; got != "one two" {
		t.Fatalf("expected first assistant content flattened, got %#v", got)
	}
	if got := input[4]["content"]; got != "done" {
		t.Fatalf("expected second assistant content flattened, got %#v", got)
	}
	if _, ok := input[0]["content"].([]map[string]interface{}); !ok {
		t.Fatalf("expected developer content shape untouched, got %#v", input[0]["content"])
	}
	if _, ok := input[1]["content"].([]map[string]interface{}); !ok {
		t.Fatalf("expected user content shape untouched, got %#v", input[1]["content"])
	}
	orig := body["input"].([]map[string]interface{})
	if _, ok := orig[2]["content"].([]map[string]interface{}); !ok {
		t.Fatalf("expected original assistant content array preserved, got %#v", orig[2]["content"])
	}
}

// TestOpenCodeConsoleGo_NormalizeToolCallsType locks the second send-time rule
// for the chat/completions dialect: non-standard tool_calls types such as
// "function_call" (legacy providers / raw history replay) must be projected to
// the strict OpenAI enum "function" for the OpenCode Console Go gateway, while
// canonical history keeps its original type.
func TestOpenCodeConsoleGo_NormalizeToolCallsType(t *testing.T) {
	messages := []map[string]interface{}{
		{
			"role":    "assistant",
			"content": "",
			"tool_calls": []map[string]interface{}{
				{
					"id":   "call_1",
					"type": "function_call",
					"function": map[string]interface{}{
						"name":      "view",
						"arguments": `{"file_path":"a.go"}`,
					},
				},
			},
		},
	}
	got := NormalizeOpenAICompatibleMessages(Context{
		Protocol: "openai",
		Profile:  openCodeProfile,
	}, messages)

	calls := got[0]["tool_calls"].([]map[string]interface{})
	if gotType := calls[0]["type"]; gotType != "function" {
		t.Fatalf("expected tool_calls type rewritten to function, got %#v", gotType)
	}
	if gotName := calls[0]["function"].(map[string]interface{})["name"]; gotName != "view" {
		t.Fatalf("expected function name preserved, got %#v", gotName)
	}
	// Canonical history must keep the original non-standard type.
	orig := messages[0]["tool_calls"].([]map[string]interface{})
	if gotType := orig[0]["type"]; gotType != "function_call" {
		t.Fatalf("expected canonical history type untouched, got %#v", gotType)
	}
}

// TestOpenCodeConsoleGo_NormalizeToolCallsTypeAnySlice covers the JSON-decoded
// shape ([]interface{} of map[string]interface{}) that raw history replay
// produces after unmarshalling a stored request body.
func TestOpenCodeConsoleGo_NormalizeToolCallsTypeAnySlice(t *testing.T) {
	messages := []map[string]interface{}{
		{
			"role":    "assistant",
			"content": "",
			"tool_calls": []interface{}{
				map[string]interface{}{
					"id":   "call_2",
					"type": "function_call",
					"function": map[string]interface{}{
						"name":      "bash",
						"arguments": `{"command":"git status"}`,
					},
				},
			},
		},
	}
	got := NormalizeOpenAICompatibleMessages(Context{
		Protocol: "openai",
		Profile:  openCodeProfile,
	}, messages)

	calls := got[0]["tool_calls"].([]map[string]interface{})
	if gotType := calls[0]["type"]; gotType != "function" {
		t.Fatalf("expected JSON-decoded tool_calls type rewritten to function, got %#v", gotType)
	}
	if gotName := calls[0]["function"].(map[string]interface{})["name"]; gotName != "bash" {
		t.Fatalf("expected function name preserved, got %#v", gotName)
	}
}

// TestOpenCodeConsoleGo_NormalizeToolCallsTypeStandard leaves already-standard
// tool_calls untouched and reports no change.
func TestOpenCodeConsoleGo_NormalizeToolCallsTypeStandard(t *testing.T) {
	messages := []map[string]interface{}{
		{
			"role":    "assistant",
			"content": "checking",
			"tool_calls": []map[string]interface{}{
				{
					"id":   "call_3",
					"type": "function",
					"function": map[string]interface{}{
						"name":      "ls",
						"arguments": `{"depth":1}`,
					},
				},
			},
		},
	}
	got := NormalizeOpenAICompatibleMessages(Context{
		Protocol: "openai",
		Profile:  openCodeProfile,
	}, messages)
	if len(got) != len(messages) || got[0] == nil {
		t.Fatalf("expected unchanged message list, got %#v", got)
	}
	if role, _ := got[0]["role"].(string); role != "assistant" {
		t.Fatalf("expected assistant role preserved, got %#v", role)
	}
	calls := got[0]["tool_calls"].([]map[string]interface{})
	if gotType := calls[0]["type"]; gotType != "function" {
		t.Fatalf("expected standard tool_calls type preserved, got %#v", gotType)
	}
}

// TestOpenCodeConsoleGo_NormalizeToolCallsTypeCombined verifies both send-time
// rules apply in one pass: developer role rewrite plus tool_calls type fix on
// separate messages of the same outgoing body.
func TestOpenCodeConsoleGo_NormalizeToolCallsTypeCombined(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "developer", "content": "be terse"},
		{
			"role":    "assistant",
			"content": "",
			"tool_calls": []map[string]interface{}{
				{
					"id":   "call_4",
					"type": "function_call",
					"function": map[string]interface{}{
						"name":      "view",
						"arguments": `{"path":"."}`,
					},
				},
			},
		},
	}
	got := NormalizeOpenAICompatibleMessages(Context{
		Protocol: "openai",
		Profile:  openCodeProfile,
	}, messages)

	if role, _ := got[0]["role"].(string); role != "system" {
		t.Fatalf("expected developer rewritten to system, got %#v", role)
	}
	calls := got[1]["tool_calls"].([]map[string]interface{})
	if gotType := calls[0]["type"]; gotType != "function" {
		t.Fatalf("expected tool_calls type rewritten to function, got %#v", gotType)
	}
	// Canonical history unchanged for both rules.
	if role, _ := messages[0]["role"].(string); role != "developer" {
		t.Fatalf("expected canonical developer untouched, got %#v", role)
	}
	orig := messages[1]["tool_calls"].([]map[string]interface{})
	if gotType := orig[0]["type"]; gotType != "function_call" {
		t.Fatalf("expected canonical tool_calls type untouched, got %#v", gotType)
	}
}

// TestOpenCodeConsoleGo_AnthropicUserStringToTextBlock locks the Console Go
// wire rule: user messages with a plain-string content are rewritten to a
// single text content block (the upstream rejects the string shorthand).
func TestOpenCodeConsoleGo_AnthropicUserStringToTextBlock(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "user", "content": "hello"},
		{"role": "assistant", "content": "hi there"},
	}
	got := NormalizeAnthropicCompatibleMessages(Context{
		Protocol: "anthropic",
		Profile:  openCodeProfile,
	}, messages)

	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d: %#v", len(got), got)
	}
	blocks, ok := got[0]["content"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected user content as block array, got %#v", got[0]["content"])
	}
	if len(blocks) != 1 || blocks[0]["type"] != "text" || blocks[0]["text"] != "hello" {
		t.Fatalf("expected single text block, got %#v", blocks)
	}
	if role, _ := got[0]["role"].(string); role != "user" {
		t.Fatalf("expected user role preserved, got %#v", got[0]["role"])
	}
	if got[1]["content"] != "hi there" {
		t.Fatalf("expected assistant string content untouched, got %#v", got[1]["content"])
	}
	// Canonical history must not be mutated in place.
	if messages[0]["content"] != "hello" {
		t.Fatalf("expected original user content intact, got %#v", messages[0]["content"])
	}
}

// TestOpenCodeConsoleGo_AnthropicUserArrayPreserved leaves user messages that
// already use content blocks untouched.
func TestOpenCodeConsoleGo_AnthropicUserArrayPreserved(t *testing.T) {
	messages := []map[string]interface{}{
		{
			"role": "user",
			"content": []map[string]interface{}{
				{"type": "text", "text": "already blocks"},
			},
		},
	}
	got := NormalizeAnthropicCompatibleMessages(Context{
		Protocol: "anthropic",
		Profile:  openCodeProfile,
	}, messages)
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	if _, ok := got[0]["content"].([]map[string]interface{}); !ok {
		t.Fatalf("expected block array preserved, got %#v", got[0]["content"])
	}
}

// TestOpenCodeConsoleGo_AnthropicResidualDeveloperToUserTextBlock projects
// turn-context developer instructions into user text blocks so the final wire
// request only contains the official Anthropic roles.
func TestOpenCodeConsoleGo_AnthropicResidualDeveloperToUserTextBlock(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "system", "content": "Base guardrail"},
		{"role": "user", "content": "check logs"},
		{"role": "developer", "content": "Persistent goal.\n\nkeep the prefix stable"},
		{"role": "assistant", "content": "I will inspect logs."},
	}
	got := NormalizeAnthropicCompatibleMessages(Context{
		Protocol: "anthropic",
		Profile:  openCodeProfile,
	}, messages)

	if len(got) != 4 {
		t.Fatalf("expected 4 messages, got %d: %#v", len(got), got)
	}
	// Leading system stays untouched for adapter system folding.
	if got[0]["role"] != "system" || got[0]["content"] != "Base guardrail" {
		t.Fatalf("expected leading system preserved, got %#v", got[0])
	}
	// Residual developer becomes a user text block.
	if role, _ := got[2]["role"].(string); role != "user" {
		t.Fatalf("expected residual developer projected to user, got %#v", got[2]["role"])
	}
	blocks, ok := got[2]["content"].([]map[string]interface{})
	if !ok || len(blocks) != 1 || blocks[0]["type"] != "text" {
		t.Fatalf("expected residual developer as text block, got %#v", got[2]["content"])
	}
	if blocks[0]["text"] != "Persistent goal.\n\nkeep the prefix stable" {
		t.Fatalf("expected developer text preserved, got %#v", blocks[0]["text"])
	}
	if got[3]["role"] != "assistant" {
		t.Fatalf("expected assistant trailing message preserved, got %#v", got[3])
	}
	// Canonical history untouched.
	if role, _ := messages[2]["role"].(string); role != "developer" {
		t.Fatalf("expected canonical developer untouched, got %#v", role)
	}
}

// TestOpenCodeConsoleGo_AnthropicResidualSystemToUserTextBlock projects
// turn-context system instructions into user text blocks as well.
func TestOpenCodeConsoleGo_AnthropicResidualSystemToUserTextBlock(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "user", "content": "hello"},
		{"role": "system", "content": "mid-turn system note"},
	}
	got := NormalizeAnthropicCompatibleMessages(Context{
		Protocol: "anthropic",
		Profile:  openCodeProfile,
	}, messages)

	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d: %#v", len(got), got)
	}
	if role, _ := got[1]["role"].(string); role != "user" {
		t.Fatalf("expected residual system projected to user, got %#v", got[1]["role"])
	}
	blocks, ok := got[1]["content"].([]map[string]interface{})
	if !ok || len(blocks) != 1 || blocks[0]["text"] != "mid-turn system note" {
		t.Fatalf("expected residual system as text block, got %#v", got[1]["content"])
	}
}

// TestOpenCodeConsoleGo_AnthropicEmptyResidualInstructionDropped removes empty
// residual instructions instead of leaking an empty user message.
func TestOpenCodeConsoleGo_AnthropicEmptyResidualInstructionDropped(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "user", "content": "hello"},
		{"role": "developer", "content": "   "},
		{"role": "assistant", "content": "hi"},
	}
	got := NormalizeAnthropicCompatibleMessages(Context{
		Protocol: "anthropic",
		Profile:  openCodeProfile,
	}, messages)

	if len(got) != 2 {
		t.Fatalf("expected empty residual instruction dropped, got %d: %#v", len(got), got)
	}
	if got[0]["role"] != "user" || got[1]["role"] != "assistant" {
		t.Fatalf("expected user + assistant remaining, got %#v", got)
	}
}

// TestOpenCodeConsoleGo_AnthropicWithoutProfile leaves messages untouched when
// the Console Go profile is not selected.
func TestOpenCodeConsoleGo_AnthropicWithoutProfile(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "user", "content": "hello"},
		{"role": "developer", "content": "goal"},
	}
	got := NormalizeAnthropicCompatibleMessages(Context{Protocol: "anthropic"}, messages)
	if got[0]["content"] != "hello" {
		t.Fatalf("expected user string content preserved without profile, got %#v", got[0]["content"])
	}
	if got[1]["role"] != "developer" {
		t.Fatalf("expected developer role preserved without profile, got %#v", got[1]["role"])
	}
}

// TestOpenCodeConsoleGo_AnthropicIgnoredForNonAnthropicProtocol verifies the
// Anthropic rules never run for other protocols.
func TestOpenCodeConsoleGo_AnthropicIgnoredForNonAnthropicProtocol(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "user", "content": "hello"},
	}
	got := NormalizeAnthropicCompatibleMessages(Context{
		Protocol: "openai",
		Profile:  openCodeProfile,
	}, messages)
	if got[0]["content"] != "hello" {
		t.Fatalf("expected openai protocol untouched, got %#v", got[0]["content"])
	}
}
