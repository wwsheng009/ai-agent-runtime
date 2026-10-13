package feishu

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// ── 测试替身（httptest 假飞书开放平台 API） ─────────────────────────────────

type recordedRequest struct {
	path    string
	query   url.Values
	headers http.Header
	body    map[string]any
}

type fakeFeishu struct {
	server *httptest.Server

	mu       sync.Mutex
	handlers map[string]func(w http.ResponseWriter, r *http.Request, body map[string]any)
	calls    map[string]int
	requests []recordedRequest
}

func newFakeFeishu(t *testing.T) *fakeFeishu {
	t.Helper()
	fake := &fakeFeishu{
		handlers: make(map[string]func(w http.ResponseWriter, r *http.Request, body map[string]any)),
		calls:    make(map[string]int),
	}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeFeishu) URL() string { return f.server.URL }

func (f *fakeFeishu) on(path string, handler func(w http.ResponseWriter, r *http.Request, body map[string]any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[path] = handler
}

// onTokenJSON 注册一个始终返回固定 JSON 的处理器。
func (f *fakeFeishu) onJSON(path string, status int, payload any) {
	f.on(path, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(payload)
	})
}

func (f *fakeFeishu) serve(w http.ResponseWriter, r *http.Request) {
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

func (f *fakeFeishu) callCount(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

func (f *fakeFeishu) lastRequest() recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return recordedRequest{}
	}
	return f.requests[len(f.requests)-1]
}

func (f *fakeFeishu) defaultTokenHandler(token string, expire int) {
	f.onJSON(tenantAccessTokenPath, http.StatusOK, map[string]any{
		"code": 0, "msg": "ok", "tenant_access_token": token, "expire": expire,
	})
}

// ── 测试辅助 ────────────────────────────────────────────────────────────────

func newTestProvider(t *testing.T, fake *fakeFeishu, opts ...Option) *Provider {
	t.Helper()
	options := []Option{
		WithAPIBase(fake.URL()),
		WithHTTPClient(fake.server.Client()),
		WithLogger(func(format string, args ...any) { t.Logf(format, args...) }),
	}
	return New(append(options, opts...)...)
}

func testConfig() connectors.Config {
	return connectors.Config{
		ID:       "conn-1",
		Provider: Name,
		Name:     "飞书测试",
		Enabled:  true,
		Mode:     "webhook",
		Settings: map[string]string{settingAppID: "cli_test"},
		Secrets: map[string]string{
			secretAppSecret:         "app-secret-value",
			secretVerificationToken: "vt-123",
			secretEncryptKey:        "ek-456",
		},
	}
}

func webhookRequest(t *testing.T, payload any) *http.Request {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal webhook payload: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/hooks/connectors/conn-1", bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func messageEventPayload(eventID, messageType, content string) map[string]any {
	return map[string]any{
		"schema": "2.0",
		"header": map[string]any{
			"event_id":   eventID,
			"event_type": eventMessageReceive,
			"token":      "vt-123",
			"app_id":     "cli_test",
			"tenant_key": "tk",
		},
		"event": map[string]any{
			"sender": map[string]any{
				"sender_id":   map[string]any{"open_id": "ou_user"},
				"sender_type": "user",
				"sender_name": "Alice",
			},
			"message": map[string]any{
				"message_id":   "om_msg_1",
				"chat_id":      "oc_chat_1",
				"chat_type":    "p2p",
				"message_type": messageType,
				"content":      content,
			},
		},
	}
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	return append(append([]byte{}, data...), bytes.Repeat([]byte{byte(padding)}, padding)...)
}

// encryptPayload 按飞书口径构造密文：base64(iv[16] || AES-256-CBC(PKCS#7))。
func encryptPayload(t *testing.T, plaintext []byte, encryptKey string) string {
	t.Helper()
	key := sha256.Sum256([]byte(encryptKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatalf("new aes cipher: %v", err)
	}
	iv := bytes.Repeat([]byte{0x2a}, aes.BlockSize)
	padded := pkcs7Pad(plaintext, aes.BlockSize)
	out := make([]byte, aes.BlockSize+len(padded))
	copy(out, iv)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out[aes.BlockSize:], padded)
	return base64.StdEncoding.EncodeToString(out)
}

