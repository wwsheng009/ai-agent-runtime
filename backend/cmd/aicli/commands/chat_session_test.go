package commands

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestAICLIMessageRoundTripPreservesTypedToolCalls(t *testing.T) {
	raw := map[string]interface{}{
		"role":    "assistant",
		"content": "",
		"tool_calls": []map[string]interface{}{
			{
				"id":   "call-1",
				"type": "function",
				"function": map[string]interface{}{
					"name":      "skill__alpha",
					"arguments": `{"prompt":"hello"}`,
				},
			},
		},
	}

	message, err := runtimeMessageFromAICLIMessage(raw)
	if err != nil {
		t.Fatalf("runtimeMessageFromAICLIMessage: %v", err)
	}

	restored, err := aicliMessageFromRuntimeMessage(message)
	if err != nil {
		t.Fatalf("aicliMessageFromRuntimeMessage: %v", err)
	}

	toolCalls, ok := restored["tool_calls"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected typed tool_calls slice, got %T", restored["tool_calls"])
	}
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	function, _ := toolCalls[0]["function"].(map[string]interface{})
	if function["name"] != "skill__alpha" {
		t.Fatalf("unexpected function payload: %#v", function)
	}
}

func TestAICLIMessageRoundTripPreservesToolMetadata(t *testing.T) {
	message := runtimetypes.Message{
		Role:       "tool",
		Content:    "Parsed JSON object with 2 keys.\nKeys: team_id, task_id",
		ToolCallID: "call-1",
		Metadata:   runtimetypes.NewMetadata(),
	}
	message.Metadata["tool_metadata"] = map[string]interface{}{
		"team_id": "team-99",
		"task_id": "task-1",
	}
	message.Metadata.Set(chatRuntimeMessageRawJSONKey, `{"role":"tool","content":"Parsed JSON object with 2 keys.\nKeys: team_id, task_id","tool_call_id":"call-1"}`)

	raw, err := aicliMessageFromRuntimeMessage(message)
	if err != nil {
		t.Fatalf("aicliMessageFromRuntimeMessage: %v", err)
	}

	restored, err := runtimeMessageFromAICLIMessage(raw)
	if err != nil {
		t.Fatalf("runtimeMessageFromAICLIMessage: %v", err)
	}

	toolMeta, ok := restored.Metadata["tool_metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected tool_metadata map, got %#v", restored.Metadata["tool_metadata"])
	}
	if toolMeta["team_id"] != "team-99" || toolMeta["task_id"] != "task-1" {
		t.Fatalf("unexpected tool metadata: %#v", toolMeta)
	}
}

func TestRuntimeMessageFromAICLIMessage_PreservesReasoningBlock(t *testing.T) {
	raw := map[string]interface{}{
		"role":              "assistant",
		"content":           "Hello!",
		"reasoning_content": "先输出 reasoning，再输出正文。",
		"reasoning_details": map[string]interface{}{
			"provider":   "nvidia",
			"format":     "openai_compatible",
			"summary":    "先输出 reasoning，再输出正文。",
			"streamable": true,
		},
	}

	message, err := runtimeMessageFromAICLIMessage(raw)
	if err != nil {
		t.Fatalf("runtimeMessageFromAICLIMessage: %v", err)
	}

	block := runtimetypes.GetReasoningBlock(message.Metadata)
	if block == nil {
		t.Fatal("expected reasoning block in runtime metadata")
	}
	if block.Provider != "nvidia" || block.Format != "openai_compatible" {
		t.Fatalf("unexpected reasoning block metadata: %#v", block)
	}
	if got := message.Metadata.GetString(chatcoreReasoningMetadataKey, ""); got != "先输出 reasoning，再输出正文。" {
		t.Fatalf("expected reasoning summary metadata, got %q", got)
	}
}

