package commands

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ---------------------------------------------------------------------------
// TUI /usage 命令测试（方案 §6.4）。
// 渲染逻辑为纯函数（返回 []string），直接断言行内容；
// 守卫分支经 captureSurfaceStdout 捕获终端输出。
// ---------------------------------------------------------------------------

func TestParseUsageCommandArgs(t *testing.T) {
	cases := []struct {
		command string
		want    []string
	}{
		{"/usage", nil},
		{"/usage ", nil},
		{"/usage cache", []string{"cache"}},
		{"/usage cache requests 5", []string{"cache", "requests", "5"}},
		{"/usage   cache   trace   msg-1", []string{"cache", "trace", "msg-1"}},
	}
	for _, tc := range cases {
		got := parseUsageCommandArgs(tc.command)
		if len(got) != len(tc.want) {
			t.Fatalf("parseUsageCommandArgs(%q) = %v, want %v", tc.command, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("parseUsageCommandArgs(%q) = %v, want %v", tc.command, got, tc.want)
			}
		}
	}
}

// fakeUsageSource 实现 cacheanalytics.Source，用于精准驱动渲染降级分支
// （partial 窗口标注、稳定错误码映射）。
type fakeUsageSource struct {
	caps       cacheanalytics.Capabilities
	overview   cacheanalytics.CacheOverview
	overviewErr error
	requests   cacheanalytics.RequestListResponse
	requestsErr error
	trace      cacheanalytics.MessageTrace
	traceErr   error
}

func (f *fakeUsageSource) Capabilities() cacheanalytics.Capabilities { return f.caps }
func (f *fakeUsageSource) Overview(string) (cacheanalytics.CacheOverview, error) {
	return f.overview, f.overviewErr
}
func (f *fakeUsageSource) Requests(string, cacheanalytics.RequestQuery) (cacheanalytics.RequestListResponse, error) {
	return f.requests, f.requestsErr
}
func (f *fakeUsageSource) Request(string, string) (cacheanalytics.CacheRequestRecord, error) {
	return cacheanalytics.CacheRequestRecord{}, cacheanalytics.ErrNotFound
}
func (f *fakeUsageSource) MessageTrace(string, string) (cacheanalytics.MessageTrace, error) {
	return f.trace, f.traceErr
}

func ratioPtr(v float64) *float64 { return &v }

func TestRenderUsageCacheOverview_PartialHeader(t *testing.T) {
	src := &fakeUsageSource{caps: cacheanalytics.Capabilities{MaxRequestsPerSession: 512}}
	src.overview = cacheanalytics.CacheOverview{
		RequestsTotal: 600,
		Tokens: cacheanalytics.CacheOverviewTokens{
			PromptTokens:        128400,
			CompletionTokens:    3200,
			CacheReadTokens:     64200,
			CacheCreationTokens: 1200,
		},
		CacheHitRatio:   ratioPtr(0.5),
		CacheWriteRatio: ratioPtr(0.0093),
		CacheStatusDistribution: cacheanalytics.CacheStatusDistribution{
			Hit: 300, Write: 150, ReportedZero: 100, NotReported: 40, Error: 10,
		},
		Coverage: cacheanalytics.CoverageInfo{Partial: true},
	}
	lines := renderUsageCacheOverview(src, "sess-abcdef1234567890xyz")
	joined := strings.Join(lines, "\n")
	// partial 窗口标注（§6.4：仅统计最近 N 条请求）。
	if !strings.Contains(lines[0], "仅统计最近 512 条请求") {
		t.Fatalf("partial header missing: %q", lines[0])
	}
	if !strings.Contains(joined, "请求总数: 600") || !strings.Contains(joined, "命中: 300") {
		t.Fatalf("distribution missing:\n%s", joined)
	}
	if !strings.Contains(joined, "未上报（未知）: 40") || !strings.Contains(joined, "未命中: 100") {
		t.Fatalf("not_reported/reported_zero missing:\n%s", joined)
	}
	// 千分位（§6.4 示例 128,400）。
	if !strings.Contains(joined, "128,400") {
		t.Fatalf("thousands separator missing:\n%s", joined)
	}
	if !strings.Contains(joined, "缓存读取率: 50.0%") {
		t.Fatalf("hit ratio missing:\n%s", joined)
	}
}

