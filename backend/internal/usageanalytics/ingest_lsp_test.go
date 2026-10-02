package usageanalytics

import (
	"path/filepath"
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// lspQueryRow 是测试里读回一行 usage_lsp_requests 的最小投影。
type lspQueryRow struct {
	SessionID          string
	PathFingerprint    string
	Server             string
	Trigger            string
	Outcome            string
	DurationMS         int
	AppendedBytes      int
	AttemptedMembers   int
	ColdProbeClassifie int
	ColdFastFail       int
}

func readLSPRows(t *testing.T, store *Store) []lspQueryRow {
	t.Helper()
	rows, err := store.db.Query(`
SELECT session_id, path_fingerprint, server, trigger, outcome, duration_ms,
       appended_bytes, attempted_members, cold_probe_classified, cold_fast_fail
FROM usage_lsp_requests
ORDER BY started_unix_nano, path_fingerprint`)
	if err != nil {
		t.Fatalf("query usage_lsp_requests: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []lspQueryRow
	for rows.Next() {
		var row lspQueryRow
		if err := rows.Scan(&row.SessionID, &row.PathFingerprint, &row.Server,
			&row.Trigger, &row.Outcome, &row.DurationMS,
			&row.AppendedBytes, &row.AttemptedMembers,
			&row.ColdProbeClassifie, &row.ColdFastFail); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// TestCollectorIngestsLSPRequests 锁定 LSP 后写请求入库。
//
// 这条测试的存在意义是**架构契约**而非功能：它证明基线的事实源已经从"回扫
// runtime-events.jsonl"迁到分析库，而后者不会因日志保留策略而退化。
func TestCollectorIngestsLSPRequests(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	bus := runtimeevents.NewBus()
	collector := newCollector(store, nil, nil)
	collector.subscribe(bus)
	defer collector.close()

	at := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	bus.Publish(runtimeevents.Event{
		Type:      runtimeevents.EventLSPRequestFinished,
		SessionID: "s-lsp",
		Timestamp: at,
		Payload: map[string]interface{}{
			"trigger": "inline", "outcome": "injected", "duration_ms": 12,
			"server": "gopls", "path_fingerprint": "fp-1",
			"appended_bytes": 100, "attempted_members": 3,
			"total_diag_count": 4, "new_diag_count": 2,
			"cold_fast_fail": false, // 首探针：键存在但为 false
		},
	})
	bus.Publish(runtimeevents.Event{
		Type:      runtimeevents.EventLSPRequestFinished,
		SessionID: "s-lsp",
		Timestamp: at.Add(time.Minute),
		Payload: map[string]interface{}{
			"trigger": "tool", "outcome": "degraded_no_fresh", "duration_ms": 8,
			"server": "gopls", "path_fingerprint": "fp-1",
			"appended_bytes": 40, "cold_fast_fail": true,
		},
	})

	rows := readLSPRows(t, store)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(rows), rows)
	}
	if rows[0].SessionID != "s-lsp" || rows[0].Server != "gopls" || rows[0].PathFingerprint != "fp-1" {
		t.Fatalf("identity columns wrong: %+v", rows[0])
	}
	if rows[0].Trigger != "inline" || rows[0].Outcome != "injected" || rows[0].DurationMS != 12 {
		t.Fatalf("row 0 scalars wrong: %+v", rows[0])
	}
	if rows[0].AppendedBytes != 100 || rows[0].AttemptedMembers != 3 {
		t.Fatalf("row 0 metrics wrong: %+v", rows[0])
	}
	// 首探针必须是 classified=1 / fast_fail=0。这一对是 eventbridge/observer.go:57-60
	// 拼命保住的语义：只存布尔值会把首探针(false)与"老构建没这个键"混为一谈。
	if rows[0].ColdProbeClassifie != 1 || rows[0].ColdFastFail != 0 {
		t.Fatalf("first probe must be classified=1 fast_fail=0, got %+v", rows[0])
	}
	if rows[1].ColdProbeClassifie != 1 || rows[1].ColdFastFail != 1 {
		t.Fatalf("repeat probe must be classified=1 fast_fail=1, got %+v", rows[1])
	}
}

// TestCollectorKeepsUnclassifiedColdProbeDistinct 覆盖"根本没有 cold_fast_fail 键"
// 的老构建样本：它必须 classified=0，与首探针的 classified=1 区分开。
func TestCollectorKeepsUnclassifiedColdProbeDistinct(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	bus := runtimeevents.NewBus()
	collector := newCollector(store, nil, nil)
	collector.subscribe(bus)
	defer collector.close()

	at := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	// 老构建样本：没有 cold_fast_fail 键。
	bus.Publish(runtimeevents.Event{
		Type: runtimeevents.EventLSPRequestFinished, SessionID: "s-old", Timestamp: at,
		Payload: map[string]interface{}{
			"trigger": "inline", "outcome": "clean", "server": "gopls",
			"path_fingerprint": "fp-old",
		},
	})
	rows := readLSPRows(t, store)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].ColdProbeClassifie != 0 {
		t.Fatalf("missing key must stay unclassified (0), got %+v", rows[0])
	}
}

// TestCollectorLSPIngestIsIdempotent 锁定重复投递不重复计数：总线重放或将来做
// 一次性回填时，同一请求不能被计两次。
func TestCollectorLSPIngestIsIdempotent(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	bus := runtimeevents.NewBus()
	collector := newCollector(store, nil, nil)
	collector.subscribe(bus)
	defer collector.close()

	at := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	event := runtimeevents.Event{
		Type: runtimeevents.EventLSPRequestFinished, SessionID: "s-dup", Timestamp: at,
		Payload: map[string]interface{}{
			"trigger": "inline", "outcome": "injected", "duration_ms": 15,
			"server": "gopls", "path_fingerprint": "fp-dup", "appended_bytes": 55,
		},
	}
	for i := 0; i < 3; i++ {
		bus.Publish(event)
	}
	rows := readLSPRows(t, store)
	if len(rows) != 1 {
		t.Fatalf("rows = %d after 3 identical deliveries, want 1", len(rows))
	}
	if rows[0].AppendedBytes != 55 || rows[0].DurationMS != 15 {
		t.Fatalf("replay corrupted values: %+v", rows[0])
	}
}

// TestCollectorIngestsLSPColdFirstPublish 覆盖本次修复的核心缺口：
// lsp.server.state 是 ChannelLiveOnly（不落盘），所以过去冷启动指标只吃得到历史
// 残留、永远不再更新。现在它必须从实时总线进库。
func TestCollectorIngestsLSPColdFirstPublish(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	bus := runtimeevents.NewBus()
	collector := newCollector(store, nil, nil)
	collector.subscribe(bus)
	defer collector.close()

	at := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	publishState := func(server string, firstPublishMS int, when time.Time) {
		payload := map[string]interface{}{"server": server, "state": "ready", "pid": 42}
		if firstPublishMS > 0 {
			payload["first_publish_ms"] = firstPublishMS
		}
		bus.Publish(runtimeevents.Event{
			Type: runtimeevents.EventLSPServerState, SessionID: "s-cold", Timestamp: when, Payload: payload,
		})
	}
	publishState("gopls", 1200, at)
	// 生命周期噪声不带首发布延迟，不该让表增长。
	publishState("gopls", 0, at.Add(time.Second))
	// 另一个池成员，独立一行。
	publishState("typescriptls", 800, at)

	rows, err := store.db.Query(`
SELECT server, first_publish_ms FROM usage_lsp_first_publish ORDER BY server`)
	if err != nil {
		t.Fatalf("query first publish: %v", err)
	}
	defer func() { _ = rows.Close() }()
	got := map[string]int{}
	for rows.Next() {
		var server string
		var ms int
		if err := rows.Scan(&server, &ms); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[server] = ms
	}
	if len(got) != 2 || got["gopls"] != 1200 || got["typescriptls"] != 800 {
		t.Fatalf("first publish rows = %+v, want {gopls:1200, typescriptls:800}", got)
	}
}

// TestCollectorKeepsEarliestFirstPublish 钉住"取首个发布"的口径。
//
// 关键在于**最早观测**而不是**最小值**：三次数值故意排成 5000 → 1200 → 9000，
// 于是"取最小值"会得到 1200，"取最早观测"得到 5000。若实现偷懒写成
// min(first_publish_ms)，这个测试会红 —— 它就是为了区分这两者而存在的。
//
// 语义依据：first_publish_ms 是"启动→首个发布"的延迟，属该池成员启动这一次
// 的属性。后来的一次上报若给出更小的数，那是重报/抖动，不该改写首次观测。
func TestCollectorKeepsEarliestFirstPublish(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	bus := runtimeevents.NewBus()
	collector := newCollector(store, nil, nil)
	collector.subscribe(bus)
	defer collector.close()

	base := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	for i, ms := range []int{5000, 1200, 9000} {
		bus.Publish(runtimeevents.Event{
			Type: runtimeevents.EventLSPServerState, SessionID: "s-e", Timestamp: base.Add(time.Duration(i) * time.Minute),
			Payload: map[string]interface{}{"server": "gopls", "state": "ready", "first_publish_ms": ms},
		})
	}
	var got int
	if err := store.db.QueryRow(
		`SELECT first_publish_ms FROM usage_lsp_first_publish WHERE session_id='s-e' AND server='gopls'`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 5000 {
		t.Fatalf("first_publish_ms = %d, want the earliest observation 5000 (not the minimum 1200)", got)
	}
}

// TestLSPIngestDoesNotChangeChannelContract 钉住本次修复的边界：
// 入库**不改**落盘契约。lsp.server.state 仍是 ChannelLiveOnly，所以每会话
// runtime-events.jsonl 的体积约束一字不动 —— 变的只是"事实进了分析库"。
//
// 少了这条，修复很容易悄悄退化成"把事件改成落盘"，那是用体积换正确性，
// 与本次"通道=体积、入库=可复算性，两件事正交"的判断相反。
func TestLSPIngestDoesNotChangeChannelContract(t *testing.T) {
	for _, eventType := range []string{
		runtimeevents.EventLSPServerState,
		runtimeevents.EventLSPDiagnosticsUpdated,
	} {
		if runtimeevents.IsPersistedEventType(eventType) {
			t.Fatalf("%s 必须保持 live-only：入库不依赖落盘，改通道是用体积换正确性", eventType)
		}
	}
	// 反面：请求事件确实仍是落盘的（它本来就 A 通道），这里只是确认没被改坏。
	if !runtimeevents.IsPersistedEventType(runtimeevents.EventLSPRequestFinished) {
		t.Fatalf("%s 必须保持落盘", runtimeevents.EventLSPRequestFinished)
	}
}
