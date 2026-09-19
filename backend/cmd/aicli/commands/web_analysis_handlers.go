package commands

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/observability"
	usageanalytics "github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// ============================================================================
// /web/api/analysis/* —— aicli micro web client「分析」页签（方案 §9.1 批次 7.1）。
//
// 数据源与 TUI /usage tools|subagents|errors（chat_usage_tools.go）同源：
// 进程内 usageanalytics.Service（usage_analytics.sqlite），不 import httpapi、
// 不发 HTTP。响应体直接序列化查询层结构体（ToolStatsResult / SubagentStatsResult /
// ErrorPatternsResult），字段名与 runtime-server /api/runtime/analytics/* 完全一致
// ——「HTTP 契约只定义一次、三端各自消费同一契约」（§9 引言）。本文件只做 aicli
// 侧装配（当前会话解析 → 服务挂载 → 缺省 session_id 注入 → 参数归一 → 委托查询层），
// 不重复实现查询/投影。
//
// 路由（GET；子路径手动解析，与缓存端点同形）：
//
//	GET /web/api/analysis/status     采集健康（attached/degraded/db_path/table_counts）
//	GET /web/api/analysis/tools      工具维度聚合（ToolStatsResult）
//	GET /web/api/analysis/subagents  子代理维度聚合（SubagentStatsResult）
//	GET /web/api/analysis/errors     失败模式 Top-N（ErrorPatternsResult）
//	GET /web/api/analysis/tool_efficiency  工具效率 / Artifact 链路快照
//	（observability.ToolEfficiencySnapshot，与 runtime /api/runtime/status 的
//	runtime.tool_efficiency 块同一结构体、同一 JSON tag —— 契约只定义一次）。
//
// 降级（与缓存端点同形，§9.1）：
//   - 无活动会话 / 分析服务未挂载 → 503 + 稳定码 analytics_disabled；
//   - 已挂载但空库 → 200 + 空数组（前端渲染「暂无数据」）；
//   - /status 永不因服务缺失而失败：attached=false + degraded=true 是稳定降级
//     事实（与 runtime /status 的 usage_analytics 块同形），前端据此显示健康条。
// ============================================================================

const (
	// chatWebAnalysisDisabledCode 分析数据不可用稳定错误码
	//（对齐 cacheanalytics.ErrDisabled → cache_analytics_disabled 风格）。
	chatWebAnalysisDisabledCode = "analytics_disabled"
	// chatWebAnalysisInvalidCode 过滤参数非法（仅时间窗；limit/top 拼写失败归一为
	// 默认上限，与 runtime 端点同语义）。
	chatWebAnalysisInvalidCode = "analytics_invalid_request"
	// chatWebAnalysisInternalCode 查询层失败。
	chatWebAnalysisInternalCode = "analytics_internal"
	// chatWebAnalysisNotFoundCode 未知子路径。
	chatWebAnalysisNotFoundCode = "analytics_not_found"
	// chatWebAnalysisMethodCode 非 GET 请求。
	chatWebAnalysisMethodCode = "analytics_method_not_allowed"
	// chatWebAnalysisScopeAll 显式全局查询：不注入当前会话过滤
	//（页签「会话 / 全局」切换；与 runtime 端点缺省全局语义一致）。
	chatWebAnalysisScopeAll = "all"
)

