package wechat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// ── 测试替身 ────────────────────────────────────────────────────────────────

type recordedRequest struct {
	path    string
	query   url.Values
	headers http.Header
	body    map[string]any
}

// fakeILink 是 httptest 假 iLink 服务：按路径注册处理器并记录请求。
type fakeILink struct {
	server *httptest.Server

	mu       sync.Mutex
	handlers map[string]func(w http.ResponseWriter, r *http.Request, body map[string]any)
	calls    map[string]int
	requests []recordedRequest
}

func newFakeILink(t *testing.T) *fakeILink {
	t.Helper()
	fake := &fakeILink{
		handlers: make(map[string]func(w http.ResponseWriter, r *http.Request, body map[string]any)),
		calls:    make(map[string]int),
	}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeILink) URL() string { return f.server.URL }

// on 注册路径处理器（handler 在无锁状态下执行）。
func (f *fakeILink) on(path string, handler func(w http.ResponseWriter, r *http.Request, body map[string]any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[path] = handler
}

func (f *fakeILink) serve(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{}
	if r.Body != nil {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if len(bytes.TrimSpace(raw)) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
	}
	f.mu.Lock()
	f.calls[r.URL.Path]++
	f.requests = append(f.requests, recordedRequest{
		path:    r.URL.Path,
		query:   r.URL.Query(),
		headers: r.Header.Clone(),
		body:    body,
	})
	handler := f.handlers[r.URL.Path]
	f.mu.Unlock()

	if handler == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	handler(w, r, body)
}

func (f *fakeILink) callCount(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

func (f *fakeILink) lastRequest(path string) (recordedRequest, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index := len(f.requests) - 1; index >= 0; index-- {
		if f.requests[index].path == path {
			return f.requests[index], true
		}
	}
	return recordedRequest{}, false
}

func writeRet0(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ret":0,"errcode":0}` + "\n"))
}

func writeJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Errorf("encode fake response: %v", err)
	}
}

// fakeState 是 PollingState 的内存实现。
type fakeState struct {
	mu     sync.Mutex
	values map[string]string
	setErr error
}

func newFakeState(values map[string]string) *fakeState {
	if values == nil {
		values = map[string]string{}
	}
	return &fakeState{values: values}
}

func (s *fakeState) Get(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	return value, ok
}

func (s *fakeState) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setErr != nil {
		return s.setErr
	}
	s.values[key] = value
	return nil
}

func (s *fakeState) value(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key]
}

func testConfig(id, base string, extra map[string]string) connectors.Config {
	settings := map[string]string{}
	if base != "" {
		settings["api_base"] = base
	}
	for key, value := range extra {
		settings[key] = value
	}
	return connectors.Config{
		ID:       id,
		Provider: Name,
		Enabled:  true,
		Mode:     "polling",
		Settings: settings,
		Secrets:  map[string]string{"bot_token": "tok"},
	}
}

var discardLogger = func(string, ...any) {}

// ── Schema 与激活 ───────────────────────────────────────────────────────────

func TestSchemaDeclaresPollingAndLoginActions(t *testing.T) {
	provider := New()
	schema := provider.Schema()
	if schema.Provider != Name || schema.Label != "微信" {
		t.Fatalf("unexpected schema identity: %+v", schema)
	}
	if len(schema.Modes) != 1 || schema.Modes[0] != "polling" {
		t.Fatalf("modes = %v, want [polling]", schema.Modes)
	}
	if len(schema.Actions) != 2 ||
		schema.Actions[0].Key != loginActionStart || schema.Actions[0].Kind != "qr" ||
		schema.Actions[1].Key != loginActionStatus {
		t.Fatalf("actions = %+v, want start_login(qr)+login_status", schema.Actions)
	}
	// 基址有官方默认值：用户无需知道/填写 iLink 地址即可扫码接入。
	var baseDefault string
	for _, field := range schema.Fields {
		if field.Key == "base_url" {
			baseDefault = field.Default
		}
	}
	if baseDefault != defaultBaseURL {
		t.Fatalf("base_url default = %q, want %q", baseDefault, defaultBaseURL)
	}
	if provider.Name() != Name {
		t.Fatalf("Name() = %q", provider.Name())
	}
	if provider.SupportsPolling(connectors.Config{}) {
		t.Fatalf("empty config should not support polling")
	}
	if !provider.SupportsPolling(testConfig("c1", "https://example.test", nil)) {
		t.Fatalf("token+base config should support polling")
	}
}

