package baselinesql

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/lsp/baseline"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// fixtureEvent 是一条用于双路径对拍的合成事件。
type fixtureEvent struct {
	Type      string
	SessionID string
	At        time.Time
	Payload   map[string]interface{}
}

// eventToolCompleted 是编辑调用的类型名。internal/events 未为它导出常量
// （contract.go 用字面量登记），故此处与 internal/lsp/baseline 的 eventToolCompleted
// 保持一致。
const eventToolCompleted = "tool.completed"

// fixture 覆盖每一条聚合分支：注入/干净/无服务/降级、冷探针首探与重探、
// 多成员、截断、降级原因、以及能触发 closure 的"注入→同文件再编辑变干净"。
func fixture() []fixtureEvent {
	base := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	return []fixtureEvent{
		{Type: runtimeevents.EventLSPRequestFinished, SessionID: "s1", At: base,
			Payload: map[string]interface{}{
				"trigger": "inline", "outcome": "injected", "duration_ms": 10,
				"server": "gopls", "path_fingerprint": "p1", "diag_fingerprint": "d1",
				"appended_bytes": 100, "appended_diag_bytes": 80,
				"appended_note_bytes": 10, "appended_empty_bytes": 10,
				"total_diag_count": 3, "new_diag_count": 2, "diag_count": 2,
				"cold_fast_fail": false,
			}},
		// 同文件后续编辑 → 干净：closure 应当闭合。
		{Type: runtimeevents.EventLSPRequestFinished, SessionID: "s1", At: base.Add(time.Minute),
			Payload: map[string]interface{}{
				"trigger": "inline", "outcome": "clean", "duration_ms": 20,
				"server": "gopls", "path_fingerprint": "p1",
				"appended_bytes": 0, "diag_count": 0,
			}},
		// 另一会话：no_server + 截断（omitted_* 非零 ⇒ truncated_requests）。
		{Type: runtimeevents.EventLSPRequestFinished, SessionID: "s2", At: base.Add(2 * time.Minute),
			Payload: map[string]interface{}{
				"trigger": "tool", "outcome": "no_server", "duration_ms": 30,
				"server": "", "path_fingerprint": "p2", "diag_count": 0,
				"omitted_items": 2, "omitted_by_chars": 500,
			}},
		// 降级 + 首探针（键存在为 false）+ 多成员 + 降级原因。
		{Type: runtimeevents.EventLSPRequestFinished, SessionID: "s1", At: base.Add(3 * time.Minute),
			Payload: map[string]interface{}{
				"trigger": "inline", "outcome": "degraded_no_fresh", "duration_ms": 40,
				"server": "gopls", "path_fingerprint": "p1", "diag_count": 0,
				"cold_fast_fail": false, "attempted_members": 3,
				"reason_category": "budget_exhausted",
			}},
		// 降级 + 重探（cold_fast_fail=true）。outcome 必须是 degraded_no_fresh：
		// 首探/重探的拆分口径只认这一个 outcome（见 Aggregate），别写成别的降级态。
		{Type: runtimeevents.EventLSPRequestFinished, SessionID: "s2", At: base.Add(4 * time.Minute),
			Payload: map[string]interface{}{
				"trigger": "tool", "outcome": "degraded_no_fresh", "duration_ms": 50,
				"server": "typescriptls", "path_fingerprint": "p3", "diag_count": 1,
				"cold_fast_fail": true, "reason_category": "server_slow",
			}},
		// 冷启动：两个池成员各一次。
		{Type: runtimeevents.EventLSPServerState, SessionID: "s1", At: base,
			Payload: map[string]interface{}{"server": "gopls", "state": "ready", "first_publish_ms": 1200}},
		{Type: runtimeevents.EventLSPServerState, SessionID: "s2", At: base,
			Payload: map[string]interface{}{"server": "typescriptls", "state": "ready", "first_publish_ms": 3000}},
		// 生命周期噪声（无 first_publish_ms）：两条路径都必须忽略。
		{Type: runtimeevents.EventLSPServerState, SessionID: "s1", At: base.Add(time.Minute),
			Payload: map[string]interface{}{"server": "gopls", "state": "starting"}},
		// 覆盖率的分子分母：编辑类 tool.completed，带输出字节。
		{Type: eventToolCompleted, SessionID: "s1", At: base.Add(30 * time.Second),
			Payload: map[string]interface{}{
				"tool_call_id": "tc1", "logical_tool": "apply_patch",
				"output_model_visible_bytes": 1100,
			}},
		{Type: eventToolCompleted, SessionID: "s1", At: base.Add(40 * time.Second),
			Payload: map[string]interface{}{"tool_call_id": "tc2", "logical_tool": "write"}},
		{Type: eventToolCompleted, SessionID: "s2", At: base.Add(50 * time.Second),
			Payload: map[string]interface{}{
				"tool_call_id": "tc3", "logical_tool": "edit",
				"output_model_visible_bytes": 400,
			}},
		// 非编辑类工具：两条路径都必须排除在分母外。
		{Type: eventToolCompleted, SessionID: "s1", At: base.Add(60 * time.Second),
			Payload: map[string]interface{}{
				"tool_call_id": "tc4", "logical_tool": "grep",
				"output_model_visible_bytes": 999999,
			}},
	}
}

