package mesh

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// 调用方编排（架构 §5.6 / §5.9，实施方案 S8）。
//
// 契约：
//   - 未知 op / 写操作未显式允许 → 本地拒绝，**不发任何网络请求**；
//   - 目标解析错误按 §5.6 原因码返回（not_found / stopped / no_endpoint / ambiguous）；
//   - 请求形状固定：POST /web/api/mesh/call + 令牌头 + 调用方头 + 幂等键；
//   - 401（令牌轮换）重读档案重试一次，仍失败 → refused + mesh_token_stale；
//   - 传输层失败折成 unreachable / timeout，绝不 panic、绝不返回 nil；
//   - 节点侧 Call 写 mesh.call.sent（只记 op/目标/状态/耗时，不记 args）。

// callCapture 记录假被调方收到的请求（handler 在别的 goroutine 里跑）。
type callCapture struct {
	mu     sync.Mutex
	method string
	path   string
	token  string
	caller string
	ctype  string
	body   []byte
}

func (c *callCapture) record(r *http.Request) {
	payload, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.method = r.Method
	c.path = r.URL.Path
	c.token = r.Header.Get(CallTokenHeader)
	c.caller = r.Header.Get(CallerNodeHeader)
	c.ctype = r.Header.Get("Content-Type")
	c.body = payload
}

func (c *callCapture) snapshot() callCapture {
	c.mu.Lock()
	defer c.mu.Unlock()
	return callCapture{method: c.method, path: c.path, token: c.token, caller: c.caller, ctype: c.ctype, body: c.body}
}

func (c *callCapture) requestBody(t *testing.T) CallRequestBody {
	t.Helper()
	var body CallRequestBody
	if err := json.Unmarshal(c.snapshot().body, &body); err != nil {
		t.Fatalf("请求体不是 CallRequestBody: %v (%s)", err, string(c.snapshot().body))
	}
	return body
}

// seedCallableNode 写一条「可调用」档案：活节点 + 回环控制面地址 + 写令牌。
func seedCallableNode(t *testing.T, paths Paths, now time.Time, nodeID, baseURL, token string) NodeRecord {
	t.Helper()
	record := viewNodeRecord(nodeID, os.Getpid(), now.Add(-5*time.Second), "", "")
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse base url %q: %v", baseURL, err)
	}
	port, _ := strconv.Atoi(parsed.Port())
	record.Endpoint = &EndpointInfo{
		Scheme:     parsed.Scheme,
		Host:       parsed.Hostname(),
		Port:       port,
		Loopback:   true,
		BaseURL:    baseURL,
		WebBaseURL: baseURL + "/web",
	}
	if token != "" {
		record.Auth = &AuthInfo{Mode: "loopback-dev", Required: true, Token: token, TokenSource: "random"}
	}
	writeViewNode(t, paths, record)
	return record
}

// callEnvelopeServer 起一个假被调方：记录请求并回固定信封。
func callEnvelopeServer(t *testing.T, capture *callCapture, httpStatus int, envelope CallEnvelope) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			capture.record(r)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(httpStatus)
		payload, err := json.Marshal(envelope)
		if err != nil {
			t.Errorf("marshal envelope: %v", err)
			return
		}
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestCallOpWhitelistParity(t *testing.T) {
	ops := CallOps()
	if len(ops) != 9 {
		t.Fatalf("白名单 = %d 项, want 9（§5.6）", len(ops))
	}
	writeOps := map[string]bool{}
	for _, spec := range ops {
		if spec.Endpoint == "" || !strings.HasPrefix(spec.Endpoint, "/web/api/") {
			t.Fatalf("op %s 的端点 %q 非法", spec.Op, spec.Endpoint)
		}
		writeOps[spec.Op] = spec.Write
	}
	for _, op := range []string{"invoke", "input", "cancel", "sessions.resume"} {
		if !writeOps[op] {
			t.Fatalf("op %s 必须是写操作（无隐式放行）", op)
		}
	}
	for _, op := range []string{"node.info", "status", "screen", "turn", "sessions.list"} {
		if writeOps[op] {
			t.Fatalf("op %s 必须是只读操作", op)
		}
	}
	if _, ok := CallOpSpecFor("node.info"); !ok {
		t.Fatal("CallOpSpecFor(node.info) 必须命中")
	}
	if _, ok := CallOpSpecFor("shell.exec"); ok {
		t.Fatal("白名单外 op 必须未命中")
	}
}

