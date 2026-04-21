package commands

import (
	"context"
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
