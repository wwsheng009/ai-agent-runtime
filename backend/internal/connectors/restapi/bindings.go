// bindings 管理面：连接器 ↔ 会话绑定的查看/绑定/解绑（docs/bots/
// 09-multi-instance-binding.md §5.2，N2/N3）。
//
// 语义：
//   - GET    /connectors/bindings                列表（可按 connector_id/chat 过滤）；
//   - PUT    /connectors/{id}/bindings/{chat}    绑定（覆盖式指针切换；乐观锁）；
//   - DELETE /connectors/{id}/bindings/{chat}    解绑（写零值墓碑，等价 /new）。
//
// 校验：connector 存在且 enabled；policy.allowed_users 非空时 chat 必须命中
// （与入站白名单同源，aicli fail-closed 不放宽）；PUT/DELETE 走 Store 的 CAS
// （锁内读改写）防止双宿主并发改绑互相覆盖。
//
// 会话存在性：v1 由调用方保证（本机校验属 P2 mesh 派发切片）；绑定是纯指针
// 操作，错误会话 id 的后果等同"绑定了一个空转会话"，可用同接口改绑纠正。
package restapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// BindingView 是绑定的对外形状（含 connector 摘要，供 UI 直接渲染）。
type BindingView struct {
	ConnectorID string `json:"connector_id"`
	Chat        string `json:"chat"`
	SessionID   string `json:"session_id,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	Connector   *BindingConnectorView `json:"connector,omitempty"`
}

// BindingConnectorView 是绑定视图内嵌的连接器摘要。
type BindingConnectorView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Enabled     bool   `json:"enabled"`
	ActiveOwner string `json:"active_owner,omitempty"`
}

func bindingView(binding connectors.Binding, cfg *connectors.Config, owner string) BindingView {
	view := BindingView{
		ConnectorID: binding.ConnectorID,
		Chat:        binding.Chat,
		SessionID:   strings.TrimSpace(binding.Session.SessionID),
		NodeID:      strings.TrimSpace(binding.Session.NodeID),
	}
	if !binding.UpdatedAt.IsZero() {
		view.UpdatedAt = binding.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	if cfg != nil {
		view.Connector = &BindingConnectorView{
			ID:          cfg.ID,
			Name:        cfg.Name,
			Provider:    cfg.Provider,
			Enabled:     cfg.Enabled,
			ActiveOwner: owner,
		}
	}
	return view
}

// listBindings 处理 GET /connectors/bindings?connector_id=&chat=。
func (h *handlers) listBindings(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	connectorID := strings.TrimSpace(r.URL.Query().Get("connector_id"))
	chatFilter := strings.TrimSpace(r.URL.Query().Get("chat"))

	bindings := h.deps.Store.ListBindings(connectorID)
	items := make([]BindingView, 0, len(bindings))
	for _, binding := range bindings {
		if chatFilter != "" && binding.Chat != chatFilter {
			continue
		}
		cfg, ok := h.deps.Store.GetConfig(binding.ConnectorID)
		if !ok {
			continue // 孤儿绑定（连接器已删）：不展示
		}
		items = append(items, bindingView(binding, &cfg, h.ownerOf(binding.ConnectorID)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// bindingRequest 是 PUT 的提交体。
type bindingRequest struct {
	SessionID         string `json:"session_id"`
	NodeID            string `json:"node_id,omitempty"`
	ExpectedUpdatedAt string `json:"expected_updated_at,omitempty"`
}

// putBinding 处理 PUT /connectors/{id}/bindings/{chat}：绑定（覆盖式切换）。
func (h *handlers) putBinding(w http.ResponseWriter, r *http.Request) {
	h.mutateBinding(w, r, false)
}

// deleteBinding 处理 DELETE /connectors/{id}/bindings/{chat}：显式解绑（墓碑）。
func (h *handlers) deleteBinding(w http.ResponseWriter, r *http.Request) {
	h.mutateBinding(w, r, true)
}

func (h *handlers) mutateBinding(w http.ResponseWriter, r *http.Request, unbind bool) {
	if !h.ready(w) {
		return
	}
	connectorID := strings.TrimSpace(mux.Vars(r)["id"])
	chat := strings.TrimSpace(mux.Vars(r)["chat"])
	if connectorID == "" || chat == "" {
		writeError(w, http.StatusBadRequest, "connector id 与 chat 必填")
		return
	}

	cfg, ok := h.deps.Store.GetConfig(connectorID)
	if !ok {
		writeError(w, http.StatusNotFound, "未知连接器："+connectorID)
		return
	}
	if !cfg.Enabled {
		writeError(w, http.StatusConflict, "连接器已停用："+connectorID)
		return
	}
	// 白名单与入站同一判定（09 §8：不能通过 bind 绕过入站准入）。
	if !cfg.Policy.AllowsUser(chat) {
		writeError(w, http.StatusForbidden, "chat 不在连接器 allowed_users 白名单内")
		return
	}

	var request bindingRequest
	if !decodeBody(h.deps, w, r, &request) {
		return
	}

	expected := time.Time{}
	if raw := strings.TrimSpace(request.ExpectedUpdatedAt); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "expected_updated_at 不是合法的 RFC3339 时间："+raw)
			return
		}
		expected = parsed
	}

	next := connectors.Binding{ConnectorID: connectorID, Chat: chat}
	if !unbind {
		sessionID := strings.TrimSpace(request.SessionID)
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, "session_id 必填（解绑请使用 DELETE）")
			return
		}
		next.Session = connectors.SessionRef{
			NodeID:    strings.TrimSpace(request.NodeID),
			SessionID: sessionID,
		}
	}

	// DELETE 未带 expected：按「读旧值 → 以旧值时间戳做 CAS」执行（解绑的乐观锁
	// 语义=基于当前状态，而非"必须不存在"——墓碑本身就是一次改写）。
	if unbind && expected.IsZero() {
		if current, ok := h.deps.Store.GetBinding(connectorID, chat); ok {
			expected = current.UpdatedAt
		}
	}

	current, swapped, err := h.deps.Store.CompareAndSwapBinding(connectorID, chat, expected, next)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "绑定写入失败："+err.Error())
		return
	}
	if !swapped {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":    "绑定已被其它操作更新（乐观锁未命中）",
			"current":  bindingView(current, &cfg, h.ownerOf(connectorID)),
			"expected": request.ExpectedUpdatedAt,
		})
		return
	}

	action := "bind"
	if unbind {
		action = "unbind"
	}
	if h.deps.Logf != nil {
		h.deps.Logf("connectors: %s %s chat=%s session=%q (via %s %s)",
			action, connectorID, chat, next.Session.SessionID, r.Method, r.URL.Path)
	}
	// 返回实现刷新后的记录（含 UpdatedAt；CAS 命中时 current 已是存储值），
	// 而不是请求体投影。
	writeJSON(w, http.StatusOK, bindingView(current, &cfg, h.ownerOf(connectorID)))
}

func (h *handlers) ownerOf(connectorID string) string {
	if owner, claimed := h.deps.Store.OwnerOf(connectorID); claimed {
		return owner.Owner
	}
	return ""
}
