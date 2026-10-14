package qqbot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// ── 假 QQ 开放平台（httptest + gorilla/websocket 假网关） ─────────────────────

type sentMessage struct {
	path    string
	payload map[string]any
}

type recordedPayload struct {
	op int
	d  map[string]any
}

// wsScript 编排单个 WS 连接的服务端行为；index 从 1 开始按连接递增。
// 脚本内不做断言（断言统一在测试协程做，避免测试结束后 t.Errorf 触发 panic）。
type wsScript func(f *fakeQQ, index int, conn *websocket.Conn)

type fakeQQ struct {
	appSecret string
	token     string

	mu           sync.Mutex
	tokenCalls   int
	gatewayCalls int
	connects     int
	identifies   []recordedPayload
	resumes      []recordedPayload
	heartbeats   []string
	sent         []sentMessage
	authHeaders  []string
	tokenStatus  int
	tokenBody    string
	msgStatus    int
	msgBody      string
	meStatus     int
	meBody       string
	// passiveStatus/passiveBody 仅对带 msg_id 的被动回复生效（模拟窗口过期
	// 400/404 等可降级错误）；主动路径（无 msg_id）仍按 msgStatus 响应。
	passiveStatus int
	passiveBody   string
	script        wsScript
	wsURL         string
}

func newFakeQQ(appSecret, token string) *fakeQQ {
	return &fakeQQ{appSecret: appSecret, token: token}
}

var wsUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// fakePNG 是媒体上传/下载测试用的最小 PNG 字节（8 字节签名即可被
// http.DetectContentType 识别为 image/png）。
var fakePNG = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x01, 0x02, 0x03}

func (f *fakeQQ) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/app/getAppAccessToken":
			f.mu.Lock()
			f.tokenCalls++
			status, body, token := f.tokenStatus, f.tokenBody, f.token
			f.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
				return
			}
			writeJSON(w, map[string]any{"access_token": token, "expires_in": "7200"})
		case r.URL.Path == "/gateway" || r.URL.Path == "/gateway/bot":
			f.mu.Lock()
			f.gatewayCalls++
			f.authHeaders = append(f.authHeaders, r.Header.Get("Authorization"))
			wsURL := f.wsURL
			f.mu.Unlock()
			writeJSON(w, map[string]any{"url": wsURL})
		case r.URL.Path == "/users/@me":
			f.mu.Lock()
			status, body := f.meStatus, f.meBody
			f.authHeaders = append(f.authHeaders, r.Header.Get("Authorization"))
			f.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
				return
			}
			writeJSON(w, map[string]any{
				"id":       "bot-openid-1",
				"username": "测试机器人",
				"avatar":   "https://example.com/avatar.png",
			})
		case strings.HasPrefix(r.URL.Path, "/media/"):
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(fakePNG)
		case r.URL.Path == "/ws":
			f.mu.Lock()
			f.connects++
			index := f.connects
			script := f.script
			f.mu.Unlock()
			conn, err := wsUpgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			if script != nil {
				script(f, index, conn)
				return
			}
			drainGateway(conn)
		case strings.HasPrefix(r.URL.Path, "/v2/"):
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			f.mu.Lock()
			f.authHeaders = append(f.authHeaders, r.Header.Get("Authorization"))
			if _, hasMsgID := payload["msg_id"]; hasMsgID && f.passiveStatus != 0 {
				status, body := f.passiveStatus, f.passiveBody
				f.mu.Unlock()
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
				return
			}
			if strings.HasSuffix(r.URL.Path, "/files") {
				f.sent = append(f.sent, sentMessage{path: r.URL.Path, payload: payload})
				f.mu.Unlock()
				writeJSON(w, map[string]any{"file_info": "file-info-1"})
				return
			}
			status, body := f.msgStatus, f.msgBody
			if status == 0 {
				f.sent = append(f.sent, sentMessage{path: r.URL.Path, payload: payload})
			}
			f.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
				return
			}
			writeJSON(w, map[string]any{"id": "msg-out-1"})
		default:
			http.NotFound(w, r)
		}
	})
}

func (f *fakeQQ) tokenCallsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenCalls
}

func (f *fakeQQ) gatewayCallsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gatewayCalls
}

func (f *fakeQQ) connectsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connects
}

func (f *fakeQQ) identifyPayloads() []recordedPayload {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedPayload(nil), f.identifies...)
}

func (f *fakeQQ) resumePayloads() []recordedPayload {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedPayload(nil), f.resumes...)
}

func (f *fakeQQ) heartbeatSeqs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.heartbeats...)
}

func (f *fakeQQ) sentMessages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

func (f *fakeQQ) recordedAuthHeaders() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.authHeaders...)
}

func (f *fakeQQ) recordIdentify(payload gatewayPayload) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identifies = append(f.identifies, recordedPayload{op: payload.Op, d: decodeRawObject(payload.D)})
}

func (f *fakeQQ) recordResume(payload gatewayPayload) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumes = append(f.resumes, recordedPayload{op: payload.Op, d: decodeRawObject(payload.D)})
}

func (f *fakeQQ) recordHeartbeat(payload gatewayPayload) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heartbeats = append(f.heartbeats, strings.TrimSpace(string(payload.D)))
}

func decodeRawObject(raw json.RawMessage) map[string]any {
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

// newFakeQQServer 启动假平台；Cleanup 顺序保证轮询先停（cancel+wait 后注册、
// 先执行），再由 httptest.Server.Close 关闭剩余连接。
func newFakeQQServer(t *testing.T, fake *fakeQQ) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(fake.handler())
	fake.mu.Lock()
	fake.wsURL = "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	fake.mu.Unlock()
	t.Cleanup(server.Close)
	return server
}

// ── WS 脚本辅助（服务端视角，无断言） ────────────────────────────────────────

func writeHello(conn *websocket.Conn, intervalMS int) bool {
	return conn.WriteJSON(map[string]any{
		"op": gatewayOpHello,
		"d":  map[string]any{"heartbeat_interval": intervalMS},
	}) == nil
}

