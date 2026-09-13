package skills

import (
	"context"
	"net/http"
	"strings"
	"sync"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// ============================================================================
// runtime server 侧统一用量分析挂载（usage_analytics.sqlite 单一数据源）。
//
// 与缓存分析（cache_analytics_handlers.go）同处挂载：进程启动（路由注册）时
// 即订阅 EventBus 实时写入，/api/runtime/analytics/* 与
// /api/runtime/sessions/{id}/cache/* 读同一个数据库。
//
// 与分析库的会话元数据（title/project/provider/...）由 chat.SessionManager
// 补齐（best-effort：查不到就保留库中已有值）。
// ============================================================================

// managerSessionMetaLookup 把 chat.SessionManager 适配为
// usageanalytics.SessionMetaLookup。
type managerSessionMetaLookup struct {
	manager *runtimechat.SessionManager
}

// SessionMeta 返回会话元数据；ok=false 表示未知（保留库里已有值）。
func (l *managerSessionMetaLookup) SessionMeta(sessionID string) (usageanalytics.SessionMeta, bool) {
	if l == nil || l.manager == nil || strings.TrimSpace(sessionID) == "" {
		return usageanalytics.SessionMeta{}, false
	}
	session, err := l.manager.Get(context.Background(), sessionID)
	if err != nil || session == nil {
		return usageanalytics.SessionMeta{}, false
	}
	meta := usageanalytics.SessionMeta{
		Title:    strings.TrimSpace(session.Metadata.Title),
		Model:    strings.TrimSpace(session.Metadata.LastModel),
		Provider: contextString(session.Metadata.Context, "provider"),
		Protocol: contextString(session.Metadata.Context, "protocol"),
	}
	// 目录绑定写入 metadata.context.workspace_path（handler.go §4.3.1）。
	workspace := contextString(session.Metadata.Context, "workspace_path", "working_directory", "project_path", "cwd")
	meta.WorkingDirectory = workspace
	meta.ProjectPath = workspace
	if meta.Provider == "" {
		meta.Provider = contextString(session.Metadata.Context, "last_provider")
	}
	if meta.Model == "" {
		meta.Model = contextString(session.Metadata.Context, "model")
	}
	switch session.State {
	case runtimechat.StateClosed:
		meta.Status = string(runtimechat.StateClosed)
	case runtimechat.StateArchived:
		meta.Status = string(runtimechat.StateArchived)
	case runtimechat.StateActive:
		meta.Status = string(runtimechat.StateActive)
	}
	return meta, true
}

func contextString(values map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if values == nil {
			return ""
		}
		raw, ok := values[key]
		if !ok {
			continue
		}
		switch typed := raw.(type) {
		case string:
			if trimmed := strings.TrimSpace(typed); trimmed != "" {
				return trimmed
			}
		case interface{ String() string }:
			if trimmed := strings.TrimSpace(typed.String()); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// usageAnalyticsMu 保护进程内单例（同路径复用，换路径重建）。
var (
	usageAnalyticsMu      sync.Mutex
	usageAnalyticsService *usageanalytics.Service
)

// attachUsageAnalyticsService 在 server 启动（路由注册）时挂载分析服务：
// 服务端 EventBus + SessionManager 元数据补齐 + 同库缓存 Source。
// 重复调用返回同一实例；路径变化时重建。
func (h *Handler) attachUsageAnalyticsService() *usageanalytics.Service {
	if h == nil {
		return nil
	}
	path := h.usageAnalyticsStorePath()
	lookup := &managerSessionMetaLookup{manager: h.sessionManager}
	var history cacheanalytics.HistoryLookup
	if h.sessionManager != nil && h.sessionManager.GetStorage() != nil {
		history = &managerCacheHistoryLookup{store: h.sessionManager.GetStorage()}
	}
	return attachUsageAnalyticsSingleton(path, h.getRuntimeEventBus(), lookup, history)
}

func attachUsageAnalyticsSingleton(
	path string,
	bus *runtimeevents.Bus,
	lookup usageanalytics.SessionMetaLookup,
	history cacheanalytics.HistoryLookup,
) *usageanalytics.Service {
	path = usageanalytics.NormalizePath(path)

	usageAnalyticsMu.Lock()
	defer usageAnalyticsMu.Unlock()
	if usageAnalyticsService != nil && usageAnalyticsService.DBPath() == path {
		return usageAnalyticsService
	}
	if usageAnalyticsService != nil {
		usageAnalyticsService.Close()
		usageAnalyticsService = nil
	}
	service, err := usageanalytics.Attach(bus, usageanalytics.Options{
		Config:  usageanalytics.Config{Path: path},
		Lookup:  lookup,
		History: history,
	})
	if err != nil || service == nil {
		return nil
	}
	usageAnalyticsService = service
	return usageAnalyticsService
}

// detachUsageAnalyticsService 关闭单例（测试/停机清理）。
func detachUsageAnalyticsService() {
	usageAnalyticsMu.Lock()
	defer usageAnalyticsMu.Unlock()
	if usageAnalyticsService != nil {
		usageAnalyticsService.Close()
		usageAnalyticsService = nil
	}
}

// usageAnalyticsStorePath 解析分析库路径：显式覆盖 > runtime store 同目录 > 默认。
func (h *Handler) usageAnalyticsStorePath() string {
	if h == nil {
		return usageanalytics.DefaultDBPath()
	}
	if path := strings.TrimSpace(h.usageAnalyticsDBPath); path != "" {
		return path
	}
	h.sessionRuntimeMu.RLock()
	runtimeStore := h.sessionRuntimeStore
	h.sessionRuntimeMu.RUnlock()
	if withPath, ok := runtimeStore.(interface{ Path() string }); ok && withPath != nil {
		if path := strings.TrimSpace(withPath.Path()); path != "" {
			return usageanalytics.PathFromRuntimeStore(path)
		}
	}
	return usageanalytics.DefaultDBPath()
}

// SetUsageAnalyticsDBPath 覆盖分析库路径（测试/自定义部署）。
func (h *Handler) SetUsageAnalyticsDBPath(path string) {
	if h == nil {
		return
	}
	h.usageAnalyticsDBPath = strings.TrimSpace(path)
}

// UsageAnalyticsDBPath 返回当前生效的分析库路径。
func (h *Handler) UsageAnalyticsDBPath() string {
	return h.usageAnalyticsStorePath()
}

// ensureUsageAnalyticsService 返回分析服务；不可用时写 503 错误信封。
func (h *Handler) ensureUsageAnalyticsService(w http.ResponseWriter) *usageanalytics.Service {
	service := h.attachUsageAnalyticsService()
	if service == nil {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    "usage_analytics_disabled",
				"message": "usage analytics not configured",
			},
		})
		return nil
	}
	return service
}
