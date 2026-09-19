package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
	usageanalytics "github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// ---------------------------------------------------------------------------
// /web/api/analysis/* —— 「分析」页签端点（runtime.analytics.v1 批次 7.1）。
//
// 直调 handler（不经 mux），照 web_cache_handlers_test.go 的夹具模式：
// 真实 EventBus + 临时 SQLite runtime store + 本地 usageanalytics 服务，
// 事件经 collector 落库后由 handler 查询层读回，字段名锁定 Go struct JSON tag。
// 覆盖：成功 / 空库 / 未挂载（analytics_disabled）三条路径 + 范围与会话过滤。
// ---------------------------------------------------------------------------

// analysisTestClockNsec 递增时间戳：bus.Publish 自动填充真实 time.Now()，
// Windows 时钟精度下同刻事件会破坏排序，测试事件显式传递增 Timestamp。
var analysisTestClockNsec int64

func nextAnalysisTestTimestamp() time.Time {
	return time.Date(2026, 3, 1, 0, 0, 0, int(atomic.AddInt64(&analysisTestClockNsec, 1000)), time.UTC)
}

// newAnalysisTestSession 构造带 EventBus + InMemoryStorage + 临时 SQLite
// runtime store 的测试会话，并挂载本地 usageanalytics 服务（collector 先订阅，
// 之后发布的事件才会落库）。分析库由 runtime store 同目录派生，保证测试绝不
// 写入用户数据目录 ~/.aicli。
func newAnalysisTestSession(t *testing.T) (*ChatSession, *runtimeevents.Bus) {
	t.Helper()
	storage := runtimechat.NewInMemoryStorage()
	manager := runtimechat.NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)

	ctx := context.Background()
	runtimeSession, err := manager.Create(ctx, "test-user")
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	if err := storage.Save(ctx, runtimeSession); err != nil {
		t.Fatalf("storage.Save: %v", err)
	}

	bus := runtimeevents.NewBus()
	runtimeStore, err := runtimechat.NewSQLiteRuntimeStore(&runtimechat.RuntimeStoreConfig{
		Path: filepath.Join(t.TempDir(), "session_runtime.sqlite"),
	})
	if err != nil {
		t.Fatalf("NewSQLiteRuntimeStore: %v", err)
	}
	t.Cleanup(func() { _ = runtimeStore.Close() })

	session := newWebTestSession()
	session.RuntimeSession = runtimeSession
	session.LocalRuntimeHost = &localChatRuntimeHost{
		EventBus:     bus,
		RuntimeStore: runtimeStore,
		SessionStore: storage,
	}
	withWebTestSession(t, session)
	if ensureLocalUsageService(session.LocalRuntimeHost) == nil {
		t.Fatal("expected local usage analytics service to attach")
	}
	// 先于 t.TempDir() 清理关闭分析库（LIFO：注册晚于 TempDir，先执行），
	// 否则 Windows 上 sqlite 句柄会阻塞临时目录删除。
	t.Cleanup(func() {
		if service := session.LocalRuntimeHost.usageSvc; service != nil {
			service.Close()
		}
	})
	return session, bus
}

// publishAnalysisToolCall 发布 tool.requested + tool.completed（默认成功；
// extra 覆盖终态字段，如 ok=false/outcome=failed/error_code）。
func publishAnalysisToolCall(t *testing.T, bus *runtimeevents.Bus, sessionID, callID, toolName string, extra map[string]interface{}) {
	t.Helper()
	bus.Publish(runtimeevents.Event{
		Type:      usageanalytics.EventToolRequested,
		SessionID: sessionID,
		Payload: map[string]interface{}{
			"tool_call_id": callID,
			"logical_tool": toolName,
			"step":         1,
			"trace_id":     "trace-" + callID,
			"turn_id":      "turn-" + callID,
		},
		Timestamp: nextAnalysisTestTimestamp(),
	})
	completed := map[string]interface{}{
		"tool_call_id": callID,
		"logical_tool": toolName,
		"step":         1,
		"trace_id":     "trace-" + callID,
		"turn_id":      "turn-" + callID,
		"ok":           true,
		"outcome":      "success",
		"duration_ms":  30,
	}
	for key, value := range extra {
		completed[key] = value
	}
	bus.Publish(runtimeevents.Event{
		Type:      usageanalytics.EventToolCompleted,
		SessionID: sessionID,
		Payload:   completed,
		Timestamp: nextAnalysisTestTimestamp(),
	})
}