// ── Schema ──────────────────────────────────────────────────────────────────

func TestSchemaDeclaresWebhookOnlyAndRequiredFields(t *testing.T) {
	schema := New().Schema()
	if schema.Provider != Name || schema.Label != "飞书" {
		t.Fatalf("unexpected provider/label: %+v", schema)
	}
	if len(schema.Modes) != 1 || schema.Modes[0] != "webhook" {
		t.Fatalf("expected modes=[webhook]（WS 未实现前不声明）, got %v", schema.Modes)
	}
	if len(schema.Actions) != 0 {
		t.Fatalf("expected no actions, got %v", schema.Actions)
	}
	fields := map[string]struct {
		fieldType string
		required  bool
	}{}
	for _, field := range schema.Fields {
		fields[field.Key] = struct {
			fieldType string
			required  bool
		}{string(field.Type), field.Required}
	}
	for key, want := range map[string]struct {
		fieldType string
		required  bool
	}{
		settingAppID:            {"text", true},
		secretAppSecret:         {"secret", true},
		secretVerificationToken: {"secret", false},
		secretEncryptKey:        {"secret", false},
		settingAPIBase:          {"url", false},
	} {
		got, ok := fields[key]
		if !ok {
			t.Fatalf("schema field %q missing", key)
		}
		if got.fieldType != want.fieldType || got.required != want.required {
			t.Fatalf("field %q: got type=%s required=%v, want type=%s required=%v", key, got.fieldType, got.required, want.fieldType, want.required)
		}
	}
}

// ── Activate / tenant_access_token ─────────────────────────────────────────

func TestActivateValidatesCredentialsAndCachesToken(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-abc", 7200)
	fake.onJSON(botInfoPath, http.StatusOK, map[string]any{
		"code": 0, "bot": map[string]any{"open_id": "ou_bot", "name": "Test Bot"},
	})
	provider := newTestProvider(t, fake)

	activation, err := provider.Activate(context.Background(), testConfig())
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if !strings.Contains(activation.Info, "api_base="+fake.URL()) || !strings.Contains(activation.Info, "bot=Test Bot") {
		t.Fatalf("unexpected activation info: %q", activation.Info)
	}
	if got := fake.callCount(tenantAccessTokenPath); got != 1 {
		t.Fatalf("expected 1 token call, got %d", got)
	}

	// 第二次调用（如 Send）复用缓存，不再请求 token 接口。
	fake.onJSON(messageCreatePath, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{"message_id": "om_out"}})
	if err := provider.Send(context.Background(), testConfig(), "oc_chat_1", []connectors.OutboundMessage{{Text: "hi"}}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if got := fake.callCount(tenantAccessTokenPath); got != 1 {
		t.Fatalf("expected token cache hit (1 call), got %d", got)
	}
}

func TestActivateRedactsAppSecretInErrors(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.onJSON(tenantAccessTokenPath, http.StatusOK, map[string]any{
		"code": 10003, "msg": "app_secret is invalid: app-secret-value",
	})
	provider := newTestProvider(t, fake)

	_, err := provider.Activate(context.Background(), testConfig())
	if err == nil {
		t.Fatal("expected Activate() error for invalid credentials")
	}
	if strings.Contains(err.Error(), "app-secret-value") {
		t.Fatalf("app_secret leaked in error: %v", err)
	}
	if !strings.Contains(err.Error(), "***") {
		t.Fatalf("expected redaction marker in error: %v", err)
	}
}

