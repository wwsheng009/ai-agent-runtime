package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/approvalexplain"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// §4.13 本地模式模型解释：无 runtime-server 时也用会话自身的 provider/model 做
// 一次只读小摘要，预算/提示词/记账与 runtimeapi 同源。

const localApprovalExplainTestSessionID = "session-local-explain"

// approvalExplainRequestCapture 收集解释请求体，用于断言预算与 prompt 同源。
type approvalExplainRequestCapture struct {
	mu       sync.Mutex
	requests []map[string]interface{}
}

func (c *approvalExplainRequestCapture) add(body map[string]interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, body)
}

func (c *approvalExplainRequestCapture) snapshot() []map[string]interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]interface{}(nil), c.requests...)
}

func newApprovalExplainOpenAIServer(t *testing.T, calls *int32, capture *approvalExplainRequestCapture, fail bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			atomic.AddInt32(calls, 1)
		}
		if capture != nil {
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
				capture.add(body)
			}
		}
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"message":"upstream 503"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-explain","object":"chat.completion","model":"local-mock-model",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"会删除 build/ 下的构建产物，不涉及源码。"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}`)
	}))
	t.Cleanup(server.Close)
	return server
}

// newLocalApprovalExplainTestSession 搭出本地模式最小可用会话：会话路由
// （provider/model）+ durable runtime state（pending 审批）+ 本地 host 的
// RuntimeStore/EventBus（记账订阅点）。
func newLocalApprovalExplainTestSession(t *testing.T, baseURL, requestID string) (*ChatSession, *runtimeevents.Bus) {
	t.Helper()
	store := runtimechat.NewInMemoryRuntimeStore(16)
	require.NoError(t, store.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: localApprovalExplainTestSessionID,
		PendingApproval: &runtimechat.ApprovalRequest{
			ID:        requestID,
			SessionID: localApprovalExplainTestSessionID,
			ToolName:  "shell_exec",
			ArgsJSON:  json.RawMessage(`{"command":"rm -rf build/"}`),
			Reason:    "writes outside workspace",
			RiskLevel: "high",
		},
	}))
	bus := runtimeevents.NewBusWithRetention(64)
	session := &ChatSession{
		ProviderName: "local-provider",
		Model:        "local-mock-model",
		Provider: config.Provider{
			Type:         "openai",
			BaseURL:      baseURL,
			APIPath:      "/v1/chat/completions",
			APIKey:       "test-key",
			DefaultModel: "local-mock-model",
		},
		RuntimeSession: &runtimechat.Session{ID: localApprovalExplainTestSessionID},
		LocalRuntimeHost: &localChatRuntimeHost{
			RuntimeStore: store,
			EventBus:     bus,
		},
	}
	return session, bus
}

func TestLocalApprovalExplainHookSuccessAndAccounting(t *testing.T) {
	t.Setenv(approvalexplain.ModeEnv, "on_demand")
	var calls int32
	capture := &approvalExplainRequestCapture{}
	server := newApprovalExplainOpenAIServer(t, &calls, capture, false)
	session, bus := newLocalApprovalExplainTestSession(t, server.URL, "approval-local")

	var events []runtimeevents.Event
	unsubscribe := bus.SubscribeCancelable("llm.request.finished", func(event runtimeevents.Event) {
		events = append(events, event)
	})
	t.Cleanup(unsubscribe)

	hook := newLocalApprovalExplainHook(session)
	require.NotNil(t, hook, "provider 可用时本地模式必须安装模型解释钩子")

	got, err := hook(context.Background(), "approval-local")
	require.NoError(t, err)
	require.Equal(t, "model", got.Source)
	require.Equal(t, "local-mock-model", got.Model)
	require.Contains(t, got.Explanation, "构建产物")
	require.EqualValues(t, 1, atomic.LoadInt32(&calls))

	// 预算与 prompt 同源：模型请求必须使用共享包的 max_tokens 与提示词。
	requests := capture.snapshot()
	require.Len(t, requests, 1)
	require.EqualValues(t, approvalexplain.MaxTokens, requests[0]["max_tokens"])
	messages, ok := requests[0]["messages"].([]interface{})
	require.True(t, ok)
	require.Len(t, messages, 2)
	require.Equal(t, approvalexplain.SystemPrompt, messages[0].(map[string]interface{})["content"])
	userPrompt, ok := messages[1].(map[string]interface{})["content"].(string)
	require.True(t, ok)
	require.Contains(t, userPrompt, "工具：shell_exec")
	require.Contains(t, userPrompt, "- command: rm -rf build/")

	// 记账：本地 bus 上与 runtimeapi 同形同义的 llm.request.finished。
	require.Len(t, events, 1)
	require.Equal(t, localApprovalExplainTestSessionID, events[0].SessionID)
	payload := events[0].Payload
	require.Equal(t, "approval_explain", payload["origin"])
	require.Equal(t, "approval_explain", payload["source"])
	require.Equal(t, "local-provider", payload["provider"])
	require.Equal(t, "local-mock-model", payload["model"])
	require.Equal(t, true, payload["success"])
	require.EqualValues(t, 100, payload["usage_prompt_tokens"])
	require.EqualValues(t, 50, payload["usage_completion_tokens"])
	require.EqualValues(t, 150, payload["usage_total_tokens"])
	require.NotContains(t, payload, "error")
	require.NotEmpty(t, payload["llm_request_id"])
}

