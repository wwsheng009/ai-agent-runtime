package runtimeapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

// 审批解释模式的运行时设置面（Web 设置页）：
//
//	GET /api/runtime/config/approval-explain
//	PUT /api/runtime/config/approval-explain  {"mode":"off|on_demand|pre_generate"}
//
// 这是**进程级行为开关**（决定「解释」按钮是否/何时真的调用模型），改动对
// 后续解释立即生效。与 agent max steps 不同，它不落 runtime.yaml：解释是
// 交互期的临时选择，重启后回到 AICLI_APPROVAL_EXPLAIN_MODE 或默认 on_demand，
// 避免把「这次调试时关掉的模型调用」变成团队共享配置。
type approvalExplainSettingsResponse struct {
	Mode      string   `json:"mode"`
	Supported []string `json:"supported_modes"`
	Updated   bool     `json:"updated,omitempty"`
}

// supportedApprovalExplainModes 与 ApprovalExplainMode 的规范取值保持同源，
// 前端只做渲染，不写死枚举。
func supportedApprovalExplainModes() []string {
	return []string{
		string(ApprovalExplainModeOff),
		string(ApprovalExplainModeOnDemand),
		string(ApprovalExplainModePreGenerate),
	}
}

// GetApprovalExplainSettings 返回当前解释模式与后端支持的模式清单。
func (h *Handler) GetApprovalExplainSettings(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		return
	}
	h.writeJSON(w, http.StatusOK, approvalExplainSettingsResponse{
		Mode:      string(h.ApprovalExplainMode()),
		Supported: supportedApprovalExplainModes(),
	})
}

// UpdateApprovalExplainSettings 切换解释模式。
//
// 非法取值 → 400 且**不改变**当前模式（与 SetApprovalExplainMode 的语义一致，
// 绝不把错配置静默当成默认值）。接受 ParseApprovalExplainMode 的别名形态
// （on-demand/ondemand/on、none/disabled…），响应回显规范取值。
func (h *Handler) UpdateApprovalExplainSettings(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}
	if _, ok := ParseApprovalExplainMode(req.Mode); !ok {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"unsupported approval explain mode "+strings.TrimSpace(req.Mode)+
				" (off|on_demand|pre_generate)"))
		return
	}
	if err := h.SetApprovalExplainMode(req.Mode); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, approvalExplainSettingsResponse{
		Mode:      string(h.ApprovalExplainMode()),
		Supported: supportedApprovalExplainModes(),
		Updated:   true,
	})
}
