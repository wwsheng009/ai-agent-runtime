package llm

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestRuntimeMessagesToProtocolMessages_OpenAINormalizesToolReplayPayload(t *testing.T) {
	assistant := types.Message{
		Role:    "assistant",
		Content: "我来查看目录。",
		ToolCalls: []types.ToolCall{
			{ID: "call_1", Name: "ls"},
		},
		Metadata: types.NewMetadata(),
	}
	types.SetReasoningBlock(assistant.Metadata, &types.ReasoningBlock{
		Format:     "openai_compatible",
		Summary:    "先看目录。",
		Visibility: types.ReasoningVisibilitySummary,
	})

	tool := types.Message{
		Role:       "tool",
		Content:    "目录: .",
		ToolCallID: "call_1",
		Metadata:   types.NewMetadata(),
	}
	tool.Metadata["artifact_refs"] = []string{"art_1"}

	messages := RuntimeMessagesToProtocolMessages([]types.Message{assistant, tool}, "openai")
	if len(messages) != 2 {
		t.Fatalf("expected 2 protocol messages, got %d", len(messages))
	}

	if got := messages[0]["reasoning_content"]; got != "先看目录。" {
		t.Fatalf("expected reasoning_content in openai request message, got %#v", got)
	}
	if _, exists := messages[0]["metadata"]; exists {
		t.Fatalf("did not expect metadata in openai request message: %#v", messages[0])
	}

	toolCalls, ok := messages[0]["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %#v", messages[0]["tool_calls"])
	}
	functionPayload, ok := toolCalls[0]["function"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected function payload map, got %#v", toolCalls[0]["function"])
	}
	if got := functionPayload["arguments"]; got != "{}" {
		t.Fatalf("expected empty args to normalize to {}, got %#v", got)
	}

	if _, exists := messages[1]["metadata"]; exists {
		t.Fatalf("did not expect metadata in tool request message: %#v", messages[1])
	}
	if got := messages[1]["tool_call_id"]; got != "call_1" {
		t.Fatalf("unexpected tool_call_id: %#v", got)
	}
}

func TestRuntimeMessagesToProtocolMessages_CodexReplaysCustomToolInput(t *testing.T) {
	raw := "*** Begin Patch\n*** End Patch"
	assistant := types.Message{
		Role: "assistant",
		ToolCalls: []types.ToolCall{{
			ID: "call_patch", Type: "custom_tool_call", Name: "apply_patch",
			Args: map[string]interface{}{"patch": raw}, RawInput: raw,
		}},
		Metadata: types.NewMetadata(),
	}
	messages := RuntimeMessagesToProtocolMessages([]types.Message{
		*types.NewUserMessage("Apply the patch"),
		assistant,
		*types.NewToolMessage("call_patch", "Done"),
	}, "codex")
	if len(messages) != 3 {
		t.Fatalf("expected complete custom-tool replay, got %#v", messages)
	}
	items := decodeSliceOfMaps(messages[1][codexResponseOutputItemsMessageKey])
	if len(items) != 1 || items[0]["type"] != "custom_tool_call" || items[0]["input"] != raw {
		t.Fatalf("expected raw custom tool input to survive replay, got %#v", items)
	}
	if messages[2]["role"] != "tool" || messages[2]["tool_call_id"] != "call_patch" {
		t.Fatalf("expected matching custom tool result to remain associated, got %#v", messages[2])
	}
}

func TestRuntimeMessagesToProtocolMessages_OpenAIIncludesCompatibleMessageOverrides(t *testing.T) {
	assistant := types.Message{
		Role:     "assistant",
		Content:  "Answer:",
		Metadata: types.NewMetadata(),
	}
	assistant.Metadata["name"] = "planner"
	assistant.Metadata["prefix"] = true
	assistant.Metadata["reasoning_content"] = "先整理上下文。"
	assistant.Metadata["artifact_refs"] = []string{"art_1"}

	messages := RuntimeMessagesToProtocolMessages([]types.Message{assistant}, "openai")
	if len(messages) != 1 {
		t.Fatalf("expected 1 protocol message, got %d", len(messages))
	}

	if got := messages[0]["name"]; got != "planner" {
		t.Fatalf("expected assistant name planner, got %#v", got)
	}
	if got := messages[0]["prefix"]; got != true {
		t.Fatalf("expected assistant prefix=true, got %#v", got)
	}
	if got := messages[0]["reasoning_content"]; got != "先整理上下文。" {
		t.Fatalf("expected reasoning_content override, got %#v", got)
	}
	if _, exists := messages[0]["artifact_refs"]; exists {
		t.Fatalf("did not expect arbitrary metadata passthrough, got %#v", messages[0])
	}
}

func TestRuntimeMessagesToProtocolMessages_OpenAIReplaysDeepSeekReasoningContentWithoutToolCalls(t *testing.T) {
	assistant := types.Message{
		Role:     "assistant",
		Content:  "目录已经确认。",
		Metadata: types.NewMetadata(),
	}
	types.SetReasoningBlock(assistant.Metadata, &types.ReasoningBlock{
		Provider:   "deepseek",
		Format:     "openai_compatible",
		Summary:    "Now I can see the directory structure. Let me show this to the user.",
		Streamable: true,
		Visibility: types.ReasoningVisibilitySummary,
	})

	messages := RuntimeMessagesToProtocolMessages([]types.Message{assistant}, "openai")
	if len(messages) != 1 {
		t.Fatalf("expected 1 protocol message, got %d", len(messages))
	}
	if got := messages[0]["reasoning_content"]; got != "Now I can see the directory structure. Let me show this to the user." {
		t.Fatalf("expected deepseek reasoning_content replay, got %#v", got)
	}
}

