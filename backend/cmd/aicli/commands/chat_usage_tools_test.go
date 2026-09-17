package commands

// 批次 7.3 / §9.3 T2：/usage 聚合视图（tools/subagents/errors）与采集健康首行。
// 渲染断言走假实现（usageAnalyticsSource 窄接口）；空库/降级/查询错误走真实
// usageanalytics schema（临时库 seed 行），与 stats 测试同一口径。

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// ---------------------------------------------------------------------------
// 假实现：只实现 /usage 聚合视图需要的最小查询面。
// ---------------------------------------------------------------------------

type fakeUsageAnalyticsSource struct {
	health    usageanalytics.AnalyticsHealth
	tools     usageanalytics.ToolStatsResult
	toolsErr  error
	subagents usageanalytics.SubagentStatsResult
	subErr    error
	patterns  usageanalytics.ErrorPatternsResult
	patErr    error

	lastToolQuery     usageanalytics.ToolStatsQuery
	lastSubagentQuery usageanalytics.SubagentStatsQuery
	lastErrorsQuery   usageanalytics.ErrorPatternsQuery
}

func (f *fakeUsageAnalyticsSource) AnalyticsHealth() usageanalytics.AnalyticsHealth {
	return f.health
}

func (f *fakeUsageAnalyticsSource) ToolStats(q usageanalytics.ToolStatsQuery) (usageanalytics.ToolStatsResult, error) {
	f.lastToolQuery = q
	return f.tools, f.toolsErr
}

func (f *fakeUsageAnalyticsSource) SubagentStats(q usageanalytics.SubagentStatsQuery) (usageanalytics.SubagentStatsResult, error) {
	f.lastSubagentQuery = q
	return f.subagents, f.subErr
}

func (f *fakeUsageAnalyticsSource) ErrorPatterns(q usageanalytics.ErrorPatternsQuery) (usageanalytics.ErrorPatternsResult, error) {
	f.lastErrorsQuery = q
	return f.patterns, f.patErr
}

func usageToolsContainsAll(t *testing.T, label, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Fatalf("%s missing %q:\n%s", label, want, text)
		}
	}
}

func usageToolsBoolPtr(v bool) *bool { return &v }

// ---------------------------------------------------------------------------
// 采集健康首行
// ---------------------------------------------------------------------------

func TestRenderUsageAnalyticsHealthLine(t *testing.T) {
	// 未挂载：`--` + 暂无数据，不报错。
	if got := renderUsageAnalyticsHealthLine(nil); got != usageHealthNoDataLine {
		t.Fatalf("nil source health line = %q", got)
	}
	// 只读降级（库/表缺失）同文案。
	degraded := &fakeUsageAnalyticsSource{health: usageanalytics.AnalyticsHealth{Degraded: true}}
	if got := renderUsageAnalyticsHealthLine(degraded); got != usageHealthNoDataLine {
		t.Fatalf("degraded health line = %q", got)
	}
	// 已挂载但空库：保留 attached 事实，同时给出 `--` 与「暂无数据」。
	empty := &fakeUsageAnalyticsSource{}
	if got := renderUsageAnalyticsHealthLine(empty); !strings.Contains(got, "attached") ||
		!strings.Contains(got, "暂无数据") || !strings.Contains(got, "--") {
		t.Fatalf("empty attached health line = %q", got)
	}
	// 已挂载有数据：行数 + 最近写入时间。
	at := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	live := &fakeUsageAnalyticsSource{health: usageanalytics.AnalyticsHealth{
		IngestedTotal: 1234,
		LastIngestAt:  &at,
	}}
	got := renderUsageAnalyticsHealthLine(live)
	usageToolsContainsAll(t, "attached health line", got, "attached", "已入库 1,234", "2026-09-17 10:00:00 UTC")
}