// publishAnalysisSubagent 发布 subagent.completed（parent_session_id 决定归属会话）。
func publishAnalysisSubagent(t *testing.T, bus *runtimeevents.Bus, sessionID, agentID string, extra map[string]interface{}) {
	t.Helper()
	payload := map[string]interface{}{
		"agent_id":          agentID,
		"session_id":        agentID,
		"parent_session_id": sessionID,
		"role":              "researcher",
		"attempt":           1,
		"duration_ms":       500,
	}
	for key, value := range extra {
		payload[key] = value
	}
	bus.Publish(runtimeevents.Event{
		Type:      usageanalytics.EventSubagentCompleted,
		SessionID: sessionID,
		Payload:   payload,
		Timestamp: nextAnalysisTestTimestamp(),
	})
}

func analysisGetJSON(t *testing.T, path string) (int, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPIAnalysis(rec, req)
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal %s: %v; body: %s", path, err, rec.Body.String())
	}
	return rec.Code, body
}

func analysisErrorCode(t *testing.T, body map[string]interface{}) string {
	t.Helper()
	errObj, ok := body["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected error envelope, got: %v", body)
	}
	code, _ := errObj["code"].(string)
	return code
}

func analysisArray(t *testing.T, body map[string]interface{}, key string) []interface{} {
	t.Helper()
	items, ok := body[key].([]interface{})
	if !ok {
		t.Fatalf("%s is not an array: %v", key, body[key])
	}
	return items
}

// ---------------------------------------------------------------------------
// 降级路径：无会话 / 会话未挂载分析服务 → 503 + analytics_disabled
// ---------------------------------------------------------------------------

func TestHandleChatWebAPIAnalysis_NoSessionDisabled(t *testing.T) {
	withWebTestSession(t, nil)

	for _, sub := range []string{"/tools", "/subagents", "/errors"} {
		code, body := analysisGetJSON(t, ChatWebAPIAnalysisPath+sub)
		if code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503", sub, code)
		}
		if got := analysisErrorCode(t, body); got != "analytics_disabled" {
			t.Fatalf("%s error code = %q, want analytics_disabled", sub, got)
		}
	}

	// /status 不因服务缺失失败：attached=false + degraded=true 是稳定降级事实
	// （前端据此显示顶部健康条）。
	code, body := analysisGetJSON(t, ChatWebAPIAnalysisPath+"/status")
	if code != http.StatusOK {
		t.Fatalf("status endpoint code = %d, want 200（健康端点不应因未挂载失败）", code)
	}
	if body["attached"] != false || body["degraded"] != true {
		t.Fatalf("status = %v, want attached=false degraded=true", body)
	}

	// /tool_efficiency 与 /status 一样先于 service==nil 检查：快照来自进程内
	// observability.GlobalMetrics（工具环遥测），无分析服务时也返回 200 + 完整
	// JSON 块（captured_at / artifact_flow / inefficiency_flags 契约字段齐全）。
	code, body = analysisGetJSON(t, ChatWebAPIAnalysisPath+"/tool_efficiency")
	if code != http.StatusOK {
		t.Fatalf("tool_efficiency code = %d, want 200", code)
	}
	if _, ok := body["captured_at"]; !ok {
		t.Fatalf("tool_efficiency missing captured_at: %v", body)
	}
	flow, ok := body["artifact_flow"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool_efficiency artifact_flow missing: %v", body)
	}
	for _, key := range []string{"archives", "truncations", "pointer_notice", "deref"} {
		if _, ok := flow[key]; !ok {
			t.Fatalf("artifact_flow missing %s: %v", key, flow)
		}
	}
	if _, ok := body["inefficiency_flags"]; !ok {
		t.Fatalf("tool_efficiency missing inefficiency_flags: %v", body)
	}

	// /status 的 schema_version / table_counts 断言保持在其自身响应上。
	_, body = analysisGetJSON(t, ChatWebAPIAnalysisPath+"/status")
	if body["schema_version"] != usageanalytics.SchemaVersion {
		t.Fatalf("schema_version = %v, want %s", body["schema_version"], usageanalytics.SchemaVersion)
	}
	if _, ok := body["table_counts"].(map[string]interface{}); !ok {
		t.Fatalf("table_counts missing: %v", body)
	}
}

