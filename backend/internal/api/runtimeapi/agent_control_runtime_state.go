package runtimeapi

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// 执行容器运行态：agent-control 列表响应里的 `runtime_state` 字段。
//
// 与身份状态（`agentcontrol.AgentStatus*`）刻意分开，两者回答不同问题：
//   - 身份状态回答「这个身份还能不能接路由」（active / stale / closed），只由
//     显式 close / reclaim / 审计收敛改写，**不随回合结束改变**；
//   - 运行态回答「这个身份的容器此刻在不在跑」（running / idle / stopped），是
//     执行容器的事实，**不落库到身份行**。
//
// 背景（本次修复的根因）：兼容投影过去只用 `session.State`（仅 closed /
// archived 算终态）推导身份状态，子代理跑完一轮后会话行仍是 open，于是身份行
// 永远显示 active（面板「运行中」），必须主代理显式 close_agent 才会变。
//
// 取证纪律：只上报**有正面证据**的运行态，取不到证据时返回空串（字段省略），
// 由前端回退到身份状态。**「读不到」不等于「已结束」**：把存储故障或未配置的
// 数据源渲染成「子代理已结束」会比原来的假运行中更难排查。
const (
	// AgentRuntimeStateRunning 容器正在执行（含等待审批 / 等待输入 / 回滚）。
	AgentRuntimeStateRunning = "running"
	// AgentRuntimeStateIdle 容器存在且当前没有在跑（回合之间 / 已跑完）。
	AgentRuntimeStateIdle = "idle"
	// AgentRuntimeStateStopped 容器已被显式停止。
	AgentRuntimeStateStopped = "stopped"
)

// 内存兜底 store 的已解析 key 形态（`storePath + "|" + storeDSN` 全空）。
// 这种 store 只覆盖本进程写入的会话，不能拿来回答「别的宿主的子代理结束了吗」。
const agentRuntimeMemoryStoreKey = "|"

// agentControlAgentView 是身份行的 HTTP 视图：durable 身份行 + 执行容器运行态。
//
// 用内嵌而非给 AgentRecord 加字段：`runtime_state` 是查询时刻的容器事实，既不
// 该写进注册表（会让两个宿主互相覆盖同一行），也不该参与身份行的相等性比较。
type agentControlAgentView struct {
	agentcontrol.AgentRecord
	// RuntimeState 见 AgentRuntimeState* ；空串（字段省略）表示本次查询没有
	// 取到运行态证据，前端应回退到身份状态，不得据此推断「已结束」。
	RuntimeState string `json:"runtime_state,omitempty"`
}

// buildAgentControlAgentViews 给一批身份行补运行态。同一页内按 session 去重，
// 避免同一个容器被反复查。
func (h *Handler) buildAgentControlAgentViews(ctx context.Context, records []agentcontrol.AgentRecord) []agentControlAgentView {
	views := make([]agentControlAgentView, 0, len(records))
	if len(records) == 0 {
		return views
	}
	states := make(map[string]string, len(records))
	for _, record := range records {
		sessionID := strings.TrimSpace(record.SessionID)
		if sessionID == "" {
			sessionID = strings.TrimSpace(record.AgentID)
		}
		state, cached := states[sessionID]
		if !cached {
			state = h.resolveAgentRuntimeState(ctx, sessionID)
			states[sessionID] = state
		}
		views = append(views, agentControlAgentView{AgentRecord: record, RuntimeState: state})
	}
	return views
}

// resolveAgentRuntimeState 解析单个会话的运行态，证据强度从高到低：
//
//  1. 本进程活体 actor：只在真正持有执行权时存在，最精确；
//  2. 持久运行态（`session_runtime_state`）：由真正执行该会话的宿主写入，跨进程
//     可见，用于「浏览器面板 / CLI 宿主各持一个进程、共用同一个 store」的场景；
//  3. 取不到证据 → 空串。
func (h *Handler) resolveAgentRuntimeState(ctx context.Context, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if h == nil || sessionID == "" {
		return ""
	}
	if hub := h.peekSessionHub(); hub != nil {
		if actor, ok := hub.Get(sessionID); ok && actor != nil {
			if state, exists := actor.StateSummary(); exists {
				if normalized := normalizeAgentRuntimeState(string(state.Status)); normalized != "" {
					return normalized
				}
			}
		}
	}
	store, durable := h.peekDurableSessionRuntimeStore()
	if !durable {
		return ""
	}
	state, err := store.LoadState(ctx, sessionID)
	if err != nil || state == nil {
		return ""
	}
	return normalizeAgentRuntimeState(string(state.Status))
}

// peekSessionHub 只读探测已建好的 actor hub，**不触发懒加载**：列表是高频只读
// 路径，不该因为一次列表面板刷新就为所有会话建 actor。
func (h *Handler) peekSessionHub() *chat.SessionHub {
	if h == nil {
		return nil
	}
	h.sessionRuntimeMu.RLock()
	defer h.sessionRuntimeMu.RUnlock()
	return h.sessionHub
}

// peekDurableSessionRuntimeStore 返回**已配置的持久**运行态 store。内存兜底
// store（key 为 "|"）与未解析（key 为空）都返回 false：这类 store 对本进程外
// 执行过的会话只会给出空白，用它回答「子代理结束了吗」必然误判。
func (h *Handler) peekDurableSessionRuntimeStore() (chat.RuntimeStateStore, bool) {
	if h == nil {
		return nil, false
	}
	h.sessionRuntimeMu.RLock()
	defer h.sessionRuntimeMu.RUnlock()
	if h.sessionRuntimeStore == nil {
		return nil, false
	}
	key := strings.TrimSpace(h.sessionRuntimeStoreKey)
	if key == "" || key == agentRuntimeMemoryStoreKey {
		return nil, false
	}
	return h.sessionRuntimeStore, true
}

// normalizeAgentRuntimeState 收口运行态取值：只承认已知状态，其余（含未知新
// 状态）一律返回空串 —— 未知不得被当作「已结束」。
func normalizeAgentRuntimeState(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(chat.SessionRunning), string(chat.SessionWaitingApproval),
		string(chat.SessionWaitingInput), string(chat.SessionRewinding):
		return AgentRuntimeStateRunning
	case string(chat.SessionIdle):
		return AgentRuntimeStateIdle
	case string(chat.SessionStopped):
		return AgentRuntimeStateStopped
	default:
		return ""
	}
}
