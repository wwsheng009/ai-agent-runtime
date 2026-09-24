package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// POST /web/api/mesh/call —— 被调方一侧（架构 §5.6 / §5.9，实施方案 S8）。
//
// 门禁：
//   - op 白名单（9 项）：未知 op → 400 + mesh_unknown_op；
//   - 写操作必须 allow_write=true，无隐式放行 → 403 + mesh_write_not_allowed；
//   - 非回环一律拒绝 → 403 + mesh_nonloopback_denied；
//   - 请求体上限 1 MiB → 413 + mesh_body_too_large；
//   - target 形如 node-* 时必须等于本进程 node_id → 404 + mesh_target_mismatch；
//   - 分发复用既有端点（node.info → /web/api/mesh/self），结果里**没有令牌原文**；
//   - journal 只记 op 与耗时，不记 args（args 可能含用户 prompt）。

// meshCallRequestBody 构造一次调用的请求体。
func meshCallRequestBody(t *testing.T, body any) []byte {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return payload
}

// meshCallRequest 构造一个「来自回环」的 POST /web/api/mesh/call 请求。
func meshCallRequest(t *testing.T, body any) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, ChatWebAPIMeshCallPath, bytes.NewReader(meshCallRequestBody(t, body)))
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "127.0.0.1:8899"
	req.Header.Set("Content-Type", "application/json")
	return req
}

// meshCallTestHostWithWorkspace 起一个「带工作区档案」的 host：跨工作区收敛
// 判定需要本进程工作区（§9.3），默认的 meshTestHost 不带工作区。
func meshCallTestHostWithWorkspace(t *testing.T, workspace string) *mesh.Host {
	t.Helper()
	t.Setenv("AICLI_MESH_DIR", t.TempDir())
	host := mesh.NewHost(mesh.HostConfig{
		WorkspacePath: workspace,
		WorkspaceName: filepath.Base(workspace),
		Warn:          func(string, ...any) {},
	})
	if err := host.Start(); err != nil {
		t.Fatalf("host.Start: %v", err)
	}
	t.Cleanup(host.Close)
	prev := mesh.Current()
	mesh.SetCurrent(host)
	t.Cleanup(func() { mesh.SetCurrent(prev) })
	return host
}

// decodeMeshCallEnvelope 解析响应信封。
func decodeMeshCallEnvelope(t *testing.T, rec *httptest.ResponseRecorder) mesh.CallEnvelope {
	t.Helper()
	var envelope mesh.CallEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("响应不是 JSON 信封: %v (%s)", err, rec.Body.String())
	}
	return envelope
}

func TestHandleChatWebAPIMeshCall_MethodNotAllowed(t *testing.T) {
	meshTestHost(t)
	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshCall(rec, loopbackRequest(ChatWebAPIMeshCallPath))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow = %q, want POST", allow)
	}
	if envelope := decodeMeshCallEnvelope(t, rec); envelope.Status != mesh.CallStatusError {
		t.Fatalf("status = %q, want error", envelope.Status)
	}
}

func TestHandleChatWebAPIMeshCall_UnknownOp(t *testing.T) {
	meshTestHost(t)
	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshCall(rec, meshCallRequest(t, map[string]any{"op": "shell.exec"}))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400（未知 op 是参数错误）", rec.Code)
	}
	envelope := decodeMeshCallEnvelope(t, rec)
	if envelope.Status != mesh.CallStatusError || envelope.Code != mesh.CallCodeUnknownOp {
		t.Fatalf("envelope = %+v, want error/%s", envelope, mesh.CallCodeUnknownOp)
	}
}

func TestHandleChatWebAPIMeshCall_WriteOpRequiresAllowWrite(t *testing.T) {
	meshTestHost(t)
	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshCall(rec, meshCallRequest(t, map[string]any{
		"op":   "invoke",
		"args": map[string]any{"prompt": "只回复两个字：收到"},
	}))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403（写操作无隐式放行）", rec.Code)
	}
	envelope := decodeMeshCallEnvelope(t, rec)
	if envelope.Status != mesh.CallStatusRefused || envelope.Code != mesh.CallCodeWriteNotAllowed {
		t.Fatalf("envelope = %+v, want refused/%s", envelope, mesh.CallCodeWriteNotAllowed)
	}
	if strings.Contains(rec.Body.String(), "收到") {
		t.Fatalf("错误信封不得回显 args 正文: %s", rec.Body.String())
	}
}

