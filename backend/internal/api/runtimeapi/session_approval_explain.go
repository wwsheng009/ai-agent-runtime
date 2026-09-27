package runtimeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/approvalexplain"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
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

	// 预算与截断阈值来自共享包 internal/approvalexplain：本地模式与
	// runtime-server 必须使用同一组数字，避免两端费用/输出形态漂移。
	approvalExplainMaxTokens = approvalexplain.MaxTokens
	approvalExplainTimeout   = approvalexplain.Timeout
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
	// 解释跟随会话路由：会话已解析出 provider+model 时优先用它（与用户正在跑的
	// 是同一个模型），否则退回运行时默认。只认「两者齐全」的会话路由——只有
	// 模型名时不做猜测，避免把请求路由到不声明该模型的 provider。
	provider, model := h.sessionRouteForApprovalExplain(ctx, sessionID)
	if provider == "" || model == "" {
		provider = strings.TrimSpace(h.llmRuntime.DefaultProvider())
		model = strings.TrimSpace(h.llmRuntime.DefaultModel())
	}
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
			{Role: "system", Content: approvalexplain.SystemPrompt},
			{Role: "user", Content: approvalexplain.UserPrompt(pending)},
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

// sessionRouteForApprovalExplain 读取会话已解析的路由（sessionmeta 的
// effective/requested provider+model）。会话不存在 / 存储超时 / 只有半边信息
// 时返回空值，由调用方退回运行时默认。读取失败不影响解释：与
// /runtime 路由透传同一容忍策略，绝不让存储抖动变成解释失败。
func (h *Handler) sessionRouteForApprovalExplain(ctx context.Context, sessionID string) (string, string) {
	if h == nil || h.sessionManager == nil {
		return "", ""
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", ""
	}
	if ctx == nil {
		ctx = context.Background()
	}
	queryCtx, cancel := context.WithTimeout(ctx, sessionStoreQueryTimeout)
	defer cancel()
	session, err := h.sessionManager.Get(queryCtx, sessionID)
	if err != nil || session == nil {
		return "", ""
	}
	context := session.Metadata.Context
	provider := firstNonEmptyString(
		sessionmeta.String(context, sessionmeta.EffectiveProvider),
		sessionmeta.String(context, sessionmeta.ProviderName),
	)
	model := firstNonEmptyString(
		sessionmeta.String(context, sessionmeta.EffectiveModel),
		sessionmeta.String(context, sessionmeta.Model),
	)
	if provider == "" || model == "" {
		return "", ""
	}
	return provider, model
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
	// payload 由共享包构造，与 aicli 本地模式逐键一致（同形同义记账）。
	payload := approvalexplain.UsagePayload(requestID, provider, model, resp, callErr)
	h.publishSessionRuntimeEvent("llm.request.finished", "", sessionID, payload)
}

// 以下薄包装保持 runtimeapi 内部调用点（与既有测试）不变，实际实现全部委托
// internal/approvalexplain；服务端与本地模式只有一份 prompt/摘要/降级逻辑。
func ruleBasedApprovalExplanation(pending *chat.ApprovalRequest) string {
	return approvalexplain.RuleBased(pending)
}

func approvalArgumentDigest(argsJSON json.RawMessage) string {
	return approvalexplain.ArgumentDigest(argsJSON)
}