func TestCallRejectsBadIntentWithoutNetwork(t *testing.T) {
	paths := testCLIPaths(t)

	// 未知 op：本地拒绝（即使网格里没有任何节点，也必须是 mesh_unknown_op）。
	result := Call(context.Background(), paths, "", CallRequest{Target: "node-any", Op: "shell.exec"})
	if result.Status != CallStatusError || result.Code != CallCodeUnknownOp {
		t.Fatalf("unknown op → %+v, want error/%s", result, CallCodeUnknownOp)
	}
	if result.Attempts != 0 {
		t.Fatalf("本地拒绝不得发起请求: attempts=%d", result.Attempts)
	}

	// 写操作无 --allow-write：refused（无隐式放行）。
	result = Call(context.Background(), paths, "", CallRequest{Target: "node-any", Op: "invoke"})
	if result.Status != CallStatusRefused || result.Code != CallCodeWriteNotAllowed {
		t.Fatalf("invoke 无 allow_write → %+v, want refused/%s", result, CallCodeWriteNotAllowed)
	}
	if result.Attempts != 0 {
		t.Fatalf("本地拒绝不得发起请求: attempts=%d", result.Attempts)
	}
	if result.HTTPStatus != http.StatusForbidden {
		t.Fatalf("http_status = %d, want 403", result.HTTPStatus)
	}

	// 显式允许后才会走到目标解析（目标不存在 → not_found）。
	result = Call(context.Background(), paths, "", CallRequest{Target: "node-any", Op: "invoke", AllowWrite: true})
	if result.Status != CallStatusNotFound || result.Code != CallCodeTargetNotFound {
		t.Fatalf("allow_write 后 → %+v, want not_found/%s", result, CallCodeTargetNotFound)
	}
}

func TestCallTargetResolutionFailures(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()

	stopped := viewNodeRecord("node-stop-20260924T100000Z", os.Getpid(), now.Add(-5*time.Second), "", "")
	stopped.Endpoint = &EndpointInfo{Scheme: "http", Host: "127.0.0.1", Port: 1, Loopback: true, BaseURL: "http://127.0.0.1:1"}
	stopped.Liveness.State = string(NodeStateStopped)
	writeViewNode(t, paths, stopped)

	// 活节点但没有回环控制面（没开 --pprof）。
	writeViewNode(t, paths, viewNodeRecord("node-noend-20260924T100000Z", os.Getpid(), now.Add(-5*time.Second), "", ""))
	// 前缀歧义：两条档案共享前缀，引用不完整 node_id。
	writeViewNode(t, paths, viewNodeRecord("node-amb-20260924T100000Z", os.Getpid(), now.Add(-5*time.Second), "", ""))
	writeViewNode(t, paths, viewNodeRecord("node-amb-20260924T100001Z", os.Getpid(), now.Add(-5*time.Second), "", ""))

	cases := []struct {
		name   string
		target string
		code   string
	}{
		{"空引用", "", CallCodeTargetNotFound},
		{"未知节点", "node-missing", CallCodeTargetNotFound},
		{"已停止", "node-stop-20260924T100000Z", CallCodeTargetStopped},
		{"无控制面", "node-noend-20260924T100000Z", CallCodeNoEndpoint},
		{"前缀歧义", "node-amb", CallCodeTargetAmbiguous},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := Call(context.Background(), paths, "", CallRequest{Target: tc.target, Op: "node.info"})
			if result.Status != CallStatusNotFound || result.Code != tc.code {
				t.Fatalf("target %q → %+v, want not_found/%s", tc.target, result, tc.code)
			}
			if result.Attempts != 0 {
				t.Fatalf("解析失败不得发起请求: attempts=%d", result.Attempts)
			}
		})
	}
}