func TestHandleChatWebAPIMeshCall_NonLoopbackRefused(t *testing.T) {
	meshTestHost(t)
	req := meshCallRequest(t, map[string]any{"op": "node.info"})
	req.RemoteAddr = "192.0.2.10:4444"
	req.Host = "192.0.2.10:8899"
	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshCall(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403（跨机默认拒绝）", rec.Code)
	}
	envelope := decodeMeshCallEnvelope(t, rec)
	if envelope.Status != mesh.CallStatusRefused || envelope.Code != mesh.CallCodeNonLoopback {
		t.Fatalf("envelope = %+v, want refused/%s", envelope, mesh.CallCodeNonLoopback)
	}
}

func TestHandleChatWebAPIMeshCall_BodyTooLarge(t *testing.T) {
	meshTestHost(t)
	oversized := bytes.Repeat([]byte("a"), mesh.CallMaxBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, ChatWebAPIMeshCallPath, bytes.NewReader(oversized))
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "127.0.0.1:8899"
	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshCall(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if envelope := decodeMeshCallEnvelope(t, rec); envelope.Code != mesh.CallCodeBodyTooLarge {
		t.Fatalf("code = %q, want %s", envelope.Code, mesh.CallCodeBodyTooLarge)
	}
}

func TestHandleChatWebAPIMeshCall_TargetMismatch(t *testing.T) {
	host := meshTestHost(t)
	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshCall(rec, meshCallRequest(t, map[string]any{
		"op":     "node.info",
		"target": "node-999999-20990101T000000Z",
	}))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404（防串线）", rec.Code)
	}
	envelope := decodeMeshCallEnvelope(t, rec)
	if envelope.Code != mesh.CallCodeTargetMismatch {
		t.Fatalf("code = %q, want %s", envelope.Code, mesh.CallCodeTargetMismatch)
	}
	if !strings.Contains(envelope.Message, host.NodeID()) {
		t.Fatalf("message 应说明本进程 node_id: %q", envelope.Message)
	}
}