func TestRuntimeMessagesToProtocolMessages_OpenAIReplaysDeepSeekEmptyReasoningContentForToolCalls(t *testing.T) {
	assistant := types.Message{
		Role:    "assistant",
		Content: "",
		ToolCalls: []types.ToolCall{
			{ID: "call_view", Name: "view"},
		},
		Metadata: types.NewMetadata(),
	}

	messages := RuntimeMessagesToProtocolMessages([]types.Message{assistant}, "openai", "deepseek")
	if len(messages) != 1 {
		t.Fatalf("expected 1 protocol message, got %d", len(messages))
	}
	if got, exists := messages[0]["reasoning_content"]; !exists || got != "" {
		t.Fatalf("expected empty deepseek reasoning_content replay, got exists=%v value=%#v", exists, got)
	}
}

func TestRuntimeMessagesToProtocolMessages_OpenAIReplaysDeepSeekEmptyReasoningContentFromModelHint(t *testing.T) {
	assistant := types.Message{
		Role:    "assistant",
		Content: "",
		ToolCalls: []types.ToolCall{
			{ID: "call_view", Name: "view"},
		},
		Metadata: types.NewMetadata(),
	}

	messages := RuntimeMessagesToProtocolMessages([]types.Message{assistant}, "openai", "", "deepseek-v4-flash")
	if len(messages) != 1 {
		t.Fatalf("expected 1 protocol message, got %d", len(messages))
	}
	if got, exists := messages[0]["reasoning_content"]; !exists || got != "" {
		t.Fatalf("expected empty deepseek reasoning_content replay from model hint, got exists=%v value=%#v", exists, got)
	}
}

func TestRuntimeMessagesToProtocolMessages_DeepSeekReplaysEmptyReasoningForPlainTextAssistantTurn(t *testing.T) {
	assistant := types.Message{
		Role:     "assistant",
		Content:  "别急，我先看看结果。",
		Metadata: types.NewMetadata(),
	}

	messages := RuntimeMessagesToProtocolMessages([]types.Message{assistant}, "openai", "deepseek-v4-flash")
	if len(messages) != 1 {
		t.Fatalf("expected 1 protocol message, got %d", len(messages))
	}
	if got, exists := messages[0]["reasoning_content"]; !exists || got != "" {
		t.Fatalf("expected empty deepseek reasoning_content key on plain-text assistant turn, got exists=%v value=%#v", exists, got)
	}
}

func TestRuntimeMessagesToProtocolMessages_DeepSeekModelHintWinsOverProviderName(t *testing.T) {
	assistant := types.Message{
		Role: "assistant",
		ToolCalls: []types.ToolCall{
			{ID: "call_view", Name: "view"},
		},
		Metadata: types.NewMetadata(),
	}

	// aicli passes (ProviderName, Model): the provider name (e.g.
	// "sub.aiok.club") does not identify DeepSeek, but the model hint must.
	messages := RuntimeMessagesToProtocolMessages([]types.Message{assistant}, "openai", "sub.aiok.club", "deepseek-v4-flash")
	if len(messages) != 1 {
		t.Fatalf("expected 1 protocol message, got %d", len(messages))
	}
	if got, exists := messages[0]["reasoning_content"]; !exists || got != "" {
		t.Fatalf("expected empty deepseek reasoning_content key from model hint, got exists=%v value=%#v", exists, got)
	}
}

func TestRuntimeMessagesToProtocolMessages_OpenAIDropsOrphanToolReplayPayload(t *testing.T) {
	assistant := types.Message{
		Role: "assistant",
		ToolCalls: []types.ToolCall{
			{ID: "call_view", Name: "view"},
		},
		Metadata: types.NewMetadata(),
	}
	validTool := types.Message{
		Role:       "tool",
		Content:    "view result",
		ToolCallID: "call_view",
		Metadata:   types.NewMetadata(),
	}

	messages := sanitizeOpenAICompatibleProtocolMessages([]map[string]interface{}{
		runtimeMessageToAdapterMessage(*types.NewToolMessage("call_old_1", "old result 1"), "openai", ""),
		runtimeMessageToAdapterMessage(*types.NewToolMessage("call_old_2", "old result 2"), "openai", ""),
		runtimeMessageToAdapterMessage(assistant, "openai", ""),
		runtimeMessageToAdapterMessage(validTool, "openai", ""),
	})

	if len(messages) != 2 {
		t.Fatalf("expected orphan tool replay messages to be dropped, got %d messages: %#v", len(messages), messages)
	}

	if got := messages[0]["role"]; got != "assistant" {
		t.Fatalf("expected assistant message first after sanitization, got %#v", messages[0])
	}
	if got := messages[1]["tool_call_id"]; got != "call_view" {
		t.Fatalf("expected only valid tool replay to remain, got %#v", messages[1])
	}
}