func TestCallHappyPathEnvelopeAndRequestShape(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	capture := &callCapture{}
	server := callEnvelopeServer(t, capture, http.StatusOK, CallEnvelope{
		SchemaVersion: SchemaVersion,
		Status:        CallStatusOK,
		NodeID:        "node-target-20260924T100000Z",
		Op:            "node.info",
		ElapsedMs:     3,
		Result:        json.RawMessage(`{"node_id":"node-target-20260924T100000Z","auth":{"mode":"loopback","token":"…"}}`),
	})
	seedCallableNode(t, paths, now, "node-target-20260924T100000Z", server.URL, "0f3a-secret-token")

	result := Call(context.Background(), paths, "node-caller-20260924T090000Z", CallRequest{
		Target: "node-target-20260924T100000Z",
		Op:     "node.info",
	})

	if !result.OK() {
		t.Fatalf("result = %+v, want ok", result)
	}
	if result.NodeID != "node-target-20260924T100000Z" || result.Op != "node.info" {
		t.Fatalf("node/op = %q/%q", result.NodeID, result.Op)
	}
	if result.Attempts != 1 || result.HTTPStatus != http.StatusOK {
		t.Fatalf("attempts/http = %d/%d, want 1/200", result.Attempts, result.HTTPStatus)
	}
	if result.ElapsedMs < 0 {
		t.Fatalf("elapsed_ms = %d", result.ElapsedMs)
	}
	var passthrough map[string]any
	if err := json.Unmarshal(result.Result, &passthrough); err != nil {
		t.Fatalf("result 必须原样透传被调方结构: %v (%s)", err, string(result.Result))
	}
	if passthrough["node_id"] != "node-target-20260924T100000Z" {
		t.Fatalf("result = %v", passthrough)
	}

	got := capture.snapshot()
	if got.method != http.MethodPost || got.path != ChatWebMeshCallPath {
		t.Fatalf("请求 = %s %s, want POST %s", got.method, got.path, ChatWebMeshCallPath)
	}
	if got.token != "0f3a-secret-token" {
		t.Fatalf("令牌头 = %q, want 档案里的原文", got.token)
	}
	if got.caller != "node-caller-20260924T090000Z" {
		t.Fatalf("调用方头 = %q", got.caller)
	}
	if !strings.HasPrefix(got.ctype, "application/json") {
		t.Fatalf("content-type = %q", got.ctype)
	}
	body := capture.requestBody(t)
	if body.Op != "node.info" || body.Target != "node-target-20260924T100000Z" {
		t.Fatalf("请求体 = %+v", body)
	}
	if body.TimeoutMs != int(CallDefaultTimeout/time.Millisecond) {
		t.Fatalf("timeout_ms = %d, want 默认 %d", body.TimeoutMs, int(CallDefaultTimeout/time.Millisecond))
	}
	if body.AllowWrite {
		t.Fatalf("只读调用不得声明 allow_write: %+v", body)
	}
}

