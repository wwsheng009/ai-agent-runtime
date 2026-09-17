package commands

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// ---------------------------------------------------------------------------
// 测试夹具：真实 usageanalytics schema（只 seed 行，不 mock 查询层）。
// ---------------------------------------------------------------------------

// statsTestCreateAnalyticsDB 创建带完整 schema 的分析库（走 usageanalytics
// 的 Open/migrate，保证与生产同构）。
func statsTestCreateAnalyticsDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), usageanalytics.DefaultDBFileName)
	store, err := usageanalytics.Open(usageanalytics.Config{Path: path})
	if err != nil {
		t.Fatalf("create analytics db: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close analytics db: %v", err)
	}
	return path
}

// statsTestExec 在临时分析库上执行 seed 语句（绕开查询层，直接构造行）。
func statsTestExec(t *testing.T, path string, statements ...string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open seeded db: %v", err)
	}
	defer func() { _ = db.Close() }()
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed statement failed: %v\n%s", err, statement)
		}
	}
}

func statsTestSeedAnalytics(t *testing.T, path string) {
	t.Helper()
	now := time.Now().UTC().UnixNano()
	statsTestExec(t, path,
		`INSERT INTO usage_sessions (session_id, title, project_path, working_directory, provider, model, protocol, status, started_at_unix_nano, ended_at_unix_nano, updated_at_unix_nano)
		 VALUES ('sess-1', '修复渲染顺序', 'E:/proj', 'E:/proj', 'openai', 'gpt-5', 'chat', 'completed', `+statsTestInt(now)+`, `+statsTestInt(now)+`, `+statsTestInt(now)+`)`,
		`INSERT INTO usage_requests (llm_request_id, session_id, trace_id, turn_id, step, provider, model, status, success, started_at_unix_nano, prompt_tokens, completion_tokens, total_tokens, usage_available)
		 VALUES ('req-1', 'sess-1', 'trace-1', 'turn-1', 1, 'openai', 'gpt-5', 'completed', 1, `+statsTestInt(now)+`, 100, 20, 120, 1)`,
		`INSERT INTO usage_tool_calls (tool_call_id, session_id, trace_id, turn_id, step, tool_name, source, kind, outcome, ok, empty_result, error_code, retryable, started_at_unix_nano, completed_at_unix_nano, duration_ms)
		 VALUES ('tool-1', 'sess-1', 'trace-1', 'turn-1', 1, 'view', 'builtin', 'read', 'failed', 0, 0, 'timeout', 1, `+statsTestInt(now)+`, `+statsTestInt(now)+`, 1500)`,
		`INSERT INTO usage_subagents (subagent_id, parent_session_id, child_session_id, role, success, completion_reason, failure_category, error_code, attempt, max_attempts, duration_ms, completed_at_unix_nano, source)
		 VALUES ('sub-1', 'sess-1', 'child-1', 'explorer', 0, 'failed', 'timeout', 'timeout', 2, 3, 2500, `+statsTestInt(now)+`, 'live')`,
	)
	// seed 绕过了事件写入路径（增量维护），这里显式重建预聚合统计，
	// 等价于生产中的迁移回填/rebuild-stats，保证 fixture 与真实库同构。
	store, err := usageanalytics.Open(usageanalytics.Config{Path: path})
	if err != nil {
		t.Fatalf("reopen seeded analytics db: %v", err)
	}
	if err := store.RebuildAllSessionStats(); err != nil {
		t.Fatalf("rebuild seeded analytics stats: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seeded analytics db: %v", err)
	}
}

func statsTestInt(value int64) string {
	return fmt.Sprintf("%d", value)
}

// statsTestCaptureExit 拦截 statsExitHook，返回读取到的退出码。
// 初值 0 表示"未触发退出钩子"，即成功路径（statsExit(0) 不调用钩子）。
func statsTestCaptureExit(t *testing.T) *int {
	t.Helper()
	code := statsExitOK
	previous := statsExitHook
	statsExitHook = func(value int) { code = value }
	t.Cleanup(func() { statsExitHook = previous })
	return &code
}