func TestUsageDegradationLines(t *testing.T) {
	cacheLines := []string{"缓存分析不可用（当前后端版本不支持）"}
	// 缓存模式：既有降级契约逐字不变（不追加健康行）。
	got := usageDegradationLines(nil, usageScreenModeOverview, cacheLines)
	if len(got) != 1 || got[0] != cacheLines[0] {
		t.Fatalf("cache degradation changed: %v", got)
	}
	got = usageDegradationLines(nil, usageScreenModeRequests, cacheLines)
	if len(got) != 1 || got[0] != cacheLines[0] {
		t.Fatalf("requests degradation changed: %v", got)
	}
	// 聚合模式：健康行 + 稳定降级文案。
	got = usageDegradationLines(nil, usageScreenModeTools, cacheLines)
	if len(got) != 2 || got[0] != usageHealthNoDataLine || got[1] != usageAnalyticsUnavailableHint {
		t.Fatalf("analytics degradation = %v", got)
	}
	// 聚合模式且服务已挂载（会话 id 缺失等场景）：健康行 + 原始行。
	live := &fakeUsageAnalyticsSource{health: usageanalytics.AnalyticsHealth{IngestedTotal: 5}}
	got = usageDegradationLines(live, usageScreenModeErrors, []string{"错误: 当前会话没有 runtime session id"})
	if len(got) != 2 || !strings.Contains(got[0], "attached") || got[1] != "错误: 当前会话没有 runtime session id" {
		t.Fatalf("analytics mounted degradation = %v", got)
	}
}

// ---------------------------------------------------------------------------
// 工具维度
// ---------------------------------------------------------------------------

func TestRenderUsageAnalyticsToolStats(t *testing.T) {
	src := &fakeUsageAnalyticsSource{tools: usageanalytics.ToolStatsResult{
		Tools: []usageanalytics.ToolStat{{
			ToolName: "view", Calls: 42, Failures: 3, FailureRate: 3.0 / 42.0,
			EmptyResults: 1, RetriedCalls: 2,
			AverageDuration: 120, P50DurationMS: 120, P95DurationMS: 480,
			ErrorTop: []usageanalytics.ErrorPattern{{Source: "tools", ErrorCode: "timeout", FailureCategory: "timeout", Count: 3}},
		}},
		Totals: usageanalytics.ToolStat{Calls: 42, Failures: 3, FailureRate: 3.0 / 42.0},
	}}

	lines := renderUsageAnalyticsToolStats(src, "sess-1", 7)
	joined := strings.Join(lines, "\n")
	usageToolsContainsAll(t, "tools lines", joined,
		"工具调用统计（当前会话，共 1 个工具；调用 42，失败 3，失败率 7.1%）",
		"#1 view 调用=42 失败=3 失败率=7.1% 空结果=1 重试=2 p50=120ms p95=480ms",
		"错误: timeout×3")
	if src.lastToolQuery.SessionID != "sess-1" || src.lastToolQuery.Limit != 7 {
		t.Fatalf("tool query = %+v", src.lastToolQuery)
	}

	// 未指定 N → 默认 10（上限由参数解析归一，渲染层只兜默认值）。
	_ = renderUsageAnalyticsToolStats(src, "sess-1", 0)
	if src.lastToolQuery.Limit != usageToolsDefaultLimit {
		t.Fatalf("default tool limit = %d", src.lastToolQuery.Limit)
	}

	// 空会话 → 明确"暂无数据"，不报错。
	empty := &fakeUsageAnalyticsSource{}
	emptyLines := renderUsageAnalyticsToolStats(empty, "sess-1", 0)
	if len(emptyLines) != 1 || !strings.Contains(emptyLines[0], "暂无数据") {
		t.Fatalf("empty tools lines = %v", emptyLines)
	}

	// 查询错误 → 稳定降级文案（不携带原始错误文本）。
	broken := &fakeUsageAnalyticsSource{toolsErr: fmt.Errorf("boom")}
	brokenLines := renderUsageAnalyticsToolStats(broken, "sess-1", 0)
	if len(brokenLines) != 1 || brokenLines[0] != usageAnalyticsUnavailableHint {
		t.Fatalf("error tools lines = %v", brokenLines)
	}

	// 未挂载 → 稳定降级文案。
	if got := renderUsageAnalyticsToolStats(nil, "sess-1", 0); len(got) != 1 || got[0] != usageAnalyticsUnavailableHint {
		t.Fatalf("nil tools lines = %v", got)
	}

	// 耗时缺失 → `--`（not_reported 语义）；失败数为 0 时为 0.0% 而非 --。
	noReport := &fakeUsageAnalyticsSource{tools: usageanalytics.ToolStatsResult{
		Tools:  []usageanalytics.ToolStat{{ToolName: "grep", Calls: 2}},
		Totals: usageanalytics.ToolStat{Calls: 2},
	}}
	noReportJoined := strings.Join(renderUsageAnalyticsToolStats(noReport, "sess-1", 0), "\n")
	usageToolsContainsAll(t, "not reported tools lines", noReportJoined, "失败率=0.0%", "p50=--", "p95=--")
}

