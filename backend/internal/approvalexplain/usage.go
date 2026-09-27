package approvalexplain

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// UsagePayload 构造审批解释调用的 `llm.request.finished` 事件 payload。
//
// runtime-server（runtimeapi.publishApprovalExplainUsage）与 aicli 本地模式
// （commands.publishLocalApprovalExplainUsage）共用这一构造器，保证两端的用量
// 账本同形同义：origin/source=approval_explain、provider/model、成功时带
// usage_*、失败时带 error。事件总线（挂 usageledger/usageanalytics）由调用方
// 选择。
func UsagePayload(requestID, provider, model string, resp *llm.LLMResponse, callErr error) map[string]interface{} {
	payload := map[string]interface{}{
		"llm_request_id": requestID,
		"success":        callErr == nil && resp != nil,
		"source":         "approval_explain",
		"origin":         "approval_explain",
		"provider":       provider,
		"model":          model,
	}
	if callErr != nil {
		payload["error"] = callErr.Error()
	}
	if resp != nil && resp.Usage != nil {
		payload["usage_prompt_tokens"] = resp.Usage.PromptTokens
		payload["usage_completion_tokens"] = resp.Usage.CompletionTokens
		payload["usage_total_tokens"] = resp.Usage.TotalTokens
	}
	return payload
}