// ---------------------------------------------------------------------------
// 空库降级
// ---------------------------------------------------------------------------

func TestStatsSessionsEmptyDatabaseDegrades(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.sqlite")

	var text bytes.Buffer
	code := runStatsSessions(statsSessionsOptions{
		statsOutput: statsOutput{out: &text, errOut: &text},
		dbPath:      missing,
		limit:       10,
	})
	if code != statsExitOK {
		t.Fatalf("空库退出码 = %d, want %d（输出: %s）", code, statsExitOK, text.String())
	}
	if !strings.Contains(text.String(), "暂无数据") {
		t.Fatalf("空库文本输出缺少「暂无数据」: %s", text.String())
	}

	var raw bytes.Buffer
	code = runStatsSessions(statsSessionsOptions{
		statsOutput: statsOutput{json: true, out: &raw, errOut: &raw},
		dbPath:      missing,
		limit:       10,
	})
	if code != statsExitOK {
		t.Fatalf("空库 JSON 退出码 = %d, want %d", code, statsExitOK)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw.Bytes(), &payload); err != nil {
		t.Fatalf("空库 JSON 解析失败: %v\n%s", err, raw.String())
	}
	sessions, ok := payload["sessions"].([]interface{})
	if !ok || len(sessions) != 0 {
		t.Fatalf("空库 sessions 应为空数组: %s", raw.String())
	}
	if count, _ := payload["count"].(float64); count != 0 {
		t.Fatalf("空库 count = %v, want 0", payload["count"])
	}
}

func TestStatsSessionUnknownSessionDegrades(t *testing.T) {
	path := statsTestCreateAnalyticsDB(t)
	statsTestSeedAnalytics(t, path)

	var text bytes.Buffer
	code := runStatsSession(statsSessionOptions{
		statsOutput: statsOutput{out: &text, errOut: &text},
		dbPath:      path,
		sessionID:   "session-does-not-exist",
	})
	if code != statsExitOK {
		t.Fatalf("未知会话退出码 = %d, want %d（输出: %s）", code, statsExitOK, text.String())
	}
	if !strings.Contains(text.String(), "暂无数据") {
		t.Fatalf("未知会话文本输出缺少「暂无数据」: %s", text.String())
	}

	var raw bytes.Buffer
	code = runStatsSession(statsSessionOptions{
		statsOutput: statsOutput{json: true, out: &raw, errOut: &raw},
		dbPath:      path,
		sessionID:   "session-does-not-exist",
	})
	if code != statsExitOK {
		t.Fatalf("未知会话 JSON 退出码 = %d, want %d", code, statsExitOK)
	}
	var detail usageanalytics.SessionUsageDetail
	if err := json.Unmarshal(raw.Bytes(), &detail); err != nil {
		t.Fatalf("未知会话 JSON 解析失败: %v\n%s", err, raw.String())
	}
	if detail.Session.SessionID != "session-does-not-exist" {
		t.Fatalf("未知会话 JSON session_id = %q", detail.Session.SessionID)
	}
	if detail.Steps == nil || detail.Turns == nil || detail.Tools == nil || detail.Subagents == nil || detail.ErrorPatterns == nil {
		t.Fatalf("未知会话 JSON 数组应为空数组而非 null: %s", raw.String())
	}
}

// ---------------------------------------------------------------------------
// 字段渲染与 --json 稳定性
// ---------------------------------------------------------------------------

