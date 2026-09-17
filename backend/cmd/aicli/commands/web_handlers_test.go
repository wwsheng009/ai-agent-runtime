package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ---------------------------------------------------------------------------
// 测试辅助
// ---------------------------------------------------------------------------

// withWebTestSession 注册一个临时 ChatSession provider，测试结束后恢复原值。
func withWebTestSession(t *testing.T, session *ChatSession) {
	t.Helper()
	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return session }
	t.Cleanup(func() {
		chatDebugDisplaySessionProvider = old
	})
}

// syncResponseRecorder 包装 httptest.ResponseRecorder 以支持并发读/写。
type syncResponseRecorder struct {
	mu  sync.Mutex
	rec *httptest.ResponseRecorder
}

func newSyncResponseRecorder() *syncResponseRecorder {
	return &syncResponseRecorder{rec: httptest.NewRecorder()}
}

func (s *syncResponseRecorder) Header() http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 返回内部 map 引用：handler 仅在流式写入前修改 header，
	// 测试在 handler 退出（<-done）后读取，由 done 通道建立 happens-before。
	return s.rec.Header()
}

func (s *syncResponseRecorder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.Write(p)
}

func (s *syncResponseRecorder) WriteHeader(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rec.WriteHeader(code)
}

func (s *syncResponseRecorder) Code() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.Code
}

func (s *syncResponseRecorder) BodyString() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.Body.String()
}

func (s *syncResponseRecorder) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rec.Flush()
}

// newWebTestSession 构造一个带 InputQueue 的假会话。
func newWebTestSession() *ChatSession {
	return &ChatSession{
		InputQueue: newChatInputQueue(nil),
	}
}

// ---------------------------------------------------------------------------
// GET /web/ — 页面
// ---------------------------------------------------------------------------

func TestHandleChatWebPage(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebPath, nil)
	rec := httptest.NewRecorder()

	HandleChatWebPage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "style.css") {
		t.Fatal("page body missing style.css reference")
	}
	if !strings.Contains(body, "app.js") {
		t.Fatal("page body missing app.js reference")
	}
	if !strings.Contains(body, "aicli micro web client") {
		t.Fatal("page body missing title")
	}
}

// TestHandleChatWebPage_HeaderLayout 锁定顶栏重排：左侧工具/状态簇（折叠会话列表、
// 主题切换、连接/轮次/发送状态）在前，会话标题 + ID 居中块在后，右侧留等宽占位。
func TestHandleChatWebPage_HeaderLayout(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebPath, nil)
	rec := httptest.NewRecorder()

	HandleChatWebPage(rec, req)

	body := rec.Body.String()
	leftIdx := strings.Index(body, `class="header-left"`)
	sessionIdx := strings.Index(body, `id="header-session"`)
	rightIdx := strings.Index(body, `class="header-right"`)
	if leftIdx < 0 || sessionIdx < 0 || rightIdx < 0 {
		t.Fatalf("header layout elements missing: left=%d session=%d right=%d", leftIdx, sessionIdx, rightIdx)
	}
	if !(leftIdx < sessionIdx && sessionIdx < rightIdx) {
		t.Fatalf("header order = left:%d session:%d right:%d, want left < session < right", leftIdx, sessionIdx, rightIdx)
	}
	for _, id := range []string{"sidebar-toggle", "theme-toggle", "connection-status", "turn-status", "send-status"} {
		if !strings.Contains(body, `id="`+id+`"`) {
			t.Fatalf("page body missing header element %q", id)
		}
	}
}

// TestHandleChatWebPage_Tabs 锁定页签集合：对话 / 技能 / 日志 / 配置 / 缓存 / 分析 / 调试 / 关于。
// 「技能」紧邻「对话」（会话的第二页签），承载当前会话的 skill 目录与详情弹层。
// 「分析」紧跟「缓存」，承载工具 / 子代理 / 失败模式聚合（runtime.analytics.v1）。
// 「调试」页签承载与 aicli /debug 一致的「状态文档」，「关于」页签展示客户端标识。
func TestHandleChatWebPage_Tabs(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebPath, nil)
	rec := httptest.NewRecorder()

	HandleChatWebPage(rec, req)

	body := rec.Body.String()
	for _, id := range []string{"tab-main-btn", "tab-skills-btn", "tab-log-btn", "tab-config-btn", "tab-cache-btn", "tab-analysis-btn", "tab-debug-btn", "tab-about-btn"} {
		if !strings.Contains(body, `id="`+id+`"`) {
			t.Fatalf("page body missing tab button %q", id)
		}
	}
	for _, id := range []string{"tab-main", "tab-skills", "tab-log", "tab-config", "tab-cache", "tab-analysis", "tab-debug", "tab-about"} {
		if !strings.Contains(body, `id="`+id+`"`) {
			t.Fatalf("page body missing tab panel %q", id)
		}
	}
	// 技能页签紧跟对话：main < skills < log。
	mainBtnIdx := strings.Index(body, `id="tab-main-btn"`)
	skillsBtnIdx := strings.Index(body, `id="tab-skills-btn"`)
	logBtnIdx := strings.Index(body, `id="tab-log-btn"`)
	if !(mainBtnIdx < skillsBtnIdx && skillsBtnIdx < logBtnIdx) {
		t.Fatalf("tab button order = main:%d skills:%d log:%d, want main < skills < log", mainBtnIdx, skillsBtnIdx, logBtnIdx)
	}
	// 技能页签：列表容器 + 刷新入口 + 点击条目打开的详情弹层外壳。
	for _, id := range []string{"skills-list", "skills-count", "skills-refresh-btn", "skill-detail-overlay", "skill-detail-body", "skill-detail-close"} {
		if !strings.Contains(body, `id="`+id+`"`) {
			t.Fatalf("skills tab missing element %q", id)
		}
	}
	// 按钮与面板同序：分析紧跟缓存、调试在分析之后、关于在调试之后，避免新的页签插错位置。
	debugBtnIdx := strings.Index(body, `id="tab-debug-btn"`)
	aboutBtnIdx := strings.Index(body, `id="tab-about-btn"`)
	cacheBtnIdx := strings.Index(body, `id="tab-cache-btn"`)
	analysisBtnIdx := strings.Index(body, `id="tab-analysis-btn"`)
	if !(cacheBtnIdx < analysisBtnIdx && analysisBtnIdx < debugBtnIdx && debugBtnIdx < aboutBtnIdx) {
		t.Fatalf("tab button order = cache:%d analysis:%d debug:%d about:%d, want cache < analysis < debug < about",
			cacheBtnIdx, analysisBtnIdx, debugBtnIdx, aboutBtnIdx)
	}
	// 分析页签：采集健康条 + 汇总卡 + 工具/子代理/失败模式三区 + 范围/自动/刷新入口
	// + 行下钻弹层（元素 id 与 js/analysis.js 一一对应）。
	for _, id := range []string{
		"analysis-status", "analysis-health", "analysis-cards",
		"analysis-tools", "analysis-subagents", "analysis-errors",
		"analysis-scope-btn", "analysis-auto-btn", "analysis-refresh-btn",
		"analysis-detail-overlay", "analysis-detail-title", "analysis-detail-body", "analysis-detail-close",
	} {
		if !strings.Contains(body, `id="`+id+`"`) {
			t.Fatalf("analysis tab missing element %q", id)
		}
	}
	// 调试页签：文档容器 + 刷新入口（数据源由 debug.js 固定为 /web/api/status?format=text）。
	for _, id := range []string{"debug-output", "debug-refresh-btn", "debug-meta"} {
		if !strings.Contains(body, `id="`+id+`"`) {
			t.Fatalf("debug tab missing element %q", id)
		}
	}
	// 关于页签：客户端名必须出现在关于面板内（顶栏已在重排中移除该标题）。
	aboutIdx := strings.Index(body, `id="tab-about"`)
	nameIdx := strings.Index(body[aboutIdx:], "aicli micro web client")
	if aboutIdx < 0 || nameIdx < 0 {
		t.Fatalf("about tab missing client name: panel=%d name=%d", aboutIdx, nameIdx)
	}
	if debugBtnIdx > aboutIdx {
		t.Fatalf("about panel must follow the debug panel: debug-btn=%d about=%d", debugBtnIdx, aboutIdx)
	}
}

