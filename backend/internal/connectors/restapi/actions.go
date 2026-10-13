package restapi

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// action 是管理面 POST /connectors/{id}/actions/{action}：
// 配置校验 → 能力探测 → Provider 执行动作 → 返回 JSON 载荷（如微信扫码二维码/登录状态）。
//
// 语义（与 webhook 入口对齐）：
//   - 未知/停用连接器统一 404（不暴露启停状态）；通道未注册同样 404；
//   - provider 未实现 connectors.ActionProvider → 405；
//   - body 可选 JSON object（空体=无参数，非法 JSON → 400，与 decodeBody 一致）；
//   - Provider 返回 error → 400；成功 → 200 + Provider 载荷。
func (h *handlers) action(w http.ResponseWriter, r *http.Request) {
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
	runner, ok := provider.(connectors.ActionProvider)
	if !ok {
		writeError(w, http.StatusMethodNotAllowed, "该通道不支持动作执行（未实现 ActionProvider）")
		return
	}
	var payload map[string]any
	if !decodeBody(h.deps, w, r, &payload) {
		return
	}
	result, err := runner.RunAction(r.Context(), cfg, mux.Vars(r)["action"], payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if result == nil {
		result = map[string]any{}
	}
	writeJSON(w, http.StatusOK, result)
}
