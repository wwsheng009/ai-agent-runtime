package skills

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/mux"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ============================================================================
// runtime server 侧 LLM 缓存分析挂载（cache.analytics.v1，方案 §3.2/§11 Phase 2）。
//
// 与 aicli 本地形态（cmd/aicli/commands/chat_cache_local.go）共用同一个
// Collector + Projector + httpapi 契约；差异只在：
//   - EventBus 为服务端 runtime host 总线（h.getRuntimeEventBus()）；
//   - HistoryLookup 适配 chat.SessionManager（storage 私有，经 Get 加载）；
//   - session 显式在路径 /api/runtime/sessions/{id}/cache/* 中（W2）。
//
// collector 在路由注册（server 启动）时即挂载，保证启动后的 LLM 请求事件
// 全部被采集；HTTP 端点惰性返回同一 Source。
// ============================================================================

// managerCacheHistoryLookup 把 chat.SessionStorage 适配为
// cacheanalytics.HistoryLookup：由消息 role/turn 反查 message_id，
// 支撑"缓存失效 → 反查请求历史与原始记录"的兜底路径（方案 §5.2）。
type managerCacheHistoryLookup struct {
	store runtimechat.SessionStorage
}

func (l *managerCacheHistoryLookup) load(sessionID string) (*runtimechat.Session, bool) {
	if l == nil || l.store == nil || strings.TrimSpace(sessionID) == "" {
		return nil, false
	}
	session, err := l.store.Load(context.Background(), sessionID)
	if err != nil || session == nil {
		return nil, false
	}
	return session, true
}

// MessageContext 返回消息在历史中的 role/turn/相邻消息。
func (l *managerCacheHistoryLookup) MessageContext(sessionID, messageID string) (cacheanalytics.MessageContext, bool) {
	session, ok := l.load(sessionID)
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

// AssistantMessageIDByTurn 返回该 turn 最后一条 assistant 消息的 message_id。
func (l *managerCacheHistoryLookup) AssistantMessageIDByTurn(sessionID, turnID string) (string, bool) {
	session, ok := l.load(sessionID)
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
func (l *managerCacheHistoryLookup) UserMessageIDByTurn(sessionID, turnID string) (string, bool) {
	session, ok := l.load(sessionID)
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
func (l *managerCacheHistoryLookup) SessionExists(sessionID string) bool {
	_, ok := l.load(sessionID)
	return ok
}

// cacheAnalyticsMu 保护惰性构建的缓存分析服务（进程内单例）。
var (
	cacheAnalyticsMu     sync.Mutex
	cacheAnalyticsSource cacheanalytics.Source
	cacheAnalyticsClose  func()
)

// attachCacheAnalyticsService 在 server 启动（路由注册）时挂载 collector：
// 服务端 EventBus + SessionManager 兜底历史。重复调用返回同一实例。
func (h *Handler) attachCacheAnalyticsService() cacheanalytics.Source {
	cacheAnalyticsMu.Lock()
	defer cacheAnalyticsMu.Unlock()
	if cacheAnalyticsSource != nil {
		return cacheAnalyticsSource
	}
	bus := h.getRuntimeEventBus()
	if bus == nil {
		return nil
	}
	var history cacheanalytics.HistoryLookup
	if h.sessionManager != nil {
		history = &managerCacheHistoryLookup{store: h.sessionManager.GetStorage()}
	}
	service := cacheanalytics.Attach(bus, cacheanalytics.Options{}, history)
	if service == nil {
		return nil
	}
	cacheAnalyticsSource = service.Source()
	cacheAnalyticsClose = service.Close
	return cacheAnalyticsSource
}

// ensureCacheAnalyticsSource 返回缓存分析数据源；未配置时写 503 错误信封。
func (h *Handler) ensureCacheAnalyticsSource(w http.ResponseWriter) cacheanalytics.Source {
	src := h.attachCacheAnalyticsService()
	if src == nil {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    "cache_analytics_disabled",
				"message": "cache analytics not configured",
			},
		})
		return nil
	}
	return src
}

// HandleSessionCache 是 /api/runtime/sessions/{id}/cache/* 的统一入口：
// 从路径变量解析 session，委托给共享 httpapi 契约（§4：契约只定义一次）。
func (h *Handler) HandleSessionCache(w http.ResponseWriter, r *http.Request) {
	src := h.ensureCacheAnalyticsSource(w)
	if src == nil {
		return
	}
	sessionID := mux.Vars(r)["id"]
	if strings.TrimSpace(sessionID) == "" {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrConfigInvalid, "missing session id"))
		return
	}
	// 共享 httpapi 契约从 query 读取 session_id（aicli 形态）；runtime server
	// 形态 session 显式在路径中（W2），这里把路径变量注入 query 后委托。
	query := r.URL.Query()
	if strings.TrimSpace(query.Get("session_id")) == "" {
		query.Set("session_id", sessionID)
		r.URL.RawQuery = query.Encode()
	}
	prefix := "/api/runtime/sessions/" + sessionID + "/cache"
	handler := cacheanalytics.Handler(prefix, src)
	if handler == nil {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    "cache_analytics_disabled",
				"message": "cache analytics not configured",
			},
		})
		return
	}
	handler.ServeHTTP(w, r)
}