// writeFixture 把事件写成 runtime-events.jsonl（回扫路径的事实源形态）。
func writeFixture(t *testing.T, root string, events []fixtureEvent) {
	t.Helper()
	dir := filepath.Join(root, "2026", "09", "29", "session-fixture", "events")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(filepath.Join(dir, "runtime-events.jsonl"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = f.Close() }()
	enc := json.NewEncoder(f)
	for _, event := range events {
		enc.Encode(map[string]interface{}{
			"type": event.Type, "session_id": event.SessionID,
			"timestamp": event.At.Format(time.RFC3339Nano), "payload": event.Payload,
		})
	}
}

// seedStore 把同一批事件经**实时总线**喂进分析库（SQL 路径的事实来源）。
func seedStore(t *testing.T, events []fixtureEvent) *usageanalytics.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "usage_analytics.sqlite")
	bus := runtimeevents.NewBus()
	// 走生产路径 Attach：它同时开库并订阅总线，与线上的接线完全一致，
	// 所以这个测试验的是真实链路而不是测试专用捷径。
	service, err := usageanalytics.Attach(bus, usageanalytics.Options{
		Config: usageanalytics.Config{Path: dbPath},
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	for _, event := range events {
		bus.Publish(runtimeevents.Event{
			Type: event.Type, SessionID: event.SessionID,
			Timestamp: event.At, Payload: event.Payload,
		})
	}
	store := service.Store()
	t.Cleanup(service.Close)
	if store == nil {
		t.Fatal("service.Store() returned nil")
	}
	return store
}

// TestBothSourcesAgreeFieldByField 是本次架构修复的核心验收。
//
// 同一批事件，一条路回扫日志、一条路查分析库，聚合代码是同一份
// （baseline.Aggregate）。因此这里要求的不是"数字接近"，而是**逐字段完全相同**。
//
// 任何一��字段不等，都说明某个字段在 SQL 侧被漏掉、被改了口径，或窗口过滤
// 语义与日志路径不一致 —— 三种都是会让看板悄悄失真的缺陷。
func TestBothSourcesAgreeFieldByField(t *testing.T) {
	events := fixture()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	root := t.TempDir()
	writeFixture(t, root, events)

	fromLog, err := baseline.Analyze(baseline.Options{Roots: []string{root}, Now: now})
	if err != nil {
		t.Fatalf("baseline.Analyze: %v", err)
	}
	store := seedStore(t, events)
	defer func() { _ = store.Close() }()
	fromDB, err := Analyze(store, Options{Now: now})
	if err != nil {
		t.Fatalf("baselinesql.Analyze: %v", err)
	}

	// 先确认 log 路径真的读到了东西，否则"两边都是空"会伪装成一致。
	if fromLog.Requests == 0 {
		t.Fatal("fixture produced no requests from the log path; the comparison would be vacuous")
	}

	// Scan 单独排除：它是**日志扫描自身**的健康度（扫了几个文件/行/用了索引），
	// 不是数据事实，SQL 路径不读日志所以恒为 0。它的归属由
	// TestSQLSourceReportsNoScanHealth 单独钉住，不混进数据面等价性。
	logData, dbData := fromLog, fromDB
	logData.Scan, dbData.Scan = baseline.ScanStats{}, baseline.ScanStats{}
	logJSON, _ := json.MarshalIndent(logData, "", "  ")
	dbJSON, _ := json.MarshalIndent(dbData, "", "  ")
	if string(logJSON) != string(dbJSON) {
		t.Errorf("data facts diverged between the log source and the SQL source\n--- log ---\n%s\n--- db ---\n%s",
			logJSON, dbJSON)
	}

	// 顺带钉住几条关键读数，防止"两边一起错"蒙混过关。
	if fromDB.Requests != 5 {
		t.Errorf("requests = %d, want 5", fromDB.Requests)
	}
	if fromDB.Sessions != 2 {
		t.Errorf("sessions = %d, want 2", fromDB.Sessions)
	}
	if fromDB.ClosureEligible != 1 || fromDB.ClosureClosed != 1 {
		t.Errorf("closure = %d/%d, want 1/1 (injected then clean on the same file)", fromDB.ClosureClosed, fromDB.ClosureEligible)
	}
	if fromDB.Truncated != 1 {
		t.Errorf("truncated_requests = %d, want 1 (the omitted_items payload row)", fromDB.Truncated)
	}
	if fromDB.ColdFirstProbe != 1 || fromDB.ColdRepeat != 1 {
		t.Errorf("cold probe split = %d/%d, want 1/1", fromDB.ColdFirstProbe, fromDB.ColdRepeat)
	}
	if fromDB.ColdFirstPublishP50MS == nil || *fromDB.ColdFirstPublishP50MS == 0 {
		t.Errorf("cold_first_publish_p50 = %v, want a real value (this is the metric that was frozen)", fromDB.ColdFirstPublishP50MS)
	}
	// 追加比 100:1100 ⇒ 编辑调用 3 次、其中带输出字节 2 次共 1500。
	if fromDB.EditCalls != 3 {
		t.Errorf("edit_calls = %d, want 3 (grep must be excluded)", fromDB.EditCalls)
	}
	if fromDB.EditOutputEvs != 2 {
		t.Errorf("edit_output_events = %d, want 2 (only calls with bytes > 0)", fromDB.EditOutputEvs)
	}
	if fromDB.EditOutput != 1500 {
		t.Errorf("edit_output_bytes = %d, want 1500", fromDB.EditOutput)
	}
	// 覆盖率分母只算 LSP 活跃会话内的编辑调用：s1 有 2 次、s2 有 1 次。
	if fromDB.ActiveEdit != 3 {
		t.Errorf("active_edit_calls = %d, want 3", fromDB.ActiveEdit)
	}
}

// TestSQLSourceReportsNoScanHealth 钉住"日志只作调试依据"的落点：
// SQL 源不得把日志扫描量编造成数据。
func TestSQLSourceReportsNoScanHealth(t *testing.T) {
	store := seedStore(t, fixture())
	defer func() { _ = store.Close() }()

	stats, err := Analyze(store, Options{})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if stats.Scan != (baseline.ScanStats{}) {
		t.Errorf("Scan = %+v, want zero: the SQL source never reads a log, so it has no scan health to report",
			stats.Scan)
	}
}

// TestSQLSourceHonorsWindow 锁定窗口过滤与日志路径同语义。
func TestSQLSourceHonorsWindow(t *testing.T) {
	events := fixture()
	store := seedStore(t, events)
	defer func() { _ = store.Close() }()

	base := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	stats, err := Analyze(store, Options{Since: base.Add(2 * time.Minute)})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	// since=+2min 只放行 10:02 及之后：3 条请求（no_server、降级首探、降级重探）。
	if stats.Requests != 3 {
		t.Fatalf("requests = %d, want 3 (window filter)", stats.Requests)
	}
	if stats.FirstAt != base.Add(2*time.Minute).Format(time.RFC3339) {
		t.Errorf("first_at = %q, want %q", stats.FirstAt, base.Add(2*time.Minute).Format(time.RFC3339))
	}
}

// TestSQLSourceOnEmptyStore 不应报错，也不该编造数字。
func TestSQLSourceOnEmptyStore(t *testing.T) {
	store, err := usageanalytics.Open(usageanalytics.Config{
		Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite"),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	stats, err := Analyze(store, Options{})
	if err != nil {
		t.Fatalf("Analyze on empty store: %v", err)
	}
	if stats.Requests != 0 || stats.Sessions != 0 {
		t.Errorf("empty store produced %d requests / %d sessions", stats.Requests, stats.Sessions)
	}
	if stats.LatencyP50MS != nil {
		t.Errorf("latency_p50 = %v, want nil (未采集 must not be rendered as 0)", *stats.LatencyP50MS)
	}
	if stats.ColdFirstPublishP50MS != nil {
		t.Errorf("cold_first_publish_p50 = %v, want nil", *stats.ColdFirstPublishP50MS)
	}
}
