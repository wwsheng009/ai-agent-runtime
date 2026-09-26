package runtimeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// §4.13：解释端点只读、可降级、不误伤别的审批。

func explainRequest(sessionID, requestID string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+sessionID+"/runtime/approvals/"+requestID+"/explain", nil)
	return mux.SetURLVars(req, map[string]string{"id": sessionID, "request_id": requestID})
}

func seedPendingApproval(t *testing.T, handler *Handler, approval *chat.ApprovalRequest) {
	t.Helper()
	store := handler.getSessionRuntimeStore()
	require.NotNil(t, store)
	require.NoError(t, store.SaveState(context.Background(), &chat.RuntimeState{
		SessionID:       "session-1",
		PendingApproval: approval,
	}))
}

func decodeExplanation(t *testing.T, rec *httptest.ResponseRecorder) sessionApprovalExplanationPayload {
	t.Helper()
	var payload sessionApprovalExplanationPayload
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	return payload
}

func TestExplainSessionApproval_ModelPathAndRulesFallback(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	seedPendingApproval(t, handler, &chat.ApprovalRequest{
		ID:              "approval-1",
		ToolName:        "shell_exec",
		ArgsJSON:        json.RawMessage(`{"command":"rm -rf build/","file_path":"build"}`),
		Reason:          "writes outside workspace",
		RiskLevel:       "high",
		RememberPattern: "cmd:rm:*",
	})

	// 无 llmRuntime、无注入实现：降级为规则摘要，且 200（UI 永远拿得到可核对的事实）。
	rec := httptest.NewRecorder()
	handler.ExplainSessionApproval(rec, explainRequest("session-1", "approval-1"))
	require.Equal(t, http.StatusOK, rec.Code)
	rules := decodeExplanation(t, rec)
	require.Equal(t, approvalExplainSourceRules, rules.Source)
	require.Empty(t, rules.Model)
	require.Contains(t, rules.Explanation, "shell_exec")
	require.Contains(t, rules.Explanation, "rm -rf build/")
	require.Contains(t, rules.Explanation, "writes outside workspace")
	require.Contains(t, rules.Explanation, "cmd:rm:*")

	// 注入模型摘要：source=model 并带上生成模型。
	handler.approvalSummarizer = func(context.Context, string, *chat.ApprovalRequest) (string, string, error) {
		return "会删除 build/ 目录下的构建产物，不涉及源码。", "mock-model", nil
	}
	rec = httptest.NewRecorder()
	handler.ExplainSessionApproval(rec, explainRequest("session-1", "approval-1"))
	require.Equal(t, http.StatusOK, rec.Code)
	model := decodeExplanation(t, rec)
	require.Equal(t, approvalExplainSourceModel, model.Source)
	require.Equal(t, "mock-model", model.Model)
	require.Contains(t, model.Explanation, "构建产物")

	// 模型失败：不得把故障变成 5xx，仍回规则摘要。
	handler.approvalSummarizer = func(context.Context, string, *chat.ApprovalRequest) (string, string, error) {
		return "", "", fmt.Errorf("upstream 503")
	}
	rec = httptest.NewRecorder()
	handler.ExplainSessionApproval(rec, explainRequest("session-1", "approval-1"))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, approvalExplainSourceRules, decodeExplanation(t, rec).Source)
}

func TestExplainSessionApproval_RejectsNonPendingRequest(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	seedPendingApproval(t, handler, &chat.ApprovalRequest{
		ID:       "approval-live",
		ToolName: "shell_exec",
	})

	// 已裁决 / 过期 / 不存在：409，且不泄漏当前 pending 的审批内容。
	rec := httptest.NewRecorder()
	handler.ExplainSessionApproval(rec, explainRequest("session-1", "approval-gone"))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.NotContains(t, rec.Body.String(), "shell_exec")

	// 会话 ID 缺失：400（不触碰 store）。
	rec = httptest.NewRecorder()
	handler.ExplainSessionApproval(rec, explainRequest("", "approval-live"))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRuleBasedApprovalExplanation_NoRememberableGrant(t *testing.T) {
	explanation := ruleBasedApprovalExplanation(&chat.ApprovalRequest{
		ID:       "approval-2",
		ToolName: "shell_exec",
		ArgsJSON: json.RawMessage(`{"command":"sudo rm -rf /"}`),
		Reason:   "dangerous command",
	})

	require.Contains(t, explanation, "不可记忆")
	require.NotContains(t, explanation, "记住")
}

func TestApprovalArgumentDigest_TruncatesAndFallsBack(t *testing.T) {
	digest := approvalArgumentDigest(json.RawMessage(`{"command":"echo hi","other":"x"}`))
	require.Contains(t, digest, "- command: echo hi")

	// 未知键：回退原始 JSON，而不是给出空摘要。
	digest = approvalArgumentDigest(json.RawMessage(`{"unknown_key":"value"}`))
	require.Contains(t, digest, "unknown_key")

	// 空参数不产生摘要行。
	require.Equal(t, "", approvalArgumentDigest(json.RawMessage(`{}`)))
}