func writeRaw(conn *websocket.Conn, payload any) bool {
	return conn.WriteJSON(payload) == nil
}

func readOne(conn *websocket.Conn) (gatewayPayload, bool) {
	var payload gatewayPayload
	if err := conn.ReadJSON(&payload); err != nil {
		return gatewayPayload{}, false
	}
	return payload, true
}

// drainGateway 保持连接直至客户端断开，并应答客户端心跳（op 11）。
func drainGateway(conn *websocket.Conn) {
	for {
		var payload gatewayPayload
		if err := conn.ReadJSON(&payload); err != nil {
			return
		}
		if payload.Op == gatewayOpHeartbeat {
			_ = conn.WriteJSON(map[string]any{"op": gatewayOpHeartbeatACK})
		}
	}
}

func readyEvent(sessionID string, seq int64) map[string]any {
	return map[string]any{
		"op": gatewayOpDispatch, "t": "READY", "s": seq,
		"d": map[string]any{"session_id": sessionID},
	}
}

func resumedEvent(seq int64) map[string]any {
	return map[string]any{
		"op": gatewayOpDispatch, "t": "RESUMED", "s": seq,
		"d": map[string]any{},
	}
}

func groupMessageEvent(id, groupOpenID, memberOpenID, content string, seq int64) map[string]any {
	return map[string]any{
		"op": gatewayOpDispatch, "t": "GROUP_AT_MESSAGE_CREATE", "s": seq,
		"d": map[string]any{
			"id":           id,
			"group_openid": groupOpenID,
			"content":      content,
			"author":       map[string]any{"member_openid": memberOpenID},
		},
	}
}

func c2cMessageEvent(id, userOpenID, content string, seq int64) map[string]any {
	return map[string]any{
		"op": gatewayOpDispatch, "t": "C2C_MESSAGE_CREATE", "s": seq,
		"d": map[string]any{
			"id":      id,
			"content": content,
			"author":  map[string]any{"user_openid": userOpenID},
		},
	}
}

// scriptIdentifyReady 通用脚本：hello → 记录 Identify → READY → 逐个事件 → 保持。
func scriptIdentifyReady(sessionID string, intervalMS int, events ...map[string]any) wsScript {
	return func(f *fakeQQ, _ int, conn *websocket.Conn) {
		if !writeHello(conn, intervalMS) {
			return
		}
		payload, ok := readOne(conn)
		if !ok {
			return
		}
		f.recordIdentify(payload)
		if !writeRaw(conn, readyEvent(sessionID, 1)) {
			return
		}
		for _, event := range events {
			if !writeRaw(conn, event) {
				return
			}
		}
		drainGateway(conn)
	}
}

// ── 测试基础设施 ────────────────────────────────────────────────────────────

type memState struct {
	mu     sync.Mutex
	values map[string]string
}

func newMemState() *memState { return &memState{values: make(map[string]string)} }

func (m *memState) Get(key string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.values[key]
	return value, ok
}

func (m *memState) Set(key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[key] = value
	return nil
}

func testConfig(id, appID, appSecret string) connectors.Config {
	return connectors.Config{
		ID:       id,
		Provider: Name,
		Name:     "qqbot test",
		Enabled:  true,
		Mode:     "gateway",
		Settings: map[string]string{"app_id": appID},
		Secrets:  map[string]string{"app_secret": appSecret},
	}
}

// startPolling 后台运行 RunPolling，并在 Cleanup 里 cancel + 等待退出
// （Cleanup LIFO：先于 server.Close 执行，避免 httptest 关闭时挂起）。
func startPolling(t *testing.T, provider *Provider, cfg connectors.Config, handle connectors.InboundHandler, state connectors.PollingState) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- provider.RunPolling(ctx, cfg, handle, state) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("RunPolling returned %v, want nil after cancel", err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("RunPolling did not stop after cancel")
		}
	})
}

func waitForMessage(t *testing.T, ch <-chan connectors.InboundMessage) connectors.InboundMessage {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("no inbound message within 5s")
		return connectors.InboundMessage{}
	}
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// ── Schema 与令牌 ───────────────────────────────────────────────────────────

func TestSchemaContract(t *testing.T) {
	schema := New().Schema()
	if schema.Provider != Name || schema.Label != "QQ 机器人" {
		t.Fatalf("schema = %+v", schema)
	}
	if len(schema.Modes) != 1 || schema.Modes[0] != "gateway" {
		t.Fatalf("modes = %v, want [gateway]", schema.Modes)
	}
	var appID, appSecret, sandbox, apiBase, markdown, shareSession, intents bool
	for _, field := range schema.Fields {
		switch field.Key {
		case "app_id":
			appID = field.Type == "text" && field.Required
		case "app_secret":
			appSecret = field.Type == "secret" && field.Required
		case "sandbox":
			sandbox = field.Type == "bool" && field.Default == "false"
		case "api_base":
			apiBase = field.Type == "url" && !field.Required
		case "markdown_support":
			markdown = field.Type == "bool" && field.Default == "false"
		case "share_session_in_channel":
			shareSession = field.Type == "bool" && field.Default == "true"
		case "intents":
			intents = field.Type == "number" && !field.Required
		}
	}
	if !appID || !appSecret || !sandbox || !apiBase || !markdown || !shareSession || !intents {
		t.Fatalf("schema fields = %+v", schema.Fields)
	}
	if len(schema.Actions) != 1 || schema.Actions[0].Key != actionCheckCredentials ||
		schema.Actions[0].Kind != "button" {
		t.Fatalf("schema actions = %+v, want check_credentials(button)", schema.Actions)
	}
}

