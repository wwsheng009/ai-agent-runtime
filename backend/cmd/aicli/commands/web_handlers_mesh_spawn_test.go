package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// POST /web/api/mesh/spawn —— 被调方一侧（架构 §5.7 / §5.9，实施方案 S9）。
//
// 门禁与契约：
//   - 非回环一律拒绝 → 403 + mesh_nonloopback_denied（拉起属于写路径，§5.8）；
//   - --mesh-allow-spawn=false → 403 + mesh_spawn_not_allowed（默认开启）；
//   - 网格关闭 → 403 + mesh_disabled（降级不 panic，§4.7）；
//   - session_id 必填；port / wait_ms 越界、detach=false、非 JSON → 400；
//   - 请求体上限 1 MiB → 413；
//   - 四态映射：reused/started → 200，not_running → 504，failed → 500（§5.9）；
//   - 令牌只出现在响应体的 url 字段里（M7）。
//
// 进程启动本身由 internal/mesh 的 spawn 测试用注入式 launcher 覆盖：这里通过
// chatWebMeshSpawnFn 替换拉起实现，验证的是 HTTP 层的参数翻译与状态映射，
// 而不是真的去 fork 一个 aicli。

// meshSpawnRequestBody 序列化一次拉起的请求体。
func meshSpawnRequestBody(t *testing.T, body any) []byte {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return payload
}

// meshSpawnRequest 构造一个「来自回环」的 POST /web/api/mesh/spawn 请求。
func meshSpawnRequest(t *testing.T, body any) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, ChatWebAPIMeshSpawnPath, bytes.NewReader(meshSpawnRequestBody(t, body)))
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "127.0.0.1:8899"
	req.Header.Set("Content-Type", "application/json")
	return req
}

// decodeMeshSpawnResponse 解析拉起响应。
func decodeMeshSpawnResponse(t *testing.T, rec *httptest.ResponseRecorder) ChatWebAPIMeshSpawnResponse {
	t.Helper()
	var resp ChatWebAPIMeshSpawnResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON: %v (%s)", err, rec.Body.String())
	}
	return resp
}

// stubMeshSpawn 把拉起实现替换为 fn（生产路径是 mesh.Spawn）。
func stubMeshSpawn(t *testing.T, fn func(mesh.SpawnRequest, mesh.SpawnOptions) mesh.SpawnResult) {
	t.Helper()
	prev := chatWebMeshSpawnFn
	chatWebMeshSpawnFn = fn
	t.Cleanup(func() { chatWebMeshSpawnFn = prev })
}

func TestHandleChatWebAPIMeshSpawn_MethodNotAllowed(t *testing.T) {
	meshTestHost(t)
	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshSpawn(rec, loopbackRequest(ChatWebAPIMeshSpawnPath))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow = %q, want POST", allow)
	}
	if resp := decodeMeshSpawnResponse(t, rec); resp.Status != "error" {
		t.Fatalf("status = %q, want error", resp.Status)
	}
}

func TestHandleChatWebAPIMeshSpawn_MeshDisabled(t *testing.T) {
	prev := mesh.Current()
	mesh.SetCurrent(nil)
	t.Cleanup(func() { mesh.SetCurrent(prev) })

	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshSpawn(rec, meshSpawnRequest(t, map[string]any{"session_id": "s1"}))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403（网格关闭只降级不报错）", rec.Code)
	}
	resp := decodeMeshSpawnResponse(t, rec)
	if resp.Status != "refused" || resp.Code != mesh.SpawnCodeMeshDisabled {
		t.Fatalf("resp = %+v, want refused/%s", resp, mesh.SpawnCodeMeshDisabled)
	}
}

func TestHandleChatWebAPIMeshSpawn_DisabledByFlag(t *testing.T) {
	meshTestHost(t)
	prev := ChatWebMeshAllowSpawn()
	SetChatWebMeshAllowSpawn(false)
	t.Cleanup(func() { SetChatWebMeshAllowSpawn(prev) })

	called := false
	stubMeshSpawn(t, func(mesh.SpawnRequest, mesh.SpawnOptions) mesh.SpawnResult {
		called = true
		return mesh.SpawnResult{}
	})

	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshSpawn(rec, meshSpawnRequest(t, map[string]any{"session_id": "s1"}))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	resp := decodeMeshSpawnResponse(t, rec)
	if resp.Status != "refused" || resp.Code != mesh.SpawnCodeNotAllowed {
		t.Fatalf("resp = %+v, want refused/%s", resp, mesh.SpawnCodeNotAllowed)
	}
	if called {
		t.Fatal("开关关闭时不得真的去拉起进程")
	}
}