func TestActivateValidatesAndCachesGetConfig(t *testing.T) {
	fake := newFakeILink(t)
	fake.on("/ilink/bot/getconfig", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeJSON(t, w, map[string]any{"ret": 0, "errcode": 0, "typing_ticket": "ticket-1"})
	})

	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	cfg := testConfig("c1", fake.URL(), map[string]string{"owner_user_id": "owner-1"})

	activation, err := provider.Activate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if !strings.Contains(activation.Info, "getconfig 校验通过") {
		t.Fatalf("activation info = %q", activation.Info)
	}
	if _, err := provider.Activate(context.Background(), cfg); err != nil {
		t.Fatalf("second Activate: %v", err)
	}
	if got := fake.callCount("/ilink/bot/getconfig"); got != 1 {
		t.Fatalf("getconfig calls = %d, want 1（typing ticket 缓存命中）", got)
	}
	request, ok := fake.lastRequest("/ilink/bot/getconfig")
	if !ok {
		t.Fatalf("no getconfig request recorded")
	}
	if got := request.headers.Get("Authorization"); got != "Bearer tok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := request.headers.Get("AuthorizationType"); got != "ilink_bot_token" {
		t.Fatalf("AuthorizationType = %q", got)
	}
	if got := request.headers.Get("iLink-App-Id"); got != "bot" {
		t.Fatalf("iLink-App-Id = %q", got)
	}
	if owner, _ := request.body["ilink_user_id"].(string); owner != "owner-1" {
		t.Fatalf("ilink_user_id = %v", request.body["ilink_user_id"])
	}
}

func TestActivateSkipsProbeWithoutOwnerAndRejectsMissingConfig(t *testing.T) {
	fake := newFakeILink(t)
	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))

	cfg := testConfig("c1", fake.URL(), nil)
	activation, err := provider.Activate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if !strings.Contains(activation.Info, "跳过 getconfig") {
		t.Fatalf("activation info = %q", activation.Info)
	}
	if got := fake.callCount("/ilink/bot/getconfig"); got != 0 {
		t.Fatalf("getconfig calls = %d, want 0", got)
	}

	missingToken := testConfig("c2", fake.URL(), nil)
	missingToken.Secrets = nil
	if _, err := provider.Activate(context.Background(), missingToken); err == nil || !strings.Contains(err.Error(), "bot_token") {
		t.Fatalf("missing token error = %v", err)
	}

	// 未配置基址时回落到官方默认地址（用户无需填写），不再报 base_url 缺失。
	plain := New(WithLogger(discardLogger))
	missingBase := connectors.Config{ID: "c3", Provider: Name, Secrets: map[string]string{"bot_token": "tok"}}
	if got := plain.baseFor(missingBase); got != defaultBaseURL {
		t.Fatalf("default base = %q, want %q", got, defaultBaseURL)
	}
	if _, err := plain.Activate(context.Background(), missingBase); err != nil {
		t.Fatalf("Activate with default base: %v", err)
	}
}

