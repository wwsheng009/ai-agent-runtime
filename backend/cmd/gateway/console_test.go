package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func getConsole(path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	consoleHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestConsoleServesIndex(t *testing.T) {
	rec := getConsole("/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "aicli-gateway") {
		t.Fatalf("index body missing title: %s", rec.Body.String())
	}
}

func TestConsoleServesAssets(t *testing.T) {
	for _, path := range []string{"/style.css", "/app.js", "/sw.js", "/icon.svg", "/icon-192.png", "/icon-512.png", "/icon-180.png", "/badge-96.png", "/manifest.webmanifest"} {
		rec := getConsole(path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Fatalf("%s served an empty body", path)
		}
	}

	rec := getConsole("/manifest.webmanifest")
	if ct := rec.Header().Get("Content-Type"); ct != "application/manifest+json" {
		t.Fatalf("manifest content type = %q, want application/manifest+json", ct)
	}
}

// TestConsoleNativeNotificationBridge guards the Tauri integration: WebView2
// rejects Web Push subscriptions, so the app must route fresh attention events
// through the native notification API instead.
func TestConsoleNativeNotificationBridge(t *testing.T) {
	app := readConsoleAsset(t, "app.js")
	for _, needle := range []string{
		"function nativeNotificationApi()",
		"window.__TAURI__.notification",
		"function sendNativeNotification(",
		"function nativeAttentionNotification(",
		"nativeAttentionNotification(node, session)",
		"aicli-gw-native-notify",
		// 原生通知桥必须经过网关通知策略（否则关闭 kind 后 App 仍会弹窗）。
		"notifyPolicyAllowsAttention(session.attention, session.session_id)",
		// 点击深链必须带正确的节点 Web 前缀（aicli=/web/），否则反代 403
		// "route not allowed"。
		"sessionOpenTarget(node, session.session_id)",
		"extra.url",
	} {
		if !strings.Contains(app, needle) {
			t.Fatalf("app.js is missing native notification bridge hook %q", needle)
		}
	}
}

// TestConsoleNotifyPolicyAsset guards the standalone 通知设置 page and its
// data plane: the nav entry/view must exist, and the GET/PUT path plus draft
// state must survive refactors (a missing id leaves the page silently empty).
func TestConsoleNotifyPolicyAsset(t *testing.T) {
	index := readConsoleAsset(t, "index.html")
	for _, needle := range []string{
		`data-view="notifications"`, `id="notify-global-enabled"`,
		`id="notify-kinds"`, `id="notify-channels"`,
		`id="notify-session-list"`, `id="notify-save"`,
	} {
		if !strings.Contains(index, needle) {
			t.Fatalf("index.html is missing %q", needle)
		}
	}
	app := readConsoleAsset(t, "app.js")
	for _, needle := range []string{
		`"/api/notify/policy"`, "function loadNotifyPolicy(", "function saveNotifyPolicy(",
		"function renderNotifyPolicy(", "notifications: \"通知设置\"",
	} {
		if !strings.Contains(app, needle) {
			t.Fatalf("app.js is missing notification policy hook %q", needle)
		}
	}
	style := readConsoleAsset(t, "style.css")
	if !strings.Contains(style, ".notify-session-row") {
		t.Fatal("style.css is missing .notify-session-row")
	}
}

// TestConsoleAttentionTrackingWired pins the fresh-attention edge: trackAttention
// must be called from both the full snapshot path and the incremental session
// patch path. The v40 refactor dropped every call site, which silently disabled
// the toast and the Tauri native notification bridge for real attention events
// (only the first load seeded, so nothing ever fired).
func TestConsoleAttentionTrackingWired(t *testing.T) {
	app := readConsoleAsset(t, "app.js")
	for _, needle := range []string{"trackAttention(nodes);", "trackAttention(lastNodes);"} {
		if !strings.Contains(app, needle) {
			t.Fatalf("app.js is missing fresh-attention call %q", needle)
		}
	}
}

// TestConsoleNotificationLocateWired pins the desktop notification UX: clicking a
// toast must only locate the session inside the console (switch view, relax
// filters, scroll + pulse), never navigate to the session web UI.
func TestConsoleNotificationLocateWired(t *testing.T) {
	app := readConsoleAsset(t, "app.js")
	for _, needle := range []string{
		`"aicli-gateway-locate"`,
		"function locateSession(",
		"flushPendingLocate()",
		`sessionFilterKind = "all"`,
		"card-updated",
	} {
		if !strings.Contains(app, needle) {
			t.Fatalf("app.js is missing notification locate hook %q", needle)
		}
	}
}

// TestConsoleApprovalQuestionInline pins the in-console approval/question UX:
// ids come from the node SSE (connected pending_* / approval_requested /
// question_asked) and decisions go back through POST /web/api/input with the
// node's own contract. The session list itself must not expose node-id or
// session-id anymore (both live in the detail dialog).
func TestConsoleApprovalQuestionInline(t *testing.T) {
	index := readConsoleAsset(t, "index.html")
	for _, needle := range []string{
		`id="chat-interactive"`, `id="ci-approve"`, `id="ci-deny"`,
		`id="ci-answer-form"`, `id="ci-answer-input"`, `id="sd-node-id"`,
	} {
		if !strings.Contains(index, needle) {
			t.Fatalf("index.html is missing %q", needle)
		}
	}
	app := readConsoleAsset(t, "app.js")
	for _, needle := range []string{
		"function chatSendApprovalDecision(",
		`{ type: "approval", request_id: requestID, allow: !!allow }`,
		"function chatSendQuestionAnswer(",
		`{ type: "question_answer", question_id: questionID, answer: text }`,
		"d.pending_approval", "d.pending_question",
		"function nodeDisplayName(", "data-attend",
		// 审批参数按「键: 值」行渲染（手机可读性），原始 JSON 兜底。
		"function chatRenderApprovalArgs(",
		"ci-arg-row",
	} {
		if !strings.Contains(app, needle) {
			t.Fatalf("app.js is missing %q", needle)
		}
	}
	if !strings.Contains(readConsoleAsset(t, "style.css"), ".ci-arg-row {") {
		t.Fatal("style.css is missing .ci-arg-row (approval args table)")
	}
	if strings.Contains(app, `card.querySelector(".s-id-code")`) {
		t.Fatal("session list still renders the session id; it must live in the detail dialog")
	}
	if strings.Contains(app, "setText(titleEl, question.question_id") {
		t.Fatal("question bar still exposes the raw question id in its title")
	}
	if !strings.Contains(app, `nodeDisplayName(node) + " · " + (node.kind || "agent")`) {
		t.Fatal("chat panel header must use nodeDisplayName; raw node ids belong to the detail dialog")
	}
	if strings.Contains(app, "return session.title || session.session_id") {
		t.Fatal("session label still falls back to the raw session id; use 未命名会话")
	}
	if !strings.Contains(app, `return title || "未命名会话"`) {
		t.Fatal("session label fallback 未命名会话 is missing")
	}
}

// TestConsoleNextPendingFlowAsset pins the in-panel 「下一条待处理」 flow: the
// header button must exist, the queue must exclude the currently open session,
// and both lifecycle ends (open/close) plus the resolved handlers must refresh
// it. Wiring failures here are silent at runtime (a hidden button is
// indistinguishable from an empty queue), which is exactly why the asset
// contract is tested.
func TestConsoleNextPendingFlowAsset(t *testing.T) {
	index := readConsoleAsset(t, "index.html")
	for _, needle := range []string{
		`id="chat-next-pending"`, `id="chat-next-pending-label"`,
	} {
		if !strings.Contains(index, needle) {
			t.Fatalf("index.html is missing %q", needle)
		}
	}
	app := readConsoleAsset(t, "app.js")
	for _, needle := range []string{
		"function pendingQueue(",
		"function chatUpdateNextPending(",
		"function chatSwitchTo(",
		"function chatOfferNextPending(",
		// The queue must skip the currently open session, or "下一条" would
		// offer to reopen the panel the user is already looking at.
		"item.session.session_id === st.session.session_id",
		// Offer only when this panel actually displayed the resolved item;
		// duplicate/late frames must not re-toast.
		"chatOfferNextPending(); } else { chatUpdateNextPending(); }",
	} {
		if !strings.Contains(app, needle) {
			t.Fatalf("app.js is missing next-pending hook %q", needle)
		}
	}
	// Both panel lifecycle ends refresh the button; missing either leaves a
	// stale count (open) or a visible button over a closed panel (close).
	openAt := strings.Index(app, "function openSessionChat(")
	closeAt := strings.Index(app, "function closeSessionChat(")
	if openAt < 0 || closeAt < 0 {
		t.Fatal("app.js: session chat lifecycle functions not found")
	}
	openBody := app[openAt:]
	if cut := strings.Index(openBody, "// ── 数据面：快照 / 渲染"); cut >= 0 {
		openBody = openBody[:cut]
	}
	closeBody := app[closeAt:]
	if cut := strings.Index(closeBody, "// ── 实时面：SSE 增量"); cut >= 0 {
		closeBody = closeBody[:cut]
	}
	if !strings.Contains(openBody, "chatUpdateNextPending();") {
		t.Fatal("openSessionChat must refresh the next-pending button")
	}
	if !strings.Contains(closeBody, "chatUpdateNextPending();") {
		t.Fatal("closeSessionChat must refresh (hide) the next-pending button")
	}
	// The click handler must guard the element (PWA may cache an older
	// index.html without the button; see the subagent-toggle precedent).
	if !strings.Contains(app, `var nextPendingBtn = el("chat-next-pending");`) {
		t.Fatal("chat-next-pending click handler must guard element existence")
	}
	// renderInbox is the single data-refresh path for the count.
	if !strings.Contains(app, "// 面板头部的「下一条待处理」按钮随同一份 inbox 数据刷新（面板关闭时自隐藏）。") {
		t.Fatal("renderInbox must refresh the next-pending button with the inbox data")
	}
}

// TestConsoleRowClickAndKeyShortcuts pins the unified open semantics: dashboard
// rows (需要处理 / 最近活动 / 节点会话行) go through openSessionInline which
// prefers the in-page panel, and the focused-card keyboard model exposes c/o/a
// for the same three destinations. Either wiring failing is silent at runtime.
func TestConsoleRowClickAndKeyShortcuts(t *testing.T) {
	app := readConsoleAsset(t, "app.js")
	for _, needle := range []string{
		"function openSessionInline(",
		"openSessionInline(item.node, item.session, chatFocusOptsFor(item.session));",
		"openSessionInline(item.node, s);",
		"openSessionInline(node, session);",
		// Keyboard: c/o/a act on the focused card, never with modifiers
		// (Ctrl+C / Ctrl+O must stay browser-native).
		`var action = e.key === "c" || e.key === "o" || e.key === "a";`,
		"!e.ctrlKey && !e.metaKey && !e.altKey",
	} {
		if !strings.Contains(app, needle) {
			t.Fatalf("app.js is missing %q", needle)
		}
	}
}

// TestConsoleReadAttentionPersistence pins the cross-refresh read state:
// markRead persists, boot re-loads with a TTL prune, and trackAttention saves
// after its cleanup sweep. A dropped call site is silent (handled to-dos pop
// back after every reload).
func TestConsoleReadAttentionPersistence(t *testing.T) {
	app := readConsoleAsset(t, "app.js")
	for _, needle := range []string{
		`var READ_ATTENTION_KEY = "aicli-gw-read-attention";`,
		"function loadReadAttention(",
		"function saveReadAttention(",
		"loadReadAttention();",
		"readAttention[attentionKey(node, session)] = Date.now();",
	} {
		if !strings.Contains(app, needle) {
			t.Fatalf("app.js is missing %q", needle)
		}
	}
	if strings.Contains(app, "readAttention[attentionKey(node, session)] = true;") {
		t.Fatal("markRead must store a timestamp (TTL), not a bare true")
	}
}

// TestConsoleDashboardStatDeepLinks pins the dashboard stat-card destinations:
// 在线节点 must land on #nodes (it used to point at the plain sessions list),
// 活跃会话 carries the running filter, and the param must preselect it.
func TestConsoleDashboardStatDeepLinks(t *testing.T) {
	index := readConsoleAsset(t, "index.html")
	for _, needle := range []string{
		`<a class="stat" href="#nodes"><span class="stat-num" id="stat-online">`,
		`<a class="stat" href="#sessions?running=1"><span class="stat-num" id="stat-sessions">`,
	} {
		if !strings.Contains(index, needle) {
			t.Fatalf("index.html is missing %q", needle)
		}
	}
	app := readConsoleAsset(t, "app.js")
	if !strings.Contains(app, `sessionFilterKind = "running";`) {
		t.Fatal("app.js must preselect the running filter for #sessions?running=1")
	}
}

func TestConsoleFallsBackToIndexForUnknownRoutes(t *testing.T) {
	rec := getConsole("/some/spa/route")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "aicli-gateway") {
		t.Fatalf("fallback did not serve index: %s", rec.Body.String())
	}
}