func TestStatsSessionsRendersSeededSession(t *testing.T) {
	path := statsTestCreateAnalyticsDB(t)
	statsTestSeedAnalytics(t, path)

	var text bytes.Buffer
	code := runStatsSessions(statsSessionsOptions{
		statsOutput: statsOutput{out: &text, errOut: &text},
		dbPath:      path,
		limit:       10,
	})
	if code != statsExitOK {
		t.Fatalf("退出码 = %d, want %d（输出: %s）", code, statsExitOK, text.String())
	}
	for _, want := range []string{"sess-1", "修复渲染顺序", "gpt-5", "120"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("文本输出缺少 %q:\n%s", want, text.String())
		}
	}

	var raw bytes.Buffer
	code = runStatsSessions(statsSessionsOptions{
		statsOutput: statsOutput{json: true, out: &raw, errOut: &raw},
		dbPath:      path,
		limit:       10,
	})
	if code != statsExitOK {
		t.Fatalf("JSON 退出码 = %d, want %d", code, statsExitOK)
	}
	// --json 字段名必须与 usageanalytics 结构体 tag 同名（稳定契约）。
	for _, key := range []string{
		`"session_id"`, `"total_requests"`, `"total_tokens"`,
		`"tool_failures"`, `"subagent_failures"`, `"usage_quality"`,
	} {
		if !strings.Contains(raw.String(), key) {
			t.Fatalf("sessions JSON 缺少稳定字段 %s:\n%s", key, raw.String())
		}
	}
	var result usageanalytics.ListResult
	if err := json.Unmarshal(raw.Bytes(), &result); err != nil {
		t.Fatalf("JSON 解析失败: %v\n%s", err, raw.String())
	}
	if result.Count != 1 || len(result.Sessions) != 1 {
		t.Fatalf("sessions JSON 条数 = %d/%d, want 1/1", result.Count, len(result.Sessions))
	}
	session := result.Sessions[0]
	if session.SessionID != "sess-1" || session.TotalRequests != 1 || session.TotalTokens != 120 {
		t.Fatalf("sessions JSON 字段渲染异常: %+v", session)
	}
	if session.ToolCallsObserved != 1 || session.ToolFailures != 1 {
		t.Fatalf("sessions JSON 工具维度异常: %+v", session)
	}
	if session.SubagentRuns != 1 || session.SubagentFailures != 1 {
		t.Fatalf("sessions JSON 子代理维度异常: %+v", session)
	}
}

func TestStatsSessionDetailRendersAndJSONStable(t *testing.T) {
	path := statsTestCreateAnalyticsDB(t)
	statsTestSeedAnalytics(t, path)

	var text bytes.Buffer
	code := runStatsSession(statsSessionOptions{
		statsOutput: statsOutput{out: &text, errOut: &text},
		dbPath:      path,
		sessionID:   "sess-1",
	})
	if code != statsExitOK {
		t.Fatalf("退出码 = %d, want %d（输出: %s）", code, statsExitOK, text.String())
	}
	for _, want := range []string{"sess-1", "view", "sub-1", "timeout"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("会话明细文本缺少 %q:\n%s", want, text.String())
		}
	}

	var raw bytes.Buffer
	code = runStatsSession(statsSessionOptions{
		statsOutput: statsOutput{json: true, out: &raw, errOut: &raw},
		dbPath:      path,
		sessionID:   "sess-1",
	})
	if code != statsExitOK {
		t.Fatalf("JSON 退出码 = %d, want %d", code, statsExitOK)
	}
	for _, key := range []string{
		`"session_id"`, `"tool_name"`, `"p95_duration_ms"`,
		`"completion_reason"`, `"failure_category"`, `"error_patterns"`,
	} {
		if !strings.Contains(raw.String(), key) {
			t.Fatalf("会话明细 JSON 缺少稳定字段 %s:\n%s", key, raw.String())
		}
	}
	var detail usageanalytics.SessionUsageDetail
	if err := json.Unmarshal(raw.Bytes(), &detail); err != nil {
		t.Fatalf("JSON 解析失败: %v\n%s", err, raw.String())
	}
	if len(detail.Tools) != 1 || detail.Tools[0].ToolName != "view" || detail.Tools[0].Failures != 1 {
		t.Fatalf("会话明细工具维度异常: %+v", detail.Tools)
	}
	if len(detail.Subagents) != 1 || detail.Subagents[0].SubagentID != "sub-1" {
		t.Fatalf("会话明细子代理维度异常: %+v", detail.Subagents)
	}
	if len(detail.ErrorPatterns) == 0 {
		t.Fatalf("会话明细失败模式为空: %+v", detail.ErrorPatterns)
	}
}