func TestRuntimeMessagesToProtocolMessages_OpenAIDropsIncompleteToolReplayBlock(t *testing.T) {
	assistant := types.Message{
		Role: "assistant",
		ToolCalls: []types.ToolCall{
			{ID: "call_1", Name: "view"},
			{ID: "call_2", Name: "grep"},
		},
		Metadata: types.NewMetadata(),
	}

	messages := sanitizeOpenAICompatibleProtocolMessages([]map[string]interface{}{
		runtimeMessageToAdapterMessage(assistant, "openai", ""),
		runtimeMessageToAdapterMessage(*types.NewToolMessage("call_2", "grep result"), "openai", ""),
		runtimeMessageToAdapterMessage(*types.NewUserMessage("继续"), "openai", ""),
	})

	if len(messages) != 1 {
		t.Fatalf("expected incomplete assistant tool replay block to be dropped, got %d messages: %#v", len(messages), messages)
	}
	if got := messages[0]["role"]; got != "user" {
		t.Fatalf("expected only the follow-up user message to remain, got %#v", messages[0])
	}
}

func TestRuntimeMessagesToProtocolMessages_CodexDropsOrphanToolReplayPayload(t *testing.T) {
	assistant := types.Message{
		Role: "assistant",
		ToolCalls: []types.ToolCall{
			{ID: "call_view", Name: "view"},
		},
		Metadata: types.NewMetadata(),
	}
	validTool := types.Message{
		Role:       "tool",
		Content:    "view result",
		ToolCallID: "call_view",
		Metadata:   types.NewMetadata(),
	}

	messages := RuntimeMessagesToProtocolMessages([]types.Message{
		*types.NewToolMessage("call_old_1", "old result 1"),
		*types.NewToolMessage("call_old_2", "old result 2"),
		assistant,
		validTool,
	}, "codex")

	if len(messages) != 2 {
		t.Fatalf("expected orphan tool replay messages to be dropped, got %d messages: %#v", len(messages), messages)
	}

	if got := messages[0]["role"]; got != "assistant" {
		t.Fatalf("expected assistant message first after sanitization, got %#v", messages[0])
	}
	if got := messages[1]["tool_call_id"]; got != "call_view" {
		t.Fatalf("expected only valid tool replay to remain, got %#v", messages[1])
	}
}

func TestRuntimeMessagesToProtocolMessages_CodexCanonicalizesAssistantReplayShape(t *testing.T) {
	assistant := types.Message{
		Role:    "assistant",
		Content: "我来查看目录。",
		ToolCalls: []types.ToolCall{
			{
				ID:   "call_ls",
				Name: "ls",
				Args: map[string]interface{}{"path": "."},
			},
		},
		Metadata: types.NewMetadata(),
	}
	types.SetReasoningBlock(assistant.Metadata, &types.ReasoningBlock{
		Format:     "openai_responses",
		Summary:    "先查看目录结构。",
		Visibility: types.ReasoningVisibilitySummary,
	})

	messages := RuntimeMessagesToProtocolMessages([]types.Message{assistant}, "codex")
	if len(messages) != 1 {
		t.Fatalf("expected 1 protocol message, got %d", len(messages))
	}

	if _, exists := messages[0]["tool_calls"]; exists {
		t.Fatalf("expected codex assistant replay to use response_output_items, got %#v", messages[0])
	}
	outputItems, ok := messages[0]["response_output_items"].([]map[string]interface{})
	if !ok || len(outputItems) != 3 {
		t.Fatalf("expected 3 response_output_items, got %T %#v", messages[0]["response_output_items"], messages[0]["response_output_items"])
	}
	if outputItems[0]["type"] != "reasoning" {
		t.Fatalf("expected first output item reasoning, got %#v", outputItems[0])
	}
	if outputItems[1]["type"] != "message" {
		t.Fatalf("expected second output item message, got %#v", outputItems[1])
	}
	if outputItems[2]["type"] != "function_call" || outputItems[2]["call_id"] != "call_ls" {
		t.Fatalf("expected function_call output item, got %#v", outputItems[2])
	}
}