func TestTenantTokenRefreshesAheadOfExpiry(t *testing.T) {
	fake := newFakeFeishu(t)
	// expire=100s 小于提前刷新窗口（300s）：写入缓存后即视为"应刷新"。
	fake.defaultTokenHandler("t-short", 100)
	provider := newTestProvider(t, fake)
	cfg := testConfig()

	if _, err := provider.tenantToken(context.Background(), cfg); err != nil {
		t.Fatalf("first tenantToken() error = %v", err)
	}
	if _, err := provider.tenantToken(context.Background(), cfg); err != nil {
		t.Fatalf("second tenantToken() error = %v", err)
	}
	if got := fake.callCount(tenantAccessTokenPath); got != 2 {
		t.Fatalf("expected refresh due to skew (2 calls), got %d", got)
	}

	// 长有效期 token 命中缓存。
	fake2 := newFakeFeishu(t)
	fake2.defaultTokenHandler("t-long", 7200)
	provider2 := newTestProvider(t, fake2)
	if _, err := provider2.tenantToken(context.Background(), cfg); err != nil {
		t.Fatalf("tenantToken() error = %v", err)
	}
	if _, err := provider2.tenantToken(context.Background(), cfg); err != nil {
		t.Fatalf("tenantToken() error = %v", err)
	}
	if got := fake2.callCount(tenantAccessTokenPath); got != 1 {
		t.Fatalf("expected cache hit (1 call), got %d", got)
	}
}

// ── HandleWebhook：challenge ───────────────────────────────────────────────

func TestHandleWebhookChallenge(t *testing.T) {
	provider := New()
	cfg := testConfig()

	result, err := provider.HandleWebhook(context.Background(), cfg, webhookRequest(t, map[string]any{
		"type": "url_verification", "challenge": "ch-123", "token": "vt-123",
	}))
	if err != nil {
		t.Fatalf("HandleWebhook() error = %v", err)
	}
	if result.Message != nil || result.Response == nil {
		t.Fatalf("expected direct challenge response, got %+v", result)
	}
	response, ok := result.Response.(map[string]string)
	if !ok || response["challenge"] != "ch-123" {
		t.Fatalf("unexpected challenge response: %#v", result.Response)
	}
}

func TestHandleWebhookChallengeRejectsTokenMismatch(t *testing.T) {
	provider := New()
	_, err := provider.HandleWebhook(context.Background(), testConfig(), webhookRequest(t, map[string]any{
		"type": "url_verification", "challenge": "ch-123", "token": "wrong",
	}))
	if err == nil || !strings.Contains(err.Error(), "token mismatch") {
		t.Fatalf("expected token mismatch error, got %v", err)
	}
}

func TestHandleWebhookChallengeWithoutVerificationTokenIsAccepted(t *testing.T) {
	provider := New()
	cfg := testConfig()
	// 兼容策略：未配置 verification_token 时无法验签，放行（并告警），
	// 保证配置不完整的连接器仍可完成飞书控制台的 URL 校验。
	delete(cfg.Secrets, secretVerificationToken)

	result, err := provider.HandleWebhook(context.Background(), cfg, webhookRequest(t, map[string]any{
		"type": "url_verification", "challenge": "ch-456", "token": "anything",
	}))
	if err != nil {
		t.Fatalf("HandleWebhook() error = %v", err)
	}
	response, ok := result.Response.(map[string]string)
	if !ok || response["challenge"] != "ch-456" {
		t.Fatalf("unexpected challenge response: %#v", result.Response)
	}
}

// ── HandleWebhook：明文事件 ────────────────────────────────────────────────

func TestHandleWebhookParsesPlaintextMessageEvent(t *testing.T) {
	provider := New()
	cfg := testConfig()

	result, err := provider.HandleWebhook(context.Background(), cfg, webhookRequest(t,
		messageEventPayload("evt-1", "text", `{"text":"hello world"}`)))
	if err != nil {
		t.Fatalf("HandleWebhook() error = %v", err)
	}
	if result.Message == nil {
		t.Fatalf("expected inbound message, got %+v", result)
	}
	message := result.Message
	if message.Chat != "oc_chat_1" || message.UserID != "ou_user" || message.UserName != "Alice" {
		t.Fatalf("unexpected message routing fields: %+v", message)
	}
	// 幂等键使用 header.event_id（平台重试携带同一 event_id）。
	if message.MessageID != "evt-1" {
		t.Fatalf("expected MessageID=event_id, got %q", message.MessageID)
	}
	if message.Text != "hello world" {
		t.Fatalf("unexpected text: %q", message.Text)
	}
	if replyTo, _ := message.ProviderData[providerDataMessageID].(string); replyTo != "om_msg_1" {
		t.Fatalf("expected provider data message id om_msg_1, got %#v", message.ProviderData)
	}
	if message.Provider != Name || message.ConnectorID != cfg.ID {
		t.Fatalf("unexpected identity fields: %+v", message)
	}
}

