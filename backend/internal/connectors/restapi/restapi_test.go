package restapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
)

// ── 测试替身 ───────────────────────────────────────────────────────────────

type fakeProvider struct {
	sent       []connectors.OutboundMessage
	webhookErr error
	// webhookResponses 非空时按序返回直接应答（challenge 场景），否则返回消息。
	webhookResponse any
}

func (p *fakeProvider) Name() string { return "fakeweb" }

func (p *fakeProvider) Schema() api.ProviderSchema {
	return api.ProviderSchema{
		Provider: "fakeweb",
		Label:    "Fake Webhook",
		Modes:    []string{"polling", "webhook"},
		Fields: []api.FieldSchema{
			{Key: "bot_token", Label: "Bot Token", Type: api.FieldSecret, Required: true},
			{Key: "mode", Label: "模式", Type: api.FieldEnum, Default: "polling",
				Options: []api.Option{{Value: "polling"}, {Value: "webhook"}}},
			{Key: "label", Label: "备注", Type: api.FieldText},
		},
	}
}

func (p *fakeProvider) Activate(context.Context, connectors.Config) (connectors.Activation, error) {
	return connectors.Activation{}, nil
}

func (p *fakeProvider) Deactivate(context.Context, connectors.Config) error { return nil }

func (p *fakeProvider) Send(_ context.Context, _ connectors.Config, _ string, msgs []connectors.OutboundMessage) error {
	p.sent = append(p.sent, msgs...)
	return nil
}

func (p *fakeProvider) HandleWebhook(_ context.Context, cfg connectors.Config, r *http.Request) (connectors.WebhookResult, error) {
	if p.webhookErr != nil {
		return connectors.WebhookResult{}, p.webhookErr
	}
	if p.webhookResponse != nil {
		return connectors.WebhookResult{Response: p.webhookResponse}, nil
	}
	return connectors.WebhookResult{Message: &connectors.InboundMessage{
		Chat:      "chat-1",
		MessageID: strings.TrimSpace(r.Header.Get("X-Message-Id")),
		UserID:    "u1",
		Text:      "来自 webhook",
	}}, nil
}

// plainProvider 不支持 webhook（用于 405 用例）；独立实现，避免嵌入隐式获得能力。
type plainProvider struct{}

func (p *plainProvider) Name() string { return "plain" }

func (p *plainProvider) Schema() api.ProviderSchema {
	return api.ProviderSchema{Provider: "plain", Label: "Plain"}
}

func (p *plainProvider) Activate(context.Context, connectors.Config) (connectors.Activation, error) {
	return connectors.Activation{}, nil
}

func (p *plainProvider) Deactivate(context.Context, connectors.Config) error { return nil }

func (p *plainProvider) Send(context.Context, connectors.Config, string, []connectors.OutboundMessage) error {
	return nil
}

type fakeBackend struct {
	mu      sync.Mutex
	handled []connectors.BackendRequest
}

func (b *fakeBackend) Name() string { return "fake-backend" }

func (b *fakeBackend) Handle(_ context.Context, req connectors.BackendRequest, _ connectors.ReplySink) (connectors.BackendResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handled = append(b.handled, req)
	return connectors.BackendResult{}, nil
}

func (b *fakeBackend) requests() []connectors.BackendRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]connectors.BackendRequest(nil), b.handled...)
}

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

type fixture struct {
	store    *connectors.MemoryStore
	backend  *fakeBackend
	provider *fakeProvider
	plain    *plainProvider
	service  *connectors.Service
	router   http.Handler
	hooks    http.Handler
}