func TestRuntimeMessagesToProtocolMessages_CodexStripsVolatileReplayFields(t *testing.T) {
	assistant := types.Message{
		Role:     "assistant",
		Metadata: types.NewMetadata(),
	}
	types.SetReasoningBlock(assistant.Metadata, &types.ReasoningBlock{
		Format:     "openai_responses",
		Visibility: types.ReasoningVisibilityOpaque,
		Metadata: map[string]interface{}{
			"response_output_items": []map[string]interface{}{
				{
					"type":              "reasoning",
					"id":                "rs_123",
					"status":            "completed",
					"encrypted_content": "opaque-token",
					"summary": []map[string]interface{}{
						{
							"type":   "summary_text",
							"id":     "sum_1",
							"status": "completed",
							"text":   "先检查最近的工具输出。",
						},
					},
				},
				{
					"type":   "message",
					"id":     "msg_123",
					"phase":  "final_answer",
					"status": "completed",
					"role":   "assistant",
					"content": []map[string]interface{}{
						{
							"type":        "output_text",
							"text":        "我先整理已知信息。",
							"logprobs":    []interface{}{},
							"annotations": []map[string]interface{}{},
						},
					},
				},
				{
					"type":      "function_call",
					"id":        "call_123",
					"status":    "completed",
					"name":      "view",
					"arguments": `{"file_path":"README.md"}`,
				},
			},
		},
	})

	messages := RuntimeMessagesToProtocolMessages([]types.Message{assistant}, "codex")
	if len(messages) != 1 {
		t.Fatalf("expected 1 protocol message, got %d", len(messages))
	}

	outputItems, ok := messages[0]["response_output_items"].([]map[string]interface{})
	if !ok || len(outputItems) != 3 {
		t.Fatalf("expected 3 response_output_items, got %#v", messages[0]["response_output_items"])
	}

	if _, exists := outputItems[0]["id"]; exists {
		t.Fatalf("did not expect reasoning item id after canonicalization: %#v", outputItems[0])
	}
	if _, exists := outputItems[0]["status"]; exists {
		t.Fatalf("did not expect reasoning item status after canonicalization: %#v", outputItems[0])
	}
	if outputItems[0]["encrypted_content"] != "opaque-token" {
		t.Fatalf("expected encrypted_content to be preserved, got %#v", outputItems[0]["encrypted_content"])
	}

	if _, exists := outputItems[1]["id"]; exists {
		t.Fatalf("did not expect message item id after canonicalization: %#v", outputItems[1])
	}
	if _, exists := outputItems[1]["phase"]; exists {
		t.Fatalf("did not expect message item phase after canonicalization: %#v", outputItems[1])
	}
	content, ok := outputItems[1]["content"].([]map[string]interface{})
	if !ok || len(content) != 1 {
		t.Fatalf("expected 1 message content part, got %#v", outputItems[1]["content"])
	}
	if _, exists := content[0]["logprobs"]; exists {
		t.Fatalf("did not expect logprobs after canonicalization: %#v", content[0])
	}
	if _, exists := content[0]["annotations"]; exists {
		t.Fatalf("did not expect empty annotations after canonicalization: %#v", content[0])
	}

	if outputItems[2]["call_id"] != "call_123" {
		t.Fatalf("expected function_call id to normalize into call_id, got %#v", outputItems[2])
	}
	if _, exists := outputItems[2]["id"]; exists {
		t.Fatalf("did not expect raw function_call id field after canonicalization: %#v", outputItems[2])
	}
	if _, exists := outputItems[2]["status"]; exists {
		t.Fatalf("did not expect function_call status after canonicalization: %#v", outputItems[2])
	}
}

func TestRuntimeMessagesToProtocolMessages_CodexPreservesEmptyReasoningSummaryForOpaqueReplay(t *testing.T) {
	assistant := types.Message{
		Role:     "assistant",
		Metadata: types.NewMetadata(),
	}
	types.SetReasoningBlock(assistant.Metadata, &types.ReasoningBlock{
		Format:     "openai_responses",
		Visibility: types.ReasoningVisibilityOpaque,
		Metadata: map[string]interface{}{
			"response_output_items": []map[string]interface{}{
				{
					"type":              "reasoning",
					"encrypted_content": "opaque-token",
					"summary":           []map[string]interface{}{},
				},
			},
		},
	})

	messages := RuntimeMessagesToProtocolMessages([]types.Message{assistant}, "codex")
	if len(messages) != 1 {
		t.Fatalf("expected 1 protocol message, got %d", len(messages))
	}

	outputItems, ok := messages[0]["response_output_items"].([]map[string]interface{})
	if !ok || len(outputItems) != 1 {
		t.Fatalf("expected 1 response_output_item, got %#v", messages[0]["response_output_items"])
	}
	if outputItems[0]["encrypted_content"] != "opaque-token" {
		t.Fatalf("expected encrypted_content to be preserved, got %#v", outputItems[0]["encrypted_content"])
	}

	summary, ok := outputItems[0]["summary"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected empty reasoning summary array to be preserved, got %#v", outputItems[0]["summary"])
	}
	if len(summary) != 0 {
		t.Fatalf("expected empty reasoning summary array, got %#v", summary)
	}
}

func TestRuntimeMessagesToProtocolMessages_CodexCanonicalizesImageGenerationReplayShape(t *testing.T) {
	assistant := types.Message{
		Role:     "assistant",
		Metadata: types.NewMetadata(),
	}
	types.SetReasoningBlock(assistant.Metadata, &types.ReasoningBlock{
		Format:     "openai_responses",
		Visibility: types.ReasoningVisibilityOpaque,
		Metadata: map[string]interface{}{
			"response_output_items": []map[string]interface{}{
				{
					"type":           "image_generation_call",
					"id":             "ig_123",
					"status":         "completed",
					"action":         "generate",
					"background":     "opaque",
					"output_format":  "png",
					"quality":        "medium",
					"size":           "1024x1024",
					"revised_prompt": "poster",
					"result":         "Zm9v",
				},
			},
		},
	})

	messages := RuntimeMessagesToProtocolMessages([]types.Message{assistant}, "codex")
	if len(messages) != 1 {
		t.Fatalf("expected 1 protocol message, got %d", len(messages))
	}

	outputItems, ok := messages[0]["response_output_items"].([]map[string]interface{})
	if !ok || len(outputItems) != 1 {
		t.Fatalf("expected 1 response_output_item, got %#v", messages[0]["response_output_items"])
	}
	if outputItems[0]["type"] != "image_generation_call" {
		t.Fatalf("expected image_generation_call item, got %#v", outputItems[0])
	}
	if outputItems[0]["id"] != "ig_123" {
		t.Fatalf("expected image_generation_call id to be preserved, got %#v", outputItems[0]["id"])
	}
	if outputItems[0]["status"] != "completed" {
		t.Fatalf("expected image_generation_call status to be preserved, got %#v", outputItems[0]["status"])
	}
	if outputItems[0]["result"] != "Zm9v" {
		t.Fatalf("expected image_generation_call result to be preserved, got %#v", outputItems[0]["result"])
	}
	if _, exists := outputItems[0]["action"]; exists {
		t.Fatalf("did not expect image_generation_call action after canonicalization: %#v", outputItems[0])
	}
	if _, exists := outputItems[0]["background"]; exists {
		t.Fatalf("did not expect image_generation_call background after canonicalization: %#v", outputItems[0])
	}
	if _, exists := outputItems[0]["output_format"]; exists {
		t.Fatalf("did not expect image_generation_call output_format after canonicalization: %#v", outputItems[0])
	}
	if _, exists := outputItems[0]["quality"]; exists {
		t.Fatalf("did not expect image_generation_call quality after canonicalization: %#v", outputItems[0])
	}
	if _, exists := outputItems[0]["size"]; exists {
		t.Fatalf("did not expect image_generation_call size after canonicalization: %#v", outputItems[0])
	}
}