// ---------------------------------------------------------------------------
// 子代理维度
// ---------------------------------------------------------------------------

func TestRenderUsageAnalyticsSubagentStats(t *testing.T) {
	completedAt := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	src := &fakeUsageAnalyticsSource{subagents: usageanalytics.SubagentStatsResult{
		Summary: usageanalytics.SubagentStatsSummary{
			Total: 2, Succeeded: 1, Failed: 1, Unknown: 0,
			FailureRate: 0.5, Timeouts: 1, Retried: 1,
			FailureCategories: map[string]int{"timeout": 1},
			Sources:           map[string]int{"live": 2},
		},
		Subagents: []usageanalytics.SubagentStat{
			{SubagentID: "sub-ok", Role: "explorer", Source: "live", Success: usageToolsBoolPtr(true), Attempt: 1, MaxAttempts: 1},
			{
				SubagentID: "sub-fail", Role: "explorer", Source: "live", Success: usageToolsBoolPtr(false),
				CompletionReason: "failed", FailureCategory: "timeout", ErrorCode: "timeout",
				Attempt: 2, MaxAttempts: 3, DurationMS: 2500, UsageTotalTokens: 1200,
				CompletedAt: completedAt,
			},
		},
	}}

	lines := renderUsageAnalyticsSubagentStats(src, "sess-1", 5, false)
	joined := strings.Join(lines, "\n")
	usageToolsContainsAll(t, "subagent lines", joined,
		"子代理完成统计（当前会话，明细 2 条（上限 5））",
		"完成: 1   失败: 1   未知: 0   完成率: 50.0%   失败率: 50.0%   重试: 1   超时: 1",
		"失败分类: timeout 1",
		"来源分布: live 2",
		"#1 sub-ok explorer 来源=live 完成 attempt=1/1",
		"#2 sub-fail explorer 来源=live 失败 分类=timeout code=timeout attempt=2/3 耗时=2.5s token=1,200 09-17 10:00:00")
	if src.lastSubagentQuery.SessionID != "sess-1" || src.lastSubagentQuery.Limit != 5 || src.lastSubagentQuery.FailedOnly {
		t.Fatalf("subagent query = %+v", src.lastSubagentQuery)
	}

	// --failed：查询层 FailedOnly=true，头部标注。
	failed := renderUsageAnalyticsSubagentStats(src, "sess-1", 0, true)
	if !src.lastSubagentQuery.FailedOnly || src.lastSubagentQuery.Limit != usageSubagentsDefaultLimit {
		t.Fatalf("failed-only query = %+v", src.lastSubagentQuery)
	}
	if !strings.Contains(strings.Join(failed, "\n"), "--failed") {
		t.Fatalf("failed-only header missing marker:\n%s", strings.Join(failed, "\n"))
	}

	// 空结果：普通/--failed 文案区分，均含"暂无数据"。
	empty := &fakeUsageAnalyticsSource{}
	emptyLines := renderUsageAnalyticsSubagentStats(empty, "sess-1", 0, false)
	if len(emptyLines) != 1 || !strings.Contains(emptyLines[0], "暂无数据") {
		t.Fatalf("empty subagent lines = %v", emptyLines)
	}
	emptyFailed := renderUsageAnalyticsSubagentStats(empty, "sess-1", 0, true)
	if len(emptyFailed) != 1 || !strings.Contains(emptyFailed[0], "暂无子代理失败记录") {
		t.Fatalf("empty failed subagent lines = %v", emptyFailed)
	}

	// 未知 success（旧生产者缺字段）→ 未知；缺失耗时/attempt → `--`。
	legacy := &fakeUsageAnalyticsSource{subagents: usageanalytics.SubagentStatsResult{
		Summary:   usageanalytics.SubagentStatsSummary{Total: 1, Unknown: 1},
		Subagents: []usageanalytics.SubagentStat{{SubagentID: "sub-legacy", Success: nil}},
	}}
	legacyJoined := strings.Join(renderUsageAnalyticsSubagentStats(legacy, "sess-1", 0, false), "\n")
	usageToolsContainsAll(t, "legacy subagent lines", legacyJoined, "未知", "attempt=--")
}

