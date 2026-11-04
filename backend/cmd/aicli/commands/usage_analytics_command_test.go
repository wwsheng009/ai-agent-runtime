package commands

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

// TestUsageAnalyticsRebuildStatsRepairsDrift 锁定方案 §6.3：
// 人为漂移经 CLI rebuild-stats 后计数与原始表一致。
func TestUsageAnalyticsRebuildStatsRepairsDrift(t *testing.T) {
	path := statsTestCreateAnalyticsDB(t)
	statsTestSeedAnalytics(t, path)
	// 人为制造漂移：seed 绕过了增量维护，计数列为 0。
	statsTestExec(t, path, `UPDATE usage_sessions SET c_total_requests = 99, c_turn_count = 9 WHERE session_id = 'sess-1'`)

	var buf bytes.Buffer
	code := runUsageAnalyticsRebuildStats(usageAnalyticsRebuildOptions{
		dbPath: path,
		all:    true,
		out:    &buf,
		errOut: &buf,
	})
	if code != statsExitOK {
		t.Fatalf("rebuild --all 退出码 = %d, want 0; output=%s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "重建完成") {
		t.Fatalf("输出缺少完成提示: %s", buf.String())
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	var requests, turns, failed int
	if err := db.QueryRow(`SELECT c_total_requests, c_turn_count, c_failed_turns FROM usage_sessions WHERE session_id='sess-1'`).
		Scan(&requests, &turns, &failed); err != nil {
		t.Fatalf("read stats: %v", err)
	}
	if requests != 1 || turns != 1 || failed != 0 {
		t.Fatalf("rebuild 结果 = %d/%d/%d, want 1/1/0", requests, turns, failed)
	}
}

// TestUsageAnalyticsRebuildStatsScopeValidation 锁定参数校验（退出码 1）。
func TestUsageAnalyticsRebuildStatsScopeValidation(t *testing.T) {
	var buf bytes.Buffer
	if code := runUsageAnalyticsRebuildStats(usageAnalyticsRebuildOptions{out: &buf, errOut: &buf}); code != statsExitUsage {
		t.Fatalf("缺少 scope 的退出码 = %d, want %d", code, statsExitUsage)
	}

	path := statsTestCreateAnalyticsDB(t)
	buf.Reset()
	code := runUsageAnalyticsRebuildStats(usageAnalyticsRebuildOptions{
		dbPath:    path,
		sessionID: "sess-1",
		all:       true,
		json:      true,
		out:       &buf,
		errOut:    &buf,
	})
	if code != statsExitUsage {
		t.Fatalf("同时给出 --session/--all 的退出码 = %d, want %d", code, statsExitUsage)
	}

	// 单会话 scope 的 JSON 输出。
	buf.Reset()
	code = runUsageAnalyticsRebuildStats(usageAnalyticsRebuildOptions{
		dbPath:    path,
		sessionID: "sess-missing",
		json:      true,
		out:       &buf,
		errOut:    &buf,
	})
	if code != statsExitOK {
		t.Fatalf("--session 退出码 = %d, want 0; output=%s", code, buf.String())
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("JSON 输出解析失败: %v (%s)", err, buf.String())
	}
	if payload["scope"] != "session" || payload["rebuilt"] != true {
		t.Fatalf("JSON 输出字段不符: %v", payload)
	}
}

// TestUsageAnalyticsCommandSurface 锁定命令注册面。
func TestUsageAnalyticsCommandSurface(t *testing.T) {
	cmd := NewUsageAnalyticsCommand()
	child, _, err := cmd.Find([]string{"rebuild-stats"})
	if err != nil || child == nil || child.Name() != "rebuild-stats" {
		t.Fatalf("rebuild-stats 子命令缺失: child=%v err=%v", child, err)
	}
	child, _, err = cmd.Find([]string{"compact-records"})
	if err != nil || child == nil || child.Name() != "compact-records" {
		t.Fatalf("compact-records 子命令缺失: child=%v err=%v", child, err)
	}
	child, _, err = cmd.Find([]string{"prune"})
	if err != nil || child == nil || child.Name() != "prune" {
		t.Fatalf("prune 子命令缺失: child=%v err=%v", child, err)
	}
	for _, name := range []string{"rebuild-stats", "compact-records", "prune"} {
		child, _, err := cmd.Find([]string{name})
		if err != nil || child == nil || child.Flags().Lookup("json") == nil {
			t.Fatalf("%s 缺少 --json flag: child=%v err=%v", name, child, err)
		}
	}
}

// TestUsageAnalyticsPruneRemovesOldRequestsAndRebuildsStats 锁定 §13 Phase 4：
// 保留期清理只删旧明细，统计按剩余请求重建。
func TestUsageAnalyticsPruneRemovesOldRequestsAndRebuildsStats(t *testing.T) {
	path := statsTestCreateAnalyticsDB(t)
	oldAt := time.Date(2025, 12, 1, 9, 0, 0, 0, time.UTC).UnixNano()
	newAt := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC).UnixNano()
	statsTestExec(t, path,
		`INSERT INTO usage_sessions (session_id, provider, model, status, started_at_unix_nano, updated_at_unix_nano)
		 VALUES ('s-prune', 'acme', 'm1', 'completed', `+statsTestInt(oldAt)+`, `+statsTestInt(newAt)+`)`,
		`INSERT INTO usage_requests (llm_request_id, session_id, trace_id, turn_id, step, provider, model, status, success, started_at_unix_nano, total_tokens, usage_available)
		 VALUES ('r-old', 's-prune', 't-old', 'turn-old', 1, 'acme', 'm1', 'success', 1, `+statsTestInt(oldAt)+`, 10, 1)`,
		`INSERT INTO usage_requests (llm_request_id, session_id, trace_id, turn_id, step, provider, model, status, success, started_at_unix_nano, total_tokens, usage_available)
		 VALUES ('r-new', 's-prune', 't-new', 'turn-new', 1, 'acme', 'm1', 'success', 1, `+statsTestInt(newAt)+`, 20, 1)`,
	)
	// 先重建一次，让预聚合列就位。
	var buf bytes.Buffer
	if code := runUsageAnalyticsRebuildStats(usageAnalyticsRebuildOptions{dbPath: path, all: true, out: &buf, errOut: &buf}); code != statsExitOK {
		t.Fatalf("预置 rebuild 退出码 = %d; %s", code, buf.String())
	}

	buf.Reset()
	code := runUsageAnalyticsPrune(usageAnalyticsPruneOptions{
		dbPath: path,
		before: "2026-01-01",
		json:   true,
		out:    &buf,
		errOut: &buf,
	})
	if code != statsExitOK {
		t.Fatalf("prune 退出码 = %d, want 0; output=%s", code, buf.String())
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("prune JSON 解析失败: %v (%s)", err, buf.String())
	}
	if payload["deleted_requests"] != float64(1) {
		t.Fatalf("deleted_requests = %v, want 1", payload["deleted_requests"])
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	var remaining, requests, tokens int
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_requests WHERE session_id='s-prune'`).Scan(&remaining); err != nil {
		t.Fatalf("count requests: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("剩余请求 = %d, want 1", remaining)
	}
	if err := db.QueryRow(`SELECT c_total_requests, c_total_tokens FROM usage_sessions WHERE session_id='s-prune'`).
		Scan(&requests, &tokens); err != nil {
		t.Fatalf("read rebuilt stats: %v", err)
	}
	if requests != 1 || tokens != 20 {
		t.Fatalf("重建统计 = %d/%d, want 1/20", requests, tokens)
	}
}

// TestUsageAnalyticsPruneRequiresBefore 锁定参数校验（退出码 1）。
func TestUsageAnalyticsPruneRequiresBefore(t *testing.T) {
	var buf bytes.Buffer
	if code := runUsageAnalyticsPrune(usageAnalyticsPruneOptions{out: &buf, errOut: &buf}); code != statsExitUsage {
		t.Fatalf("缺少 --before 的退出码 = %d, want %d", code, statsExitUsage)
	}
}

// TestUsageAnalyticsPruneRejectsVacuumFlag 锁定：在线文件级压缩已移除，
// --vacuum 只保留迁移提示并以用法错误退出。
func TestUsageAnalyticsPruneRejectsVacuumFlag(t *testing.T) {
	var buf bytes.Buffer
	code := runUsageAnalyticsPrune(usageAnalyticsPruneOptions{vacuum: true, out: &buf, errOut: &buf})
	if code != statsExitUsage {
		t.Fatalf("prune --vacuum 退出码 = %d, want %d", code, statsExitUsage)
	}
	if !strings.Contains(buf.String(), "storage compact") {
		t.Fatalf("拒绝提示应指向 storage compact: %s", buf.String())
	}
}

func TestUsageAnalyticsCompactRecordsDryRunAndRewrite(t *testing.T) {
	path := statsTestCreateAnalyticsDB(t)
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	record, err := json.Marshal(map[string]interface{}{
		"tool_call_id":               "tool-large",
		"output_original_bytes":      99999,
		"output_model_visible_bytes": 1234,
		"output":                     strings.Repeat("x", 20000),
	})
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO usage_tool_calls
		(tool_call_id, session_id, tool_name, record_json)
		VALUES ('tool-large', 's-compact', 'view', ?)`, record); err != nil {
		t.Fatalf("seed tool record: %v", err)
	}

	var buf bytes.Buffer
	code := runUsageAnalyticsCompactRecords(usageAnalyticsCompactOptions{
		dbPath:    path,
		maxBytes:  1024,
		batchSize: 1,
		dryRun:    true,
		json:      true,
		out:       &buf,
		errOut:    &buf,
	})
	if code != statsExitOK {
		t.Fatalf("dry-run 退出码 = %d; %s", code, buf.String())
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("dry-run JSON 解析失败: %v (%s)", err, buf.String())
	}
	stats := payload["stats"].(map[string]interface{})
	if stats["rewritten"] != float64(1) || stats["dry_run"] != true {
		t.Fatalf("dry-run stats = %v", stats)
	}

	buf.Reset()
	code = runUsageAnalyticsCompactRecords(usageAnalyticsCompactOptions{
		dbPath:    path,
		maxBytes:  1024,
		batchSize: 1,
		out:       &buf,
		errOut:    &buf,
	})
	if code != statsExitOK {
		t.Fatalf("compact 退出码 = %d; %s", code, buf.String())
	}
	var raw string
	if err := db.QueryRow(`SELECT record_json FROM usage_tool_calls WHERE tool_call_id='tool-large'`).Scan(&raw); err != nil {
		t.Fatalf("read compacted record: %v", err)
	}
	if strings.Contains(raw, `"output"`) || !strings.Contains(raw, `"output_model_visible_bytes"`) {
		t.Fatalf("compacted record = %s", raw)
	}
}