func TestConsoleDoesNotShadowReservedPrefixes(t *testing.T) {
	for _, path := range []string{"/api/unknown", "/gw/agent", "/n/node-x/foo"} {
		rec := getConsole(path)
		if rec.Code != http.StatusNotFound {
			body, _ := io.ReadAll(rec.Body)
			t.Fatalf("%s status = %d, want 404 (body=%s)", path, rec.Code, body)
		}
	}
}

func TestConsoleRejectsTraversal(t *testing.T) {
	rec := getConsole("/../../etc/passwd")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// readConsoleAsset reads a shell asset from the embedded FS, i.e. exactly the
// bytes the console handler ships (and not whatever happens to be on disk).
func readConsoleAsset(t *testing.T, name string) string {
	t.Helper()
	raw, err := consoleFS.ReadFile("console/" + name)
	if err != nil {
		t.Fatalf("read console/%s: %v", name, err)
	}
	return string(raw)
}

func assetVersion(t *testing.T, label, pattern, body string) string {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("%s: %q not found", label, pattern)
	}
	return m[1]
}

// TestConsoleAssetVersionsMatch guards the cache-busting contract: the version
// must be bumped in five places at once (index.html x2, sw.js CACHE + the two
// SHELL URLs, and the register() call in app.js). Miss one and phones keep
// running the old shell for hours/forever (Cloudflare edge-caches *.js/*.css on
// the versioned URL, and the SW serves its cached copy). That failure is
// invisible in development, which is exactly why it needs a test.
func TestConsoleAssetVersionsMatch(t *testing.T) {
	index := readConsoleAsset(t, "index.html")
	app := readConsoleAsset(t, "app.js")
	sw := readConsoleAsset(t, "sw.js")

	want := assetVersion(t, "index.html app.js", `app\.js\?v=(\d+)`, index)
	got := map[string]string{
		"index.html style.css":  assetVersion(t, "index.html style.css", `style\.css\?v=(\d+)`, index),
		"sw.js CACHE":           assetVersion(t, "sw.js CACHE", `aicli-gateway-shell-v(\d+)`, sw),
		"sw.js SHELL app.js":    assetVersion(t, "sw.js SHELL app.js", `app\.js\?v=(\d+)`, sw),
		"sw.js SHELL style.css": assetVersion(t, "sw.js SHELL style.css", `style\.css\?v=(\d+)`, sw),
		"app.js register sw.js": assetVersion(t, "app.js register sw.js", `sw\.js\?v=(\d+)`, app),
	}
	for label, version := range got {
		if version != want {
			t.Fatalf("%s version = %s, want %s (bump all five sites together)", label, version, want)
		}
	}
}