func TestActivateCachesAccessToken(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	for i := 0; i < 2; i++ {
		if _, err := provider.Activate(context.Background(), cfg); err != nil {
			t.Fatalf("activate #%d: %v", i, err)
		}
	}
	if got := fake.tokenCallsCount(); got != 1 {
		t.Fatalf("token calls = %d, want 1 (第二次应命中缓存)", got)
	}

	// 提前刷新：TTL 7200s、提前 5 分钟，把 now 推后 3 小时即视为过期。
	provider.now = func() time.Time { return time.Now().Add(3 * time.Hour) }
	if _, err := provider.Activate(context.Background(), cfg); err != nil {
		t.Fatalf("activate after expiry: %v", err)
	}
	if got := fake.tokenCallsCount(); got != 2 {
		t.Fatalf("token calls = %d, want 2 (过期后应重新获取)", got)
	}

	// 凭据缺失：不发令牌请求。
	if _, err := provider.Activate(context.Background(), testConfig("qq-2", "app-2", "")); err == nil {
		t.Fatal("missing app_secret must fail activation")
	}
	if _, err := provider.Activate(context.Background(), testConfig("qq-3", "", "secret-3")); err == nil {
		t.Fatal("missing app_id must fail activation")
	}
	if got := fake.tokenCallsCount(); got != 2 {
		t.Fatalf("token calls = %d, want 2 (缺凭据不应请求)", got)
	}
}

func TestTokenErrorRedactsSecret(t *testing.T) {
	fake := newFakeQQ("secret-value-1", "token-1")
	fake.tokenStatus = http.StatusBadRequest
	fake.tokenBody = `{"code":100007,"message":"appid or secret is invalid: secret-value-1"}`
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))

	_, err := provider.Activate(context.Background(), testConfig("qq-1", "app-1", "secret-value-1"))
	if err == nil {
		t.Fatal("token error must fail activation")
	}
	if strings.Contains(err.Error(), "secret-value-1") {
		t.Fatalf("app_secret leaked in error: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("error should keep the platform detail: %v", err)
	}
}

func TestGatewayRequestUsesQQBotAuthorization(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	fake.script = scriptIdentifyReady("session-1", 60000)
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL), WithRetryDelay(10*time.Millisecond))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	received := make(chan connectors.InboundMessage, 1)
	startPolling(t, provider, cfg, func(_ context.Context, msg connectors.InboundMessage) error {
		received <- msg
		return nil
	}, newMemState())

	waitFor(t, "identify payload", func() bool { return len(fake.identifyPayloads()) == 1 })
	identifies := fake.identifyPayloads()
	if identifies[0].op != gatewayOpIdentify {
		t.Fatalf("identify op = %d", identifies[0].op)
	}
	if identifies[0].d["token"] != "QQBot token-1" {
		t.Fatalf("identify token = %v", identifies[0].d["token"])
	}
	if identifies[0].d["intents"] != float64(gatewayIntents) {
		t.Fatalf("identify intents = %v, want %d", identifies[0].d["intents"], gatewayIntents)
	}
	if _, ok := identifies[0].d["shard"]; !ok {
		t.Fatalf("identify payload missing shard: %+v", identifies[0].d)
	}
	headers := fake.recordedAuthHeaders()
	if len(headers) == 0 || headers[0] != "QQBot token-1" {
		t.Fatalf("gateway auth headers = %v", headers)
	}
}

// ── Gateway 握手 / 事件 / 游标 ──────────────────────────────────────────────

func TestGatewayIdentifyReadyGroupMessagePersistsCursor(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	fake.script = scriptIdentifyReady("session-1", 60000,
		groupMessageEvent("evt-group-1", "group-openid-1", "member-openid-1", "<@!12345>  你好 群 ", 2))
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL), WithRetryDelay(10*time.Millisecond))
	cfg := testConfig("qq-1", "app-1", "secret-1")
	state := newMemState()

	received := make(chan connectors.InboundMessage, 1)
	startPolling(t, provider, cfg, func(_ context.Context, msg connectors.InboundMessage) error {
		received <- msg
		return nil
	}, state)

	msg := waitForMessage(t, received)
	if msg.ConnectorID != "qq-1" || msg.Provider != Name {
		t.Fatalf("message source = %+v", msg)
	}
	if msg.Chat != "group:group-openid-1" || msg.MessageID != "evt-group-1" {
		t.Fatalf("message chat/id = %+v", msg)
	}
	if msg.Text != "你好 群" {
		t.Fatalf("message text = %q, want 去掉 @ 标记并 trim", msg.Text)
	}
	if msg.UserID != "member-openid-1" || msg.UserName != "member-openid-1" {
		t.Fatalf("message user = %+v", msg)
	}
	if msg.ProviderData["qqbot_event_msg_id"] != "evt-group-1" {
		t.Fatalf("provider data = %+v", msg.ProviderData)
	}

	waitFor(t, "gateway session_id persisted", func() bool {
		value, ok := state.Get(gatewaySessionKey)
		return ok && value == "session-1"
	})
	waitFor(t, "gateway seq persisted", func() bool {
		value, ok := state.Get(gatewaySeqKey)
		return ok && value == "2"
	})
	if len(fake.resumePayloads()) != 0 {
		t.Fatalf("fresh session must Identify, not Resume: %+v", fake.resumePayloads())
	}
}

func TestGatewayC2CMessage(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	fake.script = scriptIdentifyReady("session-c2c", 60000,
		c2cMessageEvent("evt-c2c-1", "user-openid-1", "  hi c2c  ", 2))
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL), WithRetryDelay(10*time.Millisecond))
	cfg := testConfig("qq-1", "app-1", "secret-1")
	state := newMemState()

	received := make(chan connectors.InboundMessage, 1)
	startPolling(t, provider, cfg, func(_ context.Context, msg connectors.InboundMessage) error {
		received <- msg
		return nil
	}, state)

	msg := waitForMessage(t, received)
	if msg.Chat != "user:user-openid-1" || msg.MessageID != "evt-c2c-1" {
		t.Fatalf("message chat/id = %+v", msg)
	}
	if msg.Text != "hi c2c" || msg.UserID != "user-openid-1" {
		t.Fatalf("message = %+v", msg)
	}
	waitFor(t, "gateway seq persisted", func() bool {
		value, ok := state.Get(gatewaySeqKey)
		return ok && value == "2"
	})
}

