package commands

import (
	"net/http"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
)

// ChatWebAPICachePath 微型 Web 客户端缓存分析端点前缀（cache.analytics.v1）。
// 子路径：/capabilities、/overview、/requests[/{id}]、/messages/{id}/trace。
const ChatWebAPICachePath = "/web/api/cache"

// HandleChatWebAPICache 缓存分析 HTTP 入口：复用 cacheanalytics.Handler
//（同一契约单点定义），数据源优先为 usageanalytics 的数据库 Source
//（usage_analytics.sqlite，与 TUI /usage、runtime server 同库同源）。
// session_id 缺省为当前 runtime session id；服务不可用时返回稳定错误码
// cache_analytics_disabled（503）。
func HandleChatWebAPICache(w http.ResponseWriter, r *http.Request) {
	session := chatWebSession()
	if session == nil {
		writeWebAPIJSON(w, http.StatusServiceUnavailable, cacheErrorBody(cacheanalytics.ErrDisabled, "chat session not ready"))
		return
	}
	var src cacheanalytics.Source
	if service := ensureLocalUsageService(session.LocalRuntimeHost); service != nil {
		src = service.Source()
	}
	if src == nil {
		// 数据库不可用（例如 EventBus/宿主缺失）时回退到进程内实时投影，
		// 保证端点始终可用；契约与错误码不变。
		if service := ensureLocalCacheService(session.LocalRuntimeHost); service != nil {
			src = service.Source()
		}
	}
	if src == nil {
		writeWebAPIJSON(w, http.StatusServiceUnavailable, cacheErrorBody(cacheanalytics.ErrDisabled, "cache analytics service unavailable"))
		return
	}
	if r.URL.Query().Get("session_id") == "" {
		if sessionID := currentRuntimeSessionID(session); sessionID != "" {
			query := r.URL.Query()
			query.Set("session_id", sessionID)
			r.URL.RawQuery = query.Encode()
		}
	}
	handler := cacheanalytics.Handler(ChatWebAPICachePath, src)
	if handler == nil {
		writeWebAPIJSON(w, http.StatusServiceUnavailable, cacheErrorBody(cacheanalytics.ErrDisabled, "cache analytics handler unavailable"))
		return
	}
	handler.ServeHTTP(w, r)
}

// cacheErrorBody 稳定错误 envelope（与 cacheanalytics 错误码一致）。
func cacheErrorBody(code error, message string) map[string]interface{} {
	return map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code.Error(),
			"message": message,
		},
	}
}
