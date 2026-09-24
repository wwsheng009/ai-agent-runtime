package commands

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 前端网格实时订阅（`mesh/events` 扇入）的 asset 契约门禁（S12 / Web 子方案 §5.6）。
//
// 前端无构建步骤（ES 模块直接由 <script type="module"> 加载），因此沿用
// web_handlers_mesh_spawn_test.go / web_handlers_mesh_sessions_test.go 的
// asset 字符串断言：锁定订阅拓扑、退避/降级/节流参数与红线，防止这些
// 「只在浏览器里才看得见」的契约在后续改动中被静默删除。
//
// 行为面（真实双进程翻转延迟 ≤2s、断网轮询兜底）见
// docs/aicli/web-testing.md §2.7.2 手工清单；流本身由 E2E-DEBUG-03
// （mesh/realtime-fanin）覆盖。

// TestChatWebSessionsAssetHasMeshRealtimeView 断言 sessions.js 含实时订阅的
// 全部契约标识（§5.6 帧表 + 退避 + 轮询兜底 + 200ms 合并刷新）。
func TestChatWebSessionsAssetHasMeshRealtimeView(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleChatWebPage(rec, httptest.NewRequest(http.MethodGet, ChatWebPath+"js/sessions.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("js/sessions.js: status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	want := []string{
		// 第二条 SSE：只连本进程的扇入端点（R11：与 /web/api/events 并列且独立）。
		"/web/api/mesh/events",
		"EventSource",
		// 续传游标（§9.4：id: <seq> 行 + ?since_seq=）。
		"since_seq",
		"meshLastSeq",
		// §5.6 帧表：全部帧类型都要有落点。
		"mesh.ready",
		"mesh.peer.joined",
		"mesh.peer.left",
		"mesh.peer.updated",
		"mesh.session.changed",
		"mesh.peer.event",
		"mesh.lagged",
		// 断线退避 1s→2s→4s…上限 30s（§5.6）。
		"MESH_RECONNECT_MIN_MS = 1000",
		"MESH_RECONNECT_MAX_MS = 30000",
		// 降级：SSE 不可用 → 10s 轮询兜底（§5.6）。
		"MESH_POLL_INTERVAL_MS = 10000",
		"startMeshPolling",
		"stopMeshPolling",
		// 节流：事件驱动 + 200ms 合并刷新（§5.6 / Q11）。
		"MESH_REFRESH_THROTTLE_MS = 200",
		"scheduleMeshRefresh",
		// 订阅门槛：网格可用（self 非空）才订阅（§4.7 降级不报错）。
		"meshViewAvailable",
		"maybeStartMeshStream",
	}
	for _, token := range want {
		if !strings.Contains(body, token) {
			t.Errorf("js/sessions.js 缺少实时订阅契约标识 %q", token)
		}
	}

	// 前端红线（Web 子方案 §5.5 / M7）：网格逻辑不得触碰令牌原文
	// （本进程令牌统一走 util.js::webAuthToken），也不得自行打开既有
	// /web/api/events 的第二条连接（那条流由 sse.js 独占）。
	for _, deny := range []string{"aicli-web-token", `EventSource("/web/api/events"`} {
		if strings.Contains(body, deny) {
			t.Errorf("js/sessions.js 出现禁止内容 %q（前端红线）", deny)
		}
	}
}

// TestChatWebAssetsShareSingleWebAuthToken 断言本进程令牌的取值逻辑只有一份
// 实现（util.js::webAuthToken），sse.js 与 sessions.js 都复用它——避免
// 「两处各自读 sessionStorage / meta」在未来漂移出不同的令牌来源顺序。
func TestChatWebAssetsShareSingleWebAuthToken(t *testing.T) {
	read := func(path string) string {
		rec := httptest.NewRecorder()
		HandleChatWebPage(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, rec.Code)
		}
		return rec.Body.String()
	}

	utilJS := read(ChatWebPath + "js/util.js")
	if !strings.Contains(utilJS, "export function webAuthToken") {
		t.Fatalf("js/util.js 缺少共享令牌助手 webAuthToken")
	}
	if !strings.Contains(utilJS, "aicli-web-token") {
		t.Fatalf("js/util.js 的 webAuthToken 必须覆盖 meta 注入的令牌名")
	}

	// sse.js（既有流）与 sessions.js（网格流）都只能用助手，不得各写一份。
	for _, path := range []string{ChatWebPath + "js/sse.js", ChatWebPath + "js/sessions.js"} {
		body := read(path)
		if !strings.Contains(body, "webAuthToken") {
			t.Errorf("%s 必须复用 util.js::webAuthToken", path)
		}
		if strings.Contains(body, "aicli-web-token") {
			t.Errorf("%s 不得自行读取令牌原文（只允许经 webAuthToken）", path)
		}
	}
}
