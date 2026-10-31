package main

import (
	"net/http"
	"strings"
	"sync"
)

// notifyTargetHealth 记录每个网关自持连接器**最近一次出站失败**，供控制台
// 「通知设置 → Bot 目标」在目标行上直接显示"这条为什么没发出去"（典型：
// 微信缺 context_token、原文案见 channels/im.go；或源节点离线导致 token 同步失败）。
//
// 只放内存、不落盘：它描述运行时状态，重启后从零开始，下一次失败会重新记录。
// 这样日志里刷屏的重复错误也能收敛成"页面上看得见的一行"。
//
// 并发：SendOutbound / NotifyAttentionScoped 在 Dispatcher 的 goroutine 里调用，
// HTTP handler 在另一个 goroutine 读——统一由 mu 保护。
type notifyTargetHealth struct {
	mu     sync.Mutex
	errors map[string]string
}

// notifyTargetErrorMaxRunes 是错误文本的保留上限：足够长到可读，又不至于把
// 控制台行撑爆或让内存无界。
const notifyTargetErrorMaxRunes = 200

func newNotifyTargetHealth() *notifyTargetHealth {
	return &notifyTargetHealth{errors: make(map[string]string)}
}

// Record 记录一次出站结果：err != nil 记下错误文本；err == nil 清除该连接器的
// 旧错误（一次成功即恢复干净状态，避免过期错误长期挂在页面上）。
func (h *notifyTargetHealth) Record(connectorID string, err error) {
	if h == nil {
		return
	}
	connectorID = strings.TrimSpace(connectorID)
	if connectorID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.errors == nil {
		h.errors = make(map[string]string)
	}
	if err == nil {
		delete(h.errors, connectorID)
		return
	}
	h.errors[connectorID] = truncateNotificationText(err.Error(), notifyTargetErrorMaxRunes)
}

// RecordMany 用于"按连接器列表投递"（会话绑定路径）：全部成功才清除这组目标上的
// 旧错误；失败时把错误挂到该组每个连接器上（调用方拿不到逐个失败明细，只能整体记）。
func (h *notifyTargetHealth) RecordMany(connectorIDs []string, err error) {
	for _, id := range connectorIDs {
		h.Record(id, err)
	}
}

// Snapshot 返回 connector_id → 最近失败文本的副本（无失败时是空 map，不是 nil）。
func (h *notifyTargetHealth) Snapshot() map[string]string {
	out := make(map[string]string)
	if h == nil {
		return out
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, v := range h.errors {
		out[k] = v
	}
	return out
}

// handleNotifyIMHealth 暴露 GET /api/notify/im/health（owner 保护）：
//
//	{"errors": {"gw-<node>-wechat-...": "wechat: chat \"...\" 缺少 context_token（…）"}}
//
// 控制台据此在目标行显示「最近发送失败：…」。未配置 / 无失败时 errors 为空对象。
func handleNotifyIMHealth(health *notifyTargetHealth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"errors": health.Snapshot()})
	}
}