// TestHandleChatWebPage_DebugModule 验证调试页签的前端模块随 go:embed 发布，
// 且数据源固定为 /debug 状态文档端点（?format=text）而非其它快照。
func TestHandleChatWebPage_DebugModule(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebPath+"js/debug.js", nil)
	rec := httptest.NewRecorder()

	HandleChatWebPage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/javascript") {
		t.Fatalf("Content-Type = %q, want text/javascript", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/web/api/status?format=text") {
		t.Fatal("debug.js must fetch /web/api/status?format=text")
	}
}

// TestHandleChatWebPage_AboutTokenModule 验证关于页的写令牌显示：
// 页面提供令牌容器与复制按钮，ui.js 以注入的 meta 为主源（零额外请求）、
// /web/api/token 为回退，且 app.js 在启动序列里初始化（打开页面即可见）。
func TestHandleChatWebPage_AboutTokenModule(t *testing.T) {
	pageReq := httptest.NewRequest(http.MethodGet, ChatWebPath, nil)
	pageRec := httptest.NewRecorder()
	HandleChatWebPage(pageRec, pageReq)
	page := pageRec.Body.String()
	for _, want := range []string{`id="about-token-value"`, `id="about-token-copy"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("about panel missing token element %q", want)
		}
	}

	for _, asset := range []struct {
		path string
		want []string
	}{
		{path: "js/ui.js", want: []string{"initAboutToken", `meta[name="aicli-web-token"]`, "/web/api/token"}},
		{path: "app.js", want: []string{"initAboutToken()"}},
	} {
		req := httptest.NewRequest(http.MethodGet, ChatWebPath+asset.path, nil)
		rec := httptest.NewRecorder()
		HandleChatWebPage(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", asset.path, rec.Code)
		}
		body := rec.Body.String()
		for _, want := range asset.want {
			if !strings.Contains(body, want) {
				t.Fatalf("%s missing %q", asset.path, want)
			}
		}
	}
}

// TestHandleChatWebPage_AboutEndpointsModule 验证关于页的端点清单：
// 页面提供渲染容器，ui.js 以 /debug/endpoints?format=json（与 /debug display 同源）
// 为唯一数据源，并按 scheme 分组渲染 —— 新增端点无需改前端。
func TestHandleChatWebPage_AboutEndpointsModule(t *testing.T) {
	pageReq := httptest.NewRequest(http.MethodGet, ChatWebPath, nil)
	pageRec := httptest.NewRecorder()
	HandleChatWebPage(pageRec, pageReq)
	if body := pageRec.Body.String(); !strings.Contains(body, `id="about-endpoints"`) {
		t.Fatalf("about panel missing endpoint list container, got:\n%s", body)
	}

	req := httptest.NewRequest(http.MethodGet, ChatWebPath+"js/ui.js", nil)
	rec := httptest.NewRecorder()
	HandleChatWebPage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"/debug/endpoints?format=json", // 唯一数据源
		"renderAboutEndpoints",         // 分组渲染入口
		"loadAboutEndpoints",           // 页签激活时拉取
		"about-endpoints-auth",         // 写操作令牌标记
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("ui.js missing %q", want)
		}
	}
}

func TestHandleChatWebPage_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, ChatWebPath, nil)
	rec := httptest.NewRecorder()

	HandleChatWebPage(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// GET /web/api/screen — 屏幕快照
// ---------------------------------------------------------------------------

func TestHandleChatWebAPIScreen_TextDefault(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIScreenPath, nil)
	rec := httptest.NewRecorder()

	HandleChatWebAPIScreen(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("screen text body empty")
	}
}

func TestHandleChatWebAPIScreen_JSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIScreenPath+"?format=json", nil)
	rec := httptest.NewRecorder()

	HandleChatWebAPIScreen(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("screen JSON invalid: %v", err)
	}
}

// ---------------------------------------------------------------------------
// GET /web/api/status — 状态快照
// ---------------------------------------------------------------------------

func TestHandleChatWebAPIStatus_JSONDefault(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIStatusPath, nil)
	rec := httptest.NewRecorder()

	HandleChatWebAPIStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("status JSON invalid: %v", err)
	}
}

func TestHandleChatWebAPIStatus_Text(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIStatusPath+"?format=text", nil)
	rec := httptest.NewRecorder()

	HandleChatWebAPIStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
}

// TestHandleChatWebAPIStatus_TextIsDebugDocument 锁定调试页签的数据源与 aicli /debug
// 同源：?format=text 返回 buildChatDebugDisplayDocument 的纯文本（TUI 覆盖层渲染的
// 同一份文档），而不是另造一份摘要。
func TestHandleChatWebAPIStatus_TextIsDebugDocument(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIStatusPath+"?format=text", nil)
	rec := httptest.NewRecorder()

	HandleChatWebAPIStatus(rec, req)

	got := rec.Body.String()
	if want := BuildChatDebugDisplayText(); got != want {
		t.Fatalf("status text = %q, want BuildChatDebugDisplayText() = %q", got, want)
	}
	if strings.TrimSpace(got) == "" {
		t.Fatal("debug status text is empty, want the /debug document")
	}
}

// ---------------------------------------------------------------------------
// GET /web/api/events/schema — 事件 schema
// ---------------------------------------------------------------------------

func TestHandleChatWebAPIEventsSchema(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebAPISchemaPath, nil)
	rec := httptest.NewRecorder()

	HandleChatWebAPIEventsSchema(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var specs []webSSEEventSpec
	if err := json.Unmarshal(rec.Body.Bytes(), &specs); err != nil {
		t.Fatalf("schema JSON invalid: %v", err)
	}
	if len(specs) == 0 {
		t.Fatal("schema empty")
	}
	events := make(map[string]bool)
	for _, s := range specs {
		events[s.Event] = true
	}
	for _, want := range []string{"connected", "turn_start", "turn_end", "assistant_delta", "approval_requested", "question_asked", "heartbeat"} {
		if !events[want] {
			t.Errorf("schema missing event %q", want)
		}
	}
}

// ---------------------------------------------------------------------------
// POST /web/api/input — 输入注入
// ---------------------------------------------------------------------------

func TestHandleChatWebAPIInput_NoSession(t *testing.T) {
	withWebTestSession(t, nil)
	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath,
		strings.NewReader(`{"prompt":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	HandleChatWebAPIInput(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no active chat session") {
		t.Fatalf("body = %q, want no-active-session reason", rec.Body.String())
	}
}

func TestHandleChatWebAPIInput_PromptQueued(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)

	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath,
		strings.NewReader(`{"prompt":"hello web"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	HandleChatWebAPIInput(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response invalid: %v", err)
	}
	if resp["status"] != "queued" {
		t.Fatalf("status = %q, want queued", resp["status"])
	}
	// 输入应真实进入队列
	if count, _ := queuedInteractiveInputState(session); count < 1 {
		t.Fatalf("queued input count = %d, want >= 1", count)
	}
}

func TestHandleChatWebAPIInput_EmptyPrompt(t *testing.T) {
	withWebTestSession(t, newWebTestSession())

	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath,
		strings.NewReader(`{"prompt":"   "}`))
	rec := httptest.NewRecorder()

	HandleChatWebAPIInput(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleChatWebAPIInput_TextPlainFallback(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)

	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath,
		strings.NewReader("plain text prompt"))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()

	HandleChatWebAPIInput(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response invalid: %v", err)
	}
	if resp["status"] != "queued" {
		t.Fatalf("status = %q, want queued", resp["status"])
	}
}

func TestHandleChatWebAPIInput_ApprovalNoActor(t *testing.T) {
	session := newWebTestSession()
	session.RuntimeSession = &runtimechat.Session{ID: "session-web-approval"}
	withWebTestSession(t, session)

	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath,
		strings.NewReader(`{"type":"approval","request_id":"req_1","allow":true}`))
	rec := httptest.NewRecorder()

	HandleChatWebAPIInput(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response invalid: %v", err)
	}
	if resp["status"] != "not_found" {
		t.Fatalf("status = %q, want not_found", resp["status"])
	}
}

func TestHandleChatWebAPIInput_ApprovalMissingRequestID(t *testing.T) {
	withWebTestSession(t, newWebTestSession())

	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath,
		strings.NewReader(`{"type":"approval","allow":true}`))
	rec := httptest.NewRecorder()

	HandleChatWebAPIInput(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleChatWebAPIInput_QuestionNoActor(t *testing.T) {
	session := newWebTestSession()
	session.RuntimeSession = &runtimechat.Session{ID: "session-web-question"}
	withWebTestSession(t, session)

	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath,
		strings.NewReader(`{"type":"question_answer","question_id":"q_1","answer":"yes"}`))
	rec := httptest.NewRecorder()

	HandleChatWebAPIInput(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response invalid: %v", err)
	}
	if resp["status"] != "not_found" {
		t.Fatalf("status = %q, want not_found", resp["status"])
	}
}

func TestHandleChatWebAPIInput_Interrupt(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)

	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath,
		strings.NewReader(`{"type":"interrupt"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	HandleChatWebAPIInput(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response invalid: %v", err)
	}
	if resp["status"] != "interrupted" {
		t.Fatalf("status = %q, want interrupted", resp["status"])
	}
	// 中断标记应已设置，且未向输入队列注入任何消息。
	if !session.IsInterrupted() {
		t.Fatalf("session interrupted flag not set")
	}
	if count, _ := queuedInteractiveInputState(session); count != 0 {
		t.Fatalf("queued input count = %d, want 0 (interrupt must not enqueue)", count)
	}
	// 清理：避免中断清理协程影响后续测试。
	session.ResetInterrupt()
}

func TestHandleChatWebAPIInput_InterruptNoSession(t *testing.T) {
	withWebTestSession(t, nil)

	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath,
		strings.NewReader(`{"type":"interrupt"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	HandleChatWebAPIInput(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no active chat session") {
		t.Fatalf("body = %q, want no-active-session reason", rec.Body.String())
	}
}

func TestHandleChatWebAPIInput_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIInputPath, nil)
	rec := httptest.NewRecorder()

	HandleChatWebAPIInput(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// GET /web/api/events — SSE
// ---------------------------------------------------------------------------

// TestHandleChatWebAPIEvents_Connected 验证首事件为 connected，
// 且连接取消后 handler 正常退出（无泄漏）。
func TestHandleChatWebAPIEvents_Connected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, ChatWebAPIEventsPath, nil).WithContext(ctx)
	rec := newSyncResponseRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		HandleChatWebAPIEvents(rec, req)
	}()

	// 等待 handler 写入 connected 后取消连接。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rec.BodyString(), "event: connected") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE handler did not exit after context cancel")
	}

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	body := rec.BodyString()
	if !strings.Contains(body, "event: connected") {
		t.Fatalf("SSE body missing connected event: %q", body)
	}
	if !strings.Contains(body, `"schema_version":"skill_runtime.sse.v1"`) {
		t.Fatalf("SSE body missing schema_version envelope: %q", body)
	}
	if !strings.Contains(body, "session_active") {
		t.Fatalf("SSE connected data missing session_active: %q", body)
	}
}

// TestHandleChatWebAPIEvents_ForwardsBusEvents 验证 EventBus 事件被映射转发。
func TestHandleChatWebAPIEvents_ForwardsBusEvents(t *testing.T) {
	bus := runtimeevents.NewBus()
	session := newWebTestSession()
	session.RuntimeSession = &runtimechat.Session{ID: "session-sse-forward"}
	session.LocalRuntimeHost = &localChatRuntimeHost{EventBus: bus}
	withWebTestSession(t, session)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIEventsPath, nil).WithContext(ctx)
	rec := newSyncResponseRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		HandleChatWebAPIEvents(rec, req)
	}()

	// 等 resubscribe 循环订阅上 EventBus。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		bus.Publish(runtimeevents.Event{
			Type:      runtimechat.EventLLMRequestStarted,
			SessionID: "session-sse-forward",
			Payload: map[string]interface{}{
				"turn_id": "turn-1", "request_id": "req-1", "model": "test-model",
			},
		})
		if strings.Contains(rec.BodyString(), "event: turn_start") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE handler did not exit after context cancel")
	}

	body := rec.BodyString()
	if !strings.Contains(body, "event: turn_start") {
		t.Fatalf("SSE body missing turn_start: %q", body)
	}
	if !strings.Contains(body, `"model":"test-model"`) {
		t.Fatalf("SSE turn_start missing model field: %q", body)
	}
}

// TestHandleChatWebAPIEvents_ForwardsDynamicStatus 验证动态状态栏事件
// （aicli.chat.dynamic_status → SSE dynamic_status）被映射转发，且 payload
// 字段（active/text/role/interruptible/started_at/elapsed_ms）原样透传。
func TestHandleChatWebAPIEvents_ForwardsDynamicStatus(t *testing.T) {
	bus := runtimeevents.NewBus()
	session := newWebTestSession()
	session.RuntimeSession = &runtimechat.Session{ID: "session-sse-dyn"}
	session.LocalRuntimeHost = &localChatRuntimeHost{EventBus: bus}
	withWebTestSession(t, session)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIEventsPath, nil).WithContext(ctx)
	rec := newSyncResponseRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		HandleChatWebAPIEvents(rec, req)
	}()

	payload := map[string]interface{}{
		"active":        true,
		"text":          "◦ Retrying step=1 sub.aiok.club / openai / deepseek-v4-flash attempt=1/10 reason=transport delay=972ms",
		"role":          string(style.RoleWarning),
		"interruptible": true,
		"started_at":    "2026-09-03T10:00:00Z",
		"elapsed_ms":    int64(972),
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		bus.Publish(runtimeevents.Event{
			Type:      chatWebDynamicStatusBusEvent,
			SessionID: "session-sse-dyn",
			Payload:   payload,
		})
		if strings.Contains(rec.BodyString(), "event: dynamic_status") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE handler did not exit after context cancel")
	}

	body := rec.BodyString()
	if !strings.Contains(body, "event: dynamic_status") {
		t.Fatalf("SSE body missing dynamic_status: %q", body)
	}
	for _, want := range []string{
		`"active":true`,
		`"text":"◦ Retrying step=1 sub.aiok.club / openai / deepseek-v4-flash attempt=1/10 reason=transport delay=972ms"`,
		`"role":"Warning"`,
		`"interruptible":true`,
		`"started_at":"2026-09-03T10:00:00Z"`,
		`"elapsed_ms":972`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("SSE dynamic_status missing %s: %q", want, body)
		}
	}
}

// ---------------------------------------------------------------------------
// 事件映射单元测试
// ---------------------------------------------------------------------------

func TestChatWebSSEEventName(t *testing.T) {
	cases := []struct {
		busEvent string
		want     string
		mapped   bool
	}{
		{runtimechat.EventLLMRequestStarted, "turn_start", true},
		{"llm.request.started", "turn_start", true},
		{runtimechat.EventAssistantReasoningDelta, "reasoning_delta", true},
		{runtimechat.EventAssistantReasoning, "reasoning_delta", true},
		{runtimechat.EventLLMRequestFinished, "turn_end", true},
		{"llm.request.finished", "turn_end", true},
		{runtimechat.EventApprovalRequested, "approval_requested", true},
		{runtimechat.EventQuestionAsked, "question_asked", true},
		{chatWebDynamicStatusBusEvent, "dynamic_status", true},
		{chatWebUserSubmittedBusEvent, "screen_refresh", true},
		{chatWebModelSelectionChangedBusEvent, "model_changed", true},
		{"unknown_event_type", "unknown_event_type", false},
	}
	for _, c := range cases {
		got, mapped := chatWebSSEEventName(c.busEvent)
		if got != c.want || mapped != c.mapped {
			t.Errorf("chatWebSSEEventName(%q) = (%q,%v), want (%q,%v)",
				c.busEvent, got, mapped, c.want, c.mapped)
		}
	}
}

func TestChatWebSSEDataForEvent(t *testing.T) {
	// 1) 旧版 "text" 键（兼容 fallback）
	data := chatWebSSEDataForEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "s1",
		Payload: map[string]interface{}{
			"turn_id": "t1", "stream_id": "st1", "sequence": 3, "text": "hello",
		},
	})
	if data["text"] != "hello" || data["turn_id"] != "t1" {
		t.Fatalf("unexpected data: %#v", data)
	}
	if data["session_id"] != "s1" {
		t.Fatalf("session_id missing: %#v", data)
	}

	// 2) 新版 "delta" 键 → "text"
	data2 := chatWebSSEDataForEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "s1",
		Payload: map[string]interface{}{
			"turn_id": "t1", "stream_id": "st1", "sequence": 1, "delta": "Hel",
		},
	})
	if data2["text"] != "Hel" {
		t.Fatalf("delta→text failed: %#v", data2)
	}
	if _, ok := data2["delta"]; ok {
		t.Fatalf("delta should not be in output: %#v", data2)
	}

	// 3) "content" 键 fallback → "text"
	data3 := chatWebSSEDataForEvent(runtimeevents.Event{
		Type:    runtimechat.EventAssistantDelta,
		Payload: map[string]interface{}{"content": "lo", "turn_id": "t1"},
	})
	if data3["text"] != "lo" {
		t.Fatalf("content→text fallback failed: %#v", data3)
	}

	// 4) reasoning_delta 嵌套 reasoning → "content"
	data4 := chatWebSSEDataForEvent(runtimeevents.Event{
		Type:    runtimechat.EventAssistantReasoningDelta,
		Payload: map[string]interface{}{"reasoning": map[string]interface{}{"summary": "thinking text"}},
	})
	if data4["content"] != "thinking text" {
		t.Fatalf("reasoning→content failed: %#v", data4)
	}

	// 5) reasoning_delta reasoning 为 string → "content"
	data5 := chatWebSSEDataForEvent(runtimeevents.Event{
		Type:    runtimechat.EventAssistantReasoningDelta,
		Payload: map[string]interface{}{"reasoning": "raw thinking"},
	})
	if data5["content"] != "raw thinking" {
		t.Fatalf("reasoning string→content failed: %#v", data5)
	}

	// 6) assistant_image_progress 透传 image 元数据（含 URL 时前端可直接预览）
	data6 := chatWebSSEDataForEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantImageProgress,
		SessionID: "s1",
		Payload: map[string]interface{}{
			"turn_id": "t1",
			"status":  "generating",
			"image": map[string]interface{}{
				"phase": "sampling", "image_id": "img-1", "progress": 0.5,
				"url": "data:image/png;base64,AAAA",
			},
		},
	})
	if data6["status"] != "generating" {
		t.Fatalf("image_progress status failed: %#v", data6)
	}
	img, ok := data6["image"].(map[string]interface{})
	if !ok || img["image_id"] != "img-1" || img["url"] != "data:image/png;base64,AAAA" {
		t.Fatalf("image_progress image field not passed through: %#v", data6)
	}
}

func TestChatWebConnectedPayload_NoSession(t *testing.T) {
	payload := chatWebConnectedPayload(nil)
	if payload["session_active"] != false {
		t.Fatalf("session_active = %v, want false", payload["session_active"])
	}
	if payload["server_version"] == "" {
		t.Fatal("server_version empty")
	}
}

// ---------------------------------------------------------------------------
// 并发安全冒烟测试：多个 Web 输入同时注入不 panic。
// ---------------------------------------------------------------------------

func TestChatWebInputConcurrent(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath,
				strings.NewReader(`{"prompt":"concurrent `+string(rune('a'+n))+`"}`))
			rec := httptest.NewRecorder()
			HandleChatWebAPIInput(rec, req)
		}(i)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// GET /web/api/sessions — 会话列表
// ---------------------------------------------------------------------------

func TestHandleChatWebAPISessions_NoSession(t *testing.T) {
	withWebTestSession(t, nil)
	req := httptest.NewRequest(http.MethodGet, ChatWebAPISessionsPath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Sessions []chatWebSessionListItem `json:"sessions"`
		Current  string                   `json:"current_session_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Sessions) != 0 {
		t.Fatalf("sessions len = %d, want 0", len(body.Sessions))
	}
}

func TestHandleChatWebAPISessions_NoSessionManager(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)
	req := httptest.NewRequest(http.MethodGet, ChatWebAPISessionsPath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Sessions []chatWebSessionListItem `json:"sessions"`
		Current  string                   `json:"current_session_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Sessions) != 0 {
		t.Fatalf("sessions len = %d, want 0", len(body.Sessions))
	}
}

func TestHandleChatWebAPISessions_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, ChatWebAPISessionsPath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISessions(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// POST /web/api/sessions/resume — 恢复会话
// ---------------------------------------------------------------------------

func TestHandleChatWebAPISessionsResume_NoSession(t *testing.T) {
	withWebTestSession(t, nil)
	req := httptest.NewRequest(http.MethodPost, ChatWebAPISessionsResumePath,
		strings.NewReader(`{"session_id":"test-id"}`))
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsResume(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestHandleChatWebAPISessionsResume_InvalidJSON(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)
	req := httptest.NewRequest(http.MethodPost, ChatWebAPISessionsResumePath,
		strings.NewReader(`not json`))
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsResume(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleChatWebAPISessionsResume_EmptySessionID(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)
	req := httptest.NewRequest(http.MethodPost, ChatWebAPISessionsResumePath,
		strings.NewReader(`{"session_id":""}`))
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsResume(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleChatWebAPISessionsResume_NoSessionManager(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)
	req := httptest.NewRequest(http.MethodPost, ChatWebAPISessionsResumePath,
		strings.NewReader(`{"session_id":"test-id"}`))
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsResume(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestHandleChatWebAPISessionsResume_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebAPISessionsResumePath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsResume(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestHandleChatWebAPISessionsNew_NoSession(t *testing.T) {
	withWebTestSession(t, nil)
	req := httptest.NewRequest(http.MethodPost, ChatWebAPISessionsNewPath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsNew(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestHandleChatWebAPISessionsNew_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebAPISessionsNewPath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsNew(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// GET /web/api/sessions — 带真实存储的完整测试
// ---------------------------------------------------------------------------

// newWebTestSessionWithManager 构造一个带 InMemoryStorage SessionManager 的测试会话。
func newWebTestSessionWithManager(t *testing.T) *ChatSession {
	t.Helper()
	storage := runtimechat.NewInMemoryStorage()
	manager := runtimechat.NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)

	ctx := context.Background()
	runtimeSession, err := manager.Create(ctx, "test-user")
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	runtimeSession.Metadata.Title = "Test Session Title"
	_ = storage.Save(ctx, runtimeSession)

	session := newWebTestSession()
	session.SessionManager = manager
	session.SessionUserID = "test-user"
	session.RuntimeSession = runtimeSession
	return session
}

func TestHandleChatWebAPISessions_OrderingCurrentNotPinned(t *testing.T) {
	// 排序语义：
	//   - 默认（无 sort 参数）与 ?sort=created_at：按创建时间降序
	//   - ?sort=updated_at：按更新时间降序
	// 两种模式下当前会话都不置顶（带 current 标记但不排第一）。
	// 构造 CreatedAt 与 UpdatedAt 顺序不同的会话（oldest 最后再 Save 一次），
	// 使两种排序产生不同结果，验证排序参数真实生效。
	storage := runtimechat.NewInMemoryStorage()
	manager := runtimechat.NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)

	ctx := context.Background()
	mk := func(title string) *runtimechat.Session {
		t.Helper()
		s, err := manager.Create(ctx, "test-user")
		if err != nil {
			t.Fatalf("manager.Create: %v", err)
		}
		s.Metadata.Title = title
		s.AddMessage(*runtimetypes.NewUserMessage("conversation seed"))
		if err := storage.Save(ctx, s); err != nil {
			t.Fatalf("storage.Save: %v", err)
		}
		time.Sleep(2 * time.Millisecond) // 保证下一个 Create/Save 的时间戳严格更新
		return s
	}

	oldestCreated := mk("oldest-created") // CreatedAt 最旧
	current := mk("current-mid")          // 创建于中间，设为当前会话
	newestCreated := mk("newest-created") // CreatedAt 最新
	time.Sleep(2 * time.Millisecond)
	// 再 Save 一次 oldestCreated：其 UpdatedAt 变成最新，与 CreatedAt 顺序相反。
	if err := storage.Save(ctx, oldestCreated); err != nil {
		t.Fatalf("storage.Save(oldestCreated): %v", err)
	}

	session := newWebTestSession()
	session.SessionManager = manager
	session.SessionUserID = "test-user"
	session.RuntimeSession = current
	withWebTestSession(t, session)

	fetchOrder := func(sortParam string) ([]string, string) {
		t.Helper()
		target := ChatWebAPISessionsPath
		if sortParam != "" {
			target += "?sort=" + sortParam
		}
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		HandleChatWebAPISessions(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var body struct {
			Sessions []chatWebSessionListItem `json:"sessions"`
			Current  string                   `json:"current_session_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(body.Sessions) != 3 {
			t.Fatalf("sessions len = %d, want 3", len(body.Sessions))
		}
		if body.Current != current.ID {
			t.Fatalf("current_session_id = %q, want %q", body.Current, current.ID)
		}
		gotIDs := make([]string, 0, len(body.Sessions))
		var gotCurrent string
		for _, item := range body.Sessions {
			gotIDs = append(gotIDs, item.ID)
			if item.Current {
				gotCurrent = item.ID
			}
		}
		return gotIDs, gotCurrent
	}

	assertOrder := func(sortParam string, wantOrder []string) {
		t.Helper()
		gotIDs, gotCurrent := fetchOrder(sortParam)
		for i, want := range wantOrder {
			if gotIDs[i] != want {
				t.Fatalf("sort=%q order[%d] = %q, want %q (full: %v)", sortParam, i, gotIDs[i], want, gotIDs)
			}
		}
		if gotCurrent != current.ID {
			t.Fatalf("sort=%q current flag on %q, want %q", sortParam, gotCurrent, current.ID)
		}
	}

	// 创建时间降序：newest > current > oldest；当前会话（中间创建）不置顶。
	createdOrder := []string{newestCreated.ID, current.ID, oldestCreated.ID}
	assertOrder("", createdOrder)
	assertOrder("created_at", createdOrder)
	// 更新时间降序：oldestCreated（最后 Save）> newest > current。
	assertOrder("updated_at", []string{oldestCreated.ID, newestCreated.ID, current.ID})
}

func TestHandleChatWebAPISessions_WithManager(t *testing.T) {
	session := newWebTestSessionWithManager(t)
	withWebTestSession(t, session)
	req := httptest.NewRequest(http.MethodGet, ChatWebAPISessionsPath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Sessions []chatWebSessionListItem `json:"sessions"`
		Current  string                   `json:"current_session_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Sessions) != 1 {
		t.Fatalf("sessions len = %d, want 1", len(body.Sessions))
	}
	item := body.Sessions[0]
	if !item.Current {
		t.Fatalf("current = false, want true")
	}
	if item.Title != "Test Session Title" {
		t.Fatalf("title = %q, want %q", item.Title, "Test Session Title")
	}
	if item.ID == "" {
		t.Fatalf("id is empty")
	}
}

func TestHandleChatWebAPISessionsResume_AlreadyCurrent(t *testing.T) {
	session := newWebTestSessionWithManager(t)
	withWebTestSession(t, session)
	currentID := currentRuntimeSessionID(session)
	if currentID == "" {
		t.Fatal("current session id is empty")
	}
	req := httptest.NewRequest(http.MethodPost, ChatWebAPISessionsResumePath,
		strings.NewReader(`{"session_id":"`+currentID+`"}`))
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsResume(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Status != "already_current" {
		t.Fatalf("status = %q, want %q", body.Status, "already_current")
	}
}

func TestHandleChatWebAPISessionsResume_SessionNotFound(t *testing.T) {
	session := newWebTestSessionWithManager(t)
	withWebTestSession(t, session)
	req := httptest.NewRequest(http.MethodPost, ChatWebAPISessionsResumePath,
		strings.NewReader(`{"session_id":"non-existent-id"}`))
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsResume(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleChatWebAPISessionsResume_SessionBelongsToOtherUser(t *testing.T) {
	session := newWebTestSessionWithManager(t)
	withWebTestSession(t, session)
	// 同一 manager 中创建另一个用户的会话
	other, err := session.SessionManager.Create(context.Background(), "other-user")
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, ChatWebAPISessionsResumePath,
		strings.NewReader(`{"session_id":"`+other.ID+`"}`))
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsResume(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// POST /web/api/sessions/delete — 删除历史会话（§3.5）
// ---------------------------------------------------------------------------

// webPostJSON 发起一次 POST JSON 请求到指定 handler。
func webPostJSON(t *testing.T, target, rawJSON string, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(rawJSON))
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestHandleChatWebAPISessionsDelete_NoSession(t *testing.T) {
	withWebTestSession(t, nil)
	rec := webPostJSON(t, ChatWebAPISessionsDeletePath,
		`{"session_id":"any"}`, HandleChatWebAPISessionsDelete)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChatWebAPISessionsDelete_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebAPISessionsDeletePath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsDelete(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestHandleChatWebAPISessionsDelete_InvalidJSON(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)
	rec := webPostJSON(t, ChatWebAPISessionsDeletePath, `{broken`, HandleChatWebAPISessionsDelete)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChatWebAPISessionsDelete_EmptySessionID(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)
	rec := webPostJSON(t, ChatWebAPISessionsDeletePath, `{"session_id":"  "}`, HandleChatWebAPISessionsDelete)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChatWebAPISessionsDelete_NoSessionManager(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)
	rec := webPostJSON(t, ChatWebAPISessionsDeletePath,
		`{"session_id":"s1"}`, HandleChatWebAPISessionsDelete)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChatWebAPISessionsDelete_SessionNotFound(t *testing.T) {
	session := newWebTestSessionWithManager(t)
	withWebTestSession(t, session)
	rec := webPostJSON(t, ChatWebAPISessionsDeletePath,
		`{"session_id":"no-such-id"}`, HandleChatWebAPISessionsDelete)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChatWebAPISessionsDelete_SessionBelongsToOtherUser(t *testing.T) {
	session := newWebTestSessionWithManager(t)
	withWebTestSession(t, session)
	other, err := session.SessionManager.Create(context.Background(), "other-user")
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	rec := webPostJSON(t, ChatWebAPISessionsDeletePath,
		`{"session_id":"`+other.ID+`"}`, HandleChatWebAPISessionsDelete)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChatWebAPISessionsDelete_CurrentSessionRejected(t *testing.T) {
	session := newWebTestSessionWithManager(t)
	withWebTestSession(t, session)
	rec := webPostJSON(t, ChatWebAPISessionsDeletePath,
		`{"session_id":"`+session.RuntimeSession.ID+`"}`, HandleChatWebAPISessionsDelete)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", rec.Code, rec.Body.String())
	}
	// 删除被拒绝后当前会话必须仍然存在。
	if _, err := session.SessionManager.Get(context.Background(), session.RuntimeSession.ID); err != nil {
		t.Fatalf("current session vanished after rejected delete: %v", err)
	}
}

func TestHandleChatWebAPISessionsDelete_Success(t *testing.T) {
	session := newWebTestSessionWithManager(t)
	withWebTestSession(t, session)
	target, err := session.SessionManager.Create(context.Background(), "test-user")
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	target.Metadata.Title = "doomed"
	target.AddMessage(*runtimetypes.NewUserMessage("to be deleted"))
	if err := session.SessionManager.Update(context.Background(), target); err != nil {
		t.Fatalf("manager.Update: %v", err)
	}

	rec := webPostJSON(t, ChatWebAPISessionsDeletePath,
		`{"session_id":"`+target.ID+`"}`, HandleChatWebAPISessionsDelete)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if _, err := session.SessionManager.Get(context.Background(), target.ID); err == nil {
		t.Fatalf("session %q still present after delete", target.ID)
	}
	// 当前会话不受影响。
	if _, err := session.SessionManager.Get(context.Background(), session.RuntimeSession.ID); err != nil {
		t.Fatalf("current session lost: %v", err)
	}
}

// ---------------------------------------------------------------------------
// POST /web/api/sessions/rename — 重命名会话（§3.5）
// ---------------------------------------------------------------------------

func TestHandleChatWebAPISessionsRename_NoSession(t *testing.T) {
	withWebTestSession(t, nil)
	rec := webPostJSON(t, ChatWebAPISessionsRenamePath,
		`{"session_id":"any","title":"新标题"}`, HandleChatWebAPISessionsRename)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChatWebAPISessionsRename_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebAPISessionsRenamePath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsRename(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestHandleChatWebAPISessionsRename_InvalidJSON(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)
	rec := webPostJSON(t, ChatWebAPISessionsRenamePath, `{broken`, HandleChatWebAPISessionsRename)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChatWebAPISessionsRename_EmptySessionID(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)
	rec := webPostJSON(t, ChatWebAPISessionsRenamePath,
		`{"session_id":"","title":"x"}`, HandleChatWebAPISessionsRename)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChatWebAPISessionsRename_EmptyTitle(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)
	for _, title := range []string{"", "   ", "\t"} {
		rec := webPostJSON(t, ChatWebAPISessionsRenamePath,
			`{"session_id":"s1","title":"`+title+`"}`, HandleChatWebAPISessionsRename)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("title %q: status = %d, want 400; body: %s", title, rec.Code, rec.Body.String())
		}
	}
}

func TestHandleChatWebAPISessionsRename_TitleTooLong(t *testing.T) {
	session := newWebTestSessionWithManager(t)
	withWebTestSession(t, session)
	longTitle := strings.Repeat("长", chatWebSessionTitleMaxRunes+1)
	rec := webPostJSON(t, ChatWebAPISessionsRenamePath,
		`{"session_id":"s1","title":"`+longTitle+`"}`, HandleChatWebAPISessionsRename)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
	// 恰好 100 个字符应通过标题长度校验（后续走 not-found 分支）。
	edgeTitle := strings.Repeat("长", chatWebSessionTitleMaxRunes)
	rec = webPostJSON(t, ChatWebAPISessionsRenamePath,
		`{"session_id":"no-such-id","title":"`+edgeTitle+`"}`, HandleChatWebAPISessionsRename)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChatWebAPISessionsRename_SessionNotFound(t *testing.T) {
	session := newWebTestSessionWithManager(t)
	withWebTestSession(t, session)
	rec := webPostJSON(t, ChatWebAPISessionsRenamePath,
		`{"session_id":"no-such-id","title":"x"}`, HandleChatWebAPISessionsRename)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChatWebAPISessionsRename_SessionBelongsToOtherUser(t *testing.T) {
	session := newWebTestSessionWithManager(t)
	withWebTestSession(t, session)
	other, err := session.SessionManager.Create(context.Background(), "other-user")
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	rec := webPostJSON(t, ChatWebAPISessionsRenamePath,
		`{"session_id":"`+other.ID+`","title":"hijack"}`, HandleChatWebAPISessionsRename)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChatWebAPISessionsRename_HistorySession(t *testing.T) {
	session := newWebTestSessionWithManager(t)
	withWebTestSession(t, session)
	target, err := session.SessionManager.Create(context.Background(), "test-user")
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	target.Metadata.Title = "old title"
	if err := session.SessionManager.Update(context.Background(), target); err != nil {
		t.Fatalf("manager.Update: %v", err)
	}

	rec := webPostJSON(t, ChatWebAPISessionsRenamePath,
		`{"session_id":"`+target.ID+`","title":"  新标题  "}`, HandleChatWebAPISessionsRename)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	got, err := session.SessionManager.Get(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("manager.Get: %v", err)
	}
	if got.Metadata.Title != "新标题" {
		t.Fatalf("title = %q, want 新标题 (trimmed)", got.Metadata.Title)
	}
}

func TestHandleChatWebAPISessionsRename_CurrentSession(t *testing.T) {
	session := newWebTestSessionWithManager(t)
	withWebTestSession(t, session)
	rec := webPostJSON(t, ChatWebAPISessionsRenamePath,
		`{"session_id":"`+session.RuntimeSession.ID+`","title":"当前会话新名"}`,
		HandleChatWebAPISessionsRename)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	// 内存中的 RuntimeSession 元数据标题应同步更新。
	if session.RuntimeSession.Metadata.Title != "当前会话新名" {
		t.Fatalf("in-memory title = %q, want 当前会话新名", session.RuntimeSession.Metadata.Title)
	}
	got, err := session.SessionManager.Get(context.Background(), session.RuntimeSession.ID)
	if err != nil {
		t.Fatalf("manager.Get: %v", err)
	}
	if got.Metadata.Title != "当前会话新名" {
		t.Fatalf("stored title = %q, want 当前会话新名", got.Metadata.Title)
	}
}

// ---------------------------------------------------------------------------
// GET /web/api/screen?tail=N — 末尾行截取
// ---------------------------------------------------------------------------

func TestChatWebScreenTailHelpers(t *testing.T) {
	text := "l1\nl2\nl3\nl4\nl5"

	if got := chatWebTailTextLines(text, 0); got != text {
		t.Fatalf("tail=0 不应裁剪: %q", got)
	}
	if got := chatWebTailTextLines(text, 9); got != text {
		t.Fatalf("tail 超过行数不应裁剪: %q", got)
	}
	if got := chatWebTailTextLines(text, 2); got != "l4\nl5" {
		t.Fatalf("tail=2 = %q, want 末尾两行", got)
	}

	snap := &chatDebugScreenSnapshot{Available: true, Lines: []string{"a", "b", "c"}, Text: "a\nb\nc"}
	chatWebApplyScreenTail(snap, 2)
	if len(snap.Lines) != 2 || snap.Lines[0] != "b" || snap.Text != "b\nc" {
		t.Fatalf("snapshot tail = %+v", snap)
	}

	// ?tail 解析：缺省/非法/非正数 → 0（不裁剪）；超上限 → 钳制。
	cases := []struct {
		query string
		want  int
	}{
		{"", 0},
		{"tail=abc", 0},
		{"tail=0", 0},
		{"tail=-3", 0},
		{"tail=7", 7},
		{"tail=999999", chatWebScreenTailMaxLines},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, ChatWebAPIScreenPath+"?"+tc.query, nil)
		if got := chatWebScreenTailParam(req); got != tc.want {
			t.Fatalf("tail 参数 %q = %d, want %d", tc.query, got, tc.want)
		}
	}
}

// TestHandleChatWebAPIScreen_TailSmoke 验证 ?tail=N 在真实 handler 路径上不报错，
// 且可用时返回行数不超过 N（不可用时会话返回 "Debug Screen:" 一行，同样满足）。
func TestHandleChatWebAPIScreen_TailSmoke(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIScreenPath+"?view=tui&tail=3", nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPIScreen(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := strings.TrimRight(rec.Body.String(), "\n")
	if body == "" {
		t.Fatal("screen tail body empty")
	}
	if lines := strings.Split(body, "\n"); len(lines) > 3 {
		t.Fatalf("tail=3 返回了 %d 行: %q", len(lines), body)
	}
}
