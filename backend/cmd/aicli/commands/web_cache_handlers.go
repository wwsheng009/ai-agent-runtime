package commands

import (
	"net/http"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
)

// ChatWebAPICachePath 微型 Web 客户端缓存分析端点前缀（cache.analytics.v1）。
// 子路径：/capabilities、/overview、/requests[/{id}]、/messages/{id}/trace。
const ChatWebAPICachePath = "/web/api/cache"

// HandleChatWebAPICache 缓存分析 HTTP 入口：复用 cacheanalytics.Handler
//（同一契约单点定义），数据源为当前会话的本地 cacheanalytics.Service。
// session_id 缺省为当前 runtime session id；服务不可用时返回稳定错误码
// cache_analytics_disabled（503）。
func HandleChatWebAPICache(w http.ResponseWriter, r *http.Request) {
	session := chatWebSession()
	if session == nil {
		writeWebAPIJSON(w, http.StatusServiceUnavailable, cacheErrorBody(cacheanalytics.ErrDisabled, "chat session not ready"))
		return
	}
	service := ensureLocalCacheService(session.LocalRuntimeHost)
	if service == nil {
		writeWebAPIJSON(w, http.StatusServiceUnavailable, cacheErrorBody(cacheanalytics.ErrDisabled, "cache analytics service unavailable"))
		return
	}
	src := service.Source()
	if src == nil {
		writeWebAPIJSON(w, http.StatusServiceUnavailable, cacheErrorBody(cacheanalytics.ErrDisabled, "cache analytics source unavailable"))
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
