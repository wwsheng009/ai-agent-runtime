package runtimeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// §4.13 按需解释：对一条 pending 审批做一次**只读**摘要。
//
// 设计约束（与设计文档 §4.13 对齐）：
//   - 纯 UI：解释文本不进入工具调用决策链，端点不写任何状态、不解析决定；
//   - 可配：off / on_demand（默认）/ pre_generate 三种模式，见
//     session_approval_explain_mode.go；重复点击命中缓存，同一审批只计费一次；
//   - 降级：模型不可用 / 未配置 / 超时 / 报错，一律回退规则摘要（200 + rules），
//     让 UI 永远拿得到「这条命令在做什么」的结构化答案；
//   - 预算：一次调用有独立超时与 max_tokens 上限，参数与补丁按需截断。

const (
	approvalExplainSourceModel = "model"
	approvalExplainSourceRules = "rules"

	// 解释是一次小摘要：给足要点即可，避免把费用放大到与真实回合同量级。
	approvalExplainMaxTokens = 320
	approvalExplainTimeout   = 20 * time.Second
	// 参数（命令 / 补丁）截断上限：解释只需要「大致做什么」，不需要全文。
	approvalExplainArgLimit  = 4000
	approvalExplainLineLimit = 6
)

// sessionApprovalExplanationPayload 是端点的响应体。`source` 直接暴露给 UI，
// 便于向用户交代「这段解释来自模型还是规则模板」。
type sessionApprovalExplanationPayload struct {
	Explanation string `json:"explanation"`
	Source      string `json:"source"`
	Model       string `json:"model,omitempty"`
	// Mode 是生成这条解释时的模式（off 时不会出现 model 来源）。
	Mode string `json:"mode,omitempty"`
	// Cached 表示直接命中了缓存（未重复调用模型）。
	Cached bool `json:"cached,omitempty"`
}

// ApprovalSummarizer 允许宿主自定义「审批解释」的模型调用（可选）。
// 为 nil 时使用 Handler 持有的 llmRuntime；宿主也可在测试中注入确定性实现。
type ApprovalSummarizer func(
	ctx context.Context,
	sessionID string,
	pending *chat.ApprovalRequest,
) (explanation string, model string, err error)

// ExplainSessionApproval 处理
// POST /api/runtime/sessions/{id}/runtime/approvals/{request_id}/explain。
func (h *Handler) ExplainSessionApproval(w http.ResponseWriter, r *http.Request) {
	store := h.getSessionRuntimeStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session runtime store not configured"))
		return
	}

	sessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	requestID := strings.TrimSpace(mux.Vars(r)["request_id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}
	if requestID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "request_id is required"))
		return
	}

	queryCtx, queryCancel := sessionStoreQueryContext(r)
	defer queryCancel()
	state, err := store.LoadState(queryCtx, sessionID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	pending := pendingApprovalForExplain(state, requestID)
	if pending == nil {
		// 审批已被裁决 / 过期 / 从未存在：解释没有对象，且**不能**误伤别的审批。
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "approval is no longer pending"))
		return
	}

	h.writeJSON(w, http.StatusOK, h.approvalExplanationForRequest(r.Context(), sessionID, pending))
}

// pendingApprovalForExplain 只认「当前 pending 且 request_id 匹配」的那一条：
// 用户点解释与看到审批之间存在竞态（别人先批准/超时），不匹配就当作已消失。
func pendingApprovalForExplain(state *chat.RuntimeState, requestID string) *chat.ApprovalRequest {
	if state == nil || state.PendingApproval == nil {
		return nil
	}
	if strings.TrimSpace(state.PendingApproval.ID) != strings.TrimSpace(requestID) {
		return nil
	}
	return state.PendingApproval
}

// summarizeApproval 走会话所在运行时的一次性模型调用；任何一步不可用都返回错误，
// 由调用方降级到规则摘要（端点对模型故障永不返回 5xx）。
func (h *Handler) summarizeApproval(ctx context.Context, sessionID string, pending *chat.ApprovalRequest) (string, string, error) {
	if h == nil || pending == nil {
		return "", "", fmt.Errorf("approval explanation unavailable")
	}
	if h.approvalSummarizer != nil {
		return h.approvalSummarizer(ctx, sessionID, pending)
	}
	if h.llmRuntime == nil {
		return "", "", fmt.Errorf("llm runtime not configured")
	}
	provider := strings.TrimSpace(h.llmRuntime.DefaultProvider())
	model := strings.TrimSpace(h.llmRuntime.DefaultModel())
	if model == "" {
		return "", "", fmt.Errorf("llm runtime has no default model")
	}

	callCtx, cancel := context.WithTimeout(ctx, approvalExplainTimeout)
	defer cancel()
	requestID := fmt.Sprintf("approval-explain-%s-%d", sessionID, time.Now().UnixNano())
	resp, err := h.llmRuntime.Call(callCtx, &llm.LLMRequest{
		Provider:  provider,
		Model:     model,
		MaxTokens: approvalExplainMaxTokens,
		Messages: []runtimetypes.Message{
			{Role: "system", Content: approvalExplainSystemPrompt},
			{Role: "user", Content: approvalExplainUserPrompt(pending)},
		},
	})
	// 记账：解释也是一次真实计费调用，必须落 usage 账本（usageledger 订阅
	// `llm.request.finished`）。失败路径同样上报（success=false，无 usage），
	// 便于把「解释被点了很多次却总失败」这类现象看在眼里。
	h.publishApprovalExplainUsage(sessionID, requestID, provider, model, resp, err)
	if err != nil {
		return "", "", err
	}
	if resp == nil {
		return "", "", fmt.Errorf("empty llm response")
	}
	return strings.TrimSpace(resp.Content), model, nil
}