func TestStatsSubagentsFailedOnlyAndJSONStable(t *testing.T) {
	path := statsTestCreateAnalyticsDB(t)
	now := time.Now().UTC().UnixNano()
	statsTestExec(t, path,
		`INSERT INTO usage_subagents (subagent_id, parent_session_id, role, success, completion_reason, failure_category, attempt, max_attempts, duration_ms, completed_at_unix_nano, source)
		 VALUES ('sub-failed', 'sess-1', 'explorer', 0, 'failed', 'timeout', 2, 3, 1000, `+statsTestInt(now)+`, 'live')`,
		`INSERT INTO usage_subagents (subagent_id, parent_session_id, role, success, completion_reason, attempt, max_attempts, duration_ms, completed_at_unix_nano, source)
		 VALUES ('sub-ok', 'sess-1', 'writer', 1, 'completed', 1, 1, 500, `+statsTestInt(now-1)+`, 'live')`,
	)

	var text bytes.Buffer
	code := runStatsSubagents(statsSubagentsOptions{
		statsOutput: statsOutput{out: &text, errOut: &text},
		dbPath:      path,
		limit:       10,
		failedOnly:  true,
	})
	if code != statsExitOK {
		t.Fatalf("退出码 = %d, want %d（输出: %s）", code, statsExitOK, text.String())
	}
	if !strings.Contains(text.String(), "sub-failed") || strings.Contains(text.String(), "sub-ok") {
		t.Fatalf("--failed-only 过滤结果异常:\n%s", text.String())
	}
	if !strings.Contains(text.String(), "failure_rate=100.0%") {
		t.Fatalf("子代理摘要缺少失败率:\n%s", text.String())
	}

	var raw bytes.Buffer
	code = runStatsSubagents(statsSubagentsOptions{
		statsOutput: statsOutput{json: true, out: &raw, errOut: &raw},
		dbPath:      path,
		limit:       10,
		failedOnly:  true,
	})
	if code != statsExitOK {
		t.Fatalf("JSON 退出码 = %d, want %d", code, statsExitOK)
	}
	for _, key := range []string{`"subagents"`, `"failure_rate"`, `"failure_categories"`, `"max_attempts"`} {
		if !strings.Contains(raw.String(), key) {
			t.Fatalf("subagents JSON 缺少稳定字段 %s:\n%s", key, raw.String())
		}
	}
	var result usageanalytics.SubagentStatsResult
	if err := json.Unmarshal(raw.Bytes(), &result); err != nil {
		t.Fatalf("JSON 解析失败: %v\n%s", err, raw.String())
	}
	if len(result.Subagents) != 1 || result.Subagents[0].SubagentID != "sub-failed" {
		t.Fatalf("--failed-only JSON 过滤结果异常: %+v", result.Subagents)
	}
	if result.Summary.Failed != 1 || result.Summary.Succeeded != 0 || result.Summary.FailureRate != 1 {
		t.Fatalf("子代理摘要异常: %+v", result.Summary)
	}
	if result.Subagents[0].Success == nil || *result.Subagents[0].Success {
		t.Fatalf("失败子代理 success 字段异常: %+v", result.Subagents[0])
	}
}

