package runtimeapi

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// ── 会话 actor 延迟重建（A6；Batch 12「V15/V19 延迟收敛」的通用化） ──
//
// 运行时刷新（profile 切换、路由写入、模型/provider 选择族）要让「下一轮生效」，
// server 侧的权威路径是**驱逐空闲 actor**，让下一次 GetOrCreate 按新配置重建
// （agent / 系统提示词 / 工具策略在 actor 构建期固化，只写 sessionmeta 等于假开关）。
//
// 但 actor 正是在途 run 的所有者：无条件 StopContext 会经 cancelActive 以
// actor_stop 取消在途回合，用户只看到一句 context canceled。事故与归因链见
// docs/analysis/a6-host-stop-attribution-and-inflight-refresh-20260927.md。
// 因此在途 turn 一律不驱逐，只登记延迟重建标记，由命令入口
// reconcilePendingActorRebuild 在 actor 空闲后兑现——绝不打断本轮。
const (
	// sessionActorEvictTimeout 是驱逐空闲 actor 的等待上限：驱逐失败也不阻塞命令
	// 返回——actor 已从 hub 摘除，后台停止完成后下一次构建同样取新面。
	sessionActorEvictTimeout = 5 * time.Second

	// 延迟重建原因：落到 session_end.cancel_reason 与结构化日志，用于归因
	// 「本轮是谁停的」（与 CLI 平面 runtime_refresh:* 同口径）。
	sessionActorRebuildReasonProfileSwitch = "profile_switch"
	sessionActorRebuildReasonRoutingWrite  = "runtime_refresh:routing_write"
)

// markPendingActorRebuild 登记「actor 空闲后必须重建」的会话级标记（值=原因）。
// 标记只在进程内有效：actor 是进程内对象，重启后不存在旧 actor，下一次构建
// 本就会读到已落地的配置。
func (h *Handler) markPendingActorRebuild(sessionID, reason string) {
	if h == nil {
		return
	}
	sessionID = chat.NormalizeSessionID(strings.TrimSpace(sessionID))
	if sessionID == "" {
		return
	}
	if strings.TrimSpace(reason) == "" {
		reason = "runtime_refresh:unknown"
	}
	h.actorRebuildMu.Lock()
	defer h.actorRebuildMu.Unlock()
	if h.actorRebuildPending == nil {
		h.actorRebuildPending = make(map[string]string)
	}
	h.actorRebuildPending[sessionID] = reason
}

func (h *Handler) clearPendingActorRebuild(sessionID string) {
	if h == nil {
		return
	}
	sessionID = chat.NormalizeSessionID(strings.TrimSpace(sessionID))
	if sessionID == "" {
		return
	}
	h.actorRebuildMu.Lock()
	defer h.actorRebuildMu.Unlock()
	delete(h.actorRebuildPending, sessionID)
}

func (h *Handler) hasPendingActorRebuild(sessionID string) bool {
	return h.pendingActorRebuildReason(sessionID) != ""
}

// pendingActorRebuildReason 返回该会话的延迟重建原因；无标记时返回空串。
func (h *Handler) pendingActorRebuildReason(sessionID string) string {
	if h == nil {
		return ""
	}
	sessionID = chat.NormalizeSessionID(strings.TrimSpace(sessionID))
	if sessionID == "" {
		return ""
	}
	h.actorRebuildMu.Lock()
	defer h.actorRebuildMu.Unlock()
	return h.actorRebuildPending[sessionID]
}

// reconcilePendingActorRebuild 在命令入口兑现延迟重建：actor 已空闲（或已不在位）
// 时清除标记并驱逐旧 actor，使紧随其后的 GetOrCreate 按新配置重建。它只做一次
// 进程内 map 查询，不给命令热路径增加存储读取。
func (h *Handler) reconcilePendingActorRebuild(sessionID string) {
	reason := h.pendingActorRebuildReason(sessionID)
	if h == nil || reason == "" {
		return
	}
	hub := h.peekSessionHub()
	if hub == nil {
		h.clearPendingActorRebuild(sessionID)
		return
	}
	actor, ok := hub.Get(sessionID)
	if !ok || actor == nil {
		// 旧 actor 已经不在位（空闲驱逐/进程重启）：下一次构建天然取新配置。
		h.clearPendingActorRebuild(sessionID)
		return
	}
	if actor.RunInFlight() {
		// 仍在途：保持标记，等下一个边界。
		return
	}
	if !stopSessionActorForRebuild(hub, sessionID, reason) {
		// 停止失败：保留标记，下一次边界重试（旧 actor 仍在位，不能当已兑现）。
		return
	}
	h.clearPendingActorRebuild(sessionID)
}

// stopSessionActorForRebuild 驱逐空闲 actor：登记停因（供 session_end 归因）、
// 有界等待停止、写结构化日志。返回是否确实停止成功。
//
// 调用方必须先确认没有在途 turn（RunInFlight()==false），否则会打断它。
func stopSessionActorForRebuild(hub *chat.SessionHub, sessionID, reason string) bool {
	if hub == nil {
		return false
	}
	sessionID = chat.NormalizeSessionID(strings.TrimSpace(sessionID))
	if sessionID == "" {
		return false
	}
	if actor, ok := hub.Get(sessionID); ok && actor != nil {
		// 停因登记在 actor 上：run ctx 取消时落到 session_end.cancel_reason，
		// 使「本轮是谁停的」可归因（A6）。
		actor.SetNextStopReason(reason)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), sessionActorEvictTimeout)
	defer cancel()
	if err := hub.StopContext(stopCtx, sessionID); err != nil {
		logger.Warnf("actor stop failed: session=%s reason=%s err=%v", sessionID, reason, err)
		return false
	}
	logger.Warnf("actor stopped: session=%s reason=%s", sessionID, reason)
	return true
}