func TestRuntimeMessagesToProtocolMessages_CodexHydratesSavedImageGenerationResult(t *testing.T) {
	outputDir := t.TempDir()
	savedPath := filepath.Join(outputDir, "ig_123.png")
	raw := []byte("fake-png-payload")
	if err := os.WriteFile(savedPath, raw, 0o644); err != nil {
		t.Fatalf("write saved image: %v", err)
	}

	assistant := types.Message{
		Role:     "assistant",
		Metadata: types.NewMetadata(),
	}
	assistant.Metadata[MetadataKeyGeneratedImages] = []map[string]interface{}{
		{
			"id":         "ig_123",
			"saved_path": savedPath,
		},
	}
	types.SetReasoningBlock(assistant.Metadata, &types.ReasoningBlock{
		Format:     "openai_responses",
		Visibility: types.ReasoningVisibilityOpaque,
		Metadata: map[string]interface{}{
			"response_output_items": []map[string]interface{}{
				{
					"type":           "image_generation_call",
					"id":             "ig_123",
					"status":         "generating",
					"revised_prompt": "poster",
				},
			},
		},
	})

	messages := RuntimeMessagesToProtocolMessages([]types.Message{assistant}, "codex")
	if len(messages) != 1 {
		t.Fatalf("expected 1 protocol message, got %d", len(messages))
	}

	outputItems, ok := messages[0]["response_output_items"].([]map[string]interface{})
	if !ok || len(outputItems) != 1 {
		t.Fatalf("expected 1 response_output_item, got %#v", messages[0]["response_output_items"])
	}
	if outputItems[0]["status"] != "completed" {
		t.Fatalf("expected hydrated image_generation_call status to normalize to completed, got %#v", outputItems[0]["status"])
	}
	if outputItems[0]["result"] != base64.StdEncoding.EncodeToString(raw) {
		t.Fatalf("expected hydrated image_generation_call result from saved image, got %#v", outputItems[0]["result"])
	}
}

func TestEnforceAnthropicMessageAlternation_MergesConsecutiveUserMessages(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "user", "content": "hello"},
		{"role": "user", "content": "world"},
		{"role": "assistant", "content": "response"},
	}

	result := enforceAnthropicMessageAlternation(messages)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages after merge, got %d", len(result))
	}

	// First message should be merged user content
	if result[0]["role"] != "user" {
		t.Fatalf("expected user role, got %v", result[0]["role"])
	}
	blocks, ok := result[0]["content"].([]interface{})
	if !ok {
		t.Fatalf("expected content blocks, got %T", result[0]["content"])
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 content blocks after merge, got %d", len(blocks))
	}

	if result[1]["role"] != "assistant" {
		t.Fatalf("expected assistant role, got %v", result[1]["role"])
	}
}

func TestEnforceAnthropicMessageAlternation_MergesConsecutiveAssistantMessages(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "user", "content": "hello"},
		{"role": "assistant", "content": "part1"},
		{"role": "assistant", "content": "part2"},
	}

	result := enforceAnthropicMessageAlternation(messages)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages after merge, got %d", len(result))
	}
	if result[0]["role"] != "user" {
		t.Fatalf("expected user role first, got %v", result[0]["role"])
	}
	if result[1]["role"] != "assistant" {
		t.Fatalf("expected assistant role, got %v", result[1]["role"])
	}
	blocks, ok := result[1]["content"].([]interface{})
	if !ok {
		t.Fatalf("expected content blocks, got %T", result[1]["content"])
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 content blocks after merge, got %d", len(blocks))
	}
}

func TestEnforceAnthropicMessageAlternation_PreservesAlreadyAlternating(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "user", "content": "hello"},
		{"role": "assistant", "content": "hi"},
		{"role": "user", "content": "bye"},
	}

	result := enforceAnthropicMessageAlternation(messages)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages preserved, got %d", len(result))
	}
}