// ---------------------------------------------------------------------------
// 失败模式 Top-N
// ---------------------------------------------------------------------------

func TestRenderUsageAnalyticsErrorPatterns(t *testing.T) {
	src := &fakeUsageAnalyticsSource{patterns: usageanalytics.ErrorPatternsResult{
		Patterns: []usageanalytics.ErrorPattern{
			{Source: "tools", ErrorCode: "timeout", FailureCategory: "timeout", Count: 3},
			{Source: "subagents", FailureCategory: "context_overflow", Count: 2},
			{Source: "requests", FailureCategory: "interrupted", Count: 1},
		},
	}}

	lines := renderUsageAnalyticsErrorPatterns(src, "sess-1", 3)
	joined := strings.Join(lines, "\n")
	usageToolsContainsAll(t, "error pattern lines", joined,
		"失败模式 Top-3（当前会话，共 3 类）",
		"#1 来源=tools timeout 次数=3",
		"#2 来源=subagents context_overflow 次数=2",
		"#3 来源=requests interrupted 次数=1")
	if src.lastErrorsQuery.SessionID != "sess-1" || src.lastErrorsQuery.Top != 3 {
		t.Fatalf("error patterns query = %+v", src.lastErrorsQuery)
	}

	// 未指定 → 默认 10。
	_ = renderUsageAnalyticsErrorPatterns(src, "sess-1", 0)
	if src.lastErrorsQuery.Top != usageErrorsDefaultTop {
		t.Fatalf("default errors top = %d", src.lastErrorsQuery.Top)
	}

	empty := &fakeUsageAnalyticsSource{}
	emptyLines := renderUsageAnalyticsErrorPatterns(empty, "sess-1", 0)
	if len(emptyLines) != 1 || !strings.Contains(emptyLines[0], "暂无数据") {
		t.Fatalf("empty error pattern lines = %v", emptyLines)
	}

	broken := &fakeUsageAnalyticsSource{patErr: fmt.Errorf("boom")}
	brokenLines := renderUsageAnalyticsErrorPatterns(broken, "sess-1", 0)
	if len(brokenLines) != 1 || brokenLines[0] != usageAnalyticsUnavailableHint {
		t.Fatalf("error pattern degradation = %v", brokenLines)
	}
}

// ---------------------------------------------------------------------------
// 真实 schema：seed 临时分析库 → 查询层渲染（数据/空库两态）
// ---------------------------------------------------------------------------

