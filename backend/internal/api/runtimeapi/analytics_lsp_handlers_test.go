package runtimeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// lspBaselineFixture 与 internal/lsp/baseline 的互锁 fixture 同构
// （4 请求 / 2 会话 / P50 7ms / P95 30ms / 覆盖率 1.0 / 追加比 100:1100）。
//
// 末行是**故意写坏的** JSON（半行）。事实源迁到分析库后它不再是数据的一部分 ——
// 日志损坏只影响日志自己的健康度，不再污染读数，这正是本次修复要的结果。
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

// newLSPBaselineTestRouter 把 fixture 经**实时总线**灌进临时分析库，再让 handler
// 指到同一个库。
//
// 端点的数据面现在读库（不再回扫 chat-logs），所以测试也必须造库里的数据 ——
// 继续写 JSONL 再指望端点读到它，测的就不是生产链路了。
func newLSPBaselineTestRouter(t *testing.T, fixture string) *mux.Router {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "usage_analytics.sqlite")
	seedLSPBaselineDB(t, dbPath, fixture)
	t.Cleanup(detachUsageAnalyticsService)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.usageAnalyticsDBPath = dbPath
	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	return router
}

// seedLSPBaselineDB 逐行解析 fixture 并发布到总线；无法解析的行跳过。
func seedLSPBaselineDB(t *testing.T, dbPath, fixture string) {
	t.Helper()
	bus := runtimeevents.NewBus()
	service, err := usageanalytics.Attach(bus, usageanalytics.Options{
		Config: usageanalytics.Config{Path: dbPath},
	})
	require.NoError(t, err)
	// 注意：只经 Attach 开库。多开一个 store 会把 SQLite 文件句柄留到测试结束，
	// 让 t.TempDir 的清理失败（Windows 上文件被占用无法删除）。
	defer service.Close()
	for _, line := range strings.Split(fixture, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event struct {
			Type      string                 `json:"type"`
			SessionID string                 `json:"session_id"`
			Timestamp time.Time              `json:"timestamp"`
			Payload   map[string]interface{} `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			continue // 写坏的行不是数据，与日志路径的处理一致
		}
		bus.Publish(runtimeevents.Event{
			Type: event.Type, SessionID: event.SessionID,
			Timestamp: event.Timestamp, Payload: event.Payload,
		})
	}
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
	// 数据面必须自报事实源。缺这个字段，UI 只能看到 scan.* 全 0，无法区分
	// "扫了但没数据"和"根本没扫"，于是会渲染出误导性的"未使用增量索引"。
	require.Equal(t, lspBaselineSourceAnalyticsDB, payload["source"])

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
	// 数据面读分析库，不扫描日志 ⇒ 扫描量恒为 0。这不是"没扫到数据"，而是
	// "这条路本来就不扫日志"：文件数/行数/损坏行数是日志自身的健康度，已移出
	// 数据面。fixture 里那行故意写坏的 JSON 因此也不再影响任何读数。
	require.EqualValues(t, 0, scan["files"])
	require.EqualValues(t, 0, scan["malformed"])
	require.EqualValues(t, 0, scan["lines"])

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

// TestAnalyticsLSPBaselineCacheIsHonest 钉住缓存的"可观测 + 不串目录"两条纪律：
//
//  1. 首次 miss（自己真扫了）、TTL 内 hit（复用但如实报年龄）；
//  2. roots 换目录后必须重新 miss——缓存键含 roots，绝不能把别的目录的旧数字
//     当成当前目录的结果返回。
func TestAnalyticsLSPBaselineCacheIsHonest(t *testing.T) {
	router := newLSPBaselineTestRouter(t, lspBaselineFixture)
	const target = "/api/runtime/analytics/lsp/baseline"

	first := decodeAnalyticsPayload(t, getAnalyticsResponse(router, target))
	cache, ok := first["cache"].(map[string]interface{})
	require.True(t, ok, "cache must be an object")
	require.EqualValues(t, false, cache["hit"])
	require.EqualValues(t, 0, cache["age_seconds"])
	require.EqualValues(t, int(lspBaselineCacheTTL/time.Second), cache["ttl_seconds"])

	second := decodeAnalyticsPayload(t, getAnalyticsResponse(router, target))
	secondCache, ok := second["cache"].(map[string]interface{})
	require.True(t, ok, "cache must be an object")
	require.EqualValues(t, true, secondCache["hit"], "second GET inside TTL must be a cache hit")
	// 缓存命中不得改变任何数字：generated_at 与 stats 必须与首次完全一致。
	require.Equal(t, first["generated_at"], second["generated_at"])
	require.Equal(t, first["stats"], second["stats"])
	require.Equal(t, first["rows"], second["rows"])

	// 键里曾经有 roots（那时事实源是 chat-logs 目录，换目录必须换键）。现在事实源
	// 是分析库、roots 不再影响任何数字，所以那个"换 roots 必须重扫"的断言已失去
	// 对象并被移除；窗口隔离由 TestAnalyticsLSPBaselineWindowAndParams 覆盖。
	//
	// 仍然成立的是：缓存必须如实报告它命中了什么窗口。
	firstCache := first["cache"].(map[string]interface{})
	require.EqualValues(t, "all", first["window"])
	require.Contains(t, firstCache, "ttl_seconds")
}

// TestAnalyticsLSPBaselineCoalescesConcurrentScans 验证在途合并：并发同键 GET
// 必须只触发**一次** Analyze。无论后续请求是等到在途扫描（hit=true, age=0）还是
// 扫描已完成后命中 TTL 缓存（hit=true, age>=0），miss 都必须恰好 1 次。
func TestAnalyticsLSPBaselineCoalescesConcurrentScans(t *testing.T) {
	router := newLSPBaselineTestRouter(t, lspBaselineFixture)
	const target = "/api/runtime/analytics/lsp/baseline"
	const requests = 8

	payloads := make([]map[string]interface{}, requests)
	codes := make([]int, requests)
	var wg sync.WaitGroup
	wg.Add(requests)
	for index := 0; index < requests; index++ {
		go func(slot int) {
			defer wg.Done()
			rec := getAnalyticsResponse(router, target)
			codes[slot] = rec.Code
			payloads[slot] = decodeAnalyticsPayload(t, rec)
		}(index)
	}
	wg.Wait()

	misses := 0
	for index, payload := range payloads {
		require.Equal(t, http.StatusOK, codes[index])
		// 所有并发请求必须拿到同一组数字（合并只共享结果，不改变内容）。
		require.Equal(t, payloads[0]["stats"], payload["stats"])
		require.Equal(t, payloads[0]["generated_at"], payload["generated_at"])
		cache := payload["cache"].(map[string]interface{})
		if cache["hit"] == false {
			misses++
		}
	}
	require.Equal(t, 1, misses, "concurrent identical GETs must scan exactly once")
}
