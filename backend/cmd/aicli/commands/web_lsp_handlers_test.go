package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// /web/api/lsp/* —— 「LSP 观测」页签端点（lsp.observe.v1）。
//
// 直调 handler（不经 mux），用 display provider 注入测试会话，覆盖：
// 未启用池的稳定降级 / 诚实 pending（不伪造 0）/ 405 / 404 / 前端接线。
// ---------------------------------------------------------------------------

// withChatWebLSPSession 通过 display provider 模拟"当前活动会话"。
func withChatWebLSPSession(t *testing.T, session *ChatSession) {
	t.Helper()
	prev := chatDebugDisplaySessionProvider
	t.Cleanup(func() { chatDebugDisplaySessionProvider = prev })
	RegisterChatDebugDisplayProvider(func() *ChatSession { return session })
}

func chatWebLSPDecode(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (body=%s)", err, rec.Body.String())
	}
	return body
}

func TestChatWebLSPStatusDisabledSession(t *testing.T) {
	withChatWebLSPSession(t, newWebTestSession())

	req := httptest.NewRequest(http.MethodGet, ChatWebAPILSPPath+"/status", nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPILSP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := chatWebLSPDecode(t, rec)
	if got := body["schema_version"]; got != lspObserveSchemaVersion {
		t.Fatalf("schema_version = %v, want %q", got, lspObserveSchemaVersion)
	}
	if enabled, _ := body["enabled"].(bool); enabled {
		t.Fatalf("enabled = true, want false（测试会话未挂载 LSP 池）")
	}
	servers, ok := body["servers"].([]interface{})
	if !ok {
		t.Fatalf("servers 必须是 JSON 数组而不是 null/对象: %T", body["servers"])
	}
	if len(servers) != 0 {
		t.Fatalf("servers = %v, want []", servers)
	}
	// 未启用不是错误：不许出现 error envelope。
	if _, hasErr := body["error"]; hasErr {
		t.Fatalf("disabled 状态不得返回 error envelope: %v", body)
	}
}

func TestChatWebLSPOverviewHonestPending(t *testing.T) {
	withChatWebLSPSession(t, newWebTestSession())

	req := httptest.NewRequest(http.MethodGet, ChatWebAPILSPPath+"/overview", nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPILSP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := chatWebLSPDecode(t, rec)
	if available, _ := body["available"].(bool); available {
		t.Fatalf("available = true, want false（事件埋点未接入）")
	}
	if got := body["reason"]; got != lspInstrumentationPending {
		t.Fatalf("reason = %v, want %q", got, lspInstrumentationPending)
	}
	// 诚实降级：metrics 必须是 null（未采集），不得是 0 值对象。
	if body["metrics"] != nil {
		t.Fatalf("metrics = %v, want null（不得伪造 0）", body["metrics"])
	}
	if got, ok := body["servers_by_state"].(map[string]interface{}); !ok || len(got) != 0 {
		t.Fatalf("servers_by_state = %v, want {}", body["servers_by_state"])
	}
}

func TestChatWebLSPEventsPendingEmptyArray(t *testing.T) {
	withChatWebLSPSession(t, newWebTestSession())

	req := httptest.NewRequest(http.MethodGet, ChatWebAPILSPPath+"/events?limit=50", nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPILSP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := chatWebLSPDecode(t, rec)
	events, ok := body["events"].([]interface{})
	if !ok || len(events) != 0 {
		t.Fatalf("events = %v, want []", body["events"])
	}
	if available, _ := body["available"].(bool); available {
		t.Fatalf("available = true, want false")
	}
}

// ---------------------------------------------------------------------------
// /web/api/lsp/baseline —— §4.3 基线区块（跨会话；TTL 缓存；未采集 n/a）。
// ---------------------------------------------------------------------------

func withChatWebLSPBaselineRoot(t *testing.T, roots []string) {
	t.Helper()
	prevRoots := chatWebLSPBaselineRoots
	chatWebLSPBaselineRoots = func() []string { return roots }
	resetChatWebLSPBaselineCache()
	t.Cleanup(func() {
		chatWebLSPBaselineRoots = prevRoots
		resetChatWebLSPBaselineCache()
	})
}

func writeChatWebLSPBaselineFixture(t *testing.T, root string) {
	t.Helper()
	body := strings.Join([]string{
		`{"type":"lsp.request.finished","session_id":"s1","timestamp":"2026-09-29T10:00:00Z","payload":{"trigger":"inline","outcome":"injected","duration_ms":10,"diag_count":2,"appended_bytes":100,"server":"gopls"}}`,
		// tool_call_id 不是可选的：它是 usage_tool_calls 的主键，ingest 遇到缺它的
		// tool.completed 会整条丢弃，于是覆盖率的**分母**就没有这条调用。
		// 实测线上最近 120 个会话日志里 1299 条 tool.completed 全部带该字段
		// （覆盖率 100%），所以这是 fixture 必须写实，而不是库路径的缺口。
		`{"type":"tool.completed","session_id":"s1","timestamp":"2026-09-29T10:00:00Z","payload":{"tool_call_id":"tc1","logical_tool":"apply_patch","output_model_visible_bytes":1000}}`,
		"",
	}, "\n")
	// 事实源是分析库：灌库而不是写 JSONL。root 参数已无意义，保留仅为不改签名。
	seedLSPBaselineTestDB(t, body)
	_ = root
}

func chatWebLSPBaselineGet(t *testing.T, query string) (int, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, ChatWebAPILSPPath+"/baseline"+query, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPILSP(rec, req)
	return rec.Code, chatWebLSPDecode(t, rec)
}

func TestChatWebLSPBaselineFixtureRows(t *testing.T) {
	root := t.TempDir()
	writeChatWebLSPBaselineFixture(t, root)
	withChatWebLSPBaselineRoot(t, []string{root})

	code, body := chatWebLSPBaselineGet(t, "?days=0")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%v)", code, body)
	}
	if available, _ := body["available"].(bool); !available {
		t.Fatalf("available = false, want true (body=%v)", body)
	}
	digest, _ := body["digest"].(map[string]interface{})
	if digest["requests"] != float64(1) || digest["active_edit_calls"] != float64(1) {
		t.Fatalf("digest = %v", digest)
	}
	rows, _ := body["rows"].([]interface{})
	if len(rows) < 6 {
		t.Fatalf("rows = %d, want >= 6", len(rows))
	}
	first, _ := rows[0].(map[string]interface{})
	if first["metric"] != "lsp_edit_coverage_ratio" || first["value"] != "1.0000" {
		t.Fatalf("first row = %v", first)
	}
	scan, _ := body["scan"].(map[string]interface{})
	// 扫描量恒为 0：数据面读分析库，不扫日志。这是"这条路不扫日志"，不是故障。
	if scan["files"] != float64(0) || scan["malformed"] != float64(0) {
		t.Fatalf("scan = %v", scan)
	}
	cachedAt, _ := body["cached_at"].(string)
	if cachedAt == "" {
		t.Fatalf("cached_at missing: %v", body)
	}

	// TTL 缓存命中：即使根被清空，仍返回上一次结果（页签 15s 自动刷新不打盘）。
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove root: %v", err)
	}
	code, again := chatWebLSPBaselineGet(t, "?days=0")
	if code != http.StatusOK {
		t.Fatalf("cached status = %d, want 200", code)
	}
	if again["cached_at"] != cachedAt {
		t.Fatalf("cache miss: cached_at %v -> %v", cachedAt, again["cached_at"])
	}
}