func TestRuntimeMessageFromAICLIMessage_PreservesCodexOutputItems(t *testing.T) {
	raw := map[string]interface{}{
		"role":              "assistant",
		"content":           "",
		"reasoning_content": "先确认上下文，再继续。",
		"response_output_items": []map[string]interface{}{
			{
				"type": "reasoning",
				"summary": []map[string]interface{}{
					{
						"type": "summary_text",
						"text": "先确认上下文，再继续。",
					},
				},
				"encrypted_content": "-",
			},
		},
	}

	message, err := runtimeMessageFromAICLIMessage(raw)
	if err != nil {
		t.Fatalf("runtimeMessageFromAICLIMessage: %v", err)
	}

	block := runtimetypes.GetReasoningBlock(message.Metadata)
	if block == nil {
		t.Fatal("expected reasoning block in runtime metadata")
	}
	outputItems, ok := block.Metadata["response_output_items"].([]map[string]interface{})
	if !ok || len(outputItems) != 1 {
		t.Fatalf("expected response_output_items to be preserved, got %#v", block.Metadata["response_output_items"])
	}
	if outputItems[0]["encrypted_content"] != "-" {
		t.Fatalf("expected encrypted_content to be preserved, got %#v", outputItems[0]["encrypted_content"])
	}
}

func TestRuntimeMessageFromAICLIMessage_PreservesExplicitEmptyReasoningContent(t *testing.T) {
	raw := map[string]interface{}{
		"role":              "assistant",
		"content":           "done",
		"reasoning_content": "",
	}

	message, err := runtimeMessageFromAICLIMessage(raw)
	if err != nil {
		t.Fatalf("runtimeMessageFromAICLIMessage: %v", err)
	}

	if got, exists := message.Metadata["reasoning_content"]; !exists || got != "" {
		t.Fatalf("expected empty reasoning_content metadata, got exists=%v value=%#v", exists, got)
	}

	rawJSON := message.Metadata.GetString(chatRuntimeMessageRawJSONKey, "")
	if rawJSON == "" {
		t.Fatal("expected raw message json to be preserved")
	}
	var stored map[string]interface{}
	if err := json.Unmarshal([]byte(rawJSON), &stored); err != nil {
		t.Fatalf("unmarshal raw message json: %v", err)
	}
	if got, exists := stored["reasoning_content"]; !exists || got != "" {
		t.Fatalf("expected empty reasoning_content in stored raw json, got exists=%v value=%#v", exists, got)
	}

	restored, err := aicliMessageFromRuntimeMessage(message)
	if err != nil {
		t.Fatalf("aicliMessageFromRuntimeMessage: %v", err)
	}
	if got, exists := restored["reasoning_content"]; !exists || got != "" {
		t.Fatalf("expected empty reasoning_content after restore, got exists=%v value=%#v", exists, got)
	}
}

func TestRuntimeMessageFromAICLIMessage_RecoversMissingToolCallsFromCodexOutputItems(t *testing.T) {
	raw := map[string]interface{}{
		"role":    "assistant",
		"content": "我继续查看剩余改动。",
		"tool_calls": []map[string]interface{}{
			{
				"id":   "call_1",
				"type": "function",
				"function": map[string]interface{}{
					"name":      "execute_shell_command",
					"arguments": `{"command":"echo 1"}`,
				},
			},
		},
		"reasoning_details": map[string]interface{}{
			"format":     "openai_responses",
			"streamable": true,
			"visibility": "opaque",
			"metadata": map[string]interface{}{
				"response_output_items": []map[string]interface{}{
					{
						"type": "message",
						"role": "assistant",
						"content": []map[string]interface{}{
							{
								"type": "output_text",
								"text": "我继续查看剩余改动。",
							},
						},
					},
					{
						"type":      "function_call",
						"call_id":   "call_1",
						"name":      "execute_shell_command",
						"arguments": `{"command":"echo 1"}`,
					},
					{
						"type":      "function_call",
						"call_id":   "call_2",
						"name":      "execute_shell_command",
						"arguments": `{"command":"echo 2"}`,
					},
				},
			},
		},
	}

	message, err := runtimeMessageFromAICLIMessage(raw)
	if err != nil {
		t.Fatalf("runtimeMessageFromAICLIMessage: %v", err)
	}
	if len(message.ToolCalls) != 2 {
		t.Fatalf("expected recovered tool calls from response_output_items, got %#v", message.ToolCalls)
	}
	if message.ToolCalls[1].ID != "call_2" {
		t.Fatalf("expected second recovered tool call, got %#v", message.ToolCalls[1])
	}

	rawJSON := message.Metadata.GetString(chatRuntimeMessageRawJSONKey, "")
	if rawJSON == "" {
		t.Fatal("expected raw message json to be preserved")
	}
	var stored map[string]interface{}
	if err := json.Unmarshal([]byte(rawJSON), &stored); err != nil {
		t.Fatalf("unmarshal raw message json: %v", err)
	}
	storedToolCalls, ok := stored["tool_calls"].([]interface{})
	if !ok || len(storedToolCalls) != 2 {
		t.Fatalf("expected healed raw tool_calls, got %#v", stored["tool_calls"])
	}
}