func TestStatsErrorsTopAndJSONStable(t *testing.T) {
	path := statsTestCreateAnalyticsDB(t)
	now := time.Now().UTC().UnixNano()
	statsTestExec(t, path,
		`INSERT INTO usage_tool_calls (tool_call_id, session_id, tool_name, outcome, ok, error_code, started_at_unix_nano, completed_at_unix_nano)
		 VALUES ('tool-a', 'sess-1', 'view', 'failed', 0, 'timeout', `+statsTestInt(now)+`, `+statsTestInt(now)+`)`,
		`INSERT INTO usage_tool_calls (tool_call_id, session_id, tool_name, outcome, ok, error_code, started_at_unix_nano, completed_at_unix_nano)
		 VALUES ('tool-b', 'sess-1', 'view', 'failed', 0, 'permission_denied', `+statsTestInt(now)+`, `+statsTestInt(now)+`)`,
		`INSERT INTO usage_tool_calls (tool_call_id, session_id, tool_name, outcome, ok, error_code, started_at_unix_nano, completed_at_unix_nano)
		 VALUES ('tool-c', 'sess-1', 'view', 'failed', 0, 'timeout', `+statsTestInt(now)+`, `+statsTestInt(now)+`)`,
	)

	var text bytes.Buffer
	code := runStatsErrors(statsErrorsOptions{
		statsOutput: statsOutput{out: &text, errOut: &text},
		dbPath:      path,
		top:         1,
	})
	if code != statsExitOK {
		t.Fatalf("退出码 = %d, want %d（输出: %s）", code, statsExitOK, text.String())
	}
	if !strings.Contains(text.String(), "timeout") || strings.Contains(text.String(), "permission_denied") {
		t.Fatalf("errors --top 1 输出异常:\n%s", text.String())
	}

	var raw bytes.Buffer
	code = runStatsErrors(statsErrorsOptions{
		statsOutput: statsOutput{json: true, out: &raw, errOut: &raw},
		dbPath:      path,
		top:         1,
	})
	if code != statsExitOK {
		t.Fatalf("JSON 退出码 = %d, want %d", code, statsExitOK)
	}
	for _, key := range []string{`"patterns"`, `"error_code"`, `"failure_category"`, `"count"`} {
		if !strings.Contains(raw.String(), key) {
			t.Fatalf("errors JSON 缺少稳定字段 %s:\n%s", key, raw.String())
		}
	}
	var result usageanalytics.ErrorPatternsResult
	if err := json.Unmarshal(raw.Bytes(), &result); err != nil {
		t.Fatalf("JSON 解析失败: %v\n%s", err, raw.String())
	}
	if len(result.Patterns) != 1 || result.Patterns[0].ErrorCode != "timeout" || result.Patterns[0].Count != 2 {
		t.Fatalf("errors JSON 聚合异常: %+v", result.Patterns)
	}
}

// ---------------------------------------------------------------------------
// 退出码（0/1/2）与参数校验
// ---------------------------------------------------------------------------

func TestStatsCommandExitCodes(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.sqlite")
	corrupt := filepath.Join(dir, "corrupt.sqlite")
	if err := os.WriteFile(corrupt, []byte("this is not a sqlite database"), 0o600); err != nil {
		t.Fatalf("write corrupt db: %v", err)
	}

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"limit 0 参数错误", []string{"sessions", "--limit", "0"}, statsExitUsage},
		{"top 负值参数错误", []string{"errors", "--top", "-3"}, statsExitUsage},
		{"subagents limit 超上限", []string{"subagents", "--limit", "9999"}, statsExitUsage},
		{"session 空白 ID 参数错误", []string{"session", "   "}, statsExitUsage},
		{"空库降级", []string{"sessions", "--db", missing}, statsExitOK},
		{"空库 errors 降级", []string{"errors", "--db", missing}, statsExitOK},
		{"空库 subagents 降级", []string{"subagents", "--db", missing}, statsExitOK},
		{"损坏库确定性错误", []string{"sessions", "--db", corrupt}, statsExitDeterministic},
		{"doctor 损坏库确定性错误", []string{"doctor", "--db", corrupt, "--session-db", missing}, statsExitDeterministic},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code := statsTestCaptureExit(t)
			var buf bytes.Buffer
			cmd := NewStatsCommand()
			cmd.SetOut(&buf)
			cmd.SetErr(&buf)
			cmd.SetArgs(tc.args)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute() 返回错误: %v\n%s", err, buf.String())
			}
			if *code != tc.want {
				t.Fatalf("退出码 = %d, want %d（输出: %s）", *code, tc.want, buf.String())
			}
		})
	}
}