// usageToolsTestCreateDB 创建与生产同构的分析库并返回路径。
func usageToolsTestCreateDB(t *testing.T) string {
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

// usageToolsTestExec 直连分析库执行 seed 语句。
func usageToolsTestExec(t *testing.T, path string, statements ...string) {
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

// usageToolsTestAttach 打开只读查询服务。
func usageToolsTestAttach(t *testing.T, path string) *usageanalytics.Service {
	t.Helper()
	service, err := usageanalytics.Attach(nil, usageanalytics.Options{
		Config: usageanalytics.Config{Path: path, ReadOnly: true},
	})
	if err != nil || service == nil {
		t.Fatalf("attach analytics service: %v", err)
	}
	t.Cleanup(service.Close)
	return service
}

func TestRenderUsageAnalyticsAgainstSeededDatabase(t *testing.T) {
	path := usageToolsTestCreateDB(t)
	now := time.Now().UTC().UnixNano()
	usageToolsTestExec(t, path,
		fmt.Sprintf(`INSERT INTO usage_tool_calls (tool_call_id, session_id, trace_id, turn_id, step, tool_name, source, kind, outcome, ok, empty_result, error_code, retryable, started_at_unix_nano, completed_at_unix_nano, duration_ms)
			VALUES ('tool-1', 'sess-1', 'trace-1', 'turn-1', 1, 'view', 'builtin', 'read', 'failed', 0, 0, 'timeout', 1, %d, %d, 1500)`, now, now),
		fmt.Sprintf(`INSERT INTO usage_subagents (subagent_id, parent_session_id, child_session_id, role, success, completion_reason, failure_category, error_code, attempt, max_attempts, duration_ms, completed_at_unix_nano, source)
			VALUES ('sub-1', 'sess-1', 'child-1', 'explorer', 0, 'failed', 'timeout', 'timeout', 2, 3, 2500, %d, 'live')`, now),
	)
	service := usageToolsTestAttach(t, path)

	toolsJoined := strings.Join(renderUsageAnalyticsToolStats(service, "sess-1", 10), "\n")
	usageToolsContainsAll(t, "seeded tools", toolsJoined, "view", "调用=1", "失败=1", "失败率=100.0%", "p50=1.5s", "p95=1.5s")

	subagentsJoined := strings.Join(renderUsageAnalyticsSubagentStats(service, "sess-1", 10, false), "\n")
	usageToolsContainsAll(t, "seeded subagents", subagentsJoined,
		"失败: 1", "完成率: 0.0%", "失败率: 100.0%", "失败分类: timeout 1", "来源分布: live 1",
		"分类=timeout", "attempt=2/3", "耗时=2.5s")

	patternsJoined := strings.Join(renderUsageAnalyticsErrorPatterns(service, "sess-1", 10), "\n")
	usageToolsContainsAll(t, "seeded error patterns", patternsJoined, "失败模式 Top-10", "来源=tools timeout", "来源=subagents timeout")

	// 其他会话：查询层按 session 过滤，返回空数组而不是他人的数据。
	otherJoined := strings.Join(renderUsageAnalyticsToolStats(service, "sess-other", 10), "\n")
	if !strings.Contains(otherJoined, "暂无数据") || strings.Contains(otherJoined, "view") {
		t.Fatalf("session scope leaked:\n%s", otherJoined)
	}

	// 采集健康：已挂载且有真实行 → attached + 行数。
	healthLine := renderUsageAnalyticsHealthLine(service)
	usageToolsContainsAll(t, "seeded health", healthLine, "attached", "已入库")
}

// TestBuildUsageScreenBodyAnalyticsModes 覆盖备用屏/文档同一构图：首行健康 +
// 聚合模式内容；缓存模式内容保持既有渲染。
func TestBuildUsageScreenBodyAnalyticsModes(t *testing.T) {
	session, _, _ := newCacheTestSession(t)
	sessionID := session.RuntimeSession.ID
	service := ensureLocalUsageService(session.LocalRuntimeHost)
	if service == nil {
		t.Fatal("expected local usage analytics service to attach")
	}
	now := time.Now().UTC().UnixNano()
	usageToolsTestExec(t, service.DBPath(),
		fmt.Sprintf(`INSERT INTO usage_tool_calls (tool_call_id, session_id, trace_id, turn_id, step, tool_name, source, kind, outcome, ok, empty_result, error_code, retryable, started_at_unix_nano, completed_at_unix_nano, duration_ms)
			VALUES ('tool-screen', '%s', 'trace-x', 'turn-x', 1, 'view', 'builtin', 'read', 'ok', 1, 0, '', 0, %d, %d, 120)`, sessionID, now, now),
	)

	body, ok := buildUsageScreenBody(session, UsageScreenRequest{Mode: usageScreenModeTools, Limit: 5})
	if !ok {
		t.Fatalf("tools screen body failed: %s", body)
	}
	usageToolsContainsAll(t, "tools screen body", body, "采集健康:", "工具调用统计", "view", "调用=1")
	if !strings.HasPrefix(body, "采集健康:") {
		t.Fatalf("health line must be first:\n%s", body)
	}

	// 当前会话无失败记录 → 首行健康 + 「暂无数据」，不是错误。
	emptyBody, ok := buildUsageScreenBody(session, UsageScreenRequest{Mode: usageScreenModeErrors})
	if !ok {
		t.Fatalf("errors screen body failed: %s", emptyBody)
	}
	usageToolsContainsAll(t, "empty errors screen body", emptyBody, "采集健康:", "暂无数据")
	if strings.Contains(emptyBody, "错误") {
		t.Fatalf("empty analytics view must not error:\n%s", emptyBody)
	}

	// 缓存模式保持既有内容（健康行只前置，不替换原渲染）。
	requestsBody, ok := buildUsageScreenBody(session, UsageScreenRequest{Mode: usageScreenModeRequests, Limit: 5})
	if !ok {
		t.Fatalf("requests screen body failed: %s", requestsBody)
	}
	usageToolsContainsAll(t, "requests screen body", requestsBody, "采集健康:", "当前会话暂无 LLM 请求记录")
}

// TestExecuteStructuredUsageCommandAnalyticsDegradesWithoutService：无本地
// runtime host → 聚合模式给出健康 `--` + 稳定文案，不 panic、不报错。
func TestExecuteStructuredUsageCommandAnalyticsDegradesWithoutService(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)

	for _, command := range []string{"/usage tools", "/usage subagents --failed", "/usage errors top 5"} {
		plain := ui.RenderDocumentPlain(executeStructuredUsageCommand(session, command).Document())
		usageToolsContainsAll(t, command, plain, usageHealthNoDataLine, usageAnalyticsUnavailableHint)
	}
}

// TestHandleUsageCommandAnalyticsModes：legacy stdout 投影与结构化投影共用同一
// 解析/渲染（§9.3 T2 要求两种投影同步实现）。
func TestHandleUsageCommandAnalyticsModes(t *testing.T) {
	session, _, _ := newCacheTestSession(t)
	sessionID := session.RuntimeSession.ID
	service := ensureLocalUsageService(session.LocalRuntimeHost)
	if service == nil {
		t.Fatal("expected local usage analytics service to attach")
	}
	now := time.Now().UTC().UnixNano()
	usageToolsTestExec(t, service.DBPath(),
		fmt.Sprintf(`INSERT INTO usage_tool_calls (tool_call_id, session_id, trace_id, turn_id, step, tool_name, source, kind, outcome, ok, empty_result, error_code, retryable, started_at_unix_nano, completed_at_unix_nano, duration_ms)
			VALUES ('tool-stdout', '%s', 'trace-y', 'turn-y', 1, 'view', 'builtin', 'read', 'ok', 1, 0, '', 0, %d, %d, 120)`, sessionID, now, now),
	)

	out := captureSurfaceStdout(t, func() {
		handleUsageCommand(session, "/usage tools 5")
	})
	usageToolsContainsAll(t, "stdout tools", out, "采集健康:", "工具调用统计", "view", "调用=1")

	emptyOut := captureSurfaceStdout(t, func() {
		handleUsageCommand(session, "/usage errors")
	})
	usageToolsContainsAll(t, "stdout errors", emptyOut, "采集健康:", "暂无数据")

	// 非法参数：稳定错误文本，不渲染视图。
	badOut := captureSurfaceStdout(t, func() {
		handleUsageCommand(session, "/usage tools 0")
	})
	if !strings.Contains(badOut, "数量非法") || strings.Contains(badOut, "工具调用统计") {
		t.Fatalf("stdout invalid args output = %q", badOut)
	}
}