func TestBuildAICLIMessagesFromRuntimeHistory_RepairsTruncatedAssistantToolCall(t *testing.T) {
	raw := map[string]interface{}{
		"role":          "assistant",
		"content":       "前文内容<tool_call>write<arg_key>file_path</arg_key><arg_value>E:\\temp\\demo.txt</arg_value>",
		"finish_reason": "length",
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal raw: %v", err)
	}

	message := runtimetypes.Message{
		Role:     "assistant",
		Content:  raw["content"].(string),
		Metadata: runtimetypes.NewMetadata(),
	}
	message.Metadata.Set(chatRuntimeMessageRawJSONKey, string(data))

	restored, err := buildAICLIMessagesFromRuntimeHistory([]runtimetypes.Message{message})
	if err != nil {
		t.Fatalf("buildAICLIMessagesFromRuntimeHistory: %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected repaired assistant message, got %#v", restored)
	}
	if got := restored[0]["content"]; got != "前文内容" {
		t.Fatalf("expected truncated tool-call fragment to be stripped, got %#v", got)
	}
	metadata, _ := restored[0]["metadata"].(map[string]interface{})
	if metadata["session_repaired"] != "truncated_tool_call" {
		t.Fatalf("expected repair marker metadata, got %#v", metadata)
	}
	if _, exists := restored[0]["tool_calls"]; exists {
		t.Fatalf("did not expect repaired message to keep tool_calls, got %#v", restored[0]["tool_calls"])
	}
}

func TestBuildAICLIMessagesFromRuntimeHistory_DropsEmptyTruncatedAssistantAndOrphanTool(t *testing.T) {
	assistantRaw := map[string]interface{}{
		"role":          "assistant",
		"content":       "<tool_call>write<arg_key>file_path</arg_key><arg_value>E:\\temp\\demo.txt</arg_value>",
		"finish_reason": "length",
	}
	assistantData, err := json.Marshal(assistantRaw)
	if err != nil {
		t.Fatalf("marshal assistant raw: %v", err)
	}

	assistant := runtimetypes.Message{
		Role:     "assistant",
		Content:  assistantRaw["content"].(string),
		Metadata: runtimetypes.NewMetadata(),
	}
	assistant.Metadata.Set(chatRuntimeMessageRawJSONKey, string(assistantData))

	tool := runtimetypes.Message{
		Role:       "tool",
		Content:    "写入完成",
		ToolCallID: "call-truncated",
		Metadata:   runtimetypes.NewMetadata(),
	}

	restored, err := buildAICLIMessagesFromRuntimeHistory([]runtimetypes.Message{assistant, tool})
	if err != nil {
		t.Fatalf("buildAICLIMessagesFromRuntimeHistory: %v", err)
	}
	if len(restored) != 0 {
		t.Fatalf("expected truncated assistant and orphan tool to be dropped, got %#v", restored)
	}
}