func TestRenderUsageCacheOverview_ErrorMapping(t *testing.T) {
	cases := []struct {
		err      error
		contains string
	}{
		{cacheanalytics.ErrSessionNotFound, "会话不存在或已归档"},
		{cacheanalytics.ErrDisabled, "缓存分析不可用"},
		{fmt.Errorf("boom"), "缓存分析不可用"},
	}
	for _, tc := range cases {
		src := &fakeUsageSource{overviewErr: tc.err}
		lines := renderUsageCacheOverview(src, "sess-1")
		if len(lines) != 1 || !strings.Contains(lines[0], tc.contains) {
			t.Fatalf("err %v → %v, want single line containing %q", tc.err, lines, tc.contains)
		}
	}
}

func TestRenderUsageCacheOverview_NotReportedNoRatio(t *testing.T) {
	// 全部 not_reported：命中率必须为 --（§6.4：not_reported 不污染命中率）。
	src := &fakeUsageSource{}
	src.overview = cacheanalytics.CacheOverview{
		RequestsTotal: 2,
		Tokens:        cacheanalytics.CacheOverviewTokens{PromptTokens: 300},
		CacheStatusDistribution: cacheanalytics.CacheStatusDistribution{
			NotReported: 2,
		},
	}
	joined := strings.Join(renderUsageCacheOverview(src, "sess-1"), "\n")
	if !strings.Contains(joined, "缓存读取率: --") || !strings.Contains(joined, "缓存写入率: --") {
		t.Fatalf("expected -- ratios:\n%s", joined)
	}
}

func TestRenderUsageCacheRequests_Live(t *testing.T) {
	session, bus, _ := newCacheTestSession(t)
	sessionID := session.RuntimeSession.ID
	src := ensureLocalCacheService(session.LocalRuntimeHost).Source()

	// req-1 hit（100/200 → 50%）；req-2 write；req-3 not_reported；req-4 reported_zero。
	publishCacheStarted(t, bus, sessionID, "req-1", nil)
	publishCacheFinished(t, bus, sessionID, "req-1", map[string]interface{}{
		"usage_cache_read_tokens": 100,
	})
	publishCacheStarted(t, bus, sessionID, "req-2", nil)
	publishCacheFinished(t, bus, sessionID, "req-2", map[string]interface{}{
		"usage_cache_creation_tokens": 150,
	})
	publishCacheStarted(t, bus, sessionID, "req-3", nil)
	publishCacheFinished(t, bus, sessionID, "req-3", map[string]interface{}{
		"usage_cache_read_reported": false,
	})
	publishCacheStarted(t, bus, sessionID, "req-4", nil)
	publishCacheFinished(t, bus, sessionID, "req-4", nil)

	lines := renderUsageCacheRequests(src, sessionID, usageCacheRequestsDefaultLimit)
	joined := strings.Join(lines, "\n")
	if !strings.HasPrefix(joined, "最近 4 条 LLM 请求（共 4 条，新→旧）") {
		t.Fatalf("header mismatch: %q", lines[0])
	}
	// 排序：新→旧。
	if !strings.Contains(lines[1], "req-4") || !strings.Contains(lines[4], "req-1") {
		t.Fatalf("order mismatch (newest first):\n%s", joined)
	}
	// 状态标签。
	if !strings.Contains(joined, "命中") || !strings.Contains(joined, "写入") ||
		!strings.Contains(joined, "未上报（未知）") || !strings.Contains(joined, "未命中") {
		t.Fatalf("status labels missing:\n%s", joined)
	}
	// req-1 命中率 50.0%；req-3/req-4 命中率 --。
	req1Line := lines[4]
	if !strings.Contains(req1Line, "50.0%") || !strings.Contains(req1Line, "prompt=200") {
		t.Fatalf("req-1 line mismatch: %q", req1Line)
	}
	req3Line := lines[2]
	if !strings.Contains(req3Line, "req-3") || !strings.Contains(req3Line, "--") {
		t.Fatalf("req-3 not_reported should show --: %q", req3Line)
	}
}

