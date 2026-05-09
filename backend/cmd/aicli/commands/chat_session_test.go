package commands

import (
	"context"
	"errors"
	"strings"
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

func TestRuntimeMessageFromAICLIMessage_DecodesAnthropicToolUseInput(t *testing.T) {
	raw := map[string]interface{}{
		"role":    "assistant",
		"content": "",
		"tool_calls": []map[string]interface{}{
			{
				"type": "tool_use",
				"id":   "call-1",
				"name": "view",
				"input": map[string]interface{}{
					"file_path": "E:/projects/ai/ai-agent-runtime/backend/internal/agent/tool_parallel_scheduler.go",
					"limit":     220,
				},
			},
		},
	}

	message, err := runtimeMessageFromAICLIMessage(raw)
	if err != nil {
		t.Fatalf("runtimeMessageFromAICLIMessage: %v", err)
	}
	if len(message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(message.ToolCalls))
	}
	call := message.ToolCalls[0]
	if call.Name != "view" {
		t.Fatalf("unexpected tool name: %q", call.Name)
	}
	if got := call.Args["file_path"]; got != "E:/projects/ai/ai-agent-runtime/backend/internal/agent/tool_parallel_scheduler.go" {
		t.Fatalf("unexpected file_path: %#v", got)
	}
	switch got := call.Args["limit"].(type) {
	case int:
		if got != 220 {
			t.Fatalf("unexpected limit: %#v", got)
		}
	case float64:
		if got != 220 {
			t.Fatalf("unexpected limit: %#v", got)
		}
	default:
		t.Fatalf("unexpected limit type: %T (%#v)", got, got)
	}

	restored, err := aicliMessageFromRuntimeMessage(message)
	if err != nil {
		t.Fatalf("aicliMessageFromRuntimeMessage: %v", err)
	}
	toolCalls, ok := restored["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected one restored tool call, got %#v", restored["tool_calls"])
	}
	function, _ := toolCalls[0]["function"].(map[string]interface{})
	if function["name"] != "view" {
		t.Fatalf("unexpected restored function payload: %#v", function)
	}
	if got := function["arguments"]; got == "" {
		t.Fatal("expected restored arguments to be preserved")
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

	restored, err := aicliMessageFromRuntimeMessage(message)
	if err != nil {
		t.Fatalf("aicliMessageFromRuntimeMessage: %v", err)
	}
	if got, exists := restored["reasoning_content"]; !exists || got != "" {
		t.Fatalf("expected empty reasoning_content after restore, got exists=%v value=%#v", exists, got)
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

func TestLoadRequestedRuntimeSessionReturnsLatestMeaningfulSessionForResume(t *testing.T) {
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

	time.Sleep(10 * time.Millisecond)

	third, err := manager.Create(ctx, "tester")
	if err != nil {
		t.Fatalf("create third session: %v", err)
	}
	third.ReplaceHistory([]runtimetypes.Message{
		{
			Role:     "system",
			Content:  "system placeholder",
			Metadata: runtimetypes.NewMetadata(),
		},
	})
	if err := manager.Update(ctx, third); err != nil {
		t.Fatalf("update third session: %v", err)
	}

	loaded, err := loadRequestedRuntimeSession(ctx, manager, "tester", "", true)
	if err != nil {
		t.Fatalf("loadRequestedRuntimeSession: %v", err)
	}
	if loaded == nil || loaded.ID != second.ID {
		t.Fatalf("expected latest session %s, got %#v", second.ID, loaded)
	}
	if loaded.ID == third.ID {
		t.Fatalf("did not expect system-only session %s to be selected", third.ID)
	}
}

func TestResumeLatestRuntimeConversationSkipsSystemOnlySession(t *testing.T) {
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
	second.ReplaceHistory([]runtimetypes.Message{
		{
			Role:     "system",
			Content:  "system placeholder",
			Metadata: runtimetypes.NewMetadata(),
		},
	})
	if err := manager.Update(ctx, second); err != nil {
		t.Fatalf("update second session: %v", err)
	}

	session := &ChatSession{
		SessionManager: manager,
		SessionUserID:  "tester",
	}

	if err := resumeLatestRuntimeConversation(session); err != nil {
		t.Fatalf("resumeLatestRuntimeConversation: %v", err)
	}
	if session.RuntimeSession == nil {
		t.Fatal("expected runtime session to be restored")
	}
	if session.RuntimeSession.ID != first.ID {
		t.Fatalf("expected latest meaningful session %s, got %s", first.ID, session.RuntimeSession.ID)
	}
	if len(session.Messages) < 2 {
		t.Fatalf("expected restored chat history to include system prompt and user message, got %#v", session.Messages)
	}
	if session.Messages[1].Content != "first" {
		t.Fatalf("expected restored chat history from first session, got %#v", session.Messages)
	}
}

func TestResumeLatestRuntimeConversationSkipsCurrentSession(t *testing.T) {
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
	previous, err := manager.Create(ctx, "tester")
	if err != nil {
		t.Fatalf("create previous session: %v", err)
	}
	previous.ReplaceHistory([]runtimetypes.Message{{Role: "user", Content: "previous", Metadata: runtimetypes.NewMetadata()}})
	if err := manager.Update(ctx, previous); err != nil {
		t.Fatalf("update previous session: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	current, err := manager.Create(ctx, "tester")
	if err != nil {
		t.Fatalf("create current session: %v", err)
	}
	current.ReplaceHistory([]runtimetypes.Message{{Role: "user", Content: "current", Metadata: runtimetypes.NewMetadata()}})
	if err := manager.Update(ctx, current); err != nil {
		t.Fatalf("update current session: %v", err)
	}

	session := &ChatSession{
		SessionManager: manager,
		SessionUserID:  "tester",
		RuntimeSession: current,
	}

	if err := resumeLatestRuntimeConversation(session); err != nil {
		t.Fatalf("resumeLatestRuntimeConversation: %v", err)
	}
	if session.RuntimeSession == nil || session.RuntimeSession.ID != previous.ID {
		t.Fatalf("expected previous session %s, got %#v", previous.ID, session.RuntimeSession)
	}
}

func TestResumeLatestRuntimeConversationDoesNotFallbackToSystemOnlyAfterSkippingCurrent(t *testing.T) {
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
	systemOnly, err := manager.Create(ctx, "tester")
	if err != nil {
		t.Fatalf("create system-only session: %v", err)
	}
	systemOnly.ReplaceHistory([]runtimetypes.Message{{Role: "system", Content: "placeholder", Metadata: runtimetypes.NewMetadata()}})
	if err := manager.Update(ctx, systemOnly); err != nil {
		t.Fatalf("update system-only session: %v", err)
	}

	current, err := manager.Create(ctx, "tester")
	if err != nil {
		t.Fatalf("create current session: %v", err)
	}
	current.ReplaceHistory([]runtimetypes.Message{{Role: "user", Content: "current", Metadata: runtimetypes.NewMetadata()}})
	if err := manager.Update(ctx, current); err != nil {
		t.Fatalf("update current session: %v", err)
	}

	session := &ChatSession{
		SessionManager: manager,
		SessionUserID:  "tester",
		RuntimeSession: current,
	}

	err = resumeLatestRuntimeConversation(session)
	if !errors.Is(err, runtimechat.ErrSessionNotFound) {
		t.Fatalf("expected no resumable session after current was skipped, got %v", err)
	}
}

func TestListResumeCandidateChatSessionsSkipsCurrentAndSystemOnly(t *testing.T) {
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
	previous, err := manager.Create(ctx, "tester")
	if err != nil {
		t.Fatalf("create previous session: %v", err)
	}
	previous.ReplaceHistory([]runtimetypes.Message{{Role: "user", Content: "previous", Metadata: runtimetypes.NewMetadata()}})
	if err := manager.Update(ctx, previous); err != nil {
		t.Fatalf("update previous session: %v", err)
	}

	systemOnly, err := manager.Create(ctx, "tester")
	if err != nil {
		t.Fatalf("create system-only session: %v", err)
	}
	systemOnly.ReplaceHistory([]runtimetypes.Message{{Role: "system", Content: "placeholder", Metadata: runtimetypes.NewMetadata()}})
	if err := manager.Update(ctx, systemOnly); err != nil {
		t.Fatalf("update system-only session: %v", err)
	}

	current, err := manager.Create(ctx, "tester")
	if err != nil {
		t.Fatalf("create current session: %v", err)
	}
	current.ReplaceHistory([]runtimetypes.Message{{Role: "user", Content: "current", Metadata: runtimetypes.NewMetadata()}})
	if err := manager.Update(ctx, current); err != nil {
		t.Fatalf("update current session: %v", err)
	}

	candidates, err := listResumeCandidateChatSessions(manager, "tester", ChatSessionListFilter{}, current.ID)
	if err != nil {
		t.Fatalf("listResumeCandidateChatSessions: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ID != previous.ID {
		t.Fatalf("expected only previous session %s, got %#v", previous.ID, candidates)
	}
}

func TestRuntimeResumeSessionTitleStripsEmbeddedSessionMetadata(t *testing.T) {
	session := runtimechat.NewSession("tester")
	session.Metadata.Title = `检查 multi team执行功能机制， Session: session_20260506094548_QX3iM9PR [active]Session File: C:\Users\vince\.aicli\sessions\session_20260506094548_QX3iM9PR.json`

	title := runtimeResumeSessionTitle(session)
	if title != "检查 multi team执行功能机制" {
		t.Fatalf("expected embedded session metadata to be stripped, got %q", title)
	}
	if strings.Contains(strings.ToLower(title), "session_") || strings.Contains(strings.ToLower(title), "session file") {
		t.Fatalf("expected title to hide session metadata, got %q", title)
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