func TestBuildRuntimeHistoryFromAICLIMessages_DropsOrphanToolMessages(t *testing.T) {
	history, err := buildRuntimeHistoryFromAICLIMessages([]map[string]interface{}{
		{
			"role":         "tool",
			"tool_call_id": "missing-call",
			"content":      "tool output",
		},
		{
			"role":    "user",
			"content": "继续",
		},
	})
	if err != nil {
		t.Fatalf("buildRuntimeHistoryFromAICLIMessages: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected orphan tool message to be dropped, got %#v", history)
	}
	if history[0].Role != "user" || history[0].Content != "继续" {
		t.Fatalf("unexpected remaining history: %#v", history)
	}
}

func TestDecodeToolArguments_RecordsParseError(t *testing.T) {
	args := decodeToolArguments(`{"file_path":"E:\\projects\\ai\\ai-agent-runtime\\backend\\out.txt","content":"hello`)

	if got := args["_raw"]; got == "" {
		t.Fatal("expected raw arguments to be preserved")
	}
	if got := args["_parse_error"]; got == "" {
		t.Fatal("expected parse error marker to be recorded")
	}
	if _, ok := args["file_path"]; ok {
		t.Fatal("expected invalid JSON not to be treated as a parsed object")
	}
}

func TestLoadRequestedRuntimeSessionReturnsLatestForResume(t *testing.T) {
	storage, err := runtimechat.NewFileStorage(t.TempDir())
	if err != nil {
		t.Fatalf("new file storage: %v", err)
	}
	manager := runtimechat.NewSessionManager(storage, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		MaxHistory:      20,
		CleanupInterval: 0,
		AutoArchive:     false,
	})

	ctx := context.Background()
	first, err := manager.Create(ctx, "tester")
	if err != nil {
		t.Fatalf("create first session: %v", err)
	}
	first.ReplaceHistory([]runtimetypes.Message{{Role: "user", Content: "first", Metadata: runtimetypes.NewMetadata()}})
	if err := manager.Update(ctx, first); err != nil {
		t.Fatalf("update first session: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	second, err := manager.Create(ctx, "tester")
	if err != nil {
		t.Fatalf("create second session: %v", err)
	}
	second.ReplaceHistory([]runtimetypes.Message{{Role: "user", Content: "second", Metadata: runtimetypes.NewMetadata()}})
	if err := manager.Update(ctx, second); err != nil {
		t.Fatalf("update second session: %v", err)
	}

	loaded, err := loadRequestedRuntimeSession(ctx, manager, "tester", "", true)
	if err != nil {
		t.Fatalf("loadRequestedRuntimeSession: %v", err)
	}
	if loaded == nil || loaded.ID != second.ID {
		t.Fatalf("expected latest session %s, got %#v", second.ID, loaded)
	}
}

func TestMatchesChatSessionFilter(t *testing.T) {
	session := runtimechat.NewSession("tester")
	session.UpdateTitle("Review API gateway")
	session.Metadata.Summary = "Investigate regression"
	session.Metadata.Context = map[string]interface{}{
		chatRuntimeContextProviderName: "nvidia",
		chatRuntimeContextProtocol:     "openai",
		chatRuntimeContextModel:        "gpt-4.1",
	}

	if !matchesChatSessionFilter(session, ChatSessionListFilter{Provider: "NVIDIA"}) {
		t.Fatal("expected provider filter to match case-insensitively")
	}
	if !matchesChatSessionFilter(session, ChatSessionListFilter{Model: "gpt-4.1"}) {
		t.Fatal("expected model filter to match")
	}
	if !matchesChatSessionFilter(session, ChatSessionListFilter{Protocol: "openai"}) {
		t.Fatal("expected protocol filter to match")
	}
	if matchesChatSessionFilter(session, ChatSessionListFilter{Protocol: "codex"}) {
		t.Fatal("did not expect protocol filter to match")
	}
	if !matchesChatSessionFilter(session, ChatSessionListFilter{Query: "review"}) {
		t.Fatal("expected query to match title")
	}
	if matchesChatSessionFilter(session, ChatSessionListFilter{State: runtimechat.StateArchived}) {
		t.Fatal("did not expect state filter to match")
	}
}