func TestRenderUsageCacheRequests_LimitAndEmpty(t *testing.T) {
	session, bus, _ := newCacheTestSession(t)
	sessionID := session.RuntimeSession.ID
	src := ensureLocalCacheService(session.LocalRuntimeHost).Source()

	// 空会话。
	lines := renderUsageCacheRequests(src, sessionID, 5)
	if len(lines) != 1 || lines[0] != "当前会话暂无 LLM 请求记录" {
		t.Fatalf("empty session lines = %v", lines)
	}

	// limit 截断：3 条请求只取 2。
	for _, id := range []string{"r1", "r2", "r3"} {
		publishCacheStarted(t, bus, sessionID, id, nil)
		publishCacheFinished(t, bus, sessionID, id, map[string]interface{}{
			"usage_cache_read_tokens": 10,
		})
	}
	lines = renderUsageCacheRequests(src, sessionID, 2)
	if !strings.HasPrefix(lines[0], "最近 2 条 LLM 请求（共 3 条") {
		t.Fatalf("limit header = %q", lines[0])
	}
	if len(lines) != 3 {
		t.Fatalf("expected header+2 rows, got %d", len(lines))
	}
}

func TestRenderUsageCacheTrace_Live(t *testing.T) {
	session, bus, storage := newCacheTestSession(t)
	sessionID := session.RuntimeSession.ID
	src := ensureLocalCacheService(session.LocalRuntimeHost).Source()

	// 历史：user(msg-u1) → assistant(msg-a1)，同一 turn。
	ctx := context.Background()
	userMsg := *runtimetypes.NewUserMessage("hello")
	userMsg.Metadata = runtimetypes.Metadata{"message_id": "msg-u1", "turn_id": "turn-t1"}
	assistantMsg := runtimetypes.Message{Role: "assistant", Content: "hi"}
	assistantMsg.Metadata = runtimetypes.Metadata{"message_id": "msg-a1", "turn_id": "turn-t1"}
	session.RuntimeSession.AddMessage(userMsg)
	session.RuntimeSession.AddMessage(assistantMsg)
	if err := storage.Save(ctx, session.RuntimeSession); err != nil {
		t.Fatalf("storage.Save: %v", err)
	}

	publishCacheStarted(t, bus, sessionID, "req-1", map[string]interface{}{"logical_turn_id": "turn-t1"})
	publishCacheFinished(t, bus, sessionID, "req-1", map[string]interface{}{
		"logical_turn_id":         "turn-t1",
		"usage_cache_read_tokens": 100,
	})

	lines := renderUsageCacheTrace(src, sessionID, "msg-a1")
	joined := strings.Join(lines, "\n")
	// history_inferred 标注（§6.4：消息上下文经历史推断）。
	if !strings.Contains(lines[0], "msg-a1（推断）") {
		t.Fatalf("inferred badge missing: %q", lines[0])
	}
	if !strings.Contains(lines[0], "角色: assistant") || !strings.Contains(lines[0], "turn: turn-t1") {
		t.Fatalf("role/turn missing: %q", lines[0])
	}
	if !strings.Contains(joined, "产出请求: req-1（命中，命中率 50.0%）") {
		t.Fatalf("produced_by line missing:\n%s", joined)
	}
	if !strings.Contains(joined, "相邻消息: msg-u1") {
		t.Fatalf("neighbors missing:\n%s", joined)
	}

	// 未知消息。
	lines = renderUsageCacheTrace(src, sessionID, "msg-unknown")
	if len(lines) != 1 || !strings.Contains(lines[0], "未找到消息: msg-unknown") {
		t.Fatalf("unknown message lines = %v", lines)
	}
}

