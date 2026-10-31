package restapi

// handoff.go — 节点侧 bot 配置移交（QQ/微信）。
//
// 正常的 GET /connectors/{id} 只返回 secret_keys（密钥只写不读）。网关要
// 运行第二个 outbound-only 实例时必须拿到 provider/settings/secrets；这条
// 显式端点由网关经 pinned 隧道调用，带 X-AICLI-Handoff 头，且只在 owner
// 管理面鉴权链内可达。响应绝不写日志。
//
// 生命周期：
//  1. POST /connectors/{id}/handoff           返回配置+密钥+微信 context_tokens；
//  2. 网关创建 outbound 副本并验证；
//  3. POST /connectors/{id}/handoff/confirm   节点关闭本地通知（保留入站/回复）；
//  4. 失败时 POST /connectors/{id}/handoff/rollback 恢复本地通知。
//
// 节点继续运行原连接器负责 agent 入站/命令/回复；网关副本只负责通知出站。

import (
	"net/http"
	"strings"

	"github.com/gorilla/mux"
)

const (
	handoffHeader = "X-AICLI-Handoff"
	// wechatContextTokensKey 与 providers/wechat 的持久化键一致：JSON
	// map[chat]context_token。微信出站必须带这个词，否则 Send 直接失败。
	wechatContextTokensKey = "context_tokens"
)

type handoffBindingView struct {
	Chat      string `json:"chat"`
	SessionID string `json:"session_id,omitempty"`
	NodeID    string `json:"node_id,omitempty"`
}

type handoffResponse struct {
	ID       string               `json:"id"`
	Provider string               `json:"provider"`
	Name     string               `json:"name,omitempty"`
	Mode     string               `json:"mode,omitempty"`
	Settings map[string]string    `json:"settings,omitempty"`
	Secrets  map[string]string    `json:"secrets,omitempty"`
	State    map[string]string    `json:"state,omitempty"`
	Bindings []handoffBindingView `json:"bindings,omitempty"`
}

func (h *handlers) requireHandoff(w http.ResponseWriter, r *http.Request) bool {
	if r == nil || !strings.EqualFold(strings.TrimSpace(r.Header.Get(handoffHeader)), "1") {
		writeError(w, http.StatusForbidden, "handoff 端点仅接受网关内部移交")
		return false
	}
	return true
}

func (h *handlers) handoff(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	if !h.requireHandoff(w, r) {
		return
	}
	id := strings.TrimSpace(mux.Vars(r)["id"])
	cfg, ok := h.deps.Store.GetConfig(id)
	if !ok {
		writeError(w, http.StatusNotFound, "未知连接器")
		return
	}
	if !cfg.Enabled {
		writeError(w, http.StatusConflict, "连接器已停用，不能移交")
		return
	}
	resp := handoffResponse{
		ID:       cfg.ID,
		Provider: cfg.Provider,
		Name:     cfg.Name,
		Mode:     cfg.Mode,
		Settings: cloneStringMap(cfg.Settings),
		Secrets:  cloneStringMap(cfg.Secrets),
	}
	if strings.EqualFold(cfg.Provider, "wechat") {
		if raw, ok := h.deps.Store.PollingState(cfg.ID).Get(wechatContextTokensKey); ok && strings.TrimSpace(raw) != "" {
			resp.State = map[string]string{wechatContextTokensKey: raw}
		}
	}
	for _, binding := range h.deps.Store.ListBindings(cfg.ID) {
		item := handoffBindingView{Chat: binding.Chat, SessionID: binding.Session.SessionID, NodeID: binding.Session.NodeID}
		if item.Chat == "" {
			continue
		}
		resp.Bindings = append(resp.Bindings, item)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handlers) handoffConfirm(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	if !h.requireHandoff(w, r) {
		return
	}
	h.setLocalNotifySuppressed(w, mux.Vars(r)["id"], true)
}

func (h *handlers) handoffRollback(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	if !h.requireHandoff(w, r) {
		return
	}
	h.setLocalNotifySuppressed(w, mux.Vars(r)["id"], false)
}

func (h *handlers) setLocalNotifySuppressed(w http.ResponseWriter, id string, suppressed bool) {
	id = strings.TrimSpace(id)
	cfg, ok := h.deps.Store.GetConfig(id)
	if !ok {
		writeError(w, http.StatusNotFound, "未知连接器")
		return
	}
	cfg.Policy.Notify.SuppressLocal = suppressed
	if err := h.deps.Store.PutConfig(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "保存失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "suppress_local": suppressed})
}

// contextTokensState 供网关周期性同步微信 context_token（节点收到新入站后
// token 会更新）。只返回该状态键，不返回配置或其它密钥。
func (h *handlers) contextTokensState(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	if !h.requireHandoff(w, r) {
		return
	}
	id := strings.TrimSpace(mux.Vars(r)["id"])
	if _, ok := h.deps.Store.GetConfig(id); !ok {
		writeError(w, http.StatusNotFound, "未知连接器")
		return
	}
	raw, _ := h.deps.Store.PollingState(id).Get(wechatContextTokensKey)
	writeJSON(w, http.StatusOK, map[string]string{wechatContextTokensKey: strings.TrimSpace(raw)})
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