func TestHandleWebhookStripsMentionPlaceholders(t *testing.T) {
	provider := New()
	payload := messageEventPayload("evt-2", "text", `{"text":"@_user_1 你好 bot"}`)
	payload["event"].(map[string]any)["message"].(map[string]any)["mentions"] = []map[string]any{
		{"key": "@_user_1", "name": "Bot", "id": map[string]any{"open_id": "ou_bot"}},
	}
	result, err := provider.HandleWebhook(context.Background(), testConfig(), webhookRequest(t, payload))
	if err != nil {
		t.Fatalf("HandleWebhook() error = %v", err)
	}
	if result.Message == nil || result.Message.Text != "你好 bot" {
		t.Fatalf("unexpected text: %+v", result.Message)
	}
}

func TestHandleWebhookNonTextMessageFallsBackToPlaceholder(t *testing.T) {
	provider := New()
	result, err := provider.HandleWebhook(context.Background(), testConfig(), webhookRequest(t,
		messageEventPayload("evt-3", "image", `{"image_key":"img_1"}`)))
	if err != nil {
		t.Fatalf("HandleWebhook() error = %v", err)
	}
	if result.Message == nil {
		t.Fatalf("expected placeholder message, got %+v", result)
	}
	if result.Message.Text != "[图片]" {
		t.Fatalf("expected [图片] placeholder, got %q", result.Message.Text)
	}
	if len(result.Message.Media) != 1 || result.Message.Media[0].FileID != "img_1" || result.Message.Media[0].Kind != "image" {
		t.Fatalf("unexpected media metadata: %+v", result.Message.Media)
	}
}

func TestHandleWebhookIgnoresOtherEventsAndAppSender(t *testing.T) {
	provider := New()
	cfg := testConfig()

	result, err := provider.HandleWebhook(context.Background(), cfg, webhookRequest(t, map[string]any{
		"schema": "2.0",
		"header": map[string]any{"event_id": "evt-x", "event_type": "im.message.reaction.created_v1", "token": "vt-123"},
		"event":  map[string]any{},
	}))
	if err != nil || result.Message != nil || result.Response != nil {
		t.Fatalf("expected silent 202 result, got %+v err=%v", result, err)
	}

	payload := messageEventPayload("evt-4", "text", `{"text":"from bot"}`)
	payload["event"].(map[string]any)["sender"].(map[string]any)["sender_type"] = "app"
	result, err = provider.HandleWebhook(context.Background(), cfg, webhookRequest(t, payload))
	if err != nil || result.Message != nil {
		t.Fatalf("expected app sender to be ignored, got %+v err=%v", result, err)
	}
}

func TestHandleWebhookRejectsEventTokenMismatchAndBadJSON(t *testing.T) {
	provider := New()
	cfg := testConfig()

	payload := messageEventPayload("evt-5", "text", `{"text":"hi"}`)
	payload["header"].(map[string]any)["token"] = "wrong"
	if _, err := provider.HandleWebhook(context.Background(), cfg, webhookRequest(t, payload)); err == nil {
		t.Fatal("expected token mismatch error")
	}

	request := httptest.NewRequest(http.MethodPost, "/hooks/connectors/conn-1", strings.NewReader("{not-json"))
	if _, err := provider.HandleWebhook(context.Background(), cfg, request); err == nil || !strings.Contains(err.Error(), "decode feishu webhook") {
		t.Fatalf("expected decode error, got %v", err)
	}
}

// ── HandleWebhook：加密事件 ────────────────────────────────────────────────

func TestHandleWebhookDecryptsEncryptedEvent(t *testing.T) {
	provider := New()
	cfg := testConfig()

	plaintext, err := json.Marshal(messageEventPayload("evt-6", "text", `{"text":"encrypted hello"}`))
	if err != nil {
		t.Fatalf("marshal plaintext: %v", err)
	}
	result, err := provider.HandleWebhook(context.Background(), cfg, webhookRequest(t, map[string]any{
		"encrypt": encryptPayload(t, plaintext, "ek-456"),
	}))
	if err != nil {
		t.Fatalf("HandleWebhook() error = %v", err)
	}
	if result.Message == nil || result.Message.Text != "encrypted hello" || result.Message.MessageID != "evt-6" {
		t.Fatalf("unexpected decrypted message: %+v", result)
	}
}