func TestCallWriteOpSendsAllowWriteAndIdempotencyKey(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	capture := &callCapture{}
	server := callEnvelopeServer(t, capture, http.StatusOK, CallEnvelope{
		SchemaVersion: SchemaVersion,
		Status:        CallStatusOK,
		NodeID:        "node-target-20260924T100000Z",
		Op:            "invoke",
		Result:        json.RawMessage(`{"turn_id":"turn-1","duplicate":false}`),
	})
	seedCallableNode(t, paths, now, "node-target-20260924T100000Z", server.URL, "token-1")

	result := Call(context.Background(), paths, "", CallRequest{
		Target:          "node-target-20260924T100000Z",
		Op:              "invoke",
		Args:            json.RawMessage(`{"prompt":"只回复两个字：收到"}`),
		ClientRequestID: "mesh-42-1",
		AllowWrite:      true,
		Timeout:         5 * time.Second,
	})
	if !result.OK() {
		t.Fatalf("result = %+v, want ok", result)
	}
	body := capture.requestBody(t)
	if !body.AllowWrite {
		t.Fatalf("写操作必须声明 allow_write: %+v", body)
	}
	if body.ClientRequestID != "mesh-42-1" {
		t.Fatalf("client_request_id = %q（幂等键必须透传）", body.ClientRequestID)
	}
	if body.TimeoutMs != 5000 {
		t.Fatalf("timeout_ms = %d, want 5000", body.TimeoutMs)
	}
	if !strings.Contains(string(body.Args), "收到") {
		t.Fatalf("args 必须透传到端点: %s", string(body.Args))
	}
}

func TestCallNonEnvelopeResponseFallsBackToHTTPStatus(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()

	cases := []struct {
		httpStatus int
		want       string
	}{
		{http.StatusConflict, CallStatusBusy},
		{http.StatusNotFound, CallStatusNotFound},
		{http.StatusForbidden, CallStatusRefused},
		{http.StatusServiceUnavailable, CallStatusError},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.httpStatus), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(tc.httpStatus)
				_, _ = io.WriteString(w, "legacy callee said no")
			}))
			t.Cleanup(server.Close)
			nodeID := "node-legacy-" + strconv.Itoa(tc.httpStatus)
			seedCallableNode(t, paths, now, nodeID, server.URL, "")

			result := Call(context.Background(), paths, "", CallRequest{Target: nodeID, Op: "status"})
			if result.Status != tc.want {
				t.Fatalf("status = %q, want %q (%+v)", result.Status, tc.want, result)
			}
			if result.Code != CallCodeUpstream {
				t.Fatalf("code = %q, want %s", result.Code, CallCodeUpstream)
			}
			if !strings.Contains(result.Message, "legacy callee") {
				t.Fatalf("message 应带上原文摘要: %q", result.Message)
			}
		})
	}
}