func TestEnforceAnthropicMessageAlternation_PreservesStructuredAssistantBlocks(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "user", "content": "hello"},
		{
			"role": "assistant",
			"content": []map[string]interface{}{
				{"type": "thinking", "thinking": "plan first"},
				{"type": "text", "text": "I will inspect the repo."},
				{"type": "tool_use", "id": "call_1", "name": "execute_shell_command", "input": map[string]interface{}{"command": "pwd"}},
			},
		},
		{"role": "assistant", "content": "follow-up"},
	}

	result := enforceAnthropicMessageAlternation(messages)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages after merge, got %d", len(result))
	}
	if result[1]["role"] != "assistant" {
		t.Fatalf("expected assistant role, got %v", result[1]["role"])
	}

	blocks, ok := result[1]["content"].([]interface{})
	if !ok {
		t.Fatalf("expected assistant content blocks, got %T %#v", result[1]["content"], result[1]["content"])
	}
	if len(blocks) != 4 {
		t.Fatalf("expected 4 assistant content blocks after merge, got %d", len(blocks))
	}

	first, ok := blocks[0].(map[string]interface{})
	if !ok || first["type"] != "thinking" {
		t.Fatalf("expected first block to remain thinking, got %#v", blocks[0])
	}
	third, ok := blocks[2].(map[string]interface{})
	if !ok || third["type"] != "tool_use" {
		t.Fatalf("expected tool_use block to remain structured, got %#v", blocks[2])
	}
	last, ok := blocks[3].(map[string]interface{})
	if !ok || last["type"] != "text" || last["text"] != "follow-up" {
		t.Fatalf("expected appended text block, got %#v", blocks[3])
	}
}

func TestRuntimeMessagesToProtocolMessages_AnthropicDropsTrailingAssistantPrefill(t *testing.T) {
	messages := RuntimeMessagesToProtocolMessages([]types.Message{
		*types.NewSystemMessage("Base instruction"),
		*types.NewUserMessage("First question"),
		*types.NewAssistantMessage("First answer"),
		*types.NewDeveloperMessage("Late instruction"),
	}, "anthropic")

	if len(messages) != 3 {
		t.Fatalf("expected trailing assistant to be dropped while preserving instructions, got %d messages: %#v", len(messages), messages)
	}
	if messages[0]["role"] != "system" {
		t.Fatalf("expected system instruction to remain, got %#v", messages[0])
	}
	if messages[1]["role"] != "user" {
		t.Fatalf("expected last dialogue message to be user, got %#v", messages[1])
	}
	if messages[2]["role"] != "developer" {
		t.Fatalf("expected trailing developer instruction to remain, got %#v", messages[2])
	}
}

func TestRuntimeMessagesToProtocolMessages_AnthropicKeepsToolResultTurnAtEnd(t *testing.T) {
	assistant := types.Message{
		Role: "assistant",
		ToolCalls: []types.ToolCall{
			{ID: "call_1", Name: "list_files"},
		},
		Metadata: types.NewMetadata(),
	}
	tool := types.Message{
		Role:       "tool",
		Content:    "ok",
		ToolCallID: "call_1",
		Metadata:   types.NewMetadata(),
	}

	messages := RuntimeMessagesToProtocolMessages([]types.Message{
		*types.NewUserMessage("List files"),
		assistant,
		tool,
	}, "anthropic")

	if len(messages) != 3 {
		t.Fatalf("expected tool_result replay to be preserved, got %d messages: %#v", len(messages), messages)
	}
	if messages[len(messages)-1]["role"] != "user" {
		t.Fatalf("expected Anthropic tool_result replay to end with user, got %#v", messages[len(messages)-1])
	}
}

func TestRuntimeMessagesToProtocolMessages_AnthropicDropsIncompleteToolReplayBlock(t *testing.T) {
	assistant := types.Message{
		Role: "assistant",
		ToolCalls: []types.ToolCall{
			{ID: "call_1", Name: "view"},
			{ID: "call_2", Name: "grep"},
		},
		Metadata: types.NewMetadata(),
	}

	messages := RuntimeMessagesToProtocolMessages([]types.Message{
		*types.NewUserMessage("Inspect the repository"),
		assistant,
		*types.NewToolMessage("call_1", "view result"),
		*types.NewUserMessage("Continue without the missing result"),
	}, "anthropic")

	if len(messages) != 1 {
		t.Fatalf("expected incomplete assistant tool replay to be dropped, got %d messages: %#v", len(messages), messages)
	}
	if messages[0]["role"] != "user" {
		t.Fatalf("expected remaining user content to be preserved, got %#v", messages[0])
	}
	for _, block := range decodeSliceOfMaps(messages[0]["content"]) {
		if block["type"] == "tool_result" {
			t.Fatalf("did not expect partial orphan tool_result after sanitization: %#v", messages[0])
		}
	}
}

func TestRuntimeMessagesToProtocolMessages_AnthropicKeepsCompleteMultiToolReplay(t *testing.T) {
	assistant := types.Message{
		Role: "assistant",
		ToolCalls: []types.ToolCall{
			{ID: "call_1", Name: "view"},
			{ID: "call_2", Name: "grep"},
		},
		Metadata: types.NewMetadata(),
	}

	messages := RuntimeMessagesToProtocolMessages([]types.Message{
		*types.NewUserMessage("Inspect the repository"),
		assistant,
		*types.NewToolMessage("call_1", "view result"),
		*types.NewToolMessage("call_2", "grep result"),
	}, "anthropic")

	if len(messages) != 3 {
		t.Fatalf("expected complete multi-tool replay to be preserved, got %d messages: %#v", len(messages), messages)
	}
	blocks := decodeSliceOfMaps(messages[2]["content"])
	if len(blocks) != 2 {
		t.Fatalf("expected two tool_result blocks, got %#v", messages[2])
	}
	if blocks[0]["tool_use_id"] != "call_1" || blocks[1]["tool_use_id"] != "call_2" {
		t.Fatalf("expected both matching tool results in order, got %#v", blocks)
	}
}

