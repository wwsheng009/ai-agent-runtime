package runtimeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestAgentChat_ActorStreamingReplaysToolEventsAndPersistsOnce(t *testing.T) {
	mcpManager := &testMCPManager{}
	handler := NewHandler(skill.NewRegistry(mcpManager), nil, mcpManager)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testSequenceLLMProvider{
		name: "test-model",
		responses: []*llm.LLMResponse{
			{
				Model: "test-model",
				ToolCalls: []types.ToolCall{{
					ID:   "call-actor-1",
					Name: "echo_tool",
					Args: map[string]interface{}{"prompt": "hi"},
				}},
			},
			{
				Model:   "test-model",
				Content: "actor tool finished",
			},
		},
	}))
	handler.SetLLMRuntime(runtime)

	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	handler.SetSessionManager(sessionManager)
	session, err := sessionManager.Create(context.Background(), "actor-stream-user")
	require.NoError(t, err)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	body, err := json.Marshal(map[string]interface{}{
		"messages":     []map[string]string{{"role": "user", "content": "run actor tool"}},
		"enable_react": true,
		"stream":       true,
		"session_id":   session.ID,
		"turn_id":      "turn-actor-stream-1",
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	streamBody := rec.Body.String()
	assert.Contains(t, streamBody, `"agent_id":"api-agent"`)
	assert.Contains(t, streamBody, `"turn_id":"turn-actor-stream-1"`)
	assert.Contains(t, streamBody, "event: tool_call")
	assert.Contains(t, streamBody, "event: tool_start")
	assert.Contains(t, streamBody, "event: tool_end")
	assert.Contains(t, streamBody, `"name":"echo_tool"`)
	assert.Contains(t, streamBody, "event: result")
	assert.Contains(t, streamBody, "event: done")
	assert.Contains(t, streamBody, `"source":"agent_react"`)

	stored, err := sessionManager.GetSession(context.Background(), session.ID)
	require.NoError(t, err)
	messages := stored.GetMessages()
	require.Len(t, messages, 4, "actor owns the single history write")
	assert.Equal(t, "user", messages[0].Role)
	assert.Equal(t, "assistant", messages[1].Role)
	require.Len(t, messages[1].ToolCalls, 1)
	assert.Equal(t, "tool", messages[2].Role)
	assert.Equal(t, "assistant", messages[3].Role)
	assert.Equal(t, "actor tool finished", messages[3].Content)
}

func TestAgentChatUsesSessionActorIsConservative(t *testing.T) {
	handler := &Handler{}
	handler.sessionManager = chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(handler.sessionManager.Stop)
	handler.llmRuntime = llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model"})
	session := chat.NewSession("user")

	base := &agentChatRequest{
		Messages:    []map[string]string{{"role": "user", "content": "hi"}},
		EnableReAct: true,
	}
	require.True(t, handler.agentChatUsesSessionActor(session, base))

	multi := *base
	multi.Messages = append(multi.Messages, map[string]string{"role": "assistant", "content": "old"})
	require.False(t, handler.agentChatUsesSessionActor(session, &multi), "the last message must be user")

	multi.Messages = append(multi.Messages, map[string]string{"role": "user", "content": "next"})
	require.True(t, handler.agentChatUsesSessionActor(session, &multi), "multi-message replay is actorized")

	disabled := *base
	disabled.DisableTools = true
	require.True(t, handler.agentChatUsesSessionActor(session, &disabled), "disable_tools is a per-run actor override")

	teamRun := *base
	teamRun.TeamID = "team-1"
	teamRun.TaskID = "task-1"
	require.True(t, handler.agentChatUsesSessionActor(session, &teamRun), "team/task are context-only actor options")

	planned := *base
	planned.ExecutePlannedSubagents = true
	require.True(t, handler.agentChatUsesSessionActor(session, &planned), "ReAct direct path ignores planned-subagent fields too")

	routed := *base
	routed.EnableRoute = true
	routed.PlanningMode = "route_preferred"
	routed.PatchDecisionPolicy = "strict"
	require.True(t, handler.agentChatUsesSessionActor(session, &routed), "route/planning/patch metadata is carried by the actor adapter")
}

func TestAgentChat_MultiMessageReplayReplacesDurableHistory(t *testing.T) {
	mcpManager := &testMCPManager{}
	handler := NewHandler(skill.NewRegistry(mcpManager), nil, mcpManager)
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", &testSequenceLLMProvider{
		name: "test-model",
		responses: []*llm.LLMResponse{{
			Model:   "test-model",
			Content: "multi replay answer",
		}},
	}))
	handler.SetLLMRuntime(runtime)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	handler.SetSessionManager(sessionManager)
	session, err := sessionManager.Create(context.Background(), "multi-replay-user")
	require.NoError(t, err)
	session.AddMessage(types.Message{Role: "user", Content: "stale session history"})
	require.NoError(t, sessionManager.Update(context.Background(), session))

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	body, err := json.Marshal(map[string]interface{}{
		"messages": []map[string]string{
			{"role": "user", "content": "request history user"},
			{"role": "assistant", "content": "request history assistant"},
			{"role": "user", "content": "request next user"},
		},
		"enable_react": true,
		"session_id":   session.ID,
		"turn_id":      "turn-multi-replay",
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	stored, err := sessionManager.GetSession(context.Background(), session.ID)
	require.NoError(t, err)
	messages := stored.GetMessages()
	require.Len(t, messages, 4, "request history + appended user + assistant must be durable")
	assert.Equal(t, "request history user", messages[0].Content)
	assert.Equal(t, "request history assistant", messages[1].Content)
	assert.Equal(t, "request next user", messages[2].Content)
	assert.Equal(t, "multi replay answer", messages[3].Content)
	for _, message := range messages {
		assert.NotEqual(t, "stale session history", message.Content, "request history must be authoritative")
	}
}
