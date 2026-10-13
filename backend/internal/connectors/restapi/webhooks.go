package restapi

import (
	"errors"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// webhook 是公网入口 POST /hooks/connectors/{id}：
// 配置校验 → 能力探测 → Provider 解析/签名校验 → 入站投递。
//
// 安全与语义：
//   - 未知/停用连接器统一 404（不暴露启停状态）；
//   - 签名/挑战失败由 Provider 返回 error → 400；
//   - 队列满 → 429（调用方应重试）；重复投递由 Service 去重账本幂等忽略。
func (h *handlers) webhook(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	id := mux.Vars(r)["id"]
	cfg, ok := h.deps.Store.GetConfig(id)
	if !ok || !cfg.Enabled {
		writeError(w, http.StatusNotFound, "未知连接器")
		return
	}
	provider, ok := h.deps.Service.Provider(cfg.Provider)
	if !ok {
		writeError(w, http.StatusNotFound, "通道未注册："+cfg.Provider)
		return
	}
	hook, ok := provider.(connectors.WebhookProvider)
	if !ok {
		writeError(w, http.StatusMethodNotAllowed, "该通道不支持 webhook 接入（请使用轮询模式）")
		return
	}
	result, err := hook.HandleWebhook(r.Context(), cfg, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if result.Response != nil {
		status := result.Status
		if status == 0 {
			status = http.StatusOK
		}
		writeJSON(w, status, result.Response)
		return
	}
	if result.Message == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	msg := *result.Message
	if msg.ConnectorID == "" {
		msg.ConnectorID = cfg.ID
	}
	if msg.Provider == "" {
		msg.Provider = cfg.Provider
	}
	if err := h.deps.Service.HandleInbound(r.Context(), msg); err != nil {
		if errors.Is(err, connectors.ErrQueueFull) {
			writeError(w, http.StatusTooManyRequests, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