// 回归：上游只给 signature 不给 thinking 文本（如 strict 网关上的 opaque
// thinking）时，重放必须输出 redacted_thinking 块而非 `{"type":"thinking"}`
// 残缺块——后者会被严格网关以 "thinking.thinking: Field required" 拒绝。
func TestRuntimeMessagesToProtocolMessages_AnthropicOpaqueThinkingReplaysAsRedactedThinking(t *testing.T) {
	assistant := types.Message{
		Role:    "assistant",
		Content: "我看一下。",
		Metadata: types.NewMetadata(),
	}
	types.SetReasoningBlock(assistant.Metadata, &types.ReasoningBlock{
		Format:         "anthropic_thinking",
		Summary:        "",
		OpaqueState:    "sig_abc123",
		ReplayRequired: true,
		Streamable:     true,
		Visibility:     types.ReasoningVisibilityOpaque,
	})

	messages := RuntimeMessagesToProtocolMessages([]types.Message{
		*types.NewUserMessage("继续"),
		assistant,
		*types.NewUserMessage("再来一轮"),
	}, "anthropic")

	if len(messages) != 3 {
		t.Fatalf("expected both messages to be preserved, got %d: %#v", len(messages), messages)
	}
	blocks := decodeSliceOfMaps(messages[1]["content"])
	if len(blocks) != 2 {
		t.Fatalf("expected thinking + text blocks, got %#v", messages[1])
	}
	if blocks[0]["type"] != "redacted_thinking" {
		t.Fatalf("expected redacted_thinking block for signature-only reasoning, got %#v", blocks[0])
	}
	if blocks[0]["data"] != "sig_abc123" {
		t.Fatalf("expected signature in redacted_thinking.data, got %#v", blocks[0])
	}
	if _, exists := blocks[0]["thinking"]; exists {
		t.Fatalf("redacted_thinking block must not carry a thinking field: %#v", blocks[0])
	}
	if blocks[1]["type"] != "text" {
		t.Fatalf("expected text block after thinking, got %#v", blocks[1])
	}
}

// 有可见文本 + signature 的 reasoning 重放时保持 thinking 块（text+signature）。
func TestRuntimeMessagesToProtocolMessages_AnthropicThinkingWithTextAndSignature(t *testing.T) {
	assistant := types.Message{
		Role:    "assistant",
		Content: "结论。",
		Metadata: types.NewMetadata(),
	}
	types.SetReasoningBlock(assistant.Metadata, &types.ReasoningBlock{
		Format:         "anthropic_thinking",
		Summary:        "推理过程摘要",
		OpaqueState:    "sig_def456",
		ReplayRequired: true,
		Streamable:     true,
		Visibility:     types.ReasoningVisibilitySummary,
	})

	messages := RuntimeMessagesToProtocolMessages([]types.Message{
		*types.NewUserMessage("继续"),
		assistant,
		*types.NewUserMessage("再来一轮"),
	}, "anthropic")

	blocks := decodeSliceOfMaps(messages[1]["content"])
	if len(blocks) != 2 {
		t.Fatalf("expected thinking + text blocks, got %#v", messages[1])
	}
	if blocks[0]["type"] != "thinking" {
		t.Fatalf("expected thinking block, got %#v", blocks[0])
	}
	if blocks[0]["thinking"] != "推理过程摘要" {
		t.Fatalf("expected thinking text preserved, got %#v", blocks[0])
	}
	if blocks[0]["signature"] != "sig_def456" {
		t.Fatalf("expected signature on thinking block, got %#v", blocks[0])
	}
}

func TestNormalizeAnthropicReplayContentBlocks(t *testing.T) {
	complete := map[string]interface{}{"type": "thinking", "thinking": "text", "signature": "sig"}
	missingField := map[string]interface{}{"type": "thinking", "signature": "sig_only"}
	// 官方 omitted-display 合法形态：thinking 键存在但为空串，必须原样回传。
	emptyString := map[string]interface{}{"type": "thinking", "thinking": "", "signature": "sig_empty"}
	emptyThinking := map[string]interface{}{"type": "thinking"}
	text := map[string]interface{}{"type": "text", "text": "hi"}

	got := normalizeAnthropicReplayContentBlocks([]map[string]interface{}{
		complete, missingField, emptyString, emptyThinking, text, nil,
	})
	if len(got) != 4 {
		t.Fatalf("expected 4 normalized blocks, got %d: %#v", len(got), got)
	}
	if got[0]["type"] != "thinking" || got[0]["thinking"] != "text" || got[0]["signature"] != "sig" {
		t.Fatalf("complete thinking block must pass through untouched, got %#v", got[0])
	}
	if got[1]["type"] != "redacted_thinking" || got[1]["data"] != "sig_only" {
		t.Fatalf("missing-field thinking with signature must become redacted_thinking, got %#v", got[1])
	}
	if got[1]["thinking"] != nil {
		t.Fatalf("redacted_thinking must not keep a thinking field, got %#v", got[1])
	}
	if got[2]["type"] != "thinking" || got[2]["thinking"] != "" || got[2]["signature"] != "sig_empty" {
		t.Fatalf("empty-string thinking block must pass through untouched, got %#v", got[2])
	}
	if got[3]["type"] != "text" || got[3]["text"] != "hi" {
		t.Fatalf("non-thinking block must pass through untouched, got %#v", got[3])
	}

	untouched := normalizeAnthropicReplayContentBlocks([]map[string]interface{}{complete, emptyString, text})
	if len(untouched) != 3 || untouched[1]["type"] != "thinking" {
		t.Fatalf("all-valid blocks should be returned as-is, got %#v", untouched)
	}
	if len(normalizeAnthropicReplayContentBlocks(nil)) != 0 {
		t.Fatal("nil input should return nil")
	}
}