// publishApprovalExplainUsage 发布一次 `llm.request.finished`，让 usage 账本
// 与应用分析看到按需解释的开销。事件带 origin 标记，便于与回合内调用区分。
func (h *Handler) publishApprovalExplainUsage(
	sessionID string,
	requestID string,
	provider string,
	model string,
	resp *llm.LLMResponse,
	callErr error,
) {
	if h == nil {
		return
	}
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
	h.publishSessionRuntimeEvent("llm.request.finished", "", sessionID, payload)
}

const approvalExplainSystemPrompt = "你是审批解释器：用户即将批准或拒绝一次工具调用，需要快速判断它在做什么。\n" +
	"用不超过 3 条要点说明：1) 这条命令/补丁具体会做什么；2) 会触碰哪些路径、网络或副作用；3) 需要留意的风险点。\n" +
	"不要建议批准或拒绝，不要复述参数全文，不要输出 JSON 或 Markdown 标题。\n" +
	"用与「触发原因」相同的语言作答；判断不了时用简体中文。"

func approvalExplainUserPrompt(pending *chat.ApprovalRequest) string {
	var b strings.Builder
	b.WriteString("工具：" + approvalToolLabel(pending) + "\n")
	if reason := strings.TrimSpace(pending.Reason); reason != "" {
		b.WriteString("触发原因：" + reason + "\n")
	}
	if risk := strings.TrimSpace(pending.RiskLevel); risk != "" {
		b.WriteString("风险级别：" + risk + "\n")
	}
	if pattern := strings.TrimSpace(pending.RememberPattern); pattern != "" {
		b.WriteString("可记忆为：" + pattern + "\n")
	}
	if digest := approvalArgumentDigest(pending.ArgsJSON); digest != "" {
		b.WriteString("参数摘要：\n" + digest)
	}
	return strings.TrimSpace(b.String())
}

// ruleBasedApprovalExplanation 是模型不可用时的降级：不猜语义，只把引擎已有的
// 事实（工具 / 原因 / 风险 / 参数 / 可否记忆）整理成可核对的一段话。
func ruleBasedApprovalExplanation(pending *chat.ApprovalRequest) string {
	if pending == nil {
		return ""
	}
	lines := []string{"工具：" + approvalToolLabel(pending)}
	if reason := strings.TrimSpace(pending.Reason); reason != "" {
		lines = append(lines, "触发原因："+reason)
	}
	if risk := strings.TrimSpace(pending.RiskLevel); risk != "" {
		lines = append(lines, "风险级别："+risk)
	}
	if digest := approvalArgumentDigest(pending.ArgsJSON); digest != "" {
		lines = append(lines, "参数摘要：", digest)
	}
	if pattern := strings.TrimSpace(pending.RememberPattern); pattern != "" {
		lines = append(lines, "可记忆为："+pattern+"（批准并勾选「记住」后生效）")
	} else {
		lines = append(lines, "不可记忆：该审批每次都需要人工确认")
	}
	return strings.Join(lines, "\n")
}

func approvalToolLabel(pending *chat.ApprovalRequest) string {
	if pending == nil {
		return "unknown"
	}
	if tool := strings.TrimSpace(pending.ToolName); tool != "" {
		return tool
	}
	return "unknown"
}

// approvalArgumentDigest 优先抽取「一眼能看出在做什么」的字段，其次回退原始 JSON。
// 所有输出都截断，避免把整份补丁塞进解释或 prompt。
func approvalArgumentDigest(argsJSON json.RawMessage) string {
	raw := strings.TrimSpace(string(argsJSON))
	if raw == "" || raw == "null" || raw == "{}" {
		return ""
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(argsJSON, &decoded); err != nil {
		return truncateExplainText(raw, approvalExplainArgLimit)
	}
	priority := []string{
		"command", "command_line", "cmd", "script",
		"file_path", "path", "paths", "target_file",
		"url", "query", "pattern", "glob",
		"patch", "diff", "content", "prompt",
	}
	lines := make([]string, 0, approvalExplainLineLimit)
	seen := map[string]bool{}
	for _, key := range priority {
		value, ok := decoded[key]
		if !ok || value == nil {
			continue
		}
		text := strings.TrimSpace(approvalArgumentValue(value))
		if text == "" {
			continue
		}
		lines = append(lines, "- "+key+": "+truncateExplainText(text, approvalExplainArgLimit))
		seen[key] = true
		if len(lines) >= approvalExplainLineLimit {
			break
		}
	}
	if len(lines) == 0 {
		return truncateExplainText(raw, approvalExplainArgLimit)
	}
	return strings.Join(lines, "\n")
}

func approvalArgumentValue(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []interface{}:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(approvalArgumentValue(item)); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ", ")
	default:
		if encoded, err := json.Marshal(typed); err == nil {
			return string(encoded)
		}
		return fmt.Sprintf("%v", typed)
	}
}

func truncateExplainText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	// 按 rune 截断，避免把多字节字符切坏。
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