func TestHandleWebhookDecryptsEncryptedChallenge(t *testing.T) {
	provider := New()
	plaintext, _ := json.Marshal(map[string]any{
		"type": "url_verification", "challenge": "ch-enc", "token": "vt-123",
	})
	result, err := provider.HandleWebhook(context.Background(), testConfig(), webhookRequest(t, map[string]any{
		"encrypt": encryptPayload(t, plaintext, "ek-456"),
	}))
	if err != nil {
		t.Fatalf("HandleWebhook() error = %v", err)
	}
	response, ok := result.Response.(map[string]string)
	if !ok || response["challenge"] != "ch-enc" {
		t.Fatalf("unexpected challenge response: %#v", result.Response)
	}
}

func TestHandleWebhookDecryptFailureReturnsError(t *testing.T) {
	provider := New()
	cfg := testConfig()
	plaintext, _ := json.Marshal(messageEventPayload("evt-7", "text", `{"text":"hi"}`))

	// 密钥不匹配（另一把 encrypt_key 加密）→ 解密失败（填充校验）。
	wrongKey := encryptPayload(t, plaintext, "other-encrypt-key")
	if _, err := provider.HandleWebhook(context.Background(), cfg, webhookRequest(t, map[string]any{"encrypt": wrongKey})); err == nil {
		t.Fatal("expected decrypt failure for wrong key")
	}

	// 密文但未配置 encrypt_key → 明确报错（宿主 400）。
	cfgNoKey := testConfig()
	delete(cfgNoKey.Secrets, secretEncryptKey)
	if _, err := provider.HandleWebhook(context.Background(), cfgNoKey, webhookRequest(t, map[string]any{
		"encrypt": encryptPayload(t, plaintext, "ek-456"),
	})); err == nil || !strings.Contains(err.Error(), "encrypt_key is not configured") {
		t.Fatalf("expected missing encrypt_key error, got %v", err)
	}

	// 非法 base64 → 报错。
	if _, err := provider.HandleWebhook(context.Background(), cfg, webhookRequest(t, map[string]any{"encrypt": "!!not-base64!!"})); err == nil {
		t.Fatal("expected base64 decode error")
	}
}

// ── Send ───────────────────────────────────────────────────────────────────

func TestSendUsesReplyPathAfterInbound(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-abc", 7200)
	fake.onJSON(messageCreatePath, http.StatusOK, map[string]any{"code": 0})
	replyPath := fmt.Sprintf(messageReplyPathFmt, "om_msg_1")
	fake.onJSON(replyPath, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{"message_id": "om_out"}})
	provider := newTestProvider(t, fake)
	cfg := testConfig()

	if _, err := provider.HandleWebhook(context.Background(), cfg, webhookRequest(t,
		messageEventPayload("evt-8", "text", `{"text":"ping"}`))); err != nil {
		t.Fatalf("HandleWebhook() error = %v", err)
	}
	if err := provider.Send(context.Background(), cfg, "oc_chat_1", []connectors.OutboundMessage{{Text: "pong"}}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if got := fake.callCount(replyPath); got != 1 {
		t.Fatalf("expected 1 reply call, got %d (requests: %+v)", got, fake.requests)
	}
	if got := fake.callCount(messageCreatePath); got != 0 {
		t.Fatalf("expected no create call when reply succeeds, got %d", got)
	}
	last := fake.lastRequest()
	if last.body["msg_type"] != "text" {
		t.Fatalf("unexpected msg_type: %#v", last.body)
	}
	if content, _ := last.body["content"].(string); content != `{"text":"pong"}` {
		t.Fatalf("unexpected content: %#v", last.body["content"])
	}
	if auth := last.headers.Get("Authorization"); auth != "Bearer t-abc" {
		t.Fatalf("unexpected Authorization header: %q", auth)
	}
	if got := fake.callCount(tenantAccessTokenPath); got != 1 {
		t.Fatalf("expected cached token (1 call), got %d", got)
	}
}

