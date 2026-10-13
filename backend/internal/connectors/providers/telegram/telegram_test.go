package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// ── 假 Telegram Bot API ─────────────────────────────────────────────────────

type fakeAPI struct {
	mu sync.Mutex

	token        string
	updates      []map[string]any
	servedOffset []int64
	sent         []map[string]any
	edits        []map[string]any
	actions      []string
	failFirst    int // 前 N 次 getUpdates 返回 HTTP 500
	getMeCalls   int
	nextID       int64
}

func newFakeAPI(token string) *fakeAPI {
	return &fakeAPI{token: token, nextID: 100}
}

func (f *fakeAPI) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) != 2 || !strings.HasPrefix(parts[0], "bot") {
			http.NotFound(w, r)
			return
		}
		if strings.TrimPrefix(parts[0], "bot") != f.token {
			writeJSON(w, map[string]any{"ok": false, "error_code": 401, "description": "Unauthorized"})
			return
		}
		method := parts[1]
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)

		f.mu.Lock()
		defer f.mu.Unlock()

		switch method {
		case "getMe":
			f.getMeCalls++
			writeJSON(w, map[string]any{"ok": true, "result": map[string]any{
				"id": 1, "username": "test_bot", "first_name": "Test",
			}})
		case "getUpdates":
			if f.failFirst > 0 {
				f.failFirst--
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"ok":false,"description":"server error"}`))
				return
			}
			if offset, ok := body["offset"].(float64); ok {
				f.servedOffset = append(f.servedOffset, int64(offset))
			}
			updates := f.updates
			f.updates = nil
			if updates == nil {
				updates = []map[string]any{}
				// 模拟长轮询的等待，避免测试里空转。
				f.mu.Unlock()
				time.Sleep(10 * time.Millisecond)
				f.mu.Lock()
			}
			writeJSON(w, map[string]any{"ok": true, "result": updates})
		case "sendMessage":
			f.sent = append(f.sent, body)
			f.nextID++
			writeJSON(w, map[string]any{"ok": true, "result": map[string]any{"message_id": f.nextID}})
		case "editMessageText":
			f.edits = append(f.edits, body)
			writeJSON(w, map[string]any{"ok": true, "result": map[string]any{}})
		case "sendChatAction":
			action, _ := body["action"].(string)
			f.actions = append(f.actions, action)
			writeJSON(w, map[string]any{"ok": true, "result": true})
		default:
			writeJSON(w, map[string]any{"ok": false, "error_code": 404, "description": "Not Found"})
		}
	})
}

func (f *fakeAPI) queueTextMessage(updateID, messageID, chatID, userID int64, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, map[string]any{
		"update_id": updateID,
		"message": map[string]any{
			"message_id": messageID,
			"from":       map[string]any{"id": userID, "username": "alice"},
			"chat":       map[string]any{"id": chatID, "type": "private"},
			"text":       text,
		},
	})
}

func (f *fakeAPI) sentPayloads() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.sent...)
}

func (f *fakeAPI) editPayloads() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.edits...)
}

func (f *fakeAPI) chatActions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.actions...)
}

func (f *fakeAPI) firstServedOffset() (int64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.servedOffset) == 0 {
		return 0, false
	}
	return f.servedOffset[0], true
}

func (f *fakeAPI) getMeCallsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getMeCalls
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

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

func testConfig(token string) connectors.Config {
	return connectors.Config{
		ID:       "tg-1",
		Provider: Name,
		Name:     "telegram test",
		Enabled:  true,
		Mode:     "polling",
		Secrets:  map[string]string{"bot_token": token},
	}
}

// ── 用例 ───────────────────────────────────────────────────────────────────

func TestSchemaContract(t *testing.T) {
	schema := New().Schema()
	if schema.Provider != Name || len(schema.Fields) == 0 {
		t.Fatalf("schema = %+v", schema)
	}
	// webhook 入口（HandleWebhook/setWebhook）未实现前只能声明 polling，
	// 否则前端会放开 webhook 配置但运行时静默失效。
	if len(schema.Modes) != 1 || schema.Modes[0] != "polling" {
		t.Fatalf("modes = %v, want [polling]", schema.Modes)
	}
	var token, mode, base bool
	for _, field := range schema.Fields {
		switch field.Key {
		case "bot_token":
			token = field.Type == "secret" && field.Required
		case "mode":
			mode = field.Type == "enum" && field.Default == "polling"
		case "api_base":
			base = field.Type == "url" && !field.Required
		}
	}
	if !token || !mode || !base {
		t.Fatalf("schema fields = %+v", schema.Fields)
	}
}

func TestConfigAPIBaseOverridesProviderOption(t *testing.T) {
	optionAPI := newFakeAPI("tok") // 构造参数基址：被 Settings 覆盖后不应命中
	optionServer := httptest.NewServer(optionAPI.handler())
	defer optionServer.Close()
	settingsAPI := newFakeAPI("tok")
	settingsServer := httptest.NewServer(settingsAPI.handler())
	defer settingsServer.Close()

	provider := New(WithAPIBase(optionServer.URL))
	cfg := testConfig("tok")
	cfg.Settings = map[string]string{"api_base": settingsServer.URL + "/"} // 尾斜杠应被归一

	if _, err := provider.Activate(context.Background(), cfg); err != nil {
		t.Fatalf("activate via settings api_base: %v", err)
	}
	if settingsAPI.getMeCallsCount() == 0 {
		t.Fatal("settings api_base should be used")
	}
	if optionAPI.getMeCallsCount() != 0 {
		t.Fatal("provider option base should be bypassed when settings api_base is set")
	}
}

func TestActivateValidatesToken(t *testing.T) {
	api := newFakeAPI("good-token")
	server := httptest.NewServer(api.handler())
	defer server.Close()
	provider := New(WithAPIBase(server.URL))

	activation, err := provider.Activate(context.Background(), testConfig("good-token"))
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if activation.Info != "@test_bot" {
		t.Fatalf("activation info = %q", activation.Info)
	}

	if _, err := provider.Activate(context.Background(), testConfig("bad-token")); err == nil {
		t.Fatal("bad token must fail activation")
	}
	if _, err := provider.Activate(context.Background(), connectors.Config{}); err == nil {
		t.Fatal("missing token must fail activation")
	}
}

func TestSendPostsMessage(t *testing.T) {
	api := newFakeAPI("tok")
	server := httptest.NewServer(api.handler())
	defer server.Close()
	provider := New(WithAPIBase(server.URL))

	err := provider.Send(context.Background(), testConfig("tok"), "-100123",
		[]connectors.OutboundMessage{{Text: "第一条"}, {Text: "  "}, {Text: "第二条"}})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	sent := api.sentPayloads()
	if len(sent) != 2 {
		t.Fatalf("sent %d messages, want 2 (blank skipped)", len(sent))
	}
	if sent[0]["chat_id"] != "-100123" || sent[0]["text"] != "第一条" {
		t.Fatalf("payload = %+v", sent[0])
	}
}

func TestRunPollingParsesUpdatesAndPersistsCursor(t *testing.T) {
	api := newFakeAPI("tok")
	api.queueTextMessage(5, 11, 100, 7, "hello agent")
	server := httptest.NewServer(api.handler())
	defer server.Close()
	provider := New(WithAPIBase(server.URL), WithRetryDelay(5*time.Millisecond))
	state := newMemState()

	received := make(chan connectors.InboundMessage, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- provider.RunPolling(ctx, testConfig("tok"), func(_ context.Context, msg connectors.InboundMessage) error {
			received <- msg
			return nil
		}, state)
	}()

	var msg connectors.InboundMessage
	select {
	case msg = <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("no inbound message")
	}
	if msg.Chat != "100" || msg.MessageID != "11" || msg.Text != "hello agent" {
		t.Fatalf("message = %+v", msg)
	}
	if msg.UserID != "7" || msg.UserName != "alice" {
		t.Fatalf("user = %+v", msg)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if raw, ok := state.Get(cursorKey); ok && raw == "6" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if raw, _ := state.Get(cursorKey); raw != "6" {
		t.Fatalf("cursor = %q, want 6", raw)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunPolling returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunPolling did not stop after cancel")
	}
	if offset, ok := api.firstServedOffset(); ok && offset != 0 {
		t.Fatalf("first getUpdates offset = %d, want 0", offset)
	}
}

func TestRunPollingRetriesAfterFailure(t *testing.T) {
	api := newFakeAPI("tok")
	api.failFirst = 2
	api.queueTextMessage(9, 21, 200, 8, "after retry")
	server := httptest.NewServer(api.handler())
	defer server.Close()
	provider := New(WithAPIBase(server.URL), WithRetryDelay(5*time.Millisecond))

	received := make(chan connectors.InboundMessage, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = provider.RunPolling(ctx, testConfig("tok"), func(_ context.Context, msg connectors.InboundMessage) error {
			received <- msg
			return nil
		}, newMemState())
	}()

	select {
	case msg := <-received:
		if msg.Text != "after retry" {
			t.Fatalf("message = %+v", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("poller did not recover after transient failures")
	}
}

func TestTypingSessionSendsChatAction(t *testing.T) {
	api := newFakeAPI("tok")
	server := httptest.NewServer(api.handler())
	defer server.Close()
	provider := New(WithAPIBase(server.URL))

	session, err := provider.StartTyping(context.Background(), testConfig("tok"), "100")
	if err != nil {
		t.Fatalf("start typing: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(api.chatActions()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if actions := api.chatActions(); len(actions) == 0 || actions[0] != "typing" {
		t.Fatalf("actions = %v", actions)
	}
	if err := session.Stop(context.Background()); err != nil {
		t.Fatalf("stop typing: %v", err)
	}
}

func TestStreamingEditsPlaceholder(t *testing.T) {
	api := newFakeAPI("tok")
	server := httptest.NewServer(api.handler())
	defer server.Close()
	provider := New(WithAPIBase(server.URL))

	session, err := provider.StartStreaming(context.Background(), testConfig("tok"), "100")
	if err != nil {
		t.Fatalf("start streaming: %v", err)
	}
	if sent := api.sentPayloads(); len(sent) != 1 || sent[0]["text"] != "思考中…" {
		t.Fatalf("placeholder = %+v", sent)
	}
	if err := session.Update(context.Background(), "第一段"); err != nil {
		t.Fatal(err)
	}
	if err := session.Final(context.Background(), []connectors.OutboundMessage{{Text: "最终答复"}}); err != nil {
		t.Fatal(err)
	}
	edits := api.editPayloads()
	if len(edits) != 2 {
		t.Fatalf("edits = %d, want 2", len(edits))
	}
	if edits[1]["text"] != "最终答复" {
		t.Fatalf("final edit = %+v", edits[1])
	}
	if _, ok := edits[0]["message_id"].(float64); !ok {
		t.Fatalf("edit must target the placeholder message: %+v", edits[0])
	}
}

func TestSupportsPollingRespectsMode(t *testing.T) {
	provider := New()
	cfg := testConfig("tok")
	if !provider.SupportsPolling(cfg) {
		t.Fatal("polling mode should support polling")
	}
	cfg.Mode = "webhook"
	if provider.SupportsPolling(cfg) {
		t.Fatal("webhook mode must not start polling")
	}
	// 历史配置（前端表单写入 Settings["mode"]=webhook）：不报错、仅不轮询。
	cfg.Mode = "polling"
	cfg.Settings = map[string]string{"mode": "webhook"}
	if provider.SupportsPolling(cfg) {
		t.Fatal("settings mode=webhook must not start polling")
	}
	cfg.Settings = nil
	cfg.Mode = "polling"
	cfg.Secrets = map[string]string{}
	if provider.SupportsPolling(cfg) {
		t.Fatal("missing token must not start polling")
	}
}

func TestCallErrorsDoNotLeakToken(t *testing.T) {
	const token = "123456:AA-SECRET-TOKEN"

	t.Run("transport error", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		base := server.URL
		server.Close() // 关闭后连接被拒，client.Do 返回 *url.Error（文本含完整 URL + token）

		_, err := New(WithAPIBase(base)).Activate(context.Background(), testConfig(token))
		assertTokenRedacted(t, err, token, "getMe")
	})

	t.Run("request build error", func(t *testing.T) {
		// 非法基址让 http.NewRequestWithContext 直接失败，解析错误同样会带出完整 URL。
		_, err := New(WithAPIBase("http://[::1")).Activate(context.Background(), testConfig(token))
		assertTokenRedacted(t, err, token, "getMe")
	})
}

func assertTokenRedacted(t *testing.T, err error, token, method string) {
	t.Helper()
	if err == nil {
		t.Fatal("request against unreachable/closed base must fail")
	}
	message := err.Error()
	if strings.Contains(message, token) {
		t.Fatalf("error leaks bot_token: %q", message)
	}
	if !strings.Contains(message, method) {
		t.Fatalf("error should mention method %q: %q", method, message)
	}
}