func TestHandleChatWebAPIMeshSpawn_NonLoopbackRefused(t *testing.T) {
	meshTestHost(t)
	req := meshSpawnRequest(t, map[string]any{"session_id": "s1"})
	req.RemoteAddr = "192.0.2.10:4444"
	req.Host = "192.0.2.10:8899"
	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshSpawn(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403（跨机默认拒绝）", rec.Code)
	}
	resp := decodeMeshSpawnResponse(t, rec)
	if resp.Status != "refused" || resp.Code != mesh.CallCodeNonLoopback {
		t.Fatalf("resp = %+v, want refused/%s", resp, mesh.CallCodeNonLoopback)
	}
}

func TestHandleChatWebAPIMeshSpawn_BadRequests(t *testing.T) {
	meshTestHost(t)
	stubMeshSpawn(t, func(mesh.SpawnRequest, mesh.SpawnOptions) mesh.SpawnResult {
		t.Fatal("非法请求不得触达拉起实现")
		return mesh.SpawnResult{}
	})

	cases := []struct {
		name string
		body any
		want int
	}{
		{"missing session_id", map[string]any{"port": 0}, http.StatusBadRequest},
		{"blank session_id", map[string]any{"session_id": "   "}, http.StatusBadRequest},
		{"port out of range", map[string]any{"session_id": "s1", "port": 70000}, http.StatusBadRequest},
		{"wait_ms out of range", map[string]any{"session_id": "s1", "wait_ms": chatWebMeshSpawnMaxWaitMS + 1}, http.StatusBadRequest},
		{"negative wait_ms", map[string]any{"session_id": "s1", "wait_ms": -1}, http.StatusBadRequest},
		{"detach=false unsupported", map[string]any{"session_id": "s1", "detach": false}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			HandleChatWebAPIMeshSpawn(rec, meshSpawnRequest(t, tc.body))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			if resp := decodeMeshSpawnResponse(t, rec); resp.Status != "error" {
				t.Fatalf("status = %q, want error", resp.Status)
			}
		})
	}

	t.Run("invalid json", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, ChatWebAPIMeshSpawnPath, strings.NewReader("{not json"))
		req.RemoteAddr = "127.0.0.1:54321"
		req.Host = "127.0.0.1:8899"
		rec := httptest.NewRecorder()
		HandleChatWebAPIMeshSpawn(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("body too large", func(t *testing.T) {
		oversized := bytes.Repeat([]byte("a"), mesh.CallMaxBodyBytes+1)
		req := httptest.NewRequest(http.MethodPost, ChatWebAPIMeshSpawnPath, bytes.NewReader(oversized))
		req.RemoteAddr = "127.0.0.1:54321"
		req.Host = "127.0.0.1:8899"
		rec := httptest.NewRecorder()
		HandleChatWebAPIMeshSpawn(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", rec.Code)
		}
		if resp := decodeMeshSpawnResponse(t, rec); resp.Code != mesh.CallCodeBodyTooLarge {
			t.Fatalf("code = %q, want %s", resp.Code, mesh.CallCodeBodyTooLarge)
		}
	})
}