func newFixture(t *testing.T, authorize func(*http.Request) error, configs ...connectors.Config) *fixture {
	t.Helper()
	store := connectors.NewMemoryStore(configs...)
	backend := &fakeBackend{}
	provider := &fakeProvider{}
	plain := &plainProvider{}
	service := connectors.New(connectors.Deps{Store: store, Backend: backend, Logf: func(string, ...any) {}})
	if err := service.Register(provider); err != nil {
		t.Fatal(err)
	}
	if err := service.Register(plain); err != nil {
		t.Fatal(err)
	}
	deps := Deps{Service: service, Store: store, Authorize: authorize, Logf: func(string, ...any) {}}
	return &fixture{
		store:    store,
		backend:  backend,
		provider: provider,
		plain:    plain,
		service:  service,
		router:   NewRouter(deps),
		hooks:    NewHooksRouter(deps),
	}
}

func allowAll(*http.Request) error { return nil }

func baseConfig(id, provider string) connectors.Config {
	return connectors.Config{
		ID: id, Provider: provider, Name: id, Enabled: true, Mode: "polling",
		Secrets: map[string]string{"bot_token": "secret-token"},
	}
}

func do(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

// ── 用例 ───────────────────────────────────────────────────────────────────

func TestAuthorizeGate(t *testing.T) {
	anon := newFixture(t, nil, baseConfig("c1", "fakeweb"))
	if recorder := do(t, anon.router, http.MethodGet, "/connectors", ""); recorder.Code != http.StatusForbidden {
		t.Fatalf("无 Authorizer 应 403，got %d", recorder.Code)
	}
	denied := newFixture(t, func(*http.Request) error { return errors.New("bad token") }, baseConfig("c1", "fakeweb"))
	if recorder := do(t, denied.router, http.MethodGet, "/connectors", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("拒绝应 401，got %d", recorder.Code)
	}
	allowed := newFixture(t, allowAll, baseConfig("c1", "fakeweb"))
	if recorder := do(t, allowed.router, http.MethodGet, "/connectors", ""); recorder.Code != http.StatusOK {
		t.Fatalf("允许应 200，got %d (%s)", recorder.Code, recorder.Body.String())
	}
}

func TestCreateValidatesAndMasksSecrets(t *testing.T) {
	fx := newFixture(t, allowAll)

	missing := do(t, fx.router, http.MethodPost, "/connectors", `{"id":"c2","provider":"fakeweb"}`)
	if missing.Code != http.StatusBadRequest || !strings.Contains(missing.Body.String(), "bot_token") {
		t.Fatalf("缺少必填密钥应 400 且指出字段，got %d %s", missing.Code, missing.Body.String())
	}

	created := do(t, fx.router, http.MethodPost, "/connectors",
		`{"id":"c2","provider":"fakeweb","name":"工作群","secrets":{"bot_token":"tok-123"}}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("创建应 201，got %d %s", created.Code, created.Body.String())
	}
	body := created.Body.String()
	if strings.Contains(body, "tok-123") {
		t.Fatalf("响应不得回显密钥明文：%s", body)
	}
	if !strings.Contains(body, `"secret_keys":["bot_token"]`) {
		t.Fatalf("应只回密钥字段名：%s", body)
	}
	if !strings.Contains(body, `"mode":"polling"`) {
		t.Fatalf("应回填 Schema 默认模式：%s", body)
	}

	dup := do(t, fx.router, http.MethodPost, "/connectors", `{"id":"c2","provider":"fakeweb","secrets":{"bot_token":"x"}}`)
	if dup.Code != http.StatusConflict {
		t.Fatalf("重复 id 应 409，got %d", dup.Code)
	}
}

func TestUpdateKeepsAndClearsSecret(t *testing.T) {
	fx := newFixture(t, allowAll, baseConfig("c1", "fakeweb"))

	// 留空 = 保持
	kept := do(t, fx.router, http.MethodPut, "/connectors/c1", `{"name":"改个名","settings":{"label":"备注"}}`)
	if kept.Code != http.StatusOK {
		t.Fatalf("更新应 200，got %d %s", kept.Code, kept.Body.String())
	}
	if cfg, _ := fx.store.GetConfig("c1"); cfg.Secret("bot_token") != "secret-token" {
		t.Fatalf("留空不应清空密钥：%+v", cfg.Secrets)
	}
	if cfg, _ := fx.store.GetConfig("c1"); cfg.Name != "改个名" || cfg.Setting("label", "") != "备注" {
		t.Fatalf("普通字段应更新：%+v", cfg)
	}

	// PATCH 与 PUT 等价（契约首选 PATCH）
	patched := do(t, fx.router, http.MethodPatch, "/connectors/c1", `{"name":"再改一次"}`)
	if patched.Code != http.StatusOK {
		t.Fatalf("PATCH 应 200，got %d %s", patched.Code, patched.Body.String())
	}
	if cfg, _ := fx.store.GetConfig("c1"); cfg.Name != "再改一次" {
		t.Fatalf("PATCH 未生效：%+v", cfg)
	}

	// __clear__ = 清除
	cleared := do(t, fx.router, http.MethodPut, "/connectors/c1", `{"secrets":{"bot_token":"__clear__"}}`)
	if cleared.Code != http.StatusOK {
		t.Fatalf("清除应 200，got %d %s", cleared.Code, cleared.Body.String())
	}
	if cfg, _ := fx.store.GetConfig("c1"); cfg.Secret("bot_token") != "" {
		t.Fatalf("__clear__ 应清空密钥：%+v", cfg.Secrets)
	}

	// provider 不可变更
	denied := do(t, fx.router, http.MethodPut, "/connectors/c1", `{"provider":"plain"}`)
	if denied.Code != http.StatusBadRequest {
		t.Fatalf("变更 provider 应 400，got %d", denied.Code)
	}
}

func TestSchemaDocument(t *testing.T) {
	fx := newFixture(t, allowAll)
	recorder := do(t, fx.router, http.MethodGet, "/connectors/schema", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("schema 应 200，got %d", recorder.Code)
	}
	var payload api.Schema
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Version != SchemaVersion || len(payload.Providers) != 2 {
		t.Fatalf("schema = %+v", payload)
	}
}

func TestDeleteConnector(t *testing.T) {
	fx := newFixture(t, allowAll, baseConfig("c1", "fakeweb"))
	if recorder := do(t, fx.router, http.MethodDelete, "/connectors/c1", ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("删除应 204，got %d", recorder.Code)
	}
	if _, ok := fx.store.GetConfig("c1"); ok {
		t.Fatal("配置应已删除")
	}
	if recorder := do(t, fx.router, http.MethodDelete, "/connectors/c1", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("再次删除应 404，got %d", recorder.Code)
	}
}

func TestTestMessageEndpoint(t *testing.T) {
	fx := newFixture(t, allowAll, baseConfig("c1", "fakeweb"))
	recorder := do(t, fx.router, http.MethodPost, "/connectors/c1/test", `{"chat":"chat-9","text":"测试一下"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("测试消息应 200，got %d %s", recorder.Code, recorder.Body.String())
	}
	if len(fx.provider.sent) != 1 || fx.provider.sent[0].Text != "测试一下" {
		t.Fatalf("sent = %+v", fx.provider.sent)
	}

	noChat := do(t, fx.router, http.MethodPost, "/connectors/c1/test", `{}`)
	if noChat.Code != http.StatusBadRequest {
		t.Fatalf("缺 chat 应 400，got %d", noChat.Code)
	}
}

func TestReloadEndpoint(t *testing.T) {
	fx := newFixture(t, allowAll, baseConfig("c1", "fakeweb"))
	if recorder := do(t, fx.router, http.MethodPost, "/connectors/c1/reload", ""); recorder.Code != http.StatusAccepted {
		t.Fatalf("reload 应 202，got %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := do(t, fx.router, http.MethodPost, "/connectors/none/reload", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("未知 id 应 404，got %d", recorder.Code)
	}
}

func TestWebhookRoutesInboundMessage(t *testing.T) {
	fx := newFixture(t, allowAll, baseConfig("c1", "fakeweb"))
	request := httptest.NewRequest(http.MethodPost, "/hooks/connectors/c1", strings.NewReader("{}"))
	request.Header.Set("X-Message-Id", "hook-1")
	recorder := httptest.NewRecorder()
	fx.hooks.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("webhook 应 202，got %d %s", recorder.Code, recorder.Body.String())
	}
	waitFor(t, "backend handle", func() bool { return len(fx.backend.requests()) == 1 })
	if got := fx.backend.requests()[0]; got.Chat != "chat-1" || got.ConnectorID != "c1" {
		t.Fatalf("backend request = %+v", got)
	}
}

func TestWebhookRejectsBadRequestAndUnsupported(t *testing.T) {
	fx := newFixture(t, allowAll, baseConfig("c1", "fakeweb"), baseConfig("c2", "plain"))

	fx.provider.webhookErr = errors.New("签名校验失败")
	bad := httptest.NewRecorder()
	fx.hooks.ServeHTTP(bad, httptest.NewRequest(http.MethodPost, "/hooks/connectors/c1", strings.NewReader("{}")))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("签名失败应 400，got %d", bad.Code)
	}

	unsupported := httptest.NewRecorder()
	fx.hooks.ServeHTTP(unsupported, httptest.NewRequest(http.MethodPost, "/hooks/connectors/c2", strings.NewReader("{}")))
	if unsupported.Code != http.StatusMethodNotAllowed {
		t.Fatalf("不支持 webhook 应 405，got %d", unsupported.Code)
	}

	missing := httptest.NewRecorder()
	fx.hooks.ServeHTTP(missing, httptest.NewRequest(http.MethodPost, "/hooks/connectors/none", strings.NewReader("{}")))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("未知连接器应 404，got %d", missing.Code)
	}
}