func TestRenderUsageCacheTrace_NoProducedBy(t *testing.T) {
	src := &fakeUsageSource{}
	src.trace = cacheanalytics.MessageTrace{
		MessageID:   "msg-u1",
		MessageRole: "user",
		Neighbors:   cacheanalytics.MessageNeighbors{NextMessageID: "msg-a1"},
	}
	lines := renderUsageCacheTrace(src, "sess-1", "msg-u1")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "产出请求: （无关联请求）") {
		t.Fatalf("no produced_by line missing:\n%s", joined)
	}
	if !strings.Contains(joined, "相邻消息: - ← → msg-a1") {
		t.Fatalf("neighbor dash missing:\n%s", joined)
	}
}

func TestHandleUsageCommand_Guards(t *testing.T) {
	// nil session → 明确错误（不 panic）。
	out := captureSurfaceStdout(t, func() {
		handleUsageCommand(nil, "/usage")
	})
	if !strings.Contains(out, "当前没有活动会话") {
		t.Fatalf("nil session output = %q", out)
	}

	// service 缺失（无 LocalRuntimeHost）→ 降级提示。
	session := newWebTestSession()
	withWebTestSession(t, session)
	out = captureSurfaceStdout(t, func() {
		handleUsageCommand(session, "/usage cache")
	})
	if !strings.Contains(out, "缓存分析不可用") {
		t.Fatalf("missing service output = %q", out)
	}
}

func TestHandleUsageCommand_InvalidArgs(t *testing.T) {
	session, _, _ := newCacheTestSession(t)

	cases := []struct {
		command  string
		contains string
	}{
		{"/usage bogus", "未知子命令"},
		{"/usage cache requests 0", "数量非法"},
		{"/usage cache requests -3", "数量非法"},
		{"/usage cache requests abc", "数量非法"},
		{"/usage cache trace", "trace 需要 message_id"},
		{"/usage cache bogus", "未知 cache 子命令"},
	}
	for _, tc := range cases {
		out := captureSurfaceStdout(t, func() {
			handleUsageCommand(session, tc.command)
		})
		if !strings.Contains(out, tc.contains) {
			t.Fatalf("%q output = %q, want contains %q", tc.command, out, tc.contains)
		}
	}
}

func TestHandleUsageCommand_RequestsLimitClamp(t *testing.T) {
	// 101/999 → 截断到 100（不报错，§6.4 上限语义）。
	session, bus, _ := newCacheTestSession(t)
	sessionID := session.RuntimeSession.ID
	src := ensureLocalCacheService(session.LocalRuntimeHost).Source()
	publishCacheStarted(t, bus, sessionID, "r1", nil)
	publishCacheFinished(t, bus, sessionID, "r1", map[string]interface{}{"usage_cache_read_tokens": 5})

	// 超限参数不应报错（clamp 到 100 后正常渲染）。
	out := captureSurfaceStdout(t, func() {
		handleUsageCommand(session, "/usage cache requests 999")
	})
	if !strings.Contains(out, "最近 1 条 LLM 请求") {
		t.Fatalf("clamped output = %q", out)
	}

	// Source 层 limit 语义。
	resp, err := src.Requests(sessionID, cacheanalytics.RequestQuery{Limit: 100})
	if err != nil {
		t.Fatalf("Requests: %v", err)
	}
	if resp.Limit != 100 {
		t.Fatalf("limit = %d, want 100", resp.Limit)
	}
}

func TestUsageSourceErrorLinesNotFound(t *testing.T) {
	lines := usageSourceErrorLines(cacheanalytics.ErrNotFound)
	if len(lines) != 1 || lines[0] != "未找到对应记录" {
		t.Fatalf("lines = %v", lines)
	}
}