func TestStatsCommandArgValidation(t *testing.T) {
	run := func(args ...string) error {
		var buf bytes.Buffer
		cmd := NewStatsCommand()
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		cmd.SetArgs(args)
		return cmd.Execute()
	}
	if err := run("sessions", "extra-arg"); err == nil {
		t.Fatal("sessions 多余参数应返回错误（cobra Args 校验，主进程退出码 1）")
	}
	if err := run("session"); err == nil {
		t.Fatal("session 缺少 <session-id> 应返回错误（cobra Args 校验）")
	}
	if err := run("unknown-subcommand"); err == nil {
		t.Fatal("未知子命令应返回错误")
	}
}

func TestStatsCommandSurface(t *testing.T) {
	cmd := NewStatsCommand()
	for _, name := range []string{"sessions", "session", "errors", "subagents", "doctor"} {
		child, _, err := cmd.Find([]string{name})
		if err != nil || child == nil || child.Name() != name {
			t.Fatalf("缺少子命令 %s（err=%v）", name, err)
		}
	}
	for _, name := range []string{"db", "json"} {
		if cmd.PersistentFlags().Lookup(name) == nil {
			t.Fatalf("stats 缺少 --%s 覆盖", name)
		}
	}
	sessions, _, _ := cmd.Find([]string{"sessions"})
	if sessions.Flags().Lookup("limit") == nil {
		t.Fatal("stats sessions 缺少 --limit")
	}
	errorsCmd, _, _ := cmd.Find([]string{"errors"})
	if errorsCmd.Flags().Lookup("top") == nil {
		t.Fatal("stats errors 缺少 --top")
	}
	subagents, _, _ := cmd.Find([]string{"subagents"})
	if subagents.Flags().Lookup("failed-only") == nil || subagents.Flags().Lookup("limit") == nil {
		t.Fatal("stats subagents 缺少 --failed-only/--limit")
	}
	doctor, _, _ := cmd.Find([]string{"doctor"})
	if doctor.Flags().Lookup("session-db") == nil {
		t.Fatal("stats doctor 缺少 --session-db")
	}
}

func TestStatsUsesEnvDatabaseWhenNoFlag(t *testing.T) {
	path := statsTestCreateAnalyticsDB(t)
	statsTestSeedAnalytics(t, path)
	t.Setenv(usageanalytics.EnvDBPath, path)

	var text bytes.Buffer
	code := runStatsSessions(statsSessionsOptions{
		statsOutput: statsOutput{out: &text, errOut: &text},
		limit:       5,
	})
	if code != statsExitOK || !strings.Contains(text.String(), "sess-1") {
		t.Fatalf("env 库路径未生效（code=%d）:\n%s", code, text.String())
	}

	// --db 优先级高于 env。
	other := filepath.Join(t.TempDir(), "other.sqlite")
	var override bytes.Buffer
	code = runStatsSessions(statsSessionsOptions{
		statsOutput: statsOutput{out: &override, errOut: &override},
		dbPath:      other,
		limit:       5,
	})
	if code != statsExitOK || !strings.Contains(override.String(), "暂无数据") {
		t.Fatalf("--db 未覆盖 env（code=%d）:\n%s", code, override.String())
	}
}

// ---------------------------------------------------------------------------
// stats doctor
// ---------------------------------------------------------------------------