func TestGatewayResumeAfterRestart(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	fake.script = func(f *fakeQQ, _ int, conn *websocket.Conn) {
		if !writeHello(conn, 60000) {
			return
		}
		payload, ok := readOne(conn)
		if !ok {
			return
		}
		f.recordResume(payload)
		if !writeRaw(conn, resumedEvent(8)) {
			return
		}
		if !writeRaw(conn, c2cMessageEvent("evt-c2c-9", "user-openid-1", "resumed", 9)) {
			return
		}
		drainGateway(conn)
	}
	server := newFakeQQServer(t, fake)

	// 模拟进程重启：新 provider 实例 + 同一份已落盘游标（session_id + seq）。
	state := newMemState()
	_ = state.Set(gatewaySessionKey, "session-1")
	_ = state.Set(gatewaySeqKey, "7")
	provider := New(WithAPIBase(server.URL), WithRetryDelay(10*time.Millisecond))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	received := make(chan connectors.InboundMessage, 1)
	startPolling(t, provider, cfg, func(_ context.Context, msg connectors.InboundMessage) error {
		received <- msg
		return nil
	}, state)

	msg := waitForMessage(t, received)
	if msg.Chat != "user:user-openid-1" || msg.Text != "resumed" {
		t.Fatalf("message = %+v", msg)
	}
	resumes := fake.resumePayloads()
	if len(resumes) != 1 {
		t.Fatalf("resume payloads = %+v", resumes)
	}
	if resumes[0].op != gatewayOpResume {
		t.Fatalf("resume op = %d", resumes[0].op)
	}
	if resumes[0].d["session_id"] != "session-1" || resumes[0].d["seq"] != float64(7) {
		t.Fatalf("resume payload = %+v", resumes[0].d)
	}
	if resumes[0].d["token"] != "QQBot token-1" {
		t.Fatalf("resume token = %v", resumes[0].d["token"])
	}
	if len(fake.identifyPayloads()) != 0 {
		t.Fatalf("restart with cursor must Resume, not Identify: %+v", fake.identifyPayloads())
	}
	waitFor(t, "seq advanced to 9", func() bool {
		value, ok := state.Get(gatewaySeqKey)
		return ok && value == "9"
	})
}

func TestGatewayHeartbeatCarriesLatestSeq(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	fake.script = func(f *fakeQQ, _ int, conn *websocket.Conn) {
		if !writeHello(conn, 20) {
			return
		}
		payload, ok := readOne(conn)
		if !ok {
			return
		}
		f.recordIdentify(payload)
		if !writeRaw(conn, readyEvent("session-hb", 5)) {
			return
		}
		for i := 0; i < 2; i++ {
			heartbeat, ok := readOne(conn)
			if !ok {
				return
			}
			f.recordHeartbeat(heartbeat)
			if heartbeat.Op != gatewayOpHeartbeat {
				return
			}
			if !writeRaw(conn, map[string]any{"op": gatewayOpHeartbeatACK}) {
				return
			}
			if i == 0 {
				if !writeRaw(conn, c2cMessageEvent("evt-hb", "user-openid-1", "心跳后", 6)) {
					return
				}
			}
		}
		drainGateway(conn)
	}
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL), WithRetryDelay(10*time.Millisecond))
	cfg := testConfig("qq-1", "app-1", "secret-1")
	state := newMemState()

	received := make(chan connectors.InboundMessage, 1)
	startPolling(t, provider, cfg, func(_ context.Context, msg connectors.InboundMessage) error {
		received <- msg
		return nil
	}, state)

	waitFor(t, "two heartbeats", func() bool { return len(fake.heartbeatSeqs()) >= 2 })
	seqs := fake.heartbeatSeqs()
	if seqs[0] != "5" || seqs[1] != "6" {
		t.Fatalf("heartbeat seqs = %v, want [5 6]（READY 后 5，事件后 6）", seqs)
	}
	msg := waitForMessage(t, received)
	if msg.Text != "心跳后" {
		t.Fatalf("message = %+v", msg)
	}
	waitFor(t, "seq persisted as 6", func() bool {
		value, ok := state.Get(gatewaySeqKey)
		return ok && value == "6"
	})
}

func TestGatewayReconnectsAndResumes(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	fake.script = func(f *fakeQQ, index int, conn *websocket.Conn) {
		switch index {
		case 1:
			// 首次连接：Identify → READY → 立即掉线。
			if !writeHello(conn, 60000) {
				return
			}
			payload, ok := readOne(conn)
			if !ok {
				return
			}
			f.recordIdentify(payload)
			if !writeRaw(conn, readyEvent("session-1", 1)) {
				return
			}
			_ = conn.Close()
		default:
			// 重连：必须优先 Resume（session_id + seq 已持久化），再收事件。
			if !writeHello(conn, 60000) {
				return
			}
			payload, ok := readOne(conn)
			if !ok {
				return
			}
			f.recordResume(payload)
			if !writeRaw(conn, resumedEvent(2)) {
				return
			}
			if !writeRaw(conn, c2cMessageEvent("evt-after-reconnect", "user-openid-1", "重连后", 3)) {
				return
			}
			drainGateway(conn)
		}
	}
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL), WithRetryDelay(10*time.Millisecond))
	cfg := testConfig("qq-1", "app-1", "secret-1")
	state := newMemState()

	received := make(chan connectors.InboundMessage, 1)
	startPolling(t, provider, cfg, func(_ context.Context, msg connectors.InboundMessage) error {
		received <- msg
		return nil
	}, state)

	msg := waitForMessage(t, received)
	if msg.Chat != "user:user-openid-1" || msg.Text != "重连后" {
		t.Fatalf("message after reconnect = %+v", msg)
	}
	waitFor(t, "second gateway connection", func() bool { return fake.connectsCount() >= 2 })
	if len(fake.identifyPayloads()) != 1 {
		t.Fatalf("identify count = %d, want 1（重连应 Resume）", len(fake.identifyPayloads()))
	}
	resumes := fake.resumePayloads()
	if len(resumes) != 1 {
		t.Fatalf("resume payloads = %+v", resumes)
	}
	if resumes[0].d["session_id"] != "session-1" || resumes[0].d["seq"] != float64(1) {
		t.Fatalf("resume payload = %+v", resumes[0].d)
	}
	waitFor(t, "seq advanced after reconnect", func() bool {
		value, ok := state.Get(gatewaySeqKey)
		return ok && value == "3"
	})
}