// ---------------------------------------------------------------------------
// 统一渲染命令通道迁移测试：/usage 必须被结构化路径完整认领，任何分支
// （含参数/降级错误）都留在 CommandResult 文档内，不再落入 unified gate。
// ---------------------------------------------------------------------------

func TestTryExecuteStructuredChatCommand_UsageClaimed(t *testing.T) {
	session, _, _ := newCacheTestSession(t)
	for _, command := range []string{
		"/usage",
		"/usage cache",
		"/usage cache requests 5",
		"/usage cache trace msg-1",
		"/usage bogus",
	} {
		result, handled, err := tryExecuteStructuredChatCommand(session, command)
		if err != nil {
			t.Fatalf("%s: unexpected error %v", command, err)
		}
		if !handled {
			t.Fatalf("%s: structured path must claim /usage so the unified gate never sees it", command)
		}
		if strings.TrimSpace(ui.RenderDocumentPlain(result.Document())) == "" {
			t.Fatalf("%s: empty command document", command)
		}
	}
}

func TestExecuteStructuredUsageCommand_Overview(t *testing.T) {
	session, bus, _ := newCacheTestSession(t)
	sessionID := session.RuntimeSession.ID

	// req-1 hit（100/200）；req-2 write（150/200）。
	publishCacheStarted(t, bus, sessionID, "req-1", nil)
	publishCacheFinished(t, bus, sessionID, "req-1", map[string]interface{}{
		"usage_cache_read_tokens": 100,
	})
	publishCacheStarted(t, bus, sessionID, "req-2", nil)
	publishCacheFinished(t, bus, sessionID, "req-2", map[string]interface{}{
		"usage_cache_creation_tokens": 150,
	})

	result, handled, err := tryExecuteStructuredChatCommand(session, "/usage")
	if err != nil || !handled {
		t.Fatalf("tryExecuteStructuredChatCommand(/usage) handled=%v err=%v", handled, err)
	}
	plain := ui.RenderDocumentPlain(result.Document())
	if !strings.Contains(plain, "会话缓存统计") || !strings.Contains(plain, "请求总数: 2") {
		t.Fatalf("overview document missing stats:\n%s", plain)
	}
	if !strings.Contains(plain, "命中: 1") || !strings.Contains(plain, "写入: 1") {
		t.Fatalf("overview distribution missing:\n%s", plain)
	}
	// 100 读 / 400 prompt = 25.0%。
	if !strings.Contains(plain, "缓存读取率: 25.0%") {
		t.Fatalf("overview hit ratio missing:\n%s", plain)
	}
}

func TestExecuteStructuredUsageCommand_RequestsAndTrace(t *testing.T) {
	session, bus, storage := newCacheTestSession(t)
	sessionID := session.RuntimeSession.ID

	// 历史：user(msg-u1) → assistant(msg-a1)，同一 turn。
	ctx := context.Background()
	userMsg := *runtimetypes.NewUserMessage("hello")
	userMsg.Metadata = runtimetypes.Metadata{"message_id": "msg-u1", "turn_id": "turn-t1"}
	assistantMsg := runtimetypes.Message{Role: "assistant", Content: "hi"}
	assistantMsg.Metadata = runtimetypes.Metadata{"message_id": "msg-a1", "turn_id": "turn-t1"}
	session.RuntimeSession.AddMessage(userMsg)
	session.RuntimeSession.AddMessage(assistantMsg)
	if err := storage.Save(ctx, session.RuntimeSession); err != nil {
		t.Fatalf("storage.Save: %v", err)
	}

	publishCacheStarted(t, bus, sessionID, "req-1", map[string]interface{}{"logical_turn_id": "turn-t1"})
	publishCacheFinished(t, bus, sessionID, "req-1", map[string]interface{}{
		"logical_turn_id":         "turn-t1",
		"usage_cache_read_tokens": 100,
	})

	requestsPlain := ui.RenderDocumentPlain(executeStructuredUsageCommand(session, "/usage cache requests 5").Document())
	if !strings.Contains(requestsPlain, "最近 1 条 LLM 请求（共 1 条，新→旧）") {
		t.Fatalf("requests document header missing:\n%s", requestsPlain)
	}
	if !strings.Contains(requestsPlain, "req-1") || !strings.Contains(requestsPlain, "命中") {
		t.Fatalf("requests document row missing:\n%s", requestsPlain)
	}

	tracePlain := ui.RenderDocumentPlain(executeStructuredUsageCommand(session, "/usage cache trace msg-a1").Document())
	if !strings.Contains(tracePlain, "msg-a1（推断）") || !strings.Contains(tracePlain, "产出请求: req-1（命中，命中率 50.0%）") {
		t.Fatalf("trace document missing produced_by:\n%s", tracePlain)
	}
}