func TestStatsDoctorReportsTableCountsAndEventTypes(t *testing.T) {
	dir := t.TempDir()
	analyticsPath := filepath.Join(dir, "usage_analytics.sqlite") // 不创建 = 空库降级
	sessionPath := filepath.Join(dir, "session_runtime.sqlite")

	db, err := sql.Open("sqlite3", sessionPath)
	if err != nil {
		t.Fatalf("open session db: %v", err)
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS session_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			type TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`INSERT INTO session_events (session_id, seq, type, created_at) VALUES ('s1', 1, 'tool.completed', '2026-09-17T01:00:00Z')`,
		`INSERT INTO session_events (session_id, seq, type, created_at) VALUES ('s1', 2, 'tool.requested', '2026-09-17T01:30:00Z')`,
		`INSERT INTO session_events (session_id, seq, type, created_at) VALUES ('s1', 3, 'subagent.completed', '2026-09-17T02:00:00Z')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("seed session db: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close session db: %v", err)
	}

	var raw bytes.Buffer
	code := runStatsDoctor(statsDoctorOptions{
		statsOutput: statsOutput{json: true, out: &raw, errOut: &raw},
		dbPath:      analyticsPath,
		sessionDB:   sessionPath,
	})
	if code != statsExitOK {
		t.Fatalf("doctor 退出码 = %d, want %d（输出: %s）", code, statsExitOK, raw.String())
	}
	var report statsDoctorReport
	if err := json.Unmarshal(raw.Bytes(), &report); err != nil {
		t.Fatalf("doctor JSON 解析失败: %v\n%s", err, raw.String())
	}
	if report.AnalyticsDB.Exists || report.AnalyticsDB.Readable {
		t.Fatalf("缺失的分析库应报告 exists=false: %+v", report.AnalyticsDB)
	}
	if !report.SessionDB.Exists || !report.SessionDB.Readable {
		t.Fatalf("会话库应可读: %+v", report.SessionDB)
	}
	if got := statsTableCountValue(report.SessionDB, "session_events"); got != 3 {
		t.Fatalf("session_events 行数 = %d, want 3", got)
	}
	if got := statsEventTypeCount(report.SessionDB, "tool.completed"); got != 1 {
		t.Fatalf("tool.completed 行数 = %d, want 1", got)
	}
	if report.SessionDB.LastEventAt != "2026-09-17T02:00:00Z" {
		t.Fatalf("最近事件时间 = %q", report.SessionDB.LastEventAt)
	}
	warnings := strings.Join(report.Warnings, "\n")
	if !strings.Contains(warnings, "分析库不存在") {
		t.Fatalf("缺少分析库缺失提示:\n%s", warnings)
	}
	if !strings.Contains(warnings, "G3") {
		t.Fatalf("缺少工具回执断点提示（G3）:\n%s", warnings)
	}

	var text bytes.Buffer
	code = runStatsDoctor(statsDoctorOptions{
		statsOutput: statsOutput{out: &text, errOut: &text},
		dbPath:      analyticsPath,
		sessionDB:   sessionPath,
	})
	if code != statsExitOK {
		t.Fatalf("doctor 文本退出码 = %d, want %d", code, statsExitOK)
	}
	for _, want := range []string{"采集健康自检", "暂无数据（文件不存在）", "session_events", "事件类型", "tool.completed"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("doctor 文本缺少 %q:\n%s", want, text.String())
		}
	}
}

func TestStatsDoctorCorruptDatabase(t *testing.T) {
	corrupt := filepath.Join(t.TempDir(), "corrupt.sqlite")
	if err := os.WriteFile(corrupt, []byte("this is not a sqlite database"), 0o600); err != nil {
		t.Fatalf("write corrupt db: %v", err)
	}
	var buf bytes.Buffer
	code := runStatsDoctor(statsDoctorOptions{
		statsOutput: statsOutput{out: &buf, errOut: &buf},
		dbPath:      corrupt,
		sessionDB:   filepath.Join(t.TempDir(), "missing.sqlite"),
	})
	if code != statsExitDeterministic {
		t.Fatalf("损坏库退出码 = %d, want %d（输出: %s）", code, statsExitDeterministic, buf.String())
	}
	if !strings.Contains(buf.String(), "不可读") {
		t.Fatalf("doctor 文本未标记不可读:\n%s", buf.String())
	}
}
