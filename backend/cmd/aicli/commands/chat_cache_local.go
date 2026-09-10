package commands

import (
	"context"
	"strings"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ============================================================================
// aicli 本地 in-process 模式的 LLM 缓存分析服务（cache.analytics.v1）。
//
// 与 observeSvc 同模式：Collector 挂在 localChatRuntimeHost.EventBus 上惰性
// 构建一次，HTTP（/web/api/cache/*）与 TUI（/usage 命令）共用同一个
// cacheanalytics.Source，采集与聚合只做一次（方案 §6.1）。
//
// 与 observe 不同：collector 无 gate（记录为低敏 token/缓存统计，不含
// prompt/completion 原文，环形缓冲 1000 条封顶），TUI /usage 在默认模式下
// 也依赖它，因此只要 EventBus 就绪即挂载。
// ============================================================================

// localCacheHistoryLookup 把 host.SessionStore（runtimechat.SessionStorage）
// 适配为 cacheanalytics.HistoryLookup：由消息 role/turn 反查 message_id，
// 支撑"缓存失效 → 反查请求历史与原始记录"的兜底路径（方案 §5.2）。
type localCacheHistoryLookup struct {
	store runtimechat.SessionStorage
}

func (h *localCacheHistoryLookup) load(sessionID string) (*runtimechat.Session, bool) {
	if h == nil || h.store == nil || strings.TrimSpace(sessionID) == "" {
		return nil, false
	}
	session, err := h.store.Load(context.Background(), sessionID)
	if err != nil || session == nil {
		return nil, false
	}
	return session, true
}

// MessageContext 返回消息在历史中的 role/turn/相邻消息。
func (h *localCacheHistoryLookup) MessageContext(sessionID, messageID string) (cacheanalytics.MessageContext, bool) {
	session, ok := h.load(sessionID)
	if !ok {
		return cacheanalytics.MessageContext{}, false
	}
	history := session.History
	for i := range history {
		if runtimetypes.MessageID(history[i]) != messageID {
			continue
		}
		ctx := cacheanalytics.MessageContext{
			MessageID: messageID,
			Role:      strings.ToLower(strings.TrimSpace(history[i].Role)),
			TurnID:    runtimetypes.TurnID(history[i]),
		}
		if i > 0 {
			ctx.PrevMessageID = runtimetypes.MessageID(history[i-1])
		}
		if i+1 < len(history) {
			ctx.NextMessageID = runtimetypes.MessageID(history[i+1])
		}
		return ctx, true
	}
	return cacheanalytics.MessageContext{}, false
}

// AssistantMessageIDByTurn 返回该 turn 最后一条 assistant 消息的 message_id
//（assistant 消息 turn_id 继承自触发 user 消息，直接匹配）。
func (h *localCacheHistoryLookup) AssistantMessageIDByTurn(sessionID, turnID string) (string, bool) {
	session, ok := h.load(sessionID)
	if !ok {
		return "", false
	}
	for i := len(session.History) - 1; i >= 0; i-- {
		msg := session.History[i]
		if !strings.EqualFold(strings.TrimSpace(msg.Role), "assistant") {
			continue
		}
		if runtimetypes.TurnID(msg) != turnID {
			continue
		}
		if id := runtimetypes.MessageID(msg); id != "" {
			return id, true
		}
		return "", false
	}
	return "", false
}

// UserMessageIDByTurn 返回触发该 turn 的 user 消息 message_id。
func (h *localCacheHistoryLookup) UserMessageIDByTurn(sessionID, turnID string) (string, bool) {
	session, ok := h.load(sessionID)
	if !ok {
		return "", false
	}
	for i := range session.History {
		msg := session.History[i]
		if !strings.EqualFold(strings.TrimSpace(msg.Role), "user") {
			continue
		}
		if runtimetypes.TurnID(msg) != turnID {
			continue
		}
		if id := runtimetypes.MessageID(msg); id != "" {
			return id, true
		}
		return "", false
	}
	return "", false
}

// SessionExists 会话是否存在于历史存储。
func (h *localCacheHistoryLookup) SessionExists(sessionID string) bool {
	_, ok := h.load(sessionID)
	return ok
}

// ensureLocalCacheService 惰性构建本地缓存分析服务并缓存到 host。
// 每次调用返回同一个实例；EventBus 缺失时返回 nil。
func ensureLocalCacheService(host *localChatRuntimeHost) *cacheanalytics.Service {
	if host == nil {
		return nil
	}
	host.cacheOnce.Do(func() {
		host.cacheSvc = buildLocalCacheService(host)
	})
	return host.cacheSvc
}

func buildLocalCacheService(host *localChatRuntimeHost) *cacheanalytics.Service {
	if host == nil || host.EventBus == nil {
		return nil
	}
	var history cacheanalytics.HistoryLookup
	if host.SessionStore != nil {
		history = &localCacheHistoryLookup{store: host.SessionStore}
	}
	service := cacheanalytics.Attach(host.EventBus, cacheanalytics.Options{}, history)
	if service == nil {
		return nil
	}
	host.cleanupFns = append(host.cleanupFns, service.Close)
	return service
}

// chatLocalCacheService 返回当前活动会话的缓存分析服务；无会话时返回 nil。
func chatLocalCacheService() *cacheanalytics.Service {
	session := chatDebugDisplaySession()
	if session == nil {
		return nil
	}
	return ensureLocalCacheService(session.LocalRuntimeHost)
}