func TestSendCreatePathWithoutInboundMessage(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-abc", 7200)
	fake.onJSON(messageCreatePath, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{"message_id": "om_out"}})
	provider := newTestProvider(t, fake)

	if err := provider.Send(context.Background(), testConfig(), "oc_chat_9", []connectors.OutboundMessage{{Text: "direct"}}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	last := fake.lastRequest()
	if last.path != messageCreatePath {
		t.Fatalf("expected create path, got %q", last.path)
	}
	if last.query.Get("receive_id_type") != "chat_id" {
		t.Fatalf("expected receive_id_type=chat_id, got %v", last.query)
	}
	if last.body["receive_id"] != "oc_chat_9" || last.body["msg_type"] != "text" {
		t.Fatalf("unexpected create body: %#v", last.body)
	}
}

func TestSendFallsBackToCreateWhenReplyRejected(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-abc", 7200)
	fake.onJSON(fmt.Sprintf(messageReplyPathFmt, "om_msg_1"), http.StatusBadRequest, map[string]any{"code": 230002, "msg": "reply message not found"})
	fake.onJSON(messageCreatePath, http.StatusOK, map[string]any{"code": 0})
	provider := newTestProvider(t, fake)
	cfg := testConfig()

	if _, err := provider.HandleWebhook(context.Background(), cfg, webhookRequest(t,
		messageEventPayload("evt-9", "text", `{"text":"ping"}`))); err != nil {
		t.Fatalf("HandleWebhook() error = %v", err)
	}
	if err := provider.Send(context.Background(), cfg, "oc_chat_1", []connectors.OutboundMessage{{Text: "pong"}}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if fake.callCount(fmt.Sprintf(messageReplyPathFmt, "om_msg_1")) != 1 || fake.callCount(messageCreatePath) != 1 {
		t.Fatalf("expected reply then create fallback, requests: %+v", fake.requests)
	}
}

func TestSendErrorsAreRedacted(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-abc", 7200)
	fake.on(messageCreatePath, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"code":99991,"msg":"invalid token t-abc for app-secret-value"}`)
	})
	provider := newTestProvider(t, fake)

	err := provider.Send(context.Background(), testConfig(), "oc_chat_1", []connectors.OutboundMessage{{Text: "hi"}})
	if err == nil {
		t.Fatal("expected Send() error")
	}
	for _, secret := range []string{"t-abc", "app-secret-value"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("secret %q leaked in error: %v", secret, err)
		}
	}
	if !strings.Contains(err.Error(), "***") {
		t.Fatalf("expected redaction marker in error: %v", err)
	}
}

func TestSendRequiresChatAndSkipsEmptyText(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-abc", 7200)
	provider := newTestProvider(t, fake)

	if err := provider.Send(context.Background(), testConfig(), "  ", []connectors.OutboundMessage{{Text: "hi"}}); err == nil {
		t.Fatal("expected error for empty chat")
	}
	if err := provider.Send(context.Background(), testConfig(), "oc_chat_1", []connectors.OutboundMessage{{Media: nil}}); err != nil {
		t.Fatalf("expected empty text to be skipped, got %v", err)
	}
	if got := fake.callCount(messageCreatePath); got != 0 {
		t.Fatalf("expected no create calls, got %d", got)
	}
}

func TestActivateRequiresCredentials(t *testing.T) {
	provider := New()
	cfg := testConfig()
	delete(cfg.Secrets, secretAppSecret)
	if _, err := provider.Activate(context.Background(), cfg); err == nil {
		t.Fatal("expected error for missing app_secret")
	}
	cfg = testConfig()
	cfg.Settings[settingAppID] = ""
	if _, err := provider.Activate(context.Background(), cfg); err == nil {
		t.Fatal("expected error for missing app_id")
	}
	cfg = testConfig()
	cfg.Settings[settingAPIBase] = "://bad"
	if _, err := provider.Activate(context.Background(), cfg); err == nil {
		t.Fatal("expected error for invalid api_base")
	}
}

// 编译期断言：Provider 满足 Provider/WebhookProvider，且不实现其它可选能力。
var (
	_ connectors.Provider        = (*Provider)(nil)
	_ connectors.WebhookProvider = (*Provider)(nil)
)