// TestHandleChatWebAPIMeshSpawn_TranslatesRequest 验证 HTTP 层到 mesh.Spawn 的
// 参数翻译：请求体字段、以及必须来自本进程 host 的 Paths / SelfNodeID / PID /
// Journal（spawn 读写的目录必须就是视图看到的目录）。
func TestHandleChatWebAPIMeshSpawn_TranslatesRequest(t *testing.T) {
	host := meshTestHost(t)
	var gotReq mesh.SpawnRequest
	var gotOpts mesh.SpawnOptions
	stubMeshSpawn(t, func(req mesh.SpawnRequest, opts mesh.SpawnOptions) mesh.SpawnResult {
		gotReq, gotOpts = req, opts
		return mesh.SpawnResult{Status: mesh.SpawnStatusReused, SessionID: req.SessionID}
	})

	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshSpawn(rec, meshSpawnRequest(t, map[string]any{
		"session_id": "session_abc",
		"port":       9123,
		"wait_ms":    1500,
	}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if gotReq.SessionID != "session_abc" || gotReq.Port != 9123 || gotReq.WaitMS != 1500 {
		t.Fatalf("req = %+v, want session_abc/9123/1500", gotReq)
	}
	if gotReq.Origin != "web" {
		t.Fatalf("origin = %q, want web（默认审计标签）", gotReq.Origin)
	}
	if gotOpts.Wait != 1500*time.Millisecond {
		t.Fatalf("wait = %s, want 1.5s（wait_ms 必须真的变成预算）", gotOpts.Wait)
	}
	if gotOpts.Paths.Root != host.Paths().Root {
		t.Fatalf("paths = %q, want %q（必须复用本进程解析结果）", gotOpts.Paths.Root, host.Paths().Root)
	}
	if gotOpts.SelfNodeID != host.NodeID() {
		t.Fatalf("selfNodeID = %q, want %q", gotOpts.SelfNodeID, host.NodeID())
	}
	if gotOpts.PID != os.Getpid() {
		t.Fatalf("pid = %d, want %d", gotOpts.PID, os.Getpid())
	}
	if gotOpts.Journal == nil {
		t.Fatal("journal 必须透传（拉起要留审计）")
	}
	if gotOpts.Now == nil {
		t.Fatal("clock 必须透传（否则测试/审计时钟不一致）")
	}
}

// TestHandleChatWebAPIMeshSpawn_StatusMapping 验证 §5.9 的四态映射与 code 透传。
func TestHandleChatWebAPIMeshSpawn_StatusMapping(t *testing.T) {
	meshTestHost(t)
	cases := []struct {
		status   string
		code     string
		wantHTTP int
	}{
		{mesh.SpawnStatusReused, "", http.StatusOK},
		{mesh.SpawnStatusStarted, "", http.StatusOK},
		{mesh.SpawnStatusNotRunning, mesh.SpawnCodeTimeout, http.StatusGatewayTimeout},
		{mesh.SpawnStatusFailed, mesh.SpawnCodeWorkspaceMissing, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			stubMeshSpawn(t, func(req mesh.SpawnRequest, _ mesh.SpawnOptions) mesh.SpawnResult {
				return mesh.SpawnResult{
					Status:    tc.status,
					Code:      tc.code,
					SessionID: req.SessionID,
					Reason:    "reason for " + tc.status,
				}
			})
			rec := httptest.NewRecorder()
			HandleChatWebAPIMeshSpawn(rec, meshSpawnRequest(t, map[string]any{"session_id": "s1"}))

			if rec.Code != tc.wantHTTP {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.wantHTTP, rec.Body.String())
			}
			resp := decodeMeshSpawnResponse(t, rec)
			if resp.Status != tc.status {
				t.Fatalf("status = %q, want %q", resp.Status, tc.status)
			}
			if resp.Code != tc.code {
				t.Fatalf("code = %q, want %q（前端按 code 分支）", resp.Code, tc.code)
			}
			if resp.Reason != "reason for "+tc.status {
				t.Fatalf("reason = %q, want 透传", resp.Reason)
			}
		})
	}
}

// TestHandleChatWebAPIMeshSpawn_TokenOnlyInURL 验证 M7：令牌只允许出现在 url
// 字段里，响应体的其它字段不得带出令牌原文。
func TestHandleChatWebAPIMeshSpawn_TokenOnlyInURL(t *testing.T) {
	meshTestHost(t)
	const token = "spawn-secret-token-0123456789"
	stubMeshSpawn(t, func(req mesh.SpawnRequest, _ mesh.SpawnOptions) mesh.SpawnResult {
		return mesh.SpawnResult{
			Status:    mesh.SpawnStatusReused,
			SessionID: req.SessionID,
			NodeID:    "node-1",
			Port:      8899,
			URL:       "http://127.0.0.1:8899/web?token=" + token + "&session=" + req.SessionID,
			Reason:    "reused",
		}
	})

	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshSpawn(rec, meshSpawnRequest(t, map[string]any{"session_id": "s1"}))

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON: %v (%s)", err, rec.Body.String())
	}
	for key, value := range body {
		text, ok := value.(string)
		if !ok {
			continue
		}
		if strings.Contains(text, token) && key != "url" {
			t.Fatalf("字段 %s 泄露了令牌原文: %q", key, text)
		}
	}
	if url, _ := body["url"].(string); !strings.Contains(url, token) {
		t.Fatalf("url 必须携带令牌（窗口自举）: %q", url)
	}
}

