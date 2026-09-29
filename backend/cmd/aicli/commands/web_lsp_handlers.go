package commands

import (
	"net/http"
	"strings"
	"time"

	runtimelsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

// ============================================================================
// /web/api/lsp/* —— aicli micro web client「LSP 观测」页签（lsp.observe.v1）。
//
// 方案：docs/plan/lsp-observability-and-analysis-plan-20260929.md §5。
// 数据源与 TUI /lsp status 完全同源（当前会话 tools.Manager 的 LSP 池），
// 不引入第二套进程事实；全部子路径只读，无写路径。
//
// 降级语义（对齐 web_analysis_handlers.go 的约定）：
//   - 池未启用 → 200 + enabled=false（不是错误；页面显示未启用与开启指引）；
//   - 读数/事件埋点未接入（M1/M2 之前）→ 200 + available=false + 稳定 reason，
//     字段齐全但**不伪造 0**；前端按「待埋点」渲染（方案 §5.4 验收第 3 条）。
//
// 路由（GET；子路径手动解析，与其他 /web/api 端点族同形）：
//
//	GET /web/api/lsp/status    池状态（enabled/root/config/servers，只读不启动）
//	GET /web/api/lsp/overview  使用读数（pending 时仅池计数为真实值，metrics=null）
//	GET /web/api/lsp/events    最近 lsp.* 事件（pending 时为空数组）
//	GET /web/api/lsp/baseline  §4.3 基线报告（跨会话；TTL 缓存，未采集输出 n/a）
// ============================================================================

const (
	// lspObserveSchemaVersion 面向前端的观测契约版本；埋点落地（M2）后只增字段。
	lspObserveSchemaVersion = "lsp.observe.v1"
	// chatWebLSPMethodCode 非 GET 请求。
	chatWebLSPMethodCode = "lsp_method_not_allowed"
	// chatWebLSPNotFoundCode 未知子路径。
	chatWebLSPNotFoundCode = "lsp_not_found"
	// lspInstrumentationPending 是「事件埋点未接入」的稳定原因码：前端据此显示
	// 「待埋点」，而不是把缺失渲染成 0。
	lspInstrumentationPending = "lsp_instrumentation_pending"
)

// HandleChatWebAPILSP 「LSP 观测」页签 HTTP 入口（挂载见 pprof.go）。
func HandleChatWebAPILSP(w http.ResponseWriter, r *http.Request) {
	if r == nil || r.Method != http.MethodGet {
		writeWebAPIJSON(w, http.StatusMethodNotAllowed,
			chatWebLSPErrorBody(chatWebLSPMethodCode, "method not allowed"))
		return
	}
	switch chatWebLSPSubPath(r.URL.Path) {
	case "status":
		writeWebAPIJSON(w, http.StatusOK, chatWebLSPStatusBodyFor(chatWebSession()))
	case "overview":
		writeWebAPIJSON(w, http.StatusOK, chatWebLSPOverviewBodyFor(chatWebSession()))
	case "events":
		writeWebAPIJSON(w, http.StatusOK, chatWebLSPEventsBodyFor(chatWebSession()))
	case "baseline":
		days, ok := chatWebLSPBaselineParseDays(r.URL.Query().Get("days"))
		if !ok {
			writeWebAPIJSON(w, http.StatusBadRequest,
				chatWebLSPErrorBody(chatWebLSPBaselineInvalidDaysCode, "days must be an integer in [0,3650]"))
			return
		}
		writeWebAPIJSON(w, http.StatusOK, chatWebLSPBaselineBodyForDays(days))
	default:
		writeWebAPIJSON(w, http.StatusNotFound,
			chatWebLSPErrorBody(chatWebLSPNotFoundCode, "unknown lsp subpath"))
	}
}

// chatWebLSPStatusBody 是 /status 的响应体：池状态（与 TUI /lsp status 同源）。
type chatWebLSPStatusBody struct {
	SchemaVersion string                        `json:"schema_version"`
	Enabled       bool                          `json:"enabled"`
	Root          string                        `json:"root,omitempty"`
	ConfigPath    string                        `json:"config_path,omitempty"`
	Diagnostics   *runtimelsp.DiagnosticsConfig `json:"diagnostics,omitempty"`
	Servers       []runtimelsp.ServerStatus     `json:"servers"`
	CapturedAt    time.Time                     `json:"captured_at"`
}

// chatWebLSPStatusBodyFor 读取当前会话的 LSP 池状态；池缺失/未启用时返回
// enabled=false 的稳定事实（servers 恒为数组，避免前端拿到 null 再判空）。
func chatWebLSPStatusBodyFor(session *ChatSession) chatWebLSPStatusBody {
	body := chatWebLSPStatusBody{
		SchemaVersion: lspObserveSchemaVersion,
		Servers:       []runtimelsp.ServerStatus{},
		CapturedAt:    time.Now().UTC(),
	}
	manager := chatLSPManager(session)
	if manager == nil || !manager.LSPEnabled() {
		return body
	}
	body.Enabled = true
	body.Root = strings.TrimSpace(manager.LSPRoot())
	body.ConfigPath = chatLSPConfigPath(session)
	if cfg, ok := manager.LSPDiagnosticsConfig(); ok {
		copied := cfg
		body.Diagnostics = &copied
	}
	if statuses := manager.LSPStatuses(); len(statuses) > 0 {
		body.Servers = statuses
	}
	return body
}

// chatWebLSPOverviewBody 是 /overview 的响应体。available=false 表示事件埋点
// 尚未接入：此时 Metrics 为 null、只返回池计数这类已经真实存在的读数；
// 池已启用且读数可用时 available=true，Metrics 是真实样本（可以为全零，
// 全零是「已接入但尚无请求」的事实，不是缺失）。
type chatWebLSPOverviewBody struct {
	SchemaVersion  string                      `json:"schema_version"`
	Available      bool                        `json:"available"`
	Reason         string                      `json:"reason,omitempty"`
	Enabled        bool                        `json:"enabled"`
	ServersTotal   int                         `json:"servers_total"`
	ServersByState map[string]int              `json:"servers_by_state"`
	Metrics        *runtimelsp.MetricsSnapshot `json:"metrics"`
	CapturedAt     time.Time                   `json:"captured_at"`
}

// chatWebLSPOverviewBodyFor 组装读数；事件埋点接入后（M2）把 available 翻转为
// true 并填充 Metrics（ComputeLSPMetrics 的输出，口径见方案 §3.3）。
func chatWebLSPOverviewBodyFor(session *ChatSession) chatWebLSPOverviewBody {
	body := chatWebLSPOverviewBody{
		SchemaVersion:  lspObserveSchemaVersion,
		Available:      false,
		Reason:         lspInstrumentationPending,
		ServersByState: map[string]int{},
		CapturedAt:     time.Now().UTC(),
	}
	manager := chatLSPManager(session)
	if manager == nil || !manager.LSPEnabled() {
		return body
	}
	body.Enabled = true
	statuses := manager.LSPStatuses()
	body.ServersTotal = len(statuses)
	for _, status := range statuses {
		body.ServersByState[string(status.State)]++
	}
	if snapshot, ok := manager.LSPMetrics(); ok {
		body.Available = true
		body.Reason = ""
		body.Metrics = &snapshot
	}
	return body
}

// chatWebLSPEventItem 是 /events 的单条请求事实（字段与 lsp.RequestRecord 对齐）。
type chatWebLSPEventItem struct {
	Timestamp     string `json:"timestamp"`
	Trigger       string `json:"trigger"`
	Server        string `json:"server,omitempty"`
	Outcome       string `json:"outcome"`
	DurationMS    int64  `json:"duration_ms"`
	DiagCount     int    `json:"diag_count"`
	AppendedBytes int    `json:"appended_bytes"`
}

// chatWebLSPEventsBody 是 /events 的响应体；埋点前事件恒为空数组（不伪造）。
type chatWebLSPEventsBody struct {
	SchemaVersion string                `json:"schema_version"`
	Available     bool                  `json:"available"`
	Reason        string                `json:"reason,omitempty"`
	Events        []chatWebLSPEventItem `json:"events"`
	NextCursor    int64                 `json:"next_cursor"`
	CapturedAt    time.Time             `json:"captured_at"`
}

// chatWebLSPEventsBodyFor 读取池内最近请求明细（最新在前）；池缺失/未启用时
// 保持 available=false + 空数组的诚实降级。
func chatWebLSPEventsBodyFor(session *ChatSession) chatWebLSPEventsBody {
	body := chatWebLSPEventsBody{
		SchemaVersion: lspObserveSchemaVersion,
		Available:     false,
		Reason:        lspInstrumentationPending,
		Events:        []chatWebLSPEventItem{},
		CapturedAt:    time.Now().UTC(),
	}
	manager := chatLSPManager(session)
	if manager == nil || !manager.LSPEnabled() {
		return body
	}
	snapshot, ok := manager.LSPMetrics()
	if !ok {
		return body
	}
	body.Available = true
	body.Reason = ""
	for _, record := range snapshot.RecentRequests {
		body.Events = append(body.Events, chatWebLSPEventItem{
			Timestamp:     record.Time.UTC().Format(time.RFC3339),
			Trigger:       record.Trigger,
			Server:        record.Server,
			Outcome:       record.Outcome,
			DurationMS:    record.DurationMS,
			DiagCount:     record.DiagCount,
			AppendedBytes: record.AppendedBytes,
		})
	}
	return body
}

// chatWebLSPSubPath 返回 /web/api/lsp 之后的子路径（无首尾斜杠）。
func chatWebLSPSubPath(path string) string {
	return strings.Trim(strings.TrimPrefix(path, ChatWebAPILSPPath), "/")
}

// chatWebLSPErrorBody 稳定错误 envelope（与其它 /web/api 端点族同形）。
func chatWebLSPErrorBody(code, message string) map[string]interface{} {
	return map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}
}