func TestExecuteStructuredUsageCommand_Guards(t *testing.T) {
	// nil session → 明确错误进入文档（结构化路径绝不写终端）。
	plain := ui.RenderDocumentPlain(executeStructuredUsageCommand(nil, "/usage").Document())
	if !strings.Contains(plain, "当前没有活动会话") {
		t.Fatalf("nil session document = %q", plain)
	}

	// service 缺失（无 LocalRuntimeHost）→ 降级提示进入文档。
	session := newWebTestSession()
	withWebTestSession(t, session)
	plain = ui.RenderDocumentPlain(executeStructuredUsageCommand(session, "/usage cache").Document())
	if !strings.Contains(plain, "缓存分析不可用") {
		t.Fatalf("missing service document = %q", plain)
	}
}

func TestExecuteStructuredUsageCommand_InvalidArgs(t *testing.T) {
	session, _, _ := newCacheTestSession(t)
	cases := []struct {
		command  string
		contains string
	}{
		{"/usage bogus", "未知子命令"},
		{"/usage cache requests 0", "数量非法"},
		{"/usage cache requests -3", "数量非法"},
		{"/usage cache requests abc", "数量非法"},
		{"/usage cache trace", "trace 需要 message_id"},
		{"/usage cache bogus", "未知 cache 子命令"},
	}
	for _, tc := range cases {
		plain := ui.RenderDocumentPlain(executeStructuredUsageCommand(session, tc.command).Document())
		if !strings.Contains(plain, tc.contains) {
			t.Fatalf("%q document = %q, want contains %q", tc.command, plain, tc.contains)
		}
	}
}

// TestDispatchChatCommandUnifiedUsageRendersDocumentNotGate 是迁移的端到端
// 回归：统一渲染 interactive TTY 中 /usage 必须产出语义命令 cell（含无服务
// 降级提示），绝不能再现 unified gate 的“尚未迁移”错误。
func TestDispatchChatCommandUnifiedUsageRendersDocumentNotGate(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 18)
	coordinator.SetSurface(surface)

	var presenterOutput bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&presenterOutput) {
		t.Fatal("unified renderer did not attach")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	presenterOutput.Reset()

	// 无 LocalRuntimeHost → 缓存服务缺失，结构化文档必须携带稳定降级提示。
	withWebTestSession(t, session)
	dispatchChatCommand(session, "/usage", false)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.AppState()
	var transcript strings.Builder
	for _, cell := range state.Transcript.Cells {
		transcript.WriteString(cell.Source)
		transcript.WriteByte('\n')
	}
	if strings.Contains(transcript.String(), "/usage 尚未迁移到统一渲染命令通道") {
		t.Fatalf("/usage fell through to the unified gate: %s", transcript.String())
	}
	if !strings.Contains(transcript.String(), "缓存分析不可用（当前后端版本不支持）") {
		t.Fatalf("/usage degradation document missing: %s", transcript.String())
	}
}