func TestChatWebLSPBaselineHonestDegradation(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	withChatWebLSPBaselineRoot(t, []string{missing})

	code, body := chatWebLSPBaselineGet(t, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if available, _ := body["available"].(bool); available {
		t.Fatalf("available = true, want false (body=%v)", body)
	}
	if body["reason"] != chatWebLSPBaselineNoRootReason {
		t.Fatalf("reason = %v, want %s", body["reason"], chatWebLSPBaselineNoRootReason)
	}
	if rows, _ := body["rows"].([]interface{}); len(rows) != 0 {
		t.Fatalf("rows = %v, want empty", rows)
	}

	code, body = chatWebLSPBaselineGet(t, "?days=abc")
	if code != http.StatusBadRequest {
		t.Fatalf("invalid days status = %d, want 400", code)
	}
	errObj, _ := body["error"].(map[string]interface{})
	if errObj == nil || errObj["code"] != chatWebLSPBaselineInvalidDaysCode {
		t.Fatalf("error = %v, want code %s", body["error"], chatWebLSPBaselineInvalidDaysCode)
	}
}

func TestChatWebLSPMethodNotAllowedAndUnknownSubpath(t *testing.T) {
	withChatWebLSPSession(t, newWebTestSession())

	req := httptest.NewRequest(http.MethodPost, ChatWebAPILSPPath+"/status", nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPILSP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", rec.Code)
	}
	body := chatWebLSPDecode(t, rec)
	errObj, _ := body["error"].(map[string]interface{})
	if errObj == nil || errObj["code"] != chatWebLSPMethodCode {
		t.Fatalf("error = %v, want code %q", body["error"], chatWebLSPMethodCode)
	}

	req = httptest.NewRequest(http.MethodGet, ChatWebAPILSPPath+"/nope", nil)
	rec = httptest.NewRecorder()
	HandleChatWebAPILSP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET unknown subpath = %d, want 404", rec.Code)
	}
	body = chatWebLSPDecode(t, rec)
	errObj, _ = body["error"].(map[string]interface{})
	if errObj == nil || errObj["code"] != chatWebLSPNotFoundCode {
		t.Fatalf("error = %v, want code %q", body["error"], chatWebLSPNotFoundCode)
	}
}

// TestChatWebLSPFrontendWiring 校验 micro web client 资源接线：
// 页面资源存在、模块注册链（app.js / ui.js / menu.js）完整。
func TestChatWebLSPFrontendWiring(t *testing.T) {
	for _, wiring := range []struct {
		asset  string
		needle string
	}{
		{"js/lsp.js", "/web/api/lsp"},
		{"js/lsp.js", "lsp_instrumentation_pending"}, // 诚实降级原因码（前端显式识别）
		{"js/lsp.js", "cache: \"no-store\""},         // 与 cache.js/analysis.js 同模式
		{"app.js", "initLSP"},
		{"js/ui.js", `activateTab("lsp")`},
		{"js/ui.js", "loadLSP"},
		{"js/ui.js", "stopLSPAuto"},
		{"js/menu.js", "tab-lsp"},
		{"index.html", `id="tab-lsp"`},
		{"index.html", `data-menu-action="tab-lsp"`},
	} {
		req := httptest.NewRequest(http.MethodGet, ChatWebPath+wiring.asset, nil)
		rec := httptest.NewRecorder()
		HandleChatWebPage(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", wiring.asset, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), wiring.needle) {
			t.Fatalf("%s missing %q（LSP 观测页签未接线）", wiring.asset, wiring.needle)
		}
	}
}
