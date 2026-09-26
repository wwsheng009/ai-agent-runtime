package runtimeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
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
	require.Equal(t, string(ApprovalExplainModeOnDemand), rules.Mode)
	require.False(t, rules.Cached)
	require.Empty(t, rules.Model)
	require.Contains(t, rules.Explanation, "shell_exec")
	require.Contains(t, rules.Explanation, "rm -rf build/")
	require.Contains(t, rules.Explanation, "writes outside workspace")
	require.Contains(t, rules.Explanation, "cmd:rm:*")

	// 注入模型摘要：source=model 并带上生成模型。
	var published []map[string]interface{}
	handler.getRuntimeEventBus().Subscribe("llm.request.finished", func(event runtimeevents.Event) {
		published = append(published, event.Payload)
	})
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
	// 注入实现由宿主自担记账；只有内建 llmRuntime 路径才发 usage 事件。
	require.Empty(t, published)

	// 模型失败：不得把故障变成 5xx，仍回规则摘要。
	// 换一条新审批（不同 request_id）避免命中上一步缓存：失败路径本就不缓存。
	handler.approvalSummarizer = func(context.Context, string, *chat.ApprovalRequest) (string, string, error) {
		return "", "", fmt.Errorf("upstream 503")
	}
	seedPendingApproval(t, handler, &chat.ApprovalRequest{
		ID:       "approval-2",
		ToolName: "shell_exec",
	})
	rec = httptest.NewRecorder()
	handler.ExplainSessionApproval(rec, explainRequest("session-1", "approval-2"))
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

// §4.13 解释模式与缓存：off 不调用模型；重复点击只计费一次；失败不缓存；
// pre_generate 由读路径预热。

func TestParseApprovalExplainMode(t *testing.T) {
	for raw, want := range map[string]ApprovalExplainMode{
		"":             ApprovalExplainModeOnDemand,
		"on_demand":    ApprovalExplainModeOnDemand,
		"on-demand":    ApprovalExplainModeOnDemand,
		"OFF":          ApprovalExplainModeOff,
		"none":         ApprovalExplainModeOff,
		"pre_generate": ApprovalExplainModePreGenerate,
		"pre-generate": ApprovalExplainModePreGenerate,
		"  pre ":       ApprovalExplainModePreGenerate,
	} {
		got, ok := ParseApprovalExplainMode(raw)
		require.Truef(t, ok, "raw=%q", raw)
		require.Equalf(t, want, got, "raw=%q", raw)
	}
	_, ok := ParseApprovalExplainMode("sometimes")
	require.False(t, ok)

	// 非法值不改状态（默认仍是按需）。
	handler := NewHandler(nil, nil, nil)
	require.Equal(t, ApprovalExplainModeOnDemand, handler.ApprovalExplainMode())
	require.Error(t, handler.SetApprovalExplainMode("sometimes"))
	require.Equal(t, ApprovalExplainModeOnDemand, handler.ApprovalExplainMode())
	require.NoError(t, handler.SetApprovalExplainMode("off"))
	require.Equal(t, ApprovalExplainModeOff, handler.ApprovalExplainMode())
}

func TestApprovalExplainModeOffSkipsModel(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	require.NoError(t, handler.SetApprovalExplainMode("off"))
	seedPendingApproval(t, handler, &chat.ApprovalRequest{ID: "approval-off", ToolName: "shell_exec"})

	calls := 0
	handler.approvalSummarizer = func(context.Context, string, *chat.ApprovalRequest) (string, string, error) {
		calls++
		return "不该被调用", "mock-model", nil
	}
	rec := httptest.NewRecorder()
	handler.ExplainSessionApproval(rec, explainRequest("session-1", "approval-off"))
	payload := decodeExplanation(t, rec)
	require.Equal(t, approvalExplainSourceRules, payload.Source)
	require.Equal(t, string(ApprovalExplainModeOff), payload.Mode)
	require.Zero(t, calls, "off 模式不得调用模型")
}

func TestApprovalExplainCachesRepeatClicks(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	seedPendingApproval(t, handler, &chat.ApprovalRequest{ID: "approval-cache", ToolName: "shell_exec"})

	calls := 0
	handler.approvalSummarizer = func(context.Context, string, *chat.ApprovalRequest) (string, string, error) {
		calls++
		return "缓存命中的解释", "mock-model", nil
	}
	first := httptest.NewRecorder()
	handler.ExplainSessionApproval(first, explainRequest("session-1", "approval-cache"))
	firstPayload := decodeExplanation(t, first)
	require.Equal(t, approvalExplainSourceModel, firstPayload.Source)
	require.False(t, firstPayload.Cached)

	second := httptest.NewRecorder()
	handler.ExplainSessionApproval(second, explainRequest("session-1", "approval-cache"))
	secondPayload := decodeExplanation(t, second)
	require.Equal(t, approvalExplainSourceModel, secondPayload.Source)
	require.True(t, secondPayload.Cached, "重复点击必须命中缓存")
	require.Equal(t, "缓存命中的解释", secondPayload.Explanation)
	require.Equal(t, 1, calls, "同一审批只应计费一次")

	// 换一条审批（新 request_id）不命中旧缓存。
	seedPendingApproval(t, handler, &chat.ApprovalRequest{ID: "approval-next", ToolName: "shell_exec"})
	third := httptest.NewRecorder()
	handler.ExplainSessionApproval(third, explainRequest("session-1", "approval-next"))
	require.False(t, decodeExplanation(t, third).Cached)
	require.Equal(t, 2, calls)
}