func TestHandleChatWebAPIMeshCall_NodeInfoDispatch(t *testing.T) {
	host := meshTestHost(t)
	const rawToken = "0123456789abcdef0123456789abcdef"
	host.SetEndpoint(meshTestEndpoint(), mesh.AuthInfo{Mode: "loopback", Token: rawToken})

	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshCall(rec, meshCallRequest(t, map[string]any{"op": "node.info"}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	envelope := decodeMeshCallEnvelope(t, rec)
	if envelope.Status != mesh.CallStatusOK {
		t.Fatalf("envelope = %+v, want ok", envelope)
	}
	if envelope.NodeID != host.NodeID() || envelope.Op != "node.info" {
		t.Fatalf("envelope node/op = %q/%q, want %q/node.info", envelope.NodeID, envelope.Op, host.NodeID())
	}
	// result 必须是被调端点的原始响应：与 /web/api/mesh/self 同形。
	var result map[string]any
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatalf("result 不是 JSON 对象: %v (%s)", err, string(envelope.Result))
	}
	if nodeID, _ := result["node_id"].(string); nodeID != host.NodeID() {
		t.Fatalf("result.node_id = %q, want %q", nodeID, host.NodeID())
	}
	// M7：令牌绝不因 mesh 调用外泄（默认脱敏）。
	if strings.Contains(rec.Body.String(), rawToken) {
		t.Fatalf("响应泄露了令牌原文: %s", rec.Body.String())
	}
	if auth, ok := result["auth"].(map[string]any); ok {
		if token, _ := auth["token"].(string); strings.Contains(token, rawToken) {
			t.Fatalf("auth.token 未脱敏: %q", token)
		}
	}
}

func TestHandleChatWebAPIMeshCall_ScreenDispatchTextBody(t *testing.T) {
	meshTestHost(t)
	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshCall(rec, meshCallRequest(t, map[string]any{
		"op":   "screen",
		"args": map[string]any{"view": "tui"},
	}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	envelope := decodeMeshCallEnvelope(t, rec)
	if envelope.Status != mesh.CallStatusOK {
		t.Fatalf("envelope = %+v, want ok（无会话时 screen 也应降级 200）", envelope)
	}
	// 端点默认返回 text/plain → 信封必须仍是合法 JSON（折成 JSON 字符串）。
	if len(envelope.Result) == 0 {
		t.Fatalf("screen result 不应为空")
	}
	if !json.Valid(envelope.Result) {
		t.Fatalf("screen result 不是合法 JSON: %s", string(envelope.Result))
	}
}

func TestHandleChatWebAPIMeshCall_JournalRecordsOpOnly(t *testing.T) {
	host := meshTestHost(t)
	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshCall(rec, meshCallRequest(t, map[string]any{
		"op":   "screen",
		"args": map[string]any{"tail": 5, "prompt": "这段文字不得进 journal"},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	entries, err := mesh.ReadJournalEntries(host.Paths().JournalPath(host.NodeID()), 20)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	kinds := map[string]map[string]any{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Kind, "mesh.call.") {
			kinds[entry.Kind] = entry.Detail
		}
	}
	for _, kind := range []string{mesh.JournalCallReceived, mesh.JournalCallCompleted} {
		detail, ok := kinds[kind]
		if !ok {
			t.Fatalf("journal 缺少 %s（现有: %v）", kind, kinds)
		}
		if op, _ := detail["op"].(string); op != "screen" {
			t.Fatalf("%s detail.op = %v, want screen", kind, detail["op"])
		}
		for key, value := range detail {
			if text, _ := value.(string); strings.Contains(text, "这段文字不得进 journal") {
				t.Fatalf("%s detail[%s] 泄露了 args 正文", kind, key)
			}
		}
	}
	if _, ok := kinds[mesh.JournalCallCompleted]["elapsed_ms"]; !ok {
		t.Fatalf("completed 必须带 elapsed_ms: %v", kinds[mesh.JournalCallCompleted])
	}
}

func TestHandleChatWebAPIMeshCall_CrossWorkspaceSwitch(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "ws-self")
	host := meshCallTestHostWithWorkspace(t, workspace)
	paths := host.Paths()
	seedMeshPeerNode(t, paths, "node-peer-same", workspace, "peer-token-same")
	seedMeshPeerNode(t, paths, "node-peer-other", workspace+"-other", "peer-token-other")

	call := func(caller, op string) *httptest.ResponseRecorder {
		body := map[string]any{"op": op}
		if mesh.CallOpIsWrite(op) {
			body["allow_write"] = true
		}
		req := meshCallRequest(t, body)
		req.Header.Set(mesh.CallerNodeHeader, caller)
		rec := httptest.NewRecorder()
		HandleChatWebAPIMeshCall(rec, req)
		return rec
	}

	// 默认（收敛开关关闭）：跨工作区只读放行。
	if rec := call("node-peer-other", "node.info"); rec.Code != http.StatusOK {
		t.Fatalf("默认必须放行跨工作区只读: %d (%s)", rec.Code, rec.Body.String())
	}
	SetChatWebMeshRestrictWorkspace(true)
	t.Cleanup(func() { SetChatWebMeshRestrictWorkspace(false) })

	// 收敛开关开启：只读跨工作区仍放行（§9.3「只读仍允许」，工作区不是权限边界）。
	if rec := call("node-peer-other", "node.info"); rec.Code != http.StatusOK {
		t.Fatalf("只读跨工作区不得被收敛开关拦截: %d (%s)", rec.Code, rec.Body.String())
	}
	// 收敛开关开启：写操作跨工作区拒绝 + 原因码。
	rec := call("node-peer-other", "input")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("收敛开关开启时跨工作区写操作必须拒绝: %d (%s)", rec.Code, rec.Body.String())
	}
	envelope := decodeMeshCallEnvelope(t, rec)
	if envelope.Code != mesh.CallCodeCrossWorkspace {
		t.Fatalf("code = %q, want %s", envelope.Code, mesh.CallCodeCrossWorkspace)
	}
	// 同工作区写操作仍放行（不被收敛开关拦截；后续分发结果不在本测试断言范围）。
	if rec := call("node-peer-same", "input"); rec.Code == http.StatusForbidden {
		t.Fatalf("同工作区写操作不得被收敛开关拦截: %d (%s)", rec.Code, rec.Body.String())
	}
	// 未知调用方：只读不判定工作区（放行），写操作 fail-closed（拒绝）。
	if rec := call("", "node.info"); rec.Code != http.StatusOK {
		t.Fatalf("未知调用方的只读不得被收敛开关拦截: %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := call("", "input"); rec.Code != http.StatusForbidden {
		t.Fatalf("未知调用方的写操作必须拒绝: %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestHandleChatWebAPIMeshCall_DisabledDegrades(t *testing.T) {
	prev := mesh.Current()
	mesh.SetCurrent(nil)
	defer mesh.SetCurrent(prev)

	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshCall(rec, meshCallRequest(t, map[string]any{"op": "node.info"}))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403（网格关闭时拒绝而非 5xx）", rec.Code)
	}
	if envelope := decodeMeshCallEnvelope(t, rec); envelope.Code != mesh.CallCodeMeshDisabled {
		t.Fatalf("code = %q, want %s", envelope.Code, mesh.CallCodeMeshDisabled)
	}
}

func TestChatWebMeshCallPlanFor_ArgWhitelist(t *testing.T) {
	t.Run("node.info 是只读 GET", func(t *testing.T) {
		plan, err := chatWebMeshCallPlanFor("node.info", nil, "")
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if plan.method != http.MethodGet || plan.path != ChatWebAPIMeshSelfPath {
			t.Fatalf("plan = %+v, want GET %s", plan, ChatWebAPIMeshSelfPath)
		}
	})

	t.Run("screen 只搬运 view/tail/format", func(t *testing.T) {
		plan, err := chatWebMeshCallPlanFor("screen", json.RawMessage(`{"view":"tui","tail":50,"format":"json","evil":"1"}`), "")
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if plan.path != ChatWebAPIScreenPath || plan.method != http.MethodGet {
			t.Fatalf("plan = %+v, want GET %s", plan, ChatWebAPIScreenPath)
		}
		if got := plan.query.Encode(); got != "format=json&tail=50&view=tui" {
			t.Fatalf("query = %q", got)
		}
	})

	t.Run("invoke 透传幂等键且丢弃未知参数", func(t *testing.T) {
		plan, err := chatWebMeshCallPlanFor("invoke", json.RawMessage(`{"prompt":"hi","timeout_ms":5000,"evil":"1"}`), "mesh-42-1")
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if plan.path != ChatWebAPIInvokePath || plan.method != http.MethodPost {
			t.Fatalf("plan = %+v, want POST %s", plan, ChatWebAPIInvokePath)
		}
		var body map[string]any
		if err := json.Unmarshal(plan.body, &body); err != nil {
			t.Fatalf("body: %v", err)
		}
		if body["client_request_id"] != "mesh-42-1" || body["prompt"] != "hi" {
			t.Fatalf("body = %v", body)
		}
		if _, ok := body["evil"]; ok {
			t.Fatalf("未知参数必须被丢弃: %v", body)
		}
	})

	t.Run("cancel 折成 interrupt", func(t *testing.T) {
		plan, err := chatWebMeshCallPlanFor("cancel", json.RawMessage(`{"discard_pending":true}`), "")
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if plan.path != ChatWebAPIInputPath {
			t.Fatalf("plan.path = %q, want %s", plan.path, ChatWebAPIInputPath)
		}
		var body map[string]any
		if err := json.Unmarshal(plan.body, &body); err != nil {
			t.Fatalf("body: %v", err)
		}
		if body["type"] != "interrupt" || body["discard_pending"] != true {
			t.Fatalf("body = %v, want type=interrupt + discard_pending=true", body)
		}
	})

	t.Run("sessions.resume 只带 session_id", func(t *testing.T) {
		plan, err := chatWebMeshCallPlanFor("sessions.resume", json.RawMessage(`{"session_id":"s-1","evil":true}`), "")
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if plan.path != ChatWebAPISessionsResumePath {
			t.Fatalf("plan.path = %q", plan.path)
		}
		var body map[string]any
		if err := json.Unmarshal(plan.body, &body); err != nil {
			t.Fatalf("body: %v", err)
		}
		if body["session_id"] != "s-1" || len(body) != 1 {
			t.Fatalf("body = %v, want 只有 session_id", body)
		}
	})

	t.Run("未知 op 报错", func(t *testing.T) {
		if _, err := chatWebMeshCallPlanFor("nope", nil, ""); err == nil {
			t.Fatal("未知 op 必须报错")
		}
	})
}

func TestChatWebMeshCallStatusFromResponse(t *testing.T) {
	cases := []struct {
		code   int
		status string
	}{
		{http.StatusOK, mesh.CallStatusOK},
		{http.StatusAccepted, mesh.CallStatusOK},
		{http.StatusConflict, mesh.CallStatusBusy},
		{http.StatusNotFound, mesh.CallStatusNotFound},
		{http.StatusForbidden, mesh.CallStatusRefused},
		{http.StatusUnauthorized, mesh.CallStatusRefused},
		{http.StatusBadRequest, mesh.CallStatusError},
		{http.StatusInternalServerError, mesh.CallStatusError},
	}
	for _, tc := range cases {
		if got := chatWebMeshCallStatusFromResponse(tc.code, nil); got != tc.status {
			t.Fatalf("status(%d) = %q, want %q", tc.code, got, tc.status)
		}
	}
}

func TestChatWebMeshCallResultBody(t *testing.T) {
	if got := chatWebMeshCallResultBody([]byte(`{"a":1}`)); string(got) != `{"a":1}` {
		t.Fatalf("JSON 必须原样透传: %s", got)
	}
	text := chatWebMeshCallResultBody([]byte("Debug Screen: unavailable\n"))
	var decoded string
	if err := json.Unmarshal(text, &decoded); err != nil {
		t.Fatalf("text 必须折成 JSON 字符串: %v (%s)", err, string(text))
	}
	if !strings.Contains(decoded, "Debug Screen") {
		t.Fatalf("decoded = %q", decoded)
	}
	if got := chatWebMeshCallResultBody(nil); got != nil {
		t.Fatalf("空体 → nil，收到 %s", got)
	}
	if !chatWebMeshCallDuplicate([]byte(`{"status":"completed","duplicate":true}`)) {
		t.Fatal("duplicate=true 必须被识别")
	}
	if chatWebMeshCallDuplicate([]byte(`{"status":"completed"}`)) {
		t.Fatal("缺省 duplicate 必须为 false")
	}
}

func TestHandleChatWebAPIMeshCall_TimeoutParamPassThrough(t *testing.T) {
	// timeout_ms 是调用方的等待上限，被调方只把它透传给端点；这里断言信封的
	// elapsed_ms 是整数毫秒且非负（§5.6 信封契约）。
	meshTestHost(t)
	rec := httptest.NewRecorder()
	started := time.Now()
	HandleChatWebAPIMeshCall(rec, meshCallRequest(t, map[string]any{"op": "node.info", "timeout_ms": 1000}))
	envelope := decodeMeshCallEnvelope(t, rec)
	if envelope.ElapsedMs < 0 || envelope.ElapsedMs > time.Since(started).Milliseconds()+1000 {
		t.Fatalf("elapsed_ms = %d 不合理", envelope.ElapsedMs)
	}
	if envelope.SchemaVersion != mesh.SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", envelope.SchemaVersion, mesh.SchemaVersion)
	}
}