func TestCall401RetriesOnceWithFreshToken(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	const nodeID = "node-rotate-20260924T100000Z"

	tokens := make(chan string, 4)
	rewrite := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokens <- r.Header.Get(CallTokenHeader)
		// 第一次拒绝，并把档案里的令牌换成新值（模拟目标重启轮换令牌）。
		if len(tokens) == 1 {
			fresh := viewNodeRecord(nodeID, os.Getpid(), now.Add(-time.Second), "", "")
			fresh.Endpoint = &EndpointInfo{Scheme: "http", Host: "127.0.0.1", Port: 1, Loopback: true, BaseURL: serverURL(r)}
			fresh.Auth = &AuthInfo{Mode: "loopback-dev", Required: true, Token: "rotated-token", TokenSource: "random"}
			rewrite <- WriteNodeRecord(paths, fresh)
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"status":"refused","code":"mesh_token_stale"}`)
	}))
	t.Cleanup(server.Close)
	seedCallableNode(t, paths, now, nodeID, server.URL, "stale-token")

	result := Call(context.Background(), paths, "", CallRequest{Target: nodeID, Op: "node.info"})
	if result.Status != CallStatusRefused || result.Code != CallCodeTokenStale {
		t.Fatalf("result = %+v, want refused/%s", result, CallCodeTokenStale)
	}
	if result.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2（401 只重试一次）", result.Attempts)
	}
	if err := <-rewrite; err != nil {
		t.Fatalf("重写档案失败: %v", err)
	}
	close(tokens)
	var seen []string
	for token := range tokens {
		seen = append(seen, token)
	}
	if len(seen) != 2 || seen[0] != "stale-token" || seen[1] != "rotated-token" {
		t.Fatalf("令牌序列 = %v, want [stale-token rotated-token]", seen)
	}
}

// serverURL 从请求里还原假被调方的基地址（handler 内无法直接拿到 server.URL）。
func serverURL(r *http.Request) string {
	return "http://" + r.Host
}

func TestCallTimeoutAndUnreachable(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()

	t.Run("超时折成 timeout", func(t *testing.T) {
		slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(250 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		}))
		t.Cleanup(slow.Close)
		seedCallableNode(t, paths, now, "node-slow-20260924T100000Z", slow.URL, "")

		result := Call(context.Background(), paths, "", CallRequest{
			Target:  "node-slow-20260924T100000Z",
			Op:      "status",
			Timeout: 30 * time.Millisecond,
		})
		if result.Status != CallStatusTimeout {
			t.Fatalf("result = %+v, want timeout", result)
		}
		if result.HTTPStatus != CallHTTPStatus(CallStatusTimeout) {
			t.Fatalf("http_status = %d", result.HTTPStatus)
		}
	})

	t.Run("端口关闭折成 unreachable", func(t *testing.T) {
		dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		deadURL := dead.URL
		dead.Close()
		seedCallableNode(t, paths, now, "node-dead-20260924T100000Z", deadURL, "")

		result := Call(context.Background(), paths, "", CallRequest{Target: "node-dead-20260924T100000Z", Op: "status"})
		if result.Status != CallStatusUnreachable {
			t.Fatalf("result = %+v, want unreachable", result)
		}
		if result.Code != CallCodeUpstream {
			t.Fatalf("code = %q, want %s", result.Code, CallCodeUpstream)
		}
	})
}

func TestHostCallWritesCallerJournalWithoutArgs(t *testing.T) {
	t.Setenv("AICLI_MESH_DIR", t.TempDir())
	host := NewHost(HostConfig{Warn: func(string, ...any) {}})
	if err := host.Start(); err != nil {
		t.Fatalf("host.Start: %v", err)
	}
	t.Cleanup(host.Close)

	paths := host.Paths()
	capture := &callCapture{}
	server := callEnvelopeServer(t, capture, http.StatusOK, CallEnvelope{
		SchemaVersion: SchemaVersion,
		Status:        CallStatusOK,
		NodeID:        "node-target-20260924T100000Z",
		Op:            "screen",
		Result:        json.RawMessage(`{"view":"tui"}`),
	})
	seedCallableNode(t, paths, NowUTC(), "node-target-20260924T100000Z", server.URL, "token-1")

	result := host.Call(context.Background(), CallRequest{
		Target: "node-target-20260924T100000Z",
		Op:     "screen",
		Args:   json.RawMessage(`{"view":"tui","prompt":"这段文字不得进 journal"}`),
	})
	if !result.OK() {
		t.Fatalf("result = %+v, want ok", result)
	}
	if got := capture.snapshot().caller; got != host.NodeID() {
		t.Fatalf("调用方头 = %q, want %q", got, host.NodeID())
	}

	entries, err := ReadJournalEntries(paths.JournalPath(host.NodeID()), 20)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	var sent map[string]any
	for _, entry := range entries {
		if entry.Kind == JournalCallSent {
			sent = entry.Detail
		}
	}
	if sent == nil {
		t.Fatalf("journal 缺少 %s（现有 %d 条）", JournalCallSent, len(entries))
	}
	if sent["op"] != "screen" || sent["target"] != "node-target-20260924T100000Z" || sent["status"] != CallStatusOK {
		t.Fatalf("detail = %v", sent)
	}
	if _, ok := sent["elapsed_ms"]; !ok {
		t.Fatalf("detail 缺 elapsed_ms: %v", sent)
	}
	for key, value := range sent {
		if text, _ := value.(string); strings.Contains(text, "这段文字不得进 journal") {
			t.Fatalf("journal detail[%s] 泄露了 args 正文", key)
		}
	}
}