func TestLocalApprovalExplainHookFailureDegradesWithoutModelLine(t *testing.T) {
	t.Setenv(approvalexplain.ModeEnv, "on_demand")
	var calls int32
	server := newApprovalExplainOpenAIServer(t, &calls, nil, true)
	session, bus := newLocalApprovalExplainTestSession(t, server.URL, "approval-local-fail")

	var events []runtimeevents.Event
	unsubscribe := bus.SubscribeCancelable("llm.request.finished", func(event runtimeevents.Event) {
		events = append(events, event)
	})
	t.Cleanup(unsubscribe)

	hook := newLocalApprovalExplainHook(session)
	require.NotNil(t, hook)

	// 失败路径返回 error 且不产生模型结果。
	got, err := hook(context.Background(), "approval-local-fail")
	require.Error(t, err)
	require.Empty(t, got.Explanation)
	require.Empty(t, got.Model)

	// [6] 仍可显示：规则解释 + 一行可读原因，绝不出现模型行。
	approval := &runtimechat.ApprovalRequest{
		ID:       "approval-local-fail",
		ToolName: "execute_shell_command",
		ArgsJSON: json.RawMessage(`{"command":"rm -rf build/"}`),
	}
	lines := approvalExplainBlockLines(approval, hook)
	joined := strings.Join(lines, "\n")
	require.Contains(t, joined, "[解释] 动作：删除文件或目录（递归）")
	require.Contains(t, joined, "[解释] 来源：规则模板")
	require.Contains(t, joined, "[解释] 模型解释不可用：")
	require.NotContains(t, joined, "来源：模型")
	require.NotContains(t, joined, "模型补充")

	// 失败同样记账：success=false + error，且不带 usage。
	require.Len(t, events, 2)
	for _, event := range events {
		require.Equal(t, false, event.Payload["success"])
		require.NotEmpty(t, event.Payload["error"])
		require.NotContains(t, event.Payload, "usage_prompt_tokens")
		require.NotContains(t, event.Payload, "usage_total_tokens")
	}
}

func TestLocalApprovalExplainModeOffSkipsModelAndAccounting(t *testing.T) {
	var calls int32
	server := newApprovalExplainOpenAIServer(t, &calls, nil, false)
	session, bus := newLocalApprovalExplainTestSession(t, server.URL, "approval-local-off")
	t.Setenv(approvalexplain.ModeEnv, "OFF")

	hook := newLocalApprovalExplainHook(session)
	require.Nil(t, hook, "off 模式不得安装模型解释钩子")

	approval := &runtimechat.ApprovalRequest{
		ID:       "approval-local-off",
		ToolName: "execute_shell_command",
		ArgsJSON: json.RawMessage(`{"command":"rm -rf build/"}`),
	}
	joined := strings.Join(approvalExplainBlockLines(approval, hook), "\n")
	require.Contains(t, joined, "[解释] 来源：规则模板")
	require.Contains(t, joined, "[解释] 模型解释未启用或不可用（仅规则说明）")
	require.Zero(t, atomic.LoadInt32(&calls), "off 模式不得调用模型")
	require.Empty(t, bus.Recent(8), "off 模式不得产生记账事件")
}