func TestHandleChatWebAPIAnalysis_SessionWithoutHostDisabled(t *testing.T) {
	// 会话存在但 LocalRuntimeHost 缺失（未初始化本地 runtime）→ 服务不可用。
	withWebTestSession(t, newWebTestSession())

	code, body := analysisGetJSON(t, ChatWebAPIAnalysisPath+"/tools")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", code)
	}
	if got := analysisErrorCode(t, body); got != "analytics_disabled" {
		t.Fatalf("error code = %q, want analytics_disabled", got)
	}
}

// ---------------------------------------------------------------------------
// 空库路径：已挂载、无数据 → 200 + 空数组（前端渲染「暂无数据」）
// ---------------------------------------------------------------------------

func TestHandleChatWebAPIAnalysis_EmptyDatabase(t *testing.T) {
	newAnalysisTestSession(t)

	code, body := analysisGetJSON(t, ChatWebAPIAnalysisPath+"/tools")
	if code != http.StatusOK {
		t.Fatalf("tools status = %d, want 200; body: %v", code, body)
	}
	if items := analysisArray(t, body, "tools"); len(items) != 0 {
		t.Fatalf("tools = %v, want empty array", body["tools"])
	}

	code, body = analysisGetJSON(t, ChatWebAPIAnalysisPath+"/subagents")
	if code != http.StatusOK {
		t.Fatalf("subagents status = %d, want 200", code)
	}
	if items := analysisArray(t, body, "subagents"); len(items) != 0 {
		t.Fatalf("subagents = %v, want empty array", body["subagents"])
	}
	summary, ok := body["summary"].(map[string]interface{})
	if !ok {
		t.Fatalf("summary missing: %v", body)
	}
	if summary["total"] != float64(0) || summary["failed"] != float64(0) {
		t.Fatalf("summary = %v, want zero counts", summary)
	}

	code, body = analysisGetJSON(t, ChatWebAPIAnalysisPath+"/errors")
	if code != http.StatusOK {
		t.Fatalf("errors status = %d, want 200", code)
	}
	if items := analysisArray(t, body, "patterns"); len(items) != 0 {
		t.Fatalf("patterns = %v, want empty array", body["patterns"])
	}

	// 空库 = 已挂载但 0 行：attached=true、degraded=false、计数为 0
	//（「暂无数据」由前端按空数组渲染，不是错误分支）。
	code, body = analysisGetJSON(t, ChatWebAPIAnalysisPath+"/status")
	if code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", code)
	}
	if body["attached"] != true || body["degraded"] != false {
		t.Fatalf("status = %v, want attached=true degraded=false", body)
	}
	if body["ingested_total"] != float64(0) {
		t.Fatalf("ingested_total = %v, want 0", body["ingested_total"])
	}
	if path, _ := body["db_path"].(string); path == "" {
		t.Fatal("db_path should expose the analytics database path")
	}
}

// ---------------------------------------------------------------------------
// 成功路径：事件经 collector 落库后按契约字段读回
// ---------------------------------------------------------------------------