// seedMeshSpawnableNode 写一份「可复用的活节点」档案：live + 心跳新鲜 + 控制面
// 地址 + 令牌。seedMeshPeerNode 刻意不带 endpoint，而 §7.3 的窗口 URL 需要它。
func seedMeshSpawnableNode(t *testing.T, paths mesh.Paths, nodeID, sessionID, token string) {
	t.Helper()
	now := mesh.NowUTC()
	record := mesh.NodeRecord{
		SchemaVersion: mesh.SchemaVersion,
		NodeID:        nodeID,
		PID:           os.Getpid(),
		Kind:          "chat",
		Process:       mesh.ProcessInfo{StartedAt: now.Add(-time.Minute), Origin: "cli"},
		Capabilities:  []string{"mesh"},
		Liveness: mesh.LivenessInfo{
			StartedAt:       now.Add(-time.Minute),
			UpdatedAt:       now,
			HeartbeatAt:     now,
			HeartbeatTTLSec: int(mesh.DefaultHeartbeatTTL / time.Second),
			State:           string(mesh.NodeStateLive),
		},
		Endpoint: &mesh.EndpointInfo{
			Scheme:   "http",
			Host:     "127.0.0.1",
			Port:     8899,
			Loopback: true,
			BaseURL:  "http://127.0.0.1:8899",
		},
		Session: &mesh.SessionInfo{ID: sessionID, Title: "title", ActivatedAt: now},
		Auth:    &mesh.AuthInfo{Mode: "loopback", Token: token},
	}
	if err := mesh.WriteNodeRecord(paths, record); err != nil {
		t.Fatalf("write node record %s: %v", nodeID, err)
	}
}

// TestHandleChatWebAPIMeshSpawn_ReusesLiveNode 走真实 mesh.Spawn：会话已有活节点
// 时复用（不启动任何进程），返回 §7.3 的窗口 URL。
func TestHandleChatWebAPIMeshSpawn_ReusesLiveNode(t *testing.T) {
	host := meshTestHost(t)
	const nodeID = "node-20260101T000000Z-777"
	const token = "reuse-secret-token-0123456789"
	seedMeshSpawnableNode(t, host.Paths(), nodeID, "session_"+nodeID, token)

	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshSpawn(rec, meshSpawnRequest(t, map[string]any{"session_id": "session_" + nodeID}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	resp := decodeMeshSpawnResponse(t, rec)
	if resp.Status != mesh.SpawnStatusReused {
		t.Fatalf("status = %q, want reused（已有活节点不得再拉一个）", resp.Status)
	}
	if resp.NodeID != nodeID {
		t.Fatalf("node_id = %q, want %q", resp.NodeID, nodeID)
	}
	if !strings.Contains(resp.URL, "token="+token) {
		t.Fatalf("url 必须带该节点档案里的令牌: %q", resp.URL)
	}
	if !strings.Contains(resp.URL, "session=session_"+nodeID) {
		t.Fatalf("url 必须带会话: %q", resp.URL)
	}
	if !strings.HasSuffix(resp.URL, "/web?token="+token+"&session=session_"+nodeID) {
		t.Fatalf("url 形状应匹配 §7.3: %q", resp.URL)
	}
}

// S9 前端契约：会话列表的「在新窗口打开」按钮与 §7.3 深链消费。
//
// 前端无构建步骤，本仓库既有做法是字符串契约断言（见 web_handlers_test.go
// 的 asset 表）+ docs/aicli/web-testing.md 的手工验证，这里沿用。
func TestChatWebSessionsAssetHasMeshSpawnButton(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, ChatWebPath+"js/sessions.js", nil)
	HandleChatWebPage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"/web/api/mesh/spawn"`,
		"openSessionInNewWindow",
		"session-open-btn",
		"applyDeepLinkSession",
		"__aicli_deep_link_session",
		"win.location.replace",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("js/sessions.js 缺少 %q", want)
		}
	}
	// M7：窗口 URL（含令牌）只交给新窗口，前端不得把它写进任何浏览器存储。
	if strings.Contains(body, "aicli-web-token") {
		t.Fatalf("js/sessions.js 不应接触令牌存储键")
	}
}

// §7.3 深链自举：/web?token=<t>&session=<id> —— 令牌在内联脚本最前面转存
// sessionStorage（模块顶层的第一个 fetch 之前）并从地址栏抹掉，session 留给
// 前端 applyDeepLinkSession 对齐。
func TestChatWebPageDeepLinkBootstrap(t *testing.T) {
	withTestWebToken(t, "deep-link-token-0123")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, ChatWebPath, nil)
	HandleChatWebPage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	page := rec.Body.String()
	for _, want := range []string{
		`name="aicli-web-token" content="deep-link-token-0123"`,
		"URLSearchParams",
		"history.replaceState",
		"__aicli_deep_link_session",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("/web 缺少深链自举片段 %q", want)
		}
	}
	// 必须在 </head> 之前注入：内联脚本先于 <script type="module"> 执行。
	if strings.Index(page, "__aicli_deep_link_session") > strings.Index(page, "</head>") {
		t.Fatalf("深链自举必须在 </head> 之前")
	}
}