// chatSSERegistered extracts the quoted names of the CHAT_SSE_EVENTS array.
func chatSSERegistered(t *testing.T, app string) map[string]bool {
	t.Helper()
	start := strings.Index(app, "CHAT_SSE_EVENTS = [")
	if start < 0 {
		t.Fatal("app.js: CHAT_SSE_EVENTS array not found")
	}
	rest := app[start:]
	end := strings.Index(rest, "];")
	if end < 0 {
		t.Fatal("app.js: CHAT_SSE_EVENTS array not terminated")
	}
	names := map[string]bool{}
	for _, m := range regexp.MustCompile(`"([a-z_]+)"`).FindAllStringSubmatch(rest[:end], -1) {
		names[m[1]] = true
	}
	if len(names) == 0 {
		t.Fatal("app.js: CHAT_SSE_EVENTS is empty")
	}
	return names
}

// TestConsoleChatPanelAsset pins the two contract points of the session chat
// panel that fail silently at runtime: the modal markup must exist, and the
// EventSource named-event registration must cover every consumed event (an
// unregistered event name is dropped by the browser without any error, the
// historical "assistant_message never arrived" bug — the node-side twin of this
// guard is web_event_sequence_asset_test.go).
func TestConsoleChatPanelAsset(t *testing.T) {
	index := readConsoleAsset(t, "index.html")
	app := readConsoleAsset(t, "app.js")

	if !strings.Contains(index, `id="chat-modal"`) {
		t.Fatal(`index.html is missing id="chat-modal"`)
	}
	for _, needle := range []string{
		"chat-modal", "/web/api/screen", "/web/api/events", "/web/api/input",
		"/web/api/statusbar", "message_window", "chat-dyn-status",
		"screenBusy", "lastScreenAt", "screenETag", "If-None-Match",
		"compact=1", "chatPatchLive", "CHAT_STREAM_RENDER_MS",
		// GFM tables: the block scanner must know the pipe-row rule, otherwise a
		// whole table degrades to a paragraph (and the "-" separator line gets
		// eaten by the list rule first).
		"MD_TABLE_ROW",
	} {
		if !strings.Contains(app, needle) {
			t.Fatalf("app.js is missing %q", needle)
		}
	}
	if !strings.Contains(index, `id="chat-dyn-status"`) || !strings.Contains(index, `id="chat-segs"`) {
		t.Fatal("index.html is missing the chat status bar (#chat-dyn-status / #chat-segs)")
	}

	// Icon-first mobile actions: every icon-only control keeps a name, and the
	// status text lives in its own node so writing it cannot wipe the ::before
	// connection dot (setting textContent on #chat-live-status used to do that).
	for _, needle := range []string{
		`id="chat-live-text"`, `aria-label="跳到最新消息"`,
		`aria-label="更多会话操作"`,
		`aria-label="发送消息"`,
	} {
		if !strings.Contains(index, needle) {
			t.Fatalf("index.html is missing %q", needle)
		}
	}
	// Mobile composer contract: 16px input prevents iOS focus zoom, and the
	// 44px send target must outrank .btn.sm's explicit height.
	for _, needle := range []string{
		`font-size: 16px; line-height: 23px;`,
		`.btn.chat-send {`,
		`min-width: 44px; min-height: 44px;`,
	} {
		if !strings.Contains(readConsoleAsset(t, "style.css"), needle) {
			t.Fatalf("style.css is missing %q", needle)
		}
	}
	if strings.Contains(app, `dot.textContent = (text || "") + suffix`) {
		t.Fatal("chatSetStatus must write #chat-live-text, not #chat-live-status: textContent removes the status dot")
	}

	// The pagination metadata key is message_window; there is no "window" alias
	// (chatDebugScreenSnapshot.MessageWindow). Reading data.window fails
	// silently: has_more stays false and the cursor stays 0, so "load older"
	// never works and nothing errors. Pin the negative too, because the wrong
	// key is the natural thing to write.
	if strings.Contains(app, "data.window") {
		t.Fatal(`app.js reads data.window: the screen response key is message_window`)
	}

	registered := chatSSERegistered(t, app)
	required := []string{
		"connected", "heartbeat", "screen_refresh", "session_switched",
		"session_start", "session_end", "session_interrupted",
		"turn_start", "turn_end",
		"assistant_delta", "assistant_message", "assistant_image_progress", "reasoning_delta",
		"tool_start", "tool_end",
		"approval_requested", "approval_resolved", "question_asked", "question_answered",
		"error",
		// Status bar: dynamic_status carries the Retrying/Analyzing/Running line,
		// model_changed invalidates the static segments. Missing either one leaves
		// the status bar silently dead (the browser drops unregistered frames).
		"dynamic_status", "model_changed",
	}
	for _, name := range required {
		if !registered[name] {
			t.Fatalf("CHAT_SSE_EVENTS never registers %q: the browser would drop that frame silently", name)
		}
	}

	// The reverse direction: a registered name without a handler is dead weight
	// (and a consumed case that is not registered can never fire).
	start := strings.Index(app, "function chatOnEvent")
	if start < 0 {
		t.Fatal("app.js: chatOnEvent not found")
	}
	body := app[start:]
	if cut := strings.Index(body, "function chatResetForSession"); cut >= 0 {
		body = body[:cut]
	}
	handled := map[string]bool{}
	for _, m := range regexp.MustCompile(`case "([a-z_]+)":`).FindAllStringSubmatch(body, -1) {
		handled[m[1]] = true
		// "message" is the unnamed default frame, delivered via onmessage
		// rather than a named listener, so it is legitimately unregistered.
		if m[1] != "message" && !registered[m[1]] {
			t.Fatalf("chatOnEvent handles %q but CHAT_SSE_EVENTS does not register it", m[1])
		}
	}
	for name := range registered {
		if !handled[name] {
			t.Fatalf("CHAT_SSE_EVENTS registers %q but chatOnEvent has no case for it", name)
		}
	}

	// session_start / session_end are per-run boundaries, not session switches: the
	// actor publishes them at the start of every run and right after
	// assistant_message, with a payload carrying only turn_id. Wiring them to a full
	// panel reset wipes the transcript, the scroll position and the reading progress
	// on every single turn (plus a "正在加载消息…" flash) — and nothing else in this
	// file would notice. Only session_switched (which carries a new session_id) may
	// reset the panel.
	for _, name := range []string{`case "session_start":`, `case "session_end":`} {
		at := strings.Index(body, name)
		if at < 0 {
			t.Fatalf("chatOnEvent has no %s", name)
		}
		arm := body[at:]
		if cut := strings.Index(arm, "break;"); cut >= 0 {
			arm = arm[:cut]
		}
		if strings.Contains(arm, "chatResetForSession") {
			t.Fatalf("%s is a per-run boundary, not a session switch: resetting the panel there wipes it every turn", name)
		}
	}

	// A live assistant row must carry content: turn_start sets streaming=true long
	// before the first assistant delta, so gating the row on streaming alone leaves
	// an empty「助手」bubble pinned at the bottom for the whole reasoning/tool phase.
	renderStart := strings.Index(app, "function chatLiveRows(")
	if renderStart < 0 {
		t.Fatal("app.js: chatLiveRows not found")
	}
	renderBody := app[renderStart:]
	if cut := strings.Index(renderBody, "function chatRender()"); cut >= 0 {
		renderBody = renderBody[:cut]
	}
	if strings.Contains(renderBody, "if (st.live.assistant || st.live.streaming)") {
		t.Fatal("chatLiveRows gates the live assistant row on streaming alone: an empty 助手 bubble stays pinned during reasoning/tool phases")
	}
	if !strings.Contains(renderBody, "if (st.live.assistant || liveImages.length)") {
		t.Fatal("chatLiveRows must render the live assistant row only when it has text or images")
	}
	// Streaming deltas must stay on the live-only patch path. Re-entering the
	// full renderer per token reintroduces the whole-transcript scan this guard
	// exists to prevent.
	scheduleStart := strings.Index(app, "function chatScheduleRender")
	if scheduleStart < 0 {
		t.Fatal("app.js: chatScheduleRender not found")
	}
	scheduleBody := app[scheduleStart:]
	if cut := strings.Index(scheduleBody, "function chatCancelRender"); cut >= 0 {
		scheduleBody = scheduleBody[:cut]
	}
	if strings.Contains(scheduleBody, "chatRender()") {
		t.Fatal("chatScheduleRender must patch only live rows, not run the full chatRender")
	}
	if !strings.Contains(scheduleBody, "chatPatchLive()") {
		t.Fatal("chatScheduleRender must call chatPatchLive")
	}
}