func TestHandleChatWebAPIAnalysis_ToolsSubagentsErrors(t *testing.T) {
	session, bus := newAnalysisTestSession(t)
	sessionID := session.RuntimeSession.ID

	publishAnalysisToolCall(t, bus, sessionID, "call-view", "view", nil)
	publishAnalysisToolCall(t, bus, sessionID, "call-shell", "shell", map[string]interface{}{
		"ok": false, "outcome": "failed", "error_code": "TOOL_TIMEOUT", "retryable": true, "duration_ms": 120,
	})
	publishAnalysisSubagent(t, bus, sessionID, "child-1", map[string]interface{}{
		"success": false, "error_code": "UPSTREAM_RATE_LIMITED",
	})

	// --- /tools ---
	code, body := analysisGetJSON(t, ChatWebAPIAnalysisPath+"/tools")
	if code != http.StatusOK {
		t.Fatalf("tools status = %d, want 200; body: %v", code, body)
	}
	tools := analysisArray(t, body, "tools")
	if len(tools) != 2 {
		t.Fatalf("tools = %v, want 2 rows", body["tools"])
	}
	first, ok := tools[0].(map[string]interface{})
	if !ok {
		t.Fatalf("tool row is not an object: %v", tools[0])
	}
	// 排序：calls 相同 → tool_name 升序，shell 在 view 之前。
	if first["tool_name"] != "shell" {
		t.Fatalf("first tool = %v, want shell（calls DESC, tool_name ASC）", first["tool_name"])
	}
	for _, key := range []string{
		"tool_name", "calls", "failures", "failure_rate", "empty_results", "retried_calls",
		"average_duration_ms", "min_duration_ms", "max_duration_ms", "p50_duration_ms", "p95_duration_ms",
	} {
		if _, exists := first[key]; !exists {
			t.Fatalf("tool row missing contract field %q: %v", key, first)
		}
	}
	if first["calls"] != float64(1) || first["failures"] != float64(1) || first["failure_rate"] != float64(1) {
		t.Fatalf("shell row = %v, want calls=1 failures=1 failure_rate=1", first)
	}
	if first["retried_calls"] != float64(1) || first["p95_duration_ms"] != float64(120) {
		t.Fatalf("shell retry/duration = %v, want retried_calls=1 p95=120", first)
	}
	if first["min_duration_ms"] != float64(120) || first["max_duration_ms"] != float64(120) {
		t.Fatalf("shell min/max duration = %v, want 120/120", first)
	}
	if top, ok := first["error_top"].([]interface{}); !ok || len(top) == 0 {
		t.Fatalf("shell error_top = %v, want TOOL_TIMEOUT pattern", first["error_top"])
	}
	totals, ok := body["totals"].(map[string]interface{})
	if !ok {
		t.Fatalf("totals missing: %v", body)
	}
	if totals["calls"] != float64(2) || totals["failures"] != float64(1) || totals["tool_name"] != "all" {
		t.Fatalf("totals = %v, want calls=2 failures=1 tool_name=all", totals)
	}
	if totals["min_duration_ms"] != float64(30) || totals["max_duration_ms"] != float64(120) || totals["average_duration_ms"] != float64(75) {
		t.Fatalf("totals duration = %v, want min=30 max=120 avg=75", totals)
	}

	// --- /subagents ---
	code, body = analysisGetJSON(t, ChatWebAPIAnalysisPath+"/subagents")
	if code != http.StatusOK {
		t.Fatalf("subagents status = %d, want 200; body: %v", code, body)
	}
	summary, ok := body["summary"].(map[string]interface{})
	if !ok {
		t.Fatalf("summary missing: %v", body)
	}
	if summary["total"] != float64(1) || summary["failed"] != float64(1) || summary["succeeded"] != float64(0) {
		t.Fatalf("summary = %v, want total=1 failed=1 succeeded=0", summary)
	}
	if summary["failure_rate"] != float64(1) {
		t.Fatalf("failure_rate = %v, want 1", summary["failure_rate"])
	}
	categories, ok := summary["failure_categories"].(map[string]interface{})
	if !ok {
		t.Fatalf("failure_categories missing: %v", summary)
	}
	if categories["rate_limited"] != float64(1) {
		t.Fatalf("failure_categories = %v, want rate_limited=1", categories)
	}
	subagents := analysisArray(t, body, "subagents")
	if len(subagents) != 1 {
		t.Fatalf("subagents = %v, want 1 row", body["subagents"])
	}
	row := subagents[0].(map[string]interface{})
	if row["subagent_id"] != "child-1" || row["success"] != false || row["failure_category"] != "rate_limited" {
		t.Fatalf("subagent row = %v, want child-1/failed/rate_limited", row)
	}
	for _, key := range []string{"parent_session_id", "completion_reason", "attempt", "max_attempts", "duration_ms", "usage_total_tokens"} {
		if _, exists := row[key]; !exists {
			t.Fatalf("subagent row missing contract field %q: %v", key, row)
		}
	}

	// --- /errors ---
	code, body = analysisGetJSON(t, ChatWebAPIAnalysisPath+"/errors")
	if code != http.StatusOK {
		t.Fatalf("errors status = %d, want 200; body: %v", code, body)
	}
	patterns := analysisArray(t, body, "patterns")
	if len(patterns) == 0 {
		t.Fatalf("patterns = %v, want non-empty", body["patterns"])
	}
	var sawToolTimeout, sawSubagentRateLimited bool
	for _, item := range patterns {
		pattern, _ := item.(map[string]interface{})
		switch pattern["error_code"] {
		case "TOOL_TIMEOUT":
			sawToolTimeout = pattern["source"] == "tools" && pattern["failure_category"] == "timeout" && pattern["count"] == float64(1)
		case "UPSTREAM_RATE_LIMITED":
			sawSubagentRateLimited = pattern["source"] == "subagents" && pattern["failure_category"] == "rate_limited"
		}
	}
	if !sawToolTimeout || !sawSubagentRateLimited {
		t.Fatalf("patterns = %v, want TOOL_TIMEOUT(tools/timeout) + UPSTREAM_RATE_LIMITED(subagents/rate_limited)", patterns)
	}

	// --- /status：已挂载且已入库 ---
	code, body = analysisGetJSON(t, ChatWebAPIAnalysisPath+"/status")
	if code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", code)
	}
	if body["attached"] != true || body["degraded"] != false {
		t.Fatalf("status = %v, want attached=true degraded=false", body)
	}
	if total, _ := body["ingested_total"].(float64); total <= 0 {
		t.Fatalf("ingested_total = %v, want > 0", body["ingested_total"])
	}
	counts, ok := body["table_counts"].(map[string]interface{})
	if !ok || counts["tool_calls"] != float64(2) {
		t.Fatalf("table_counts = %v, want tool_calls=2", body["table_counts"])
	}
}

