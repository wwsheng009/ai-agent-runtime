package restapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// postWebhook 经公网入口投递一条入站消息（触发入队/处理日志）。
func postWebhook(t *testing.T, fx *fixture) {
	t.Helper()
	recorder := do(t, fx.hooks, http.MethodPost, "/hooks/connectors/c1", "")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("webhook = %d (%s), want 202", recorder.Code, recorder.Body.String())
	}
}

func decodeLogs(t *testing.T, body []byte) logsResponse {
	t.Helper()
	var payload logsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode logs payload: %v (%s)", err, string(body))
	}
	return payload
}

// TestLogsEndpointReturnsEntriesAndStats 覆盖 GET /connectors/{id}/logs 的载荷与字段。
func TestLogsEndpointReturnsEntriesAndStats(t *testing.T) {
	fx := newFixture(t, allowAll, baseConfig("c1", "fakeweb"))
	postWebhook(t, fx)
	waitFor(t, "inbound processed", func() bool {
		entries, _ := fx.service.Logs("c1", 10)
		return hasSummary(entries, "处理完成")
	})

	recorder := do(t, fx.router, http.MethodGet, "/connectors/c1/logs?limit=50", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("logs = %d (%s), want 200", recorder.Code, recorder.Body.String())
	}
	payload := decodeLogs(t, recorder.Body.Bytes())
	if len(payload.Entries) < 3 {
		t.Fatalf("entries = %d, want >=3（入队/开始处理/处理完成）：%s", len(payload.Entries), recorder.Body.String())
	}
	newest := payload.Entries[0]
	if !strings.Contains(newest.Summary, "处理完成") {
		t.Fatalf("entries[0] = %+v, want 最新在前（处理完成）", newest)
	}
	if newest.Level == "" || newest.Direction == "" || newest.ConnectorID != "c1" || newest.Time.IsZero() {
		t.Fatalf("entry 字段不完整：%+v", newest)
	}
	if payload.Stats.Inbound != 1 || payload.Stats.Outbound != 0 || payload.Stats.Errors != 0 {
		t.Fatalf("stats = %+v, want inbound=1 outbound=0 errors=0", payload.Stats)
	}
	if payload.Stats.LastActivityAt.IsZero() {
		t.Fatalf("stats.last_activity_at 应被设置：%+v", payload.Stats)
	}
}

func hasSummary(entries []connectors.LogEntry, contains string) bool {
	for _, entry := range entries {
		if strings.Contains(entry.Summary, contains) {
			return true
		}
	}
	return false
}

// TestLogsEndpointLimitConvergence 覆盖 limit 收敛（缺省/负值→50、超大→200 上限）
// 与 404/400 边界。
func TestLogsEndpointLimitConvergence(t *testing.T) {
	fx := newFixture(t, allowAll, baseConfig("c1", "fakeweb"))
	// 每条消息产生 3 条日志（入队/开始处理/处理完成）；17 条 → 51 条。
	for i := 0; i < 17; i++ {
		postWebhook(t, fx)
	}
	waitFor(t, "all messages processed", func() bool {
		entries, _ := fx.service.Logs("c1", 200)
		return len(entries) >= 51
	})

	defaulted := do(t, fx.router, http.MethodGet, "/connectors/c1/logs", "")
	if defaulted.Code != http.StatusOK {
		t.Fatalf("logs (default limit) = %d, want 200", defaulted.Code)
	}
	if got := len(decodeLogs(t, defaulted.Body.Bytes()).Entries); got != 50 {
		t.Fatalf("default limit entries = %d, want 50", got)
	}

	negative := do(t, fx.router, http.MethodGet, "/connectors/c1/logs?limit=-3", "")
	if negative.Code != http.StatusOK {
		t.Fatalf("logs (negative limit) = %d, want 200", negative.Code)
	}
	if got := len(decodeLogs(t, negative.Body.Bytes()).Entries); got != 50 {
		t.Fatalf("negative limit entries = %d, want 50", got)
	}

	oversized := do(t, fx.router, http.MethodGet, "/connectors/c1/logs?limit=99999", "")
	if oversized.Code != http.StatusOK {
		t.Fatalf("logs (oversized limit) = %d, want 200", oversized.Code)
	}
	if got := len(decodeLogs(t, oversized.Body.Bytes()).Entries); got != 51 {
		t.Fatalf("oversized limit entries = %d, want 51（环内全部，且 limit 收敛到 200）", got)
	}

	bad := do(t, fx.router, http.MethodGet, "/connectors/c1/logs?limit=abc", "")
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("logs (bad limit) = %d, want 400", bad.Code)
	}

	unknown := do(t, fx.router, http.MethodGet, "/connectors/ghost/logs", "")
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("logs (unknown connector) = %d, want 404", unknown.Code)
	}
}

// TestLogsEndpointDisabledConnectorStillVisible 覆盖「未知 404，但停用仍可查」语义。
func TestLogsEndpointDisabledConnectorStillVisible(t *testing.T) {
	cfg := baseConfig("c1", "fakeweb")
	cfg.Enabled = false
	fx := newFixture(t, allowAll, cfg)

	recorder := do(t, fx.router, http.MethodGet, "/connectors/c1/logs", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("disabled connector logs = %d (%s), want 200", recorder.Code, recorder.Body.String())
	}
	payload := decodeLogs(t, recorder.Body.Bytes())
	if payload.Entries == nil || len(payload.Entries) != 0 {
		t.Fatalf("entries = %v, want 空数组（非 null）：%s", payload.Entries, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"entries":[]`) {
		t.Fatalf("空日志应序列化为 []：%s", recorder.Body.String())
	}
	if payload.Stats != (connectors.ConnectorStats{}) {
		t.Fatalf("stats = %+v, want 零值", payload.Stats)
	}
}

// TestConnectorViewIncludesRuntimeStats 覆盖视图 additive 字段 runtime.stats（列表 + 详情）。
func TestConnectorViewIncludesRuntimeStats(t *testing.T) {
	fx := newFixture(t, allowAll, baseConfig("c1", "fakeweb"))
	postWebhook(t, fx)
	waitFor(t, "inbound counted", func() bool {
		stats, _ := fx.service.Stats("c1")
		return stats.Inbound == 1
	})

	detail := do(t, fx.router, http.MethodGet, "/connectors/c1", "")
	if detail.Code != http.StatusOK {
		t.Fatalf("detail = %d, want 200", detail.Code)
	}
	var view ConnectorView
	if err := json.Unmarshal(detail.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if view.Runtime == nil || view.Runtime.Stats.Inbound != 1 {
		t.Fatalf("detail runtime = %+v, want stats.inbound=1 (%s)", view.Runtime, detail.Body.String())
	}
	// additive：旧字段照常返回。
	if view.ID != "c1" || view.Provider != "fakeweb" || !view.Enabled {
		t.Fatalf("旧字段回归：%+v", view)
	}

	list := do(t, fx.router, http.MethodGet, "/connectors", "")
	var payload listResponse
	if err := json.Unmarshal(list.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("list items = %d, want 1", len(payload.Items))
	}
	if item := payload.Items[0]; item.Runtime == nil || item.Runtime.Stats.Inbound != 1 {
		t.Fatalf("list runtime = %+v, want stats.inbound=1 (%s)", payload.Items[0].Runtime, list.Body.String())
	}
}