// HandleChatWebAPIAnalysis 「分析」页签 HTTP 入口（挂载见 pprof.go）。
func HandleChatWebAPIAnalysis(w http.ResponseWriter, r *http.Request) {
	if r == nil || r.Method != http.MethodGet {
		writeWebAPIJSON(w, http.StatusMethodNotAllowed,
			chatWebAnalysisErrorBody(chatWebAnalysisMethodCode, "method not allowed"))
		return
	}
	session := chatWebSession()
	service := chatWebAnalysisService(session)

	// 健康端点先于服务检查：服务缺失不是错误，而是 attached=false 的降级事实。
	if chatWebAnalysisSubPath(r.URL.Path) == "status" {
		writeWebAPIJSON(w, http.StatusOK, chatWebAnalysisStatusPayload(session, service))
		return
	}
	// 工具效率快照来自进程内 observability.GlobalMetrics（工具环遥测），不依赖
	// usageanalytics 服务：与 /status 一样先于 service==nil 检查返回。计数器是
	// 进程级聚合，scope 过滤不适用（返回全量快照，前端不做会话切片）。
	if chatWebAnalysisSubPath(r.URL.Path) == "tool_efficiency" {
		writeWebAPIJSON(w, http.StatusOK, observability.SnapshotToolEfficiency())
		return
	}
	if service == nil {
		writeWebAPIJSON(w, http.StatusServiceUnavailable,
			chatWebAnalysisErrorBody(chatWebAnalysisDisabledCode, "usage analytics service unavailable"))
		return
	}

	values := chatWebAnalysisValues(r, session)
	switch chatWebAnalysisSubPath(r.URL.Path) {
	case "tools":
		query, err := chatWebAnalysisToolStatsQuery(values)
		if err != nil {
			writeWebAPIJSON(w, http.StatusBadRequest, chatWebAnalysisErrorBody(chatWebAnalysisInvalidCode, err.Error()))
			return
		}
		result, err := service.ToolStats(query)
		if err != nil {
			writeWebAPIJSON(w, http.StatusInternalServerError,
				chatWebAnalysisErrorBody(chatWebAnalysisInternalCode, "usage analytics query failed"))
			return
		}
		writeWebAPIJSON(w, http.StatusOK, result)
	case "subagents":
		query, err := chatWebAnalysisSubagentStatsQuery(values)
		if err != nil {
			writeWebAPIJSON(w, http.StatusBadRequest, chatWebAnalysisErrorBody(chatWebAnalysisInvalidCode, err.Error()))
			return
		}
		result, err := service.SubagentStats(query)
		if err != nil {
			writeWebAPIJSON(w, http.StatusInternalServerError,
				chatWebAnalysisErrorBody(chatWebAnalysisInternalCode, "usage analytics query failed"))
			return
		}
		writeWebAPIJSON(w, http.StatusOK, result)
	case "errors":
		query, err := chatWebAnalysisErrorPatternsQuery(values)
		if err != nil {
			writeWebAPIJSON(w, http.StatusBadRequest, chatWebAnalysisErrorBody(chatWebAnalysisInvalidCode, err.Error()))
			return
		}
		result, err := service.ErrorPatterns(query)
		if err != nil {
			writeWebAPIJSON(w, http.StatusInternalServerError,
				chatWebAnalysisErrorBody(chatWebAnalysisInternalCode, "usage analytics query failed"))
			return
		}
		writeWebAPIJSON(w, http.StatusOK, result)
	default:
		writeWebAPIJSON(w, http.StatusNotFound,
			chatWebAnalysisErrorBody(chatWebAnalysisNotFoundCode, "unknown analysis endpoint"))
	}
}

// chatWebAnalysisService 返回当前会话的本地分析服务（未挂载/构建失败 → nil）。
// 与 /web/api/cache/* 复用同一 ensureLocalUsageService 落点。
func chatWebAnalysisService(session *ChatSession) *usageanalytics.Service {
	if session == nil {
		return nil
	}
	return ensureLocalUsageService(session.LocalRuntimeHost)
}

// chatWebAnalysisSubPath 返回 /web/api/analysis 之后的子路径（无首尾斜杠）。
func chatWebAnalysisSubPath(path string) string {
	return strings.Trim(strings.TrimPrefix(path, ChatWebAPIAnalysisPath), "/")
}

// chatWebAnalysisStatusPayload 采集健康块，键与 runtime /status 的
// usage_analytics 块一致（attached/db_path/ingested_total/conflict_total/
// last_ingest_at/table_counts/degraded）；attached=false 时字段仍然齐全。
func chatWebAnalysisStatusPayload(session *ChatSession, service *usageanalytics.Service) map[string]interface{} {
	payload := map[string]interface{}{
		"schema_version": usageanalytics.SchemaVersion,
		"generated_at":   time.Now().UTC(),
		"session_id":     currentRuntimeSessionID(session),
		"scope":          "session",
		"attached":       false,
		"degraded":       true,
		"db_path":        "",
		"ingested_total": int64(0),
		"conflict_total": int64(0),
		"last_ingest_at": nil,
		"table_counts":   usageanalytics.HealthTableCounts{},
	}
	health := usageanalytics.AnalyticsHealth{Degraded: true}
	if service != nil {
		health = service.AnalyticsHealth()
		payload["attached"] = true
		payload["degraded"] = health.Degraded
		payload["db_path"] = service.DBPath()
	}
	payload["ingested_total"] = health.IngestedTotal
	payload["conflict_total"] = health.ConflictTotal
	payload["last_ingest_at"] = health.LastIngestAt
	payload["table_counts"] = health.TableCounts
	return payload
}