// ── Send ────────────────────────────────────────────────────────────────────

func TestSendGroupPassiveReplyIncrementsMsgSeq(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	fake.script = scriptIdentifyReady("session-1", 60000,
		groupMessageEvent("evt-group-1", "group-openid-1", "member-openid-1", "你好", 2))
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL), WithRetryDelay(10*time.Millisecond))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	received := make(chan connectors.InboundMessage, 1)
	startPolling(t, provider, cfg, func(_ context.Context, msg connectors.InboundMessage) error {
		received <- msg
		return nil
	}, newMemState())
	waitForMessage(t, received)

	err := provider.Send(context.Background(), cfg, "group:group-openid-1", []connectors.OutboundMessage{
		{Text: "回复一"},
		{Text: "   "}, // 空白消息跳过
		{Text: "回复二"},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	sent := fake.sentMessages()
	if len(sent) != 2 {
		t.Fatalf("sent = %+v, want 2 (空白跳过)", sent)
	}
	want := []struct {
		content string
		seq     float64
	}{{"回复一", 1}, {"回复二", 2}}
	for i, expect := range want {
		if sent[i].path != "/v2/groups/group-openid-1/messages" {
			t.Fatalf("sent[%d].path = %s", i, sent[i].path)
		}
		if sent[i].payload["content"] != expect.content || sent[i].payload["msg_type"] != float64(0) {
			t.Fatalf("sent[%d] = %+v", i, sent[i].payload)
		}
		if sent[i].payload["msg_id"] != "evt-group-1" {
			t.Fatalf("sent[%d] msg_id = %v, want 入站事件 id", i, sent[i].payload["msg_id"])
		}
		if sent[i].payload["msg_seq"] != expect.seq {
			t.Fatalf("sent[%d] msg_seq = %v, want %v", i, sent[i].payload["msg_seq"], expect.seq)
		}
	}

	// 再次 Send：同一 chat 的 msg_seq 继续递增。
	if err := provider.Send(context.Background(), cfg, "group:group-openid-1",
		[]connectors.OutboundMessage{{Text: "回复三"}}); err != nil {
		t.Fatalf("second send: %v", err)
	}
	sent = fake.sentMessages()
	if len(sent) != 3 || sent[2].payload["msg_seq"] != float64(3) {
		t.Fatalf("third message = %+v, want msg_seq 3", sent)
	}
}

func TestSendC2CPassiveReply(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	fake.script = scriptIdentifyReady("session-1", 60000,
		c2cMessageEvent("evt-c2c-1", "user-openid-1", "你好", 2))
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL), WithRetryDelay(10*time.Millisecond))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	received := make(chan connectors.InboundMessage, 1)
	startPolling(t, provider, cfg, func(_ context.Context, msg connectors.InboundMessage) error {
		received <- msg
		return nil
	}, newMemState())
	waitForMessage(t, received)

	if err := provider.Send(context.Background(), cfg, "user:user-openid-1",
		[]connectors.OutboundMessage{{Text: "C2C 回复"}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	sent := fake.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("sent = %+v", sent)
	}
	if sent[0].path != "/v2/users/user-openid-1/messages" {
		t.Fatalf("path = %s", sent[0].path)
	}
	if sent[0].payload["content"] != "C2C 回复" || sent[0].payload["msg_id"] != "evt-c2c-1" ||
		sent[0].payload["msg_seq"] != float64(1) {
		t.Fatalf("payload = %+v", sent[0].payload)
	}
}

func TestSendProactiveWithoutInboundMessage(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	// 无入站 msg_id：镜像上游主动消息路径（不带 msg_id/msg_seq）。
	if err := provider.Send(context.Background(), cfg, "user:user-openid-9",
		[]connectors.OutboundMessage{{Text: "主动消息"}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	sent := fake.sentMessages()
	if len(sent) != 1 || sent[0].path != "/v2/users/user-openid-9/messages" {
		t.Fatalf("sent = %+v", sent)
	}
	if _, ok := sent[0].payload["msg_id"]; ok {
		t.Fatalf("proactive send must not carry msg_id: %+v", sent[0].payload)
	}
	if _, ok := sent[0].payload["msg_seq"]; ok {
		t.Fatalf("proactive send must not carry msg_seq: %+v", sent[0].payload)
	}
}

func TestSendErrorRedactsSecret(t *testing.T) {
	fake := newFakeQQ("secret-value-1", "token-1")
	fake.msgStatus = http.StatusInternalServerError
	fake.msgBody = `{"code":500,"message":"internal error: secret-value-1"}`
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-value-1")

	err := provider.Send(context.Background(), cfg, "user:user-openid-9",
		[]connectors.OutboundMessage{{Text: "会失败"}})
	if err == nil {
		t.Fatal("error response must fail Send")
	}
	if strings.Contains(err.Error(), "secret-value-1") {
		t.Fatalf("app_secret leaked in send error: %v", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error should keep the HTTP status: %v", err)
	}
}

func TestSendChannelUnsupported(t *testing.T) {
	provider := New()
	cfg := testConfig("qq-1", "app-1", "secret-1")
	err := provider.Send(context.Background(), cfg, "channel:chan-1",
		[]connectors.OutboundMessage{{Text: "x"}})
	if err == nil || !strings.Contains(err.Error(), "频道") {
		t.Fatalf("channel send must fail with a clear error, got %v", err)
	}
}

// ── 事件解析边界 ────────────────────────────────────────────────────────────

func TestParseGatewayMessageEdgeCases(t *testing.T) {
	cases := []struct {
		name      string
		eventType string
		data      string
		wantOK    bool
		wantChat  string
		wantUser  string
	}{
		{
			name: "group 仅 @ 无文本跳过", eventType: "GROUP_AT_MESSAGE_CREATE",
			data:   `{"id":"e1","group_openid":"g1","content":"<@!bot>   ","author":{"member_openid":"m1"}}`,
			wantOK: false,
		},
		{
			name: "group 缺 author 跳过", eventType: "GROUP_AT_MESSAGE_CREATE",
			data:   `{"id":"e1","group_openid":"g1","content":"hi","author":{}}`,
			wantOK: false,
		},
		{
			name: "未知事件跳过", eventType: "SOMETHING_ELSE",
			data:   `{}`,
			wantOK: false,
		},
		{
			name: "group msg_id 兜底", eventType: "GROUP_AT_MESSAGE_CREATE",
			data:   `{"msg_id":"e3","group_openid":"g1","content":"hi","author":{"member_openid":"m1"}}`,
			wantOK: true, wantChat: "group:g1", wantUser: "m1",
		},
		{
			name: "频道 @ 消息", eventType: "AT_MESSAGE_CREATE",
			data:   `{"id":"e2","channel_id":"c1","guild_id":"gu1","content":"<@!bot> 频道你好","author":{"id":"u1"}}`,
			wantOK: true, wantChat: "channel:c1", wantUser: "u1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, ok := parseGatewayMessage(connectors.Config{ID: "qq-1"}, tc.eventType, json.RawMessage(tc.data))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (msg=%+v)", ok, tc.wantOK, msg)
			}
			if !ok {
				return
			}
			if msg.Chat != tc.wantChat || msg.UserID != tc.wantUser || msg.Text == "" {
				t.Fatalf("message = %+v", msg)
			}
		})
	}
}