// ---------------------------------------------------------------------------
// 范围开关：缺省注入当前会话；scope=all 全库；显式 session_id 覆盖
// ---------------------------------------------------------------------------

func TestHandleChatWebAPIAnalysis_ScopeAndSessionFilter(t *testing.T) {
	session, bus := newAnalysisTestSession(t)
	sessionID := session.RuntimeSession.ID

	publishAnalysisToolCall(t, bus, sessionID, "call-current", "view", nil)
	publishAnalysisToolCall(t, bus, "session-other", "call-other", "shell", nil)

	// 缺省：后端注入当前会话 → 只有当前会话的 view。
	_, body := analysisGetJSON(t, ChatWebAPIAnalysisPath+"/tools")
	tools := analysisArray(t, body, "tools")
	if len(tools) != 1 || tools[0].(map[string]interface{})["tool_name"] != "view" {
		t.Fatalf("default scope tools = %v, want only view（当前会话）", body["tools"])
	}

	// scope=all：不注入会话过滤（显式全局）→ 两个会话的聚合都在。
	_, body = analysisGetJSON(t, ChatWebAPIAnalysisPath+"/tools?scope=all")
	if tools = analysisArray(t, body, "tools"); len(tools) != 2 {
		t.Fatalf("scope=all tools = %v, want 2 rows（全局）", body["tools"])
	}

	// 显式 session_id 优先于当前会话（保留 runtime 端点语义）。
	_, body = analysisGetJSON(t, ChatWebAPIAnalysisPath+"/tools?session_id=session-other")
	tools = analysisArray(t, body, "tools")
	if len(tools) != 1 || tools[0].(map[string]interface{})["tool_name"] != "shell" {
		t.Fatalf("explicit session tools = %v, want only shell", body["tools"])
	}

	// 子代理表同规则：全局包含其它父会话的子代理。
	publishAnalysisSubagent(t, bus, sessionID, "child-current", map[string]interface{}{"success": true})
	publishAnalysisSubagent(t, bus, "session-other", "child-other", map[string]interface{}{"success": true})
	_, body = analysisGetJSON(t, ChatWebAPIAnalysisPath+"/subagents")
	if rows := analysisArray(t, body, "subagents"); len(rows) != 1 {
		t.Fatalf("default scope subagents = %v, want 1 row", body["subagents"])
	}
	_, body = analysisGetJSON(t, ChatWebAPIAnalysisPath+"/subagents?scope=all")
	if rows := analysisArray(t, body, "subagents"); len(rows) != 2 {
		t.Fatalf("scope=all subagents = %v, want 2 rows", body["subagents"])
	}
}