func TestLocalApprovalExplainModeSemantics(t *testing.T) {
	cases := map[string]approvalexplain.Mode{
		"":             approvalexplain.ModeOnDemand,
		"on":           approvalexplain.ModeOnDemand,
		"on-demand":    approvalexplain.ModeOnDemand,
		"OFF":          approvalexplain.ModeOff,
		"disabled":     approvalexplain.ModeOff,
		"pre_generate": approvalexplain.ModeOnDemand, // 本地无预热通路，按 on_demand 处理
		"pre":          approvalexplain.ModeOnDemand,
		"bogus":        approvalexplain.ModeOnDemand,
	}
	for raw, want := range cases {
		t.Setenv(approvalexplain.ModeEnv, raw)
		require.Equalf(t, want, localApprovalExplainMode(), "raw=%q", raw)
	}
}

func TestLocalApprovalExplainHookRequiresProvider(t *testing.T) {
	t.Setenv(approvalexplain.ModeEnv, "on_demand")
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "session-no-provider"},
		LocalRuntimeHost: &localChatRuntimeHost{
			RuntimeStore: runtimechat.NewInMemoryRuntimeStore(4),
			EventBus:     runtimeevents.NewBusWithRetention(8),
		},
	}
	require.Nil(t, newLocalApprovalExplainHook(session), "无 provider 不得注入模型解释钩子")
	require.Nil(t, newChatRuntimeEventBridge(session).explainApproval)
}

// CLI 端到端：本地模式下 [6] 走真实 provider 路径，输出「来源：模型 …」；
// 同一投影重复按 [6] 只调一次模型；解释之后的 1/2 决策不受影响。
func TestChatRuntimeEvents_LocalApprovalExplainModelPathCallsOnceAndKeepsDecisionFlow(t *testing.T) {
	t.Setenv(approvalexplain.ModeEnv, "on_demand")
	var calls int32
	server := newApprovalExplainOpenAIServer(t, &calls, nil, false)
	session, _ := newLocalApprovalExplainTestSession(t, server.URL, "req-local-explain")
	session.InputReader = bufio.NewReader(strings.NewReader("6\n6\n1\n"))
	session.NoInteractive = true

	bridge := newChatRuntimeEventBridge(session)
	require.NotNil(t, bridge.explainApproval)

	approval := &runtimechat.ApprovalRequest{
		ID:       "req-local-explain",
		ToolName: "execute_shell_command",
		ArgsJSON: json.RawMessage(`{"command":"rm -rf build/"}`),
	}
	var answer chatApprovalAnswer
	var askErr error
	output := captureStdout(t, func() {
		answer, askErr = bridge.askApproval(approval, nil)
	})
	require.NoError(t, askErr)
	require.True(t, answer.Allowed, "解释路径不得阻塞或改变批准决策")
	require.EqualValues(t, 1, atomic.LoadInt32(&calls), "同一投影重复按 [6] 只调一次模型")
	require.Contains(t, output, "[解释] 来源：模型 local-mock-model")
	require.Contains(t, output, "[解释] 模型补充（local-mock-model）：会删除 build/ 下的构建产物，不涉及源码。")
}

// 本地模型解释失败时 [6] 只降级、不消费决定：随后的 [1] 仍照常批准。
func TestChatRuntimeEvents_LocalApprovalExplainFailureKeepsDecisionOpen(t *testing.T) {
	t.Setenv(approvalexplain.ModeEnv, "on_demand")
	var calls int32
	server := newApprovalExplainOpenAIServer(t, &calls, nil, true)
	session, _ := newLocalApprovalExplainTestSession(t, server.URL, "req-local-explain-fail")
	session.InputReader = bufio.NewReader(strings.NewReader("6\n1\n"))
	session.NoInteractive = true

	bridge := newChatRuntimeEventBridge(session)
	require.NotNil(t, bridge.explainApproval)

	approval := &runtimechat.ApprovalRequest{
		ID:       "req-local-explain-fail",
		ToolName: "execute_shell_command",
		ArgsJSON: json.RawMessage(`{"command":"rm -rf build/"}`),
	}
	var answer chatApprovalAnswer
	var askErr error
	output := captureStdout(t, func() {
		answer, askErr = bridge.askApproval(approval, nil)
	})
	require.NoError(t, askErr)
	require.True(t, answer.Allowed, "解释失败不得阻塞或代替批准/拒绝")
	require.EqualValues(t, 1, atomic.LoadInt32(&calls))
	require.Contains(t, output, "[解释] 来源：规则模板")
	require.Contains(t, output, "[解释] 模型解释不可用：")
	require.NotContains(t, output, "来源：模型")
}