func TestApprovalExplainFailureIsNotCached(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	seedPendingApproval(t, handler, &chat.ApprovalRequest{ID: "approval-flaky", ToolName: "shell_exec"})

	calls := 0
	handler.approvalSummarizer = func(context.Context, string, *chat.ApprovalRequest) (string, string, error) {
		calls++
		return "", "", fmt.Errorf("upstream 503")
	}
	rec := httptest.NewRecorder()
	handler.ExplainSessionApproval(rec, explainRequest("session-1", "approval-flaky"))
	require.Equal(t, approvalExplainSourceRules, decodeExplanation(t, rec).Source)

	// 第二次点击（模型恢复）应重新尝试，而不是把一次抖动钉死成永久降级。
	handler.approvalSummarizer = func(context.Context, string, *chat.ApprovalRequest) (string, string, error) {
		calls++
		return "恢复后的解释", "mock-model", nil
	}
	rec = httptest.NewRecorder()
	handler.ExplainSessionApproval(rec, explainRequest("session-1", "approval-flaky"))
	payload := decodeExplanation(t, rec)
	require.Equal(t, approvalExplainSourceModel, payload.Source)
	require.Equal(t, 2, calls)
}

func TestApprovalExplainOffIgnoresCachedModelResult(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	seedPendingApproval(t, handler, &chat.ApprovalRequest{ID: "approval-switch", ToolName: "shell_exec"})
	handler.approvalSummarizer = func(context.Context, string, *chat.ApprovalRequest) (string, string, error) {
		return "模型解释", "mock-model", nil
	}
	rec := httptest.NewRecorder()
	handler.ExplainSessionApproval(rec, explainRequest("session-1", "approval-switch"))
	require.Equal(t, approvalExplainSourceModel, decodeExplanation(t, rec).Source)

	require.NoError(t, handler.SetApprovalExplainMode("off"))
	rec = httptest.NewRecorder()
	handler.ExplainSessionApproval(rec, explainRequest("session-1", "approval-switch"))
	payload := decodeExplanation(t, rec)
	require.Equal(t, approvalExplainSourceRules, payload.Source)
	require.False(t, payload.Cached)
	require.Equal(t, string(ApprovalExplainModeOff), payload.Mode)
}

func TestApprovalExplainPreGenerateWarmsOnRuntimeStateRead(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	require.NoError(t, handler.SetApprovalExplainMode("pre_generate"))
	seedPendingApproval(t, handler, &chat.ApprovalRequest{
		ID:        "approval-warm",
		SessionID: "session-1",
		ToolName:  "shell_exec",
	})

	calls := int32(0)
	handler.approvalSummarizer = func(context.Context, string, *chat.ApprovalRequest) (string, string, error) {
		atomic.AddInt32(&calls, 1)
		return "预生成的解释", "mock-model", nil
	}

	state, err := handler.getSessionRuntimeStore().LoadState(context.Background(), "session-1")
	require.NoError(t, err)
	require.NotNil(t, state)
	handler.MaybeWarmApprovalExplanation(state)

	// 预热是后台动作：等缓存落地，再验证点击直接命中且只计费一次。
	cache := handler.approvalExplainCacheStore()
	require.Eventually(t, func() bool { return cache.has("session-1", "approval-warm") }, 5*time.Second, 10*time.Millisecond)

	rec := httptest.NewRecorder()
	handler.ExplainSessionApproval(rec, explainRequest("session-1", "approval-warm"))
	payload := decodeExplanation(t, rec)
	require.Equal(t, approvalExplainSourceModel, payload.Source)
	require.True(t, payload.Cached)
	require.Equal(t, string(ApprovalExplainModePreGenerate), payload.Mode)
	require.EqualValues(t, 1, atomic.LoadInt32(&calls))

	// 非 pre_generate 模式下预热是空操作。
	handler2 := NewHandler(nil, nil, nil)
	seedPendingApproval(t, handler2, &chat.ApprovalRequest{ID: "approval-idle", SessionID: "session-1", ToolName: "shell_exec"})
	handler2.approvalSummarizer = func(context.Context, string, *chat.ApprovalRequest) (string, string, error) {
		t.Error("on_demand 模式读 /runtime 不应触发预生成")
		return "", "", nil
	}
	state2, err := handler2.getSessionRuntimeStore().LoadState(context.Background(), "session-1")
	require.NoError(t, err)
	handler2.MaybeWarmApprovalExplanation(state2)
	require.False(t, handler2.approvalExplainCacheStore().has("session-1", "approval-idle"))
}

func TestExplainSessionApprovalSingleFlightDeduplicatesConcurrentClicks(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	seedPendingApproval(t, handler, &chat.ApprovalRequest{ID: "approval-sf", ToolName: "shell_exec"})

	release := make(chan struct{})
	var calls int32
	handler.approvalSummarizer = func(ctx context.Context, _ string, _ *chat.ApprovalRequest) (string, string, error) {
		atomic.AddInt32(&calls, 1)
		select {
		case <-release:
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
		return "单飞结果", "mock-model", nil
	}

	bodies := make([][]byte, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			handler.ExplainSessionApproval(rec, explainRequest("session-1", "approval-sf"))
			bodies[idx] = rec.Body.Bytes()
		}(i)
	}
	require.Eventually(t, func() bool { return atomic.LoadInt32(&calls) == 1 }, 5*time.Second, 10*time.Millisecond)
	close(release)
	wg.Wait()

	require.EqualValues(t, 1, atomic.LoadInt32(&calls), "并发点击只应触发一次模型调用")
	for i, body := range bodies {
		var payload sessionApprovalExplanationPayload
		require.NoErrorf(t, json.Unmarshal(body, &payload), "第 %d 个响应", i+1)
		require.Equal(t, approvalExplainSourceModel, payload.Source)
		require.Equal(t, "单飞结果", payload.Explanation)
	}
}
