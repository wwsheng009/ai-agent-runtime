package runtimeapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// lspBaselineFixture 与 internal/lsp/baseline 的互锁 fixture 同构
// （4 请求 / 2 会话 / P50 7ms / P95 30ms / 覆盖率 1.0 / 追加比 100:1100 / 1 条损坏）。
const lspBaselineFixture = `{"type":"lsp.request.finished","session_id":"s1","timestamp":"2026-09-29T10:00:00Z","payload":{"trigger":"inline","outcome":"injected","duration_ms":10,"diag_count":2,"total_diag_count":3,"new_diag_count":2,"appended_bytes":100,"appended_diag_bytes":80,"appended_note_bytes":10,"appended_empty_bytes":10,"attempted_members":2,"server":"gopls","path_fingerprint":"p1","diag_fingerprint":"d1"}}
{"type":"lsp.request.finished","session_id":"s1","timestamp":"2026-09-29T10:02:00Z","payload":{"trigger":"tool","outcome":"clean","duration_ms":7,"path_fingerprint":"p1"}}
{"type":"lsp.request.finished","session_id":"s1","timestamp":"2026-09-29T10:01:00Z","payload":{"trigger":"inline","outcome":"degraded_no_fresh","duration_ms":30,"reason_category":"wait_timeout","cold_fast_fail":false}}
{"type":"lsp.request.finished","session_id":"s2","timestamp":"2026-09-29T10:02:00Z","payload":{"trigger":"tool","outcome":"no_server","duration_ms":5}}
{"type":"lsp.server.state","session_id":"s1","timestamp":"2026-09-29T10:00:00Z","payload":{"server":"gopls","state":"ready","pid":42,"first_publish_ms":1234}}
{"type":"tool.completed","session_id":"s1","timestamp":"2026-09-29T10:00:00Z","payload":{"logical_tool":"apply_patch","output_model_visible_bytes":1000}}
{"type":"tool.completed","session_id":"s1","timestamp":"2026-09-29T10:01:00Z","payload":{"logical_tool":"write","output_model_visible_bytes":100}}
{"type":"tool.completed","session_id":"s1","timestamp":"2026-09-29T10:01:30Z","payload":{"logical_tool":"grep"}}
{"type":"tool.completed","payload":{
`

func newLSPBaselineTestRouter(t *testing.T, fixture string) *mux.Router {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "2026", "09", "29", "sess_a", "events")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "runtime-events.jsonl"), []byte(fixture), 0o644))

	original := lspBaselineRoots
	lspBaselineRoots = func() []string { return []string{root} }
	t.Cleanup(func() { lspBaselineRoots = original })

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	return router
}

// TestAnalyticsLSPBaselineSharesContract 验证端点输出与 §3.3/§4.3 基线同口径：
// 数字直接来自 internal/lsp/baseline（与 TUI `/lsp baseline` 同一实现）。
func TestAnalyticsLSPBaselineSharesContract(t *testing.T) {
	router := newLSPBaselineTestRouter(t, lspBaselineFixture)

	rec := getAnalyticsResponse(router, "/api/runtime/analytics/lsp/baseline")
	require.Equal(t, http.StatusOK, rec.Code)

	payload := decodeAnalyticsPayload(t, rec)
	require.Equal(t, lspBaselineSchemaVersion, payload["schema_version"])
	require.Equal(t, "all", payload["window"])

	stats, ok := payload["stats"].(map[string]interface{})
	require.True(t, ok, "stats must be an object")
	require.EqualValues(t, 4, stats["requests"])
	require.EqualValues(t, 2, stats["sessions"])
	require.EqualValues(t, 1, stats["injected"])
	require.EqualValues(t, 1, stats["degraded"])
	require.EqualValues(t, 1, stats["no_server"])
	require.EqualValues(t, 7, stats["latency_p50_ms"])
	require.EqualValues(t, 30, stats["latency_p95_ms"])

	scan, ok := payload["scan"].(map[string]interface{})
	require.True(t, ok, "scan must be an object")
	require.EqualValues(t, 1, scan["files"])
	require.EqualValues(t, 1, scan["malformed"])

	rows := analyticsRows(t, payload, "rows")
	require.NotEmpty(t, rows, "§4.3 rows must be present")
	for _, row := range rows {
		item, ok := row.(map[string]interface{})
		require.True(t, ok)
		require.NotEmpty(t, item["metric"])
		require.NotEmpty(t, item["value"])
		require.NotEmpty(t, item["conclusion"])
	}
}

// TestAnalyticsLSPBaselineWindowAndParams 覆盖时间窗与参数归一。
func TestAnalyticsLSPBaselineWindowAndParams(t *testing.T) {
	router := newLSPBaselineTestRouter(t, lspBaselineFixture)

	// since 窗口只保留 10:02 的两条请求。
	rec := getAnalyticsResponse(router, "/api/runtime/analytics/lsp/baseline?since=2026-09-29T10:01:30Z")
	require.Equal(t, http.StatusOK, rec.Code)
	payload := decodeAnalyticsPayload(t, rec)
	stats := payload["stats"].(map[string]interface{})
	require.EqualValues(t, 2, stats["requests"])
	require.Equal(t, "since=2026-09-29T10:01:30Z", payload["window"])

	// days 合法（fixture 在窗口内）。
	daysRec := getAnalyticsResponse(router, "/api/runtime/analytics/lsp/baseline?days=30")
	require.Equal(t, http.StatusOK, daysRec.Code)
	require.Equal(t, "days=30", decodeAnalyticsPayload(t, daysRec)["window"])

	for _, target := range []string{
		"/api/runtime/analytics/lsp/baseline?since=2026-09-29T10:01:30Z&days=3",
		"/api/runtime/analytics/lsp/baseline?since=not-a-time",
		"/api/runtime/analytics/lsp/baseline?days=abc",
		"/api/runtime/analytics/lsp/baseline?days=0",
		"/api/runtime/analytics/lsp/baseline?days=-3",
	} {
		require.Equal(t, http.StatusBadRequest, getAnalyticsResponse(router, target).Code, target)
	}
}

// TestAnalyticsLSPBaselineRequiresAdmin 验证非回环且无令牌时 403。
func TestAnalyticsLSPBaselineRequiresAdmin(t *testing.T) {
	router := newLSPBaselineTestRouter(t, lspBaselineFixture)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/analytics/lsp/baseline", nil)
	req.RemoteAddr = "192.0.2.9:4321"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, strings.ToLower(rec.Body.String()), "permission")
}