func TestWebhookChallengeResponse(t *testing.T) {
	fx := newFixture(t, allowAll, baseConfig("c1", "fakeweb"))
	fx.provider.webhookResponse = map[string]any{"challenge": "abc"}
	recorder := httptest.NewRecorder()
	fx.hooks.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/hooks/connectors/c1", strings.NewReader("{}")))
	if recorder.Code != http.StatusOK {
		t.Fatalf("challenge 应 200，got %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"challenge":"abc"`) {
		t.Fatalf("challenge 应答 = %s", recorder.Body.String())
	}
	if len(fx.backend.requests()) != 0 {
		t.Fatal("challenge 不应投递消息")
	}
}

func TestWebhookDisabledConnectorHidden(t *testing.T) {
	cfg := baseConfig("c1", "fakeweb")
	cfg.Enabled = false
	fx := newFixture(t, allowAll, cfg)
	recorder := httptest.NewRecorder()
	fx.hooks.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/hooks/connectors/c1", strings.NewReader("{}")))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("停用连接器 webhook 应 404，got %d", recorder.Code)
	}
}

func TestViewsDoNotLeakPolicySecrets(t *testing.T) {
	cfg := baseConfig("c1", "fakeweb")
	cfg.Settings = map[string]string{"label": "备注"}
	cfg.Policy = connectors.Policy{CommandPrefix: "/", AllowedUsers: []string{"u1"}, ReplyMode: "final"}
	fx := newFixture(t, allowAll, cfg)
	recorder := do(t, fx.router, http.MethodGet, "/connectors/c1", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET 应 200，got %d", recorder.Code)
	}
	var view ConnectorView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Policy.AllowedUsers[0] != "u1" || view.Policy.ReplyMode != "final" {
		t.Fatalf("policy 视图 = %+v", view.Policy)
	}
	if view.Settings["label"] != "备注" || view.Settings["bot_token"] != "" {
		t.Fatalf("settings 视图 = %+v", view.Settings)
	}
	if fmt.Sprint(view.SecretKeys) != "[bot_token]" {
		t.Fatalf("secret_keys = %v", view.SecretKeys)
	}
}