// ---------------------------------------------------------------------------
// 参数与路由边界：非法时间窗 400、limit 拼写失败归一、未知子路径 404、非 GET 405
// ---------------------------------------------------------------------------

func TestHandleChatWebAPIAnalysis_RequestBoundaries(t *testing.T) {
	newAnalysisTestSession(t)

	code, body := analysisGetJSON(t, ChatWebAPIAnalysisPath+"/tools?from=not-a-time")
	if code != http.StatusBadRequest {
		t.Fatalf("invalid from status = %d, want 400", code)
	}
	if got := analysisErrorCode(t, body); got != "analytics_invalid_request" {
		t.Fatalf("invalid from code = %q, want analytics_invalid_request", got)
	}

	code, body = analysisGetJSON(t, ChatWebAPIAnalysisPath+"/subagents?to=not-a-time")
	if code != http.StatusBadRequest {
		t.Fatalf("invalid to status = %d, want 400", code)
	}
	if got := analysisErrorCode(t, body); got != "analytics_invalid_request" {
		t.Fatalf("invalid to code = %q, want analytics_invalid_request", got)
	}

	// limit/top 拼写失败不 400：归一为 0，由查询层取默认上限。
	for _, sub := range []string{"/tools?limit=abc", "/subagents?limit=-2", "/errors?top=abc"} {
		if code, body := analysisGetJSON(t, ChatWebAPIAnalysisPath+sub); code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200（只读端点不因过滤参数拼写失败而 400）; body: %v", sub, code, body)
		}
	}

	code, body = analysisGetJSON(t, ChatWebAPIAnalysisPath+"/unknown")
	if code != http.StatusNotFound {
		t.Fatalf("unknown endpoint status = %d, want 404", code)
	}
	if got := analysisErrorCode(t, body); got != "analytics_not_found" {
		t.Fatalf("unknown endpoint code = %q, want analytics_not_found", got)
	}

	req := httptest.NewRequest(http.MethodPost, ChatWebAPIAnalysisPath+"/tools", nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPIAnalysis(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// 前端模块装配：go:embed 发布 + app.js/ui.js 引用（页签懒加载分支）
// ---------------------------------------------------------------------------

func TestHandleChatWebPage_AnalysisModule(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebPath+"js/analysis.js", nil)
	rec := httptest.NewRecorder()

	HandleChatWebPage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/javascript") {
		t.Fatalf("Content-Type = %q, want text/javascript", ct)
	}
	body := rec.Body.String()
	for _, needle := range []string{
		"/web/api/analysis",  // 数据源固定为分析端点前缀
		`cache: "no-store"`,  // 与 cache.js 同模式：禁用浏览器缓存
		"attached=false",     // 未挂载 → 顶部健康条（§9.1 降级）
		"暂无数据",               // 空库渲染
		"data-analysis-tool", // 工具行下钻
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("analysis.js missing %q", needle)
		}
	}

	// 注册链：app.js 调用 initAnalysis；ui.js 在页签激活分支懒加载并支持离开时停自动刷新。
	for _, wiring := range []struct {
		asset  string
		needle string
	}{
		{"app.js", "initAnalysis"},
		{"js/ui.js", `activateTab("analysis")`},
		{"js/ui.js", "loadAnalysis"},
		{"js/ui.js", "stopAnalysisAuto"},
	} {
		req := httptest.NewRequest(http.MethodGet, ChatWebPath+wiring.asset, nil)
		rec := httptest.NewRecorder()
		HandleChatWebPage(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", wiring.asset, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), wiring.needle) {
			t.Fatalf("%s missing %q（分析页签未接线）", wiring.asset, wiring.needle)
		}
	}
}