// ── 授权校验动作（check_credentials） ───────────────────────────────────────

func TestRunActionCheckCredentials(t *testing.T) {
	fake := newFakeQQ("secret-value-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-value-1")

	result, err := provider.RunAction(context.Background(), cfg, actionCheckCredentials, nil)
	if err != nil {
		t.Fatalf("check credentials: %v", err)
	}
	if result["ok"] != true || result["action"] != actionCheckCredentials || result["app_id"] != "app-1" {
		t.Fatalf("result = %+v", result)
	}
	if result["api_base"] != server.URL {
		t.Fatalf("api_base = %v", result["api_base"])
	}
	bot, ok := result["bot"].(map[string]any)
	if !ok || bot["username"] != "测试机器人" || bot["id"] != "bot-openid-1" {
		t.Fatalf("bot = %+v", result["bot"])
	}
	message, _ := result["message"].(string)
	if !strings.Contains(message, "测试机器人") || !strings.Contains(message, "app-1") {
		t.Fatalf("message = %q", message)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "secret-value-1") {
		t.Fatalf("secret leaked in action result: %s", raw)
	}
}

func TestRunActionCheckCredentialsErrors(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))

	// 未知动作。
	if _, err := provider.RunAction(context.Background(), testConfig("qq-1", "app-1", "secret-1"), "nope", nil); err == nil {
		t.Fatal("unknown action must fail")
	}
	// 缺密钥：不发请求。
	if _, err := provider.RunAction(context.Background(), testConfig("qq-2", "app-2", ""), actionCheckCredentials, nil); err == nil {
		t.Fatal("missing app_secret must fail")
	}
	// /users/@me 失败（上游 500）：错误透传且脱敏。
	fake.meStatus = http.StatusInternalServerError
	fake.meBody = `{"message":"internal: secret-1"}`
	_, err := provider.RunAction(context.Background(), testConfig("qq-3", "app-3", "secret-1"), actionCheckCredentials, nil)
	if err == nil {
		t.Fatal("me failure must propagate")
	}
	if strings.Contains(err.Error(), "secret-1") {
		t.Fatalf("app_secret leaked: %v", err)
	}
}

// ── 出站：markdown / 分片 / 降级 / 媒体 ─────────────────────────────────────

