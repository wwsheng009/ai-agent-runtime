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
	// 数据源优先统一用量库（usage_analytics.sqlite），并按会话回退到进程内
	// 实时投影 + runtime 镜像（session_runtime.sqlite cache_requests）：
	// 用量库存在不代表其中有该会话的行（未挂载用量采集/升级前的会话），
	// 只判断"服务是否可用"会让恢复后的会话历史查询命中空结果而页面无数据。
	var usageSrc, mirrorSrc cacheanalytics.Source
	if service := ensureLocalUsageService(session.LocalRuntimeHost); service != nil {
		usageSrc = service.Source()
	}
	if service := ensureLocalCacheService(session.LocalRuntimeHost); service != nil {
		mirrorSrc = service.Source()
	}
	src := cacheanalytics.NewSessionFallbackSource(usageSrc, mirrorSrc)
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
