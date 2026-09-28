package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
)

// GetKnowledgeStatus 处理 GET /api/runtime/knowledge/status（06 §4 Phase 1 交付 5）。
//
// 契约：
//   - 未接线（mode=off / 启动期降级 / 未注入句柄）返回 200 + mode=off 的最小载荷：
//     状态面的语义是"知识层现在是什么状态"，off 是合法状态——404/503 只会迫使
//     调用方（CLI 状态栏、前端设置页）去猜。
//   - 只读：不触发索引、不写库；owner 与 reader 角色行为一致（reader 也能看到
//     owner 的索引成果，并带 degraded_reason 标注只读与锁持有者 pid）。
//   - 内部错误返回 500，错误体与 /fs、/git 同形：{error:{code,message}}。
func (h *Handler) GetKnowledgeStatus(w http.ResponseWriter, r *http.Request) {
	report, err := h.knowledgeStatusReport(r.Context())
	if err != nil {
		knowledgeWriteError(w, http.StatusInternalServerError, err)
		return
	}
	knowledgeWriteJSON(w, http.StatusOK, report)
}

// knowledgeStatusReport 组装状态载荷；nil Handler / nil Activation 都返回 off 载荷
// （Activation.Status 本身 nil-safe，这里只兜底 h 为 nil 的防御性路径）。
func (h *Handler) knowledgeStatusReport(ctx context.Context) (knowledge.StatusReport, error) {
	if h == nil {
		var activation *knowledge.Activation
		return activation.Status(ctx)
	}
	return h.knowledgeActivation.Status(ctx)
}

// knowledgeWriteJSON 输出 JSON 响应。
func knowledgeWriteJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// knowledgeWriteError 输出统一错误体。
func knowledgeWriteError(w http.ResponseWriter, status int, err error) {
	message := "unknown error"
	if err != nil {
		message = err.Error()
	}
	knowledgeWriteJSON(w, status, map[string]interface{}{
		"error": map[string]interface{}{
			"code":    "knowledge_status_failed",
			"message": message,
		},
	})
}