func TestSendMarkdownWhenEnabled(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")
	cfg.Settings["markdown_support"] = "true"

	if err := provider.Send(context.Background(), cfg, "user:user-openid-1",
		[]connectors.OutboundMessage{{Text: "**加粗**"}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	sent := fake.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("sent = %+v", sent)
	}
	markdown, ok := sent[0].payload["markdown"].(map[string]any)
	if !ok || markdown["content"] != "**加粗**" || sent[0].payload["msg_type"] != float64(2) {
		t.Fatalf("markdown payload = %+v", sent[0].payload)
	}
	if _, ok := sent[0].payload["content"]; ok {
		t.Fatalf("markdown send must not carry plain content: %+v", sent[0].payload)
	}
}

func TestSendChunksLongTextByRunes(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	long := strings.Repeat("汉", maxMessageRunes+500)
	if err := provider.Send(context.Background(), cfg, "user:user-openid-1",
		[]connectors.OutboundMessage{{Text: long}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	sent := fake.sentMessages()
	if len(sent) != 2 {
		t.Fatalf("sent = %d messages, want 2", len(sent))
	}
	first, _ := sent[0].payload["content"].(string)
	second, _ := sent[1].payload["content"].(string)
	if len([]rune(first)) != maxMessageRunes || len([]rune(second)) != 500 {
		t.Fatalf("chunk runes = %d/%d, want %d/500",
			len([]rune(first)), len([]rune(second)), maxMessageRunes)
	}
	if first+second != long {
		t.Fatal("chunks must reassemble to the original text")
	}
}

func TestSendPassiveFallsBackToProactive(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	fake.script = scriptIdentifyReady("session-1", 60000,
		groupMessageEvent("evt-passive-1", "group-openid-1", "member-openid-1", "你好", 2))
	fake.passiveStatus = http.StatusBadRequest
	fake.passiveBody = `{"code":22009,"message":"reply message expired"}`
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL), WithRetryDelay(10*time.Millisecond))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	received := make(chan connectors.InboundMessage, 1)
	startPolling(t, provider, cfg, func(_ context.Context, msg connectors.InboundMessage) error {
		received <- msg
		return nil
	}, newMemState())
	waitForMessage(t, received)

	if err := provider.Send(context.Background(), cfg, "group:group-openid-1",
		[]connectors.OutboundMessage{{Text: "回复"}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	sent := fake.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want 1（被动失败后仅主动重试落盘）", sent)
	}
	if _, ok := sent[0].payload["msg_id"]; ok {
		t.Fatalf("fallback send must be proactive: %+v", sent[0].payload)
	}
	if sent[0].payload["content"] != "回复" {
		t.Fatalf("payload = %+v", sent[0].payload)
	}
}

func TestSendMediaUploadsAndSendsRichMedia(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	err := provider.Send(context.Background(), cfg, "group:group-openid-1",
		[]connectors.OutboundMessage{{
			Media: []connectors.Media{{Kind: "image", URL: server.URL + "/media/img.png"}},
		}})
	if err != nil {
		t.Fatalf("send media: %v", err)
	}
	sent := fake.sentMessages()
	if len(sent) != 2 {
		t.Fatalf("sent = %+v, want upload + message", sent)
	}
	upload := sent[0]
	if upload.path != "/v2/groups/group-openid-1/files" || upload.payload["file_type"] != float64(1) ||
		upload.payload["srv_send_msg"] != false {
		t.Fatalf("upload = %+v", upload)
	}
	encoded, _ := upload.payload["file_data"].(string)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || !bytes.Equal(decoded, fakePNG) {
		t.Fatalf("file_data mismatch: %v", err)
	}
	msg := sent[1]
	if msg.path != "/v2/groups/group-openid-1/messages" || msg.payload["msg_type"] != float64(7) {
		t.Fatalf("media message = %+v", msg)
	}
	media, ok := msg.payload["media"].(map[string]any)
	if !ok || media["file_info"] != "file-info-1" {
		t.Fatalf("media payload = %+v", msg.payload)
	}
}

func TestSendMediaFromLocalFile(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("hello media"), 0o600); err != nil {
		t.Fatalf("write temp media: %v", err)
	}
	err := provider.Send(context.Background(), cfg, "user:user-openid-1",
		[]connectors.OutboundMessage{{Media: []connectors.Media{{Path: path}}}})
	if err != nil {
		t.Fatalf("send local media: %v", err)
	}
	sent := fake.sentMessages()
	if len(sent) != 2 || sent[0].path != "/v2/users/user-openid-1/files" ||
		sent[0].payload["file_type"] != float64(4) {
		t.Fatalf("sent = %+v", sent)
	}
}

// ── 引用消息与群会话隔离键 ──────────────────────────────────────────────────

func TestParseGroupQuotedAndIsolation(t *testing.T) {
	quoted := `{"id":"e9","group_openid":"g1","content":"<@!bot> 继续","author":{"member_openid":"m1"},"message_reference":{"content":"上一句"}}`
	msg, ok := parseGatewayMessage(connectors.Config{ID: "qq-1"}, "GROUP_AT_MESSAGE_CREATE", json.RawMessage(quoted))
	if !ok || msg.Text != "Quoted: 上一句\n继续" {
		t.Fatalf("quoted message = %+v (ok=%v)", msg, ok)
	}
	if msg.Chat != "group:g1" {
		t.Fatalf("default chat = %s, want group:g1", msg.Chat)
	}

	nested := `{"id":"e10","group_openid":"g1","content":"继续","author":{"member_openid":"m1"},"message_reference":{"message":{"content":"嵌套引用"}}}`
	msg, ok = parseGatewayMessage(connectors.Config{ID: "qq-1"}, "GROUP_AT_MESSAGE_CREATE", json.RawMessage(nested))
	if !ok || msg.Text != "Quoted: 嵌套引用\n继续" {
		t.Fatalf("nested quoted message = %+v (ok=%v)", msg, ok)
	}

	isolated := connectors.Config{ID: "qq-1", Settings: map[string]string{"share_session_in_channel": "false"}}
	msg, ok = parseGatewayMessage(isolated, "GROUP_AT_MESSAGE_CREATE", json.RawMessage(quoted))
	if !ok || msg.Chat != "group:g1:user:m1" {
		t.Fatalf("isolated chat = %s (ok=%v), want group:g1:user:m1", msg.Chat, ok)
	}
}

func TestParseChatIsolatedGroupKey(t *testing.T) {
	target, err := parseChat("group:group-openid-1:user:member-openid-1")
	if err != nil {
		t.Fatalf("parse chat: %v", err)
	}
	if target.kind != chatKindGroup || target.openID != "group-openid-1" ||
		target.chat != "group:group-openid-1:user:member-openid-1" {
		t.Fatalf("target = %+v", target)
	}
}

func TestSendIsolatedGroupChatUsesGroupEndpoint(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	fake.script = scriptIdentifyReady("session-1", 60000,
		groupMessageEvent("evt-iso-1", "group-openid-1", "member-openid-1", "你好", 2))
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL), WithRetryDelay(10*time.Millisecond))
	cfg := testConfig("qq-1", "app-1", "secret-1")
	cfg.Settings["share_session_in_channel"] = "false"

	received := make(chan connectors.InboundMessage, 1)
	startPolling(t, provider, cfg, func(_ context.Context, msg connectors.InboundMessage) error {
		received <- msg
		return nil
	}, newMemState())
	msg := waitForMessage(t, received)
	if msg.Chat != "group:group-openid-1:user:member-openid-1" {
		t.Fatalf("inbound chat = %s", msg.Chat)
	}
	if err := provider.Send(context.Background(), cfg, msg.Chat,
		[]connectors.OutboundMessage{{Text: "回复"}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	sent := fake.sentMessages()
	if len(sent) != 1 || sent[0].path != "/v2/groups/group-openid-1/messages" {
		t.Fatalf("sent = %+v", sent)
	}
	if sent[0].payload["msg_id"] != "evt-iso-1" || sent[0].payload["msg_seq"] != float64(1) {
		t.Fatalf("payload = %+v", sent[0].payload)
	}
}

// ── Gateway intents 覆盖（对齐 codex-server 的 qqbot_intents） ─────────────

func TestGatewayIdentifyIntentsOverrideAndFallback(t *testing.T) {
	// 覆盖：settings.intents 按原值下发 Identify。
	custom := newFakeQQ("secret-1", "token-1")
	custom.script = scriptIdentifyReady("session-custom", 60000)
	customServer := newFakeQQServer(t, custom)
	customProvider := New(WithAPIBase(customServer.URL), WithRetryDelay(10*time.Millisecond))
	customCfg := testConfig("qq-1", "app-1", "secret-1")
	customCfg.Settings["intents"] = "1073741824" // 1<<30
	startPolling(t, customProvider, customCfg, func(_ context.Context, _ connectors.InboundMessage) error { return nil }, newMemState())
	waitFor(t, "custom identify", func() bool { return len(custom.identifyPayloads()) == 1 })
	if got := custom.identifyPayloads()[0].d["intents"]; got != float64(1<<30) {
		t.Fatalf("custom intents = %v, want %d", got, 1<<30)
	}

	// 非法值：回退默认（网关仍可连接）。
	fallback := newFakeQQ("secret-2", "token-2")
	fallback.script = scriptIdentifyReady("session-default", 60000)
	fallbackServer := newFakeQQServer(t, fallback)
	fallbackProvider := New(WithAPIBase(fallbackServer.URL), WithRetryDelay(10*time.Millisecond))
	fallbackCfg := testConfig("qq-2", "app-2", "secret-2")
	fallbackCfg.Settings["intents"] = "not-a-number"
	startPolling(t, fallbackProvider, fallbackCfg, func(_ context.Context, _ connectors.InboundMessage) error { return nil }, newMemState())
	waitFor(t, "fallback identify", func() bool { return len(fallback.identifyPayloads()) == 1 })
	if got := fallback.identifyPayloads()[0].d["intents"]; got != float64(gatewayIntents) {
		t.Fatalf("fallback intents = %v, want %d", got, gatewayIntents)
	}
} // ── 入站富媒体 / 语音 ASR / 昵称（2026-10-15，对齐官方 attachments 字段） ────

func TestParseGroupImageOnlyMessageNotDropped(t *testing.T) {
	data := `{"id":"e-img","group_openid":"g1","content":"","author":{"member_openid":"m1","username":"小明"},` +
		`"attachments":[{"content_type":"image/jpeg","filename":"photo.jpg",` +
		`"url":"https://multimedia.nt.qq.com.cn/download?appid=x&fileid=y","width":1920,"height":1080,"size":256000}]}`
	msg, ok := parseGatewayMessage(connectors.Config{ID: "qq-1"}, "GROUP_AT_MESSAGE_CREATE", json.RawMessage(data))
	if !ok {
		t.Fatal("image-only message must not be dropped")
	}
	if msg.Text != "[图片: photo.jpg]" {
		t.Fatalf("text = %q", msg.Text)
	}
	if msg.UserName != "小明" || msg.UserID != "m1" {
		t.Fatalf("user = %q/%q, want 小明/m1", msg.UserID, msg.UserName)
	}
	if len(msg.Media) != 1 {
		t.Fatalf("media = %+v", msg.Media)
	}
	media := msg.Media[0]
	if media.Kind != "image" || media.FileName != "photo.jpg" || media.URL == "" || media.Size != 256000 {
		t.Fatalf("media = %+v", media)
	}
}

func TestParseC2CVoicePrefersASRText(t *testing.T) {
	data := `{"id":"e-voice","content":"","author":{"user_openid":"u1"},` +
		`"attachments":[{"content_type":"voice","filename":"a.silk","url":"https://x/voice",` +
		`"voice_wav_url":"https://x/voice.wav","asr_refer_text":"今天天气不错"}]}`
	msg, ok := parseGatewayMessage(connectors.Config{ID: "qq-1"}, "C2C_MESSAGE_CREATE", json.RawMessage(data))
	if !ok {
		t.Fatal("voice-only message must not be dropped")
	}
	if msg.Text != "今天天气不错" {
		t.Fatalf("text = %q, want ASR transcription", msg.Text)
	}
	if len(msg.Media) != 1 || msg.Media[0].Kind != "voice" {
		t.Fatalf("media = %+v", msg.Media)
	}
}

func TestParseC2CVoiceWithoutASRPlaceholder(t *testing.T) {
	data := `{"id":"e-voice2","content":"","author":{"user_openid":"u1"},` +
		`"attachments":[{"content_type":"voice","filename":"a.silk","url":"https://x/voice"}]}`
	msg, ok := parseGatewayMessage(connectors.Config{ID: "qq-1"}, "C2C_MESSAGE_CREATE", json.RawMessage(data))
	if !ok || msg.Text != "[语音: a.silk]" {
		t.Fatalf("text = %q (ok=%v), want [语音: a.silk]", msg.Text, ok)
	}
}

func TestParseTextWithAttachmentsAppendsPlaceholder(t *testing.T) {
	data := `{"id":"e-mix","group_openid":"g1","content":"<@!bot> 看看这个",` +
		`"author":{"member_openid":"m1"},` +
		`"attachments":[{"content_type":"image/png","filename":"a.png","url":"https://x/a.png"},` +
		`{"content_type":"file","filename":"doc.pdf","url":"https://x/doc.pdf"}]}`
	msg, ok := parseGatewayMessage(connectors.Config{ID: "qq-1"}, "GROUP_AT_MESSAGE_CREATE", json.RawMessage(data))
	if !ok {
		t.Fatal("mixed message must parse")
	}
	if msg.Text != "看看这个\n[图片: a.png] [文件: doc.pdf]" {
		t.Fatalf("text = %q", msg.Text)
	}
	if len(msg.Media) != 2 {
		t.Fatalf("media = %+v", msg.Media)
	}
}
