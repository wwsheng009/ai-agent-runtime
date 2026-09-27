package runtimeapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/approvalexplain"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// §4.13 共享单一真源：runtime-server 的规则降级 / 参数摘要 / 模式解析 / 预算
// 必须与 internal/approvalexplain 逐字节一致（本地模式使用同一实现）。

func TestApprovalExplainDelegatesToSharedPackage(t *testing.T) {
	pending := &chat.ApprovalRequest{
		ToolName:        "shell_exec",
		ArgsJSON:        json.RawMessage(`{"command":"rm -rf build/","paths":["a","b"]}`),
		Reason:          "writes outside workspace",
		RiskLevel:       "high",
		RememberPattern: "cmd:rm:*",
	}
	require.Equal(t, approvalexplain.RuleBased(pending), ruleBasedApprovalExplanation(pending))

	args := json.RawMessage(`{"unknown":"x"}`)
	require.Equal(t, approvalexplain.ArgumentDigest(args), approvalArgumentDigest(args))

	for _, raw := range []string{"", "on_demand", "pre-generate", "OFF", "disabled", "bogus"} {
		want, wantOK := approvalexplain.ParseMode(raw)
		got, gotOK := ParseApprovalExplainMode(raw)
		require.Equalf(t, want, got, "raw=%q", raw)
		require.Equalf(t, wantOK, gotOK, "raw=%q", raw)
	}

	require.Equal(t, approvalexplain.MaxTokens, approvalExplainMaxTokens)
	require.Equal(t, approvalexplain.Timeout, approvalExplainTimeout)
}

// 端点返回的 rules 文本与共享包输出逐字节相同（`[6]` 降级路径的守门用例）。
func TestExplainSessionApprovalRulesTextMatchesSharedSource(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	pending := &chat.ApprovalRequest{
		ID:              "approval-shared",
		ToolName:        "shell_exec",
		ArgsJSON:        json.RawMessage(`{"command":"rm -rf build/"}`),
		Reason:          "writes outside workspace",
		RiskLevel:       "high",
		RememberPattern: "cmd:rm:*",
	}
	seedPendingApproval(t, handler, pending)

	rec := httptest.NewRecorder()
	handler.ExplainSessionApproval(rec, explainRequest("session-1", "approval-shared"))
	require.Equal(t, http.StatusOK, rec.Code)
	payload := decodeExplanation(t, rec)
	require.Equal(t, approvalExplainSourceRules, payload.Source)
	require.Equal(t, approvalexplain.RuleBased(pending), payload.Explanation)
}

// 服务端发布的事件 payload 与共享 UsagePayload 逐键一致（同形同义记账）。
func TestApprovalExplainUsagePayloadMatchesSharedSource(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	var published []map[string]interface{}
	handler.getRuntimeEventBus().Subscribe("llm.request.finished", func(event runtimeevents.Event) {
		published = append(published, event.Payload)
	})

	resp := &llm.LLMResponse{Usage: &runtimetypes.TokenUsage{
		PromptTokens:     7,
		CompletionTokens: 3,
		TotalTokens:      10,
	}}
	handler.publishApprovalExplainUsage("session-1", "req-1", "provider-a", "model-b", resp, nil)
	require.Len(t, published, 1)
	want := approvalexplain.UsagePayload("req-1", "provider-a", "model-b", resp, nil)
	require.Equal(t, want, published[0])

	failure := &llm.LLMResponse{}
	callErr := errors.New("upstream 503")
	handler.publishApprovalExplainUsage("session-1", "req-2", "provider-a", "model-b", failure, callErr)
	require.Len(t, published, 2)
	wantFailed := approvalexplain.UsagePayload("req-2", "provider-a", "model-b", failure, callErr)
	require.Equal(t, wantFailed, published[1])
	require.Equal(t, false, published[1]["success"])
}