func TestActivateRedactsTokenFromErrors(t *testing.T) {
	fake := newFakeILink(t)
	fake.on("/ilink/bot/getconfig", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"debug":"Bearer tok rejected"}`))
	})

	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	cfg := testConfig("c1", fake.URL(), map[string]string{"owner_user_id": "owner-1"})
	// getconfig 探测失败已降级为告警（2026-10-14：避免新 bot 的引导死锁），
	// 激活成功但 Info 中必须携带脱敏后的错误文本（token 不可泄漏）。
	activation, err := provider.Activate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("probe failure should degrade, not fail activation: %v", err)
	}
	if !strings.Contains(activation.Info, "已降级为告警") {
		t.Fatalf("expected degraded-probe marker in info: %q", activation.Info)
	}
	if strings.Contains(activation.Info, "tok") {
		t.Fatalf("token leaked in info: %v", activation.Info)
	}
	if !strings.Contains(activation.Info, "***") {
		t.Fatalf("expected redacted marker in info: %v", activation.Info)
	}
}

// TestActivateProbeFailureAllowsPollingBoot 锁定引导死锁修复（2026-10-14）：
// 新扫码的 bot 在 owner 发出首条消息前 getconfig 必然 ret=-4，激活不得因此
// 失败——否则轮询永远起不来，收不到首条消息（收不到 → 永远建不了关系）。
func TestActivateProbeFailureAllowsPollingBoot(t *testing.T) {
	fake := newFakeILink(t)
	fake.on("/ilink/bot/getconfig", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeJSON(t, w, map[string]any{"ret": -4, "errmsg": "GetTypingTicket rpc failed"})
	})

	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	cfg := testConfig("c1", fake.URL(), map[string]string{"owner_user_id": "owner-1"})
	activation, err := provider.Activate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("probe failure must not fail activation (bootstrap deadlock): %v", err)
	}
	if !provider.SupportsPolling(cfg) {
		t.Fatal("polling must be bootable after degraded probe")
	}
	if !strings.Contains(activation.Info, "ret=-4") {
		t.Fatalf("expected probe error detail in info: %q", activation.Info)
	}
}

// ── 长轮询 ──────────────────────────────────────────────────────────────────

const sampleGetUpdatesBatch = `{
  "ret": 0,
  "errcode": 0,
  "get_updates_buf": "buf-1",
  "longpolling_timeout_ms": 2000,
  "msgs": [
    {
      "from_user_id": "u1",
      "client_id": "cli-1",
      "session_id": "sess-1",
      "message_type": 1,
      "message_state": 2,
      "context_token": "ctx-1",
      "create_time_ms": "1730000000000",
      "item_list": [
        {"type": 1, "text_item": {"text": "你好", "ref_msg": {"text": "被引用的消息"}}},
        {"type": 2, "image_item": {"mid_size": 2048, "media": {"full_url": "https://cdn.example/img-1"}}},
        {"type": 3, "voice_item": {"text": "语音转写", "encode_type": 1}},
        {"type": 1, "text_item": {"text": "第二段"}},
        {"type": 4, "file_item": {"file_name": "报告.pdf", "len": "321"}},
        {"type": 5, "video_item": {"video_size": 999}},
        {"type": 3, "voice_item": {}}
      ]
    },
    {
      "from_user_id": "u1",
      "client_id": "cli-2",
      "message_type": 2,
      "item_list": [{"type": 1, "text_item": {"text": "bot echo"}}]
    }
  ]
}`

func TestRunPollingParsesTextQuoteAndDegradesMedia(t *testing.T) {
	fake := newFakeILink(t)
	var callMu sync.Mutex
	calls := 0
	fake.on("/ilink/bot/getupdates", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		callMu.Lock()
		calls++
		current := calls
		callMu.Unlock()
		if current == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleGetUpdatesBatch))
			return
		}
		// 后续长轮询挂起直到客户端取消（避免空响应紧循环）。
		<-r.Context().Done()
	})

	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	cfg := testConfig("c1", fake.URL(), nil)
	state := newFakeState(nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	received := make(chan connectors.InboundMessage, 4)
	done := make(chan error, 1)
	go func() {
		done <- provider.RunPolling(ctx, cfg, func(_ context.Context, msg connectors.InboundMessage) error {
			received <- msg
			return nil
		}, state)
	}()

	var msg connectors.InboundMessage
	select {
	case msg = <-received:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for inbound message")
	}

	wantText := "Quoted: 被引用的消息\n你好\n语音转写\n第二段\n[图片]\n[文件: 报告.pdf]\n[视频]\n[语音]"
	if msg.Text != wantText {
		t.Fatalf("inbound text mismatch:\n got %q\nwant %q", msg.Text, wantText)
	}
	if msg.Chat != "u1" || msg.UserID != "u1" || msg.ChatTitle != "sess-1" {
		t.Fatalf("unexpected routing fields: %+v", msg)
	}
	if !strings.HasPrefix(msg.MessageID, "wechat:") {
		t.Fatalf("message id = %q", msg.MessageID)
	}
	if token, _ := msg.ProviderData[contextTokenKey].(string); token != "ctx-1" {
		t.Fatalf("context_token = %v", msg.ProviderData[contextTokenKey])
	}
	if got := provider.contextToken("c1", "u1"); got != "ctx-1" {
		t.Fatalf("remembered context token = %q", got)
	}
	if len(msg.Media) != 4 {
		t.Fatalf("media = %+v, want 4 entries", msg.Media)
	}
	if msg.Media[0].Kind != "image" || msg.Media[0].Size != 2048 || msg.Media[0].FileID != "https://cdn.example/img-1" {
		t.Fatalf("image media = %+v", msg.Media[0])
	}
	if msg.Media[1].Kind != "file" || msg.Media[1].FileName != "报告.pdf" || msg.Media[1].Size != 321 {
		t.Fatalf("file media = %+v", msg.Media[1])
	}
	if msg.Media[2].Kind != "video" || msg.Media[2].Size != 999 {
		t.Fatalf("video media = %+v", msg.Media[2])
	}
	if msg.Media[3].Kind != "voice" {
		t.Fatalf("voice media = %+v", msg.Media[3])
	}

	// 只处理用户消息（message_type=2 的 bot 回显被忽略）。
	select {
	case extra := <-received:
		t.Fatalf("unexpected extra message: %+v", extra)
	case <-time.After(150 * time.Millisecond):
	}

	// 游标先落盘再处理消息。
	if got := state.value(syncBufKey); got != "buf-1" {
		t.Fatalf("sync_buf = %q, want buf-1", got)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunPolling returned %v, want nil on ctx cancel", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("RunPolling did not stop after cancel")
	}
}

func TestRunPollingResumesPersistedCursor(t *testing.T) {
	fake := newFakeILink(t)
	var mu sync.Mutex
	call := 0
	fake.on("/ilink/bot/getupdates", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		mu.Lock()
		call++
		current := call
		mu.Unlock()
		if current == 1 {
			if got, _ := body["get_updates_buf"].(string); got != "buf-existing" {
				t.Errorf("first getupdates buf = %q, want persisted buf-existing", got)
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ret":0,"errcode":0,"get_updates_buf":"buf-next","msgs":[]}`))
		// 后续空转响应：短挂起后返回，避免轮询紧循环（客户端需读到响应体 EOF）。
		select {
		case <-time.After(20 * time.Millisecond):
		case <-r.Context().Done():
		}
	})

	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	cfg := testConfig("c1", fake.URL(), nil)
	state := newFakeState(map[string]string{syncBufKey: "buf-existing"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- provider.RunPolling(ctx, cfg, func(context.Context, connectors.InboundMessage) error { return nil }, state)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if state.value(syncBufKey) == "buf-next" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := state.value(syncBufKey); got != "buf-next" {
		t.Fatalf("sync_buf = %q, want buf-next（重启后应从持久化游标续传）", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunPolling returned %v, want nil", err)
	}
}

func TestRunPollingRetriesAndReportsSessionExpired(t *testing.T) {
	fake := newFakeILink(t)
	var callMu sync.Mutex
	calls := 0
	fake.on("/ilink/bot/getupdates", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		callMu.Lock()
		calls++
		current := calls
		callMu.Unlock()
		switch current {
		case 1:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
		case 2:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ret":-14,"errcode":-14,"errmsg":"session expired"}`))
		case 3:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ret":0,"errcode":0,"get_updates_buf":"buf-2","msgs":[{"from_user_id":"u1","message_type":1,"context_token":"ctx-2","item_list":[{"type":1,"text_item":{"text":"恢复"}}]}]}`))
		default:
			<-r.Context().Done()
		}
	})

	var logMu sync.Mutex
	var logs strings.Builder
	provider := New(WithAPIBase(fake.URL()), WithRetryDelay(5*time.Millisecond), WithLogger(func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logs.WriteString(format)
		logs.WriteString("\n")
	}))
	cfg := testConfig("c1", fake.URL(), nil)
	state := newFakeState(nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	received := make(chan connectors.InboundMessage, 1)
	done := make(chan error, 1)
	go func() {
		done <- provider.RunPolling(ctx, cfg, func(_ context.Context, msg connectors.InboundMessage) error {
			received <- msg
			return nil
		}, state)
	}()

	select {
	case msg := <-received:
		if msg.Text != "恢复" {
			t.Fatalf("recovered message text = %q", msg.Text)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("polling did not recover after transient error / session expired")
	}
	logMu.Lock()
	logText := logs.String()
	logMu.Unlock()
	if !strings.Contains(logText, "会话已过期") {
		t.Fatalf("expected session-expired log, got: %s", logText)
	}
	if fake.callCount("/ilink/bot/getupdates") < 3 {
		t.Fatalf("getupdates calls = %d, want >= 3", fake.callCount("/ilink/bot/getupdates"))
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunPolling returned %v, want nil", err)
	}
}

// ── 发送与 markdown 降级 ────────────────────────────────────────────────────

func sentText(t *testing.T, body map[string]any) string {
	t.Helper()
	msg, ok := body["msg"].(map[string]any)
	if !ok {
		t.Fatalf("request body missing msg: %+v", body)
	}
	items, ok := msg["item_list"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("request body missing item_list: %+v", msg)
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("item = %+v", items[0])
	}
	textItem, ok := item["text_item"].(map[string]any)
	if !ok {
		t.Fatalf("text_item = %+v", item)
	}
	text, _ := textItem["text"].(string)
	return text
}

func TestSendCleansMarkdownAndCarriesReplyContext(t *testing.T) {
	fake := newFakeILink(t)
	fake.on("/ilink/bot/sendmessage", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeRet0(w)
	})

	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	provider.rememberContextToken("c1", "u1", "ctx-1")
	cfg := testConfig("c1", fake.URL(), nil)

	text := "**粗体** 和 `code`\n# 标题\n\n> 引用\n\n---\n- 列表"
	if err := provider.Send(context.Background(), cfg, "u1", []connectors.OutboundMessage{{Text: text}}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	request, ok := fake.lastRequest("/ilink/bot/sendmessage")
	if !ok {
		t.Fatalf("no sendmessage request recorded")
	}
	want := "粗体 和 code\n标题\n\n引用\n\n- 列表"
	if got := sentText(t, request.body); got != want {
		t.Fatalf("cleaned text mismatch:\n got %q\nwant %q", got, want)
	}
	msg := request.body["msg"].(map[string]any)
	if msg["to_user_id"] != "u1" || msg["context_token"] != "ctx-1" {
		t.Fatalf("reply context = %+v", msg)
	}
	if msg["message_type"] != float64(messageTypeBot) || msg["message_state"] != float64(messageStateComplete) {
		t.Fatalf("message flags = %+v", msg)
	}
	if baseInfo, ok := request.body["base_info"].(map[string]any); !ok || baseInfo["channel_version"] != channelVersion {
		t.Fatalf("base_info = %+v", request.body["base_info"])
	}
	if got := request.headers.Get("Authorization"); got != "Bearer tok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := request.headers.Get("AuthorizationType"); got != "ilink_bot_token" {
		t.Fatalf("AuthorizationType = %q", got)
	}
}

func TestSendRequiresContextTokenAndRedactsErrors(t *testing.T) {
	fake := newFakeILink(t)
	fake.on("/ilink/bot/sendmessage", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"debug":"Bearer tok rejected"}`))
	})

	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	cfg := testConfig("c1", fake.URL(), nil)

	if err := provider.Send(context.Background(), cfg, "u-unknown", []connectors.OutboundMessage{{Text: "hi"}}); err == nil ||
		!strings.Contains(err.Error(), "context_token") {
		t.Fatalf("missing context token error = %v", err)
	}
	if fake.callCount("/ilink/bot/sendmessage") != 0 {
		t.Fatalf("sendmessage should not be called without context token")
	}

	provider.rememberContextToken("c1", "u1", "ctx-1")
	err := provider.Send(context.Background(), cfg, "u1", []connectors.OutboundMessage{{Text: "hi"}})
	if err == nil {
		t.Fatalf("expected send failure")
	}
	if strings.Contains(err.Error(), "tok") {
		t.Fatalf("token leaked in error: %v", err)
	}
	if !strings.Contains(err.Error(), "***") || !strings.Contains(err.Error(), "sendmessage text failed") {
		t.Fatalf("unexpected error text: %v", err)
	}
}

// ── typing ──────────────────────────────────────────────────────────────────

func TestTypingLifecycleUsesTicketAndCancelStatus(t *testing.T) {
	fake := newFakeILink(t)
	fake.on("/ilink/bot/getconfig", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeJSON(t, w, map[string]any{"ret": 0, "errcode": 0, "typing_ticket": "tk-1"})
	})
	var mu sync.Mutex
	var statuses []float64
	fake.on("/ilink/bot/sendtyping", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		mu.Lock()
		if status, ok := body["status"].(float64); ok {
			statuses = append(statuses, status)
		}
		mu.Unlock()
		writeRet0(w)
	})

	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	provider.rememberContextToken("c1", "u1", "ctx-1")
	cfg := testConfig("c1", fake.URL(), nil)

	session, err := provider.StartTyping(context.Background(), cfg, "u1")
	if err != nil {
		t.Fatalf("StartTyping: %v", err)
	}
	if session == nil {
		t.Fatalf("expected typing session")
	}
	if err := session.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := session.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}

	mu.Lock()
	got := append([]float64(nil), statuses...)
	mu.Unlock()
	if len(got) != 2 || got[0] != typingStatusTyping || got[1] != typingStatusCancel {
		t.Fatalf("typing statuses = %v, want [1 2]", got)
	}
	if fake.callCount("/ilink/bot/getconfig") != 1 {
		t.Fatalf("getconfig calls = %d, want 1（typing ticket 缓存）", fake.callCount("/ilink/bot/getconfig"))
	}
}

func TestTypingWithoutTicketIsSilentlySkipped(t *testing.T) {
	fake := newFakeILink(t)
	fake.on("/ilink/bot/getconfig", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeJSON(t, w, map[string]any{"ret": 0, "errcode": 0, "typing_ticket": ""})
	})

	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	provider.rememberContextToken("c1", "u1", "ctx-1")
	cfg := testConfig("c1", fake.URL(), nil)

	session, err := provider.StartTyping(context.Background(), cfg, "u1")
	if err != nil || session != nil {
		t.Fatalf("StartTyping = (%v, %v), want (nil, nil)", session, err)
	}

	if _, err := provider.StartTyping(context.Background(), cfg, "u-missing"); err == nil ||
		!strings.Contains(err.Error(), "context_token") {
		t.Fatalf("missing context token typing error = %v", err)
	}
}

// ── 扫码登录 action ─────────────────────────────────────────────────────────

func TestRunActionLoginFlowConfirmsAndBackfills(t *testing.T) {
	fake := newFakeILink(t)
	// scaned_but_redirect 指向第二台假服务：确认状态必须切换到新基址轮询。
	redirectTarget := newFakeILink(t)
	fake.on("/ilink/bot/get_bot_qrcode", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if got := r.URL.Query().Get("bot_type"); got != "3" {
			t.Errorf("bot_type = %q, want 3", got)
		}
		writeJSON(t, w, map[string]any{
			"ret": 0, "errcode": 0,
			"qrcode":             "qr-1",
			"qrcode_img_content": "data:image/png;base64,QQ==",
		})
	})
	fake.on("/ilink/bot/get_qrcode_status", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeJSON(t, w, map[string]any{
			"ret": 0, "status": "scaned_but_redirect",
			"redirect_host": strings.TrimPrefix(redirectTarget.URL(), "http://"),
		})
	})
	redirectTarget.on("/ilink/bot/get_qrcode_status", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeJSON(t, w, map[string]any{
			"ret": 0, "status": "confirmed",
			"bot_token":     "tk-9",
			"ilink_bot_id":  "acct-1",
			"ilink_user_id": "owner-1",
			"baseurl":       "https://ilink.example",
		})
	})

	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	cfg := testConfig("c1", fake.URL(), nil)

	start, err := provider.RunAction(context.Background(), cfg, loginActionStart, nil)
	if err != nil {
		t.Fatalf("start_login: %v", err)
	}
	if start["status"] != loginStatusWait {
		t.Fatalf("start_login status = %v", start["status"])
	}
	loginID, _ := start["login_id"].(string)
	if !strings.HasPrefix(loginID, "wechat_login_") {
		t.Fatalf("login_id = %q", loginID)
	}
	if start["qr_code_content"] != "data:image/png;base64,QQ==" {
		t.Fatalf("qr_code_content = %v", start["qr_code_content"])
	}

	scanned, err := provider.RunAction(context.Background(), cfg, loginActionStatus, map[string]any{"login_id": loginID})
	if err != nil {
		t.Fatalf("login_status(scanned): %v", err)
	}
	if scanned["status"] != loginStatusScanned {
		t.Fatalf("scanned status = %v", scanned["status"])
	}

	// 缺省 login_id 时回退到该连接器最近一次会话。
	confirmed, err := provider.RunAction(context.Background(), cfg, loginActionStatus, nil)
	if err != nil {
		t.Fatalf("login_status(confirmed): %v", err)
	}
	if confirmed["status"] != loginStatusConfirmed || confirmed["credential_ready"] != true {
		t.Fatalf("confirmed payload = %+v", confirmed)
	}
	settings, _ := confirmed["settings"].(map[string]any)
	if settings["base_url"] != "https://ilink.example" || settings["account_id"] != "acct-1" || settings["owner_user_id"] != "owner-1" {
		t.Fatalf("settings patch = %+v", settings)
	}
	secrets, _ := confirmed["secrets"].(map[string]any)
	if secrets["bot_token"] != "tk-9" {
		t.Fatalf("secrets patch = %+v", secrets)
	}

	// 配置回填前，进程内缓存凭据已可支撑后续收发。
	notYetBackfilled := connectors.Config{ID: "c1", Provider: Name, Settings: map[string]string{"api_base": fake.URL()}}
	if got := provider.tokenFor(notYetBackfilled); got != "tk-9" {
		t.Fatalf("cached credential token = %q", got)
	}
	if got := provider.confirmedSession("c1"); got == nil || got.baseURL != "https://ilink.example" {
		t.Fatalf("confirmed session = %+v", got)
	}
	if fake.callCount("/ilink/bot/get_qrcode_status") != 1 {
		t.Fatalf("primary status calls = %d, want 1（scanned 后轮询基址应切到 redirect_host）", fake.callCount("/ilink/bot/get_qrcode_status"))
	}
	if redirectTarget.callCount("/ilink/bot/get_qrcode_status") != 1 {
		t.Fatalf("redirect target status calls = %d, want 1", redirectTarget.callCount("/ilink/bot/get_qrcode_status"))
	}
}

func TestStartLoginIsIdempotentAndStatusFallsBack(t *testing.T) {
	fake := newFakeILink(t)
	fake.on("/ilink/bot/get_bot_qrcode", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeJSON(t, w, map[string]any{
			"ret": 0, "errcode": 0,
			"qrcode":             "qr-1",
			"qrcode_img_content": "data:image/png;base64,QQ==",
		})
	})
	fake.on("/ilink/bot/get_qrcode_status", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeJSON(t, w, map[string]any{"ret": 0, "status": "wait"})
	})

	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	cfg := testConfig("c1", fake.URL(), nil)

	// 幂等：重复 start_login 复用同一会话（前端 StrictMode 双调用/重复点击不再让旧 login_id 失效）。
	first, err := provider.RunAction(context.Background(), cfg, loginActionStart, nil)
	if err != nil {
		t.Fatalf("start_login #1: %v", err)
	}
	second, err := provider.RunAction(context.Background(), cfg, loginActionStart, nil)
	if err != nil {
		t.Fatalf("start_login #2: %v", err)
	}
	if first["login_id"] != second["login_id"] {
		t.Fatalf("start_login 非幂等：%v vs %v", first["login_id"], second["login_id"])
	}
	if got := fake.callCount("/ilink/bot/get_bot_qrcode"); got != 1 {
		t.Fatalf("qrcode calls = %d, want 1（幂等复用不重复出码）", got)
	}

	// 容错：未知 login_id 回落到该连接器最近一次会话，而不是报 not found。
	status, err := provider.RunAction(context.Background(), cfg, loginActionStatus, map[string]any{"login_id": "wechat_login_stale"})
	if err != nil {
		t.Fatalf("login_status(过期 login_id) 应回落而非报错：%v", err)
	}
	if status["status"] != loginStatusWait {
		t.Fatalf("fallback status = %v", status["status"])
	}
}

func TestRunActionRefreshesExpiredQRCodeUpToLimit(t *testing.T) {
	fake := newFakeILink(t)
	var mu sync.Mutex
	qrCalls := 0
	fake.on("/ilink/bot/get_bot_qrcode", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		mu.Lock()
		qrCalls++
		current := qrCalls
		mu.Unlock()
		writeJSON(t, w, map[string]any{
			"ret": 0, "qrcode": fmt.Sprintf("qr-%d", current),
			"qrcode_img_content": fmt.Sprintf("img-%d", current),
		})
	})
	fake.on("/ilink/bot/get_qrcode_status", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeJSON(t, w, map[string]any{"ret": 0, "status": "expired"})
	})

	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	cfg := testConfig("c1", fake.URL(), nil)

	start, err := provider.RunAction(context.Background(), cfg, loginActionStart, nil)
	if err != nil {
		t.Fatalf("start_login: %v", err)
	}
	loginID, _ := start["login_id"].(string)

	for refresh := 1; refresh <= loginMaxRefreshCount; refresh++ {
		payload, err := provider.RunAction(context.Background(), cfg, loginActionStatus, map[string]any{"login_id": loginID})
		if err != nil {
			t.Fatalf("login_status refresh %d: %v", refresh, err)
		}
		if payload["status"] != loginStatusWait {
			t.Fatalf("refresh %d status = %v", refresh, payload["status"])
		}
		if payload["refresh_count"] != float64(refresh) && payload["refresh_count"] != refresh {
			t.Fatalf("refresh %d count = %v", refresh, payload["refresh_count"])
		}
		if payload["qr_code"] != fmt.Sprintf("qr-%d", refresh+1) {
			t.Fatalf("refresh %d qr_code = %v", refresh, payload["qr_code"])
		}
	}

	final, err := provider.RunAction(context.Background(), cfg, loginActionStatus, map[string]any{"login_id": loginID})
	if err != nil {
		t.Fatalf("login_status exhausted: %v", err)
	}
	if final["status"] != loginStatusExpired {
		t.Fatalf("exhausted status = %v", final["status"])
	}
	if fake.callCount("/ilink/bot/get_bot_qrcode") != loginMaxRefreshCount+1 {
		t.Fatalf("qrcode calls = %d, want %d", fake.callCount("/ilink/bot/get_bot_qrcode"), loginMaxRefreshCount+1)
	}
}

func TestRunActionRejectsUnknownAndMissingSession(t *testing.T) {
	fake := newFakeILink(t)
	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	cfg := testConfig("c1", fake.URL(), nil)

	if _, err := provider.RunAction(context.Background(), cfg, "nope", nil); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("unknown action error = %v", err)
	}
	if _, err := provider.RunAction(context.Background(), cfg, loginActionStatus, nil); err == nil || !strings.Contains(err.Error(), "login session") {
		t.Fatalf("missing session error = %v", err)
	}
}