func TestAnthropicToolUseBlock_DefaultsEmptyInputForNoArgToolCall(t *testing.T) {
	block := anthropicToolUseBlock(map[string]interface{}{
		"id":   "call_1",
		"name": "list_mcp_resources",
	})
	if block == nil {
		t.Fatal("expected tool_use block, got nil")
	}

	input, ok := block["input"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected input map, got %T %#v", block["input"], block["input"])
	}
	if len(input) != 0 {
		t.Fatalf("expected empty input map, got %#v", input)
	}
}

func TestGeminiFunctionCallPart_DefaultsEmptyArgsForNoArgToolCall(t *testing.T) {
	part := geminiFunctionCallPart(map[string]interface{}{
		"id":   "call_1",
		"name": "list_mcp_resources",
	})
	if part == nil {
		t.Fatal("expected functionCall part, got nil")
	}

	functionCall, ok := part["functionCall"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected functionCall map, got %T %#v", part["functionCall"], part["functionCall"])
	}
	args, ok := functionCall["args"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected args map, got %T %#v", functionCall["args"], functionCall["args"])
	}
	if len(args) != 0 {
		t.Fatalf("expected empty args map, got %#v", args)
	}
}

func TestEncodeProviderToolCalls_NormalizesFunctionCallType(t *testing.T) {
	calls := []ToolCall{
		{
			ID:   "call_flat",
			Type: "function_call", // Responses/codex 协议泄漏的历史值
			Function: ToolCallFunc{
				Name:      "list_files",
				Arguments: `{"path":"."}`,
			},
		},
		{
			ID:   "call_ok",
			Type: "function",
			Function: ToolCallFunc{
				Name:      "search",
				Arguments: `{"q":"go"}`,
			},
		},
		{
			ID:   "call_custom",
			Type: "custom_tool_call",
			Function: ToolCallFunc{
				Name:      "apply_patch",
				Arguments: "raw input",
			},
		},
	}
	encoded := encodeProviderToolCalls(calls)
	if len(encoded) != 3 {
		t.Fatalf("expected 3 encoded tool calls, got %d: %#v", len(encoded), encoded)
	}
	if got := encoded[0]["type"]; got != "function" {
		t.Errorf("expected function_call to be coerced to %q, got %#v", "function", got)
	}
	if got := encoded[1]["type"]; got != "function" {
		t.Errorf("expected function type preserved, got %#v", got)
	}
	if got := encoded[2]["type"]; got != "custom_tool_call" {
		t.Errorf("expected custom_tool_call preserved, got %#v", got)
	}
	if fn, ok := encoded[0]["function"].(map[string]interface{}); !ok || fn["name"] != "list_files" {
		t.Errorf("expected nested function payload for flat call, got %#v", encoded[0])
	}
}

func TestEncodeRuntimeToolCalls_PreservesCustomInput(t *testing.T) {
	patch := "PATCH-BEGIN\nPATCH-END"
	calls := []types.ToolCall{
		{
			ID:       "call_custom",
			Type:     "custom_tool_call",
			Name:     "apply_patch",
			RawInput: patch,
		},
		{
			ID:   "call_raw_fallback",
			Type: "custom_tool_call",
			Name: "apply_patch",
			Args: map[string]interface{}{"_raw": patch},
		},
		{
			ID:   "call_fn",
			Type: "function",
			Name: "search",
			Args: map[string]interface{}{"q": "go"},
		},
		{
			ID:   "call_nil_args",
			Name: "noop",
		},
		{
			ID:   "call_null_field",
			Name: "noop",
			Args: map[string]interface{}{"value": nil},
		},
	}

	encoded := EncodeRuntimeToolCalls(calls)
	if len(encoded) != 5 {
		t.Fatalf("expected 5 encoded tool calls, got %d: %#v", len(encoded), encoded)
	}
	if got := encoded[0]["type"]; got != "custom_tool_call" {
		t.Errorf("expected custom type preserved, got %#v", got)
	}
	if got := encoded[0]["input"]; got != patch {
		t.Errorf("expected raw input preserved verbatim, got %#v", got)
	}
	if got := encoded[1]["input"]; got != patch {
		t.Errorf("expected _raw fallback input, got %#v", got)
	}
	fn, ok := encoded[2]["function"].(map[string]interface{})
	if !ok || fn["name"] != "search" || fn["arguments"] != `{"q":"go"}` {
		t.Errorf("expected nested function payload, got %#v", encoded[2])
	}
	fnNil, ok := encoded[3]["function"].(map[string]interface{})
	if !ok || fnNil["arguments"] != "{}" {
		t.Errorf("expected nil args coerced to empty object, got %#v", encoded[3])
	}
	fnNull, ok := encoded[4]["function"].(map[string]interface{})
	if !ok || fnNull["arguments"] != `{"value":null}` {
		t.Errorf("expected null field preserved as-is, got %#v", encoded[4])
	}
}