// chatWebAnalysisValues 归一查询参数：缺省注入当前会话 id；?scope=all 显式全局
// （对齐缓存端点"缺省当前会话"的先例）。
func chatWebAnalysisValues(r *http.Request, session *ChatSession) url.Values {
	if r == nil || r.URL == nil {
		return url.Values{}
	}
	values := r.URL.Query()
	if strings.EqualFold(strings.TrimSpace(values.Get("scope")), chatWebAnalysisScopeAll) {
		values.Del("session_id")
		values.Del("session")
		return values
	}
	if chatWebAnalysisFirstValue(values, "session_id", "session") == "" {
		if sessionID := currentRuntimeSessionID(session); sessionID != "" {
			values.Set("session_id", sessionID)
		}
	}
	return values
}

// chatWebAnalysisToolStatsQuery 解析 /tools 参数
// （session_id/session、tool/tool_name、outcome、from/to、limit）。
func chatWebAnalysisToolStatsQuery(values url.Values) (usageanalytics.ToolStatsQuery, error) {
	query := usageanalytics.ToolStatsQuery{}
	from, to, err := chatWebAnalysisTimeWindow(values)
	if err != nil {
		return query, err
	}
	query.From = from
	query.To = to
	query.SessionID = chatWebAnalysisFirstValue(values, "session_id", "session")
	query.ToolName = chatWebAnalysisFirstValue(values, "tool", "tool_name")
	query.Outcome = strings.TrimSpace(values.Get("outcome"))
	query.Limit = chatWebAnalysisLimit(values.Get("limit"))
	return query, nil
}

// chatWebAnalysisSubagentStatsQuery 解析 /subagents 参数
// （session_id/session、failure_category、failed_only、from/to、limit）。
func chatWebAnalysisSubagentStatsQuery(values url.Values) (usageanalytics.SubagentStatsQuery, error) {
	query := usageanalytics.SubagentStatsQuery{}
	from, to, err := chatWebAnalysisTimeWindow(values)
	if err != nil {
		return query, err
	}
	query.From = from
	query.To = to
	query.SessionID = chatWebAnalysisFirstValue(values, "session_id", "session")
	query.FailureCategory = strings.TrimSpace(values.Get("failure_category"))
	query.FailedOnly = chatWebAnalysisBool(values.Get("failed_only"))
	query.Limit = chatWebAnalysisLimit(values.Get("limit"))
	return query, nil
}

// chatWebAnalysisErrorPatternsQuery 解析 /errors 参数
// （session_id/session、source、top、from/to）。
func chatWebAnalysisErrorPatternsQuery(values url.Values) (usageanalytics.ErrorPatternsQuery, error) {
	query := usageanalytics.ErrorPatternsQuery{}
	from, to, err := chatWebAnalysisTimeWindow(values)
	if err != nil {
		return query, err
	}
	query.From = from
	query.To = to
	query.SessionID = chatWebAnalysisFirstValue(values, "session_id", "session")
	query.Source = strings.TrimSpace(values.Get("source"))
	query.Top = chatWebAnalysisLimit(values.Get("top"))
	return query, nil
}

// chatWebAnalysisFirstValue 返回第一个非空参数值（兼容 alias，如 session_id/session）。
func chatWebAnalysisFirstValue(values url.Values, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(values.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

// chatWebAnalysisTimeWindow 解析 from/to 时间窗（RFC3339 / 2006-01-02 /
// 2006-01-02 15:04:05；to 为纯日期时按"包含整天"处理）——与 runtime
// /api/runtime/analytics/* 的时间窗语义一致。
func chatWebAnalysisTimeWindow(values url.Values) (time.Time, time.Time, error) {
	from, err := chatWebAnalysisOptionalTime(values.Get("from"))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid from value")
	}
	toRaw := strings.TrimSpace(values.Get("to"))
	to, err := chatWebAnalysisOptionalTime(toRaw)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid to value")
	}
	if len(toRaw) == len("2006-01-02") && !to.IsZero() {
		to = to.AddDate(0, 0, 1)
	}
	return from, to, nil
}

func chatWebAnalysisOptionalTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed, nil
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed, nil
	}
	if parsed, err := time.ParseInLocation("2006-01-02", raw, time.Local); err == nil {
		return parsed, nil
	}
	if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", raw, time.Local); err == nil {
		return parsed, nil
	}
	return time.Time{}, fmt.Errorf("invalid time value")
}

// chatWebAnalysisLimit 解析 limit/top：非法值或负值归一为 0（由查询层取默认上限），
// 只读端点不因过滤参数拼写失败而 400。
func chatWebAnalysisLimit(raw string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
}

// chatWebAnalysisBool 解析布尔过滤参数：空值或非法值归一为 false。
func chatWebAnalysisBool(raw string) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	return err == nil && value
}

// chatWebAnalysisErrorBody 稳定错误 envelope（与 cache_analytics_disabled 同形）。
func chatWebAnalysisErrorBody(code, message string) map[string]interface{} {
	return map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}
}
