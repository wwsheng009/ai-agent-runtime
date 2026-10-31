package restapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// wechatFakeProvider 复用 fakeProvider 的行为，只把 Name 换成 wechat，供
// handoff/state 用例走微信分支（Schema 仍提供 bot_token 密钥字段）。
type wechatFakeProvider struct{ *fakeProvider }

func (p *wechatFakeProvider) Name() string { return "wechat" }

func newWechatFixture(t *testing.T, configs ...connectors.Config) *fixture {
	t.Helper()
	store := connectors.NewMemoryStore(configs...)
	backend := &fakeBackend{}
	provider := &wechatFakeProvider{fakeProvider: &fakeProvider{}}
	service := connectors.New(connectors.Deps{Store: store, Backend: backend, Logf: func(string, ...any) {}})
	if err := service.Register(provider); err != nil {
		t.Fatal(err)
	}
	deps := Deps{Service: service, Store: store, Authorize: allowAll, Logf: func(string, ...any) {}}
	return &fixture{
		store:    store,
		backend:  backend,
		provider: provider.fakeProvider,
		service:  service,
		router:   NewRouter(deps),
		hooks:    NewHooksRouter(deps),
	}
}

func doHandoff(t *testing.T, handler http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set(handoffHeader, "1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestHandoffExportConfirmAndView(t *testing.T) {
	cfg := baseConfig("c1", "wechat")
	cfg.Settings = map[string]string{"base_url": "https://example.test"}
	f := newWechatFixture(t, cfg)
	if err := f.store.PutBinding(connectors.Binding{
		ConnectorID: "c1",
		Chat:        "owner-chat",
		Session:     connectors.SessionRef{NodeID: "node-a", SessionID: "sess-1"},
	}); err != nil {
		t.Fatalf("binding: %v", err)
	}
	if err := f.store.PollingState("c1").Set("context_tokens", `{"owner-chat":"ctx-token"}`); err != nil {
		t.Fatalf("state: %v", err)
	}

	// 无 handoff 头必须拒绝。
	req := httptest.NewRequest(http.MethodPost, "/connectors/c1/handoff", nil)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing handoff header = %d, want 403", rec.Code)
	}

	rec = doHandoff(t, f.router, http.MethodPost, "/connectors/c1/handoff")
	if rec.Code != http.StatusOK {
		t.Fatalf("handoff = %d %s", rec.Code, rec.Body.String())
	}
	var payload handoffResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Provider != "wechat" || payload.Secrets["bot_token"] != "secret-token" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Settings["base_url"] != "https://example.test" {
		t.Fatalf("settings = %+v", payload.Settings)
	}
	if payload.State["context_tokens"] == "" {
		t.Fatalf("wechat context_tokens missing: %+v", payload.State)
	}
	if len(payload.Bindings) != 1 || payload.Bindings[0].Chat != "owner-chat" || payload.Bindings[0].SessionID != "sess-1" {
		t.Fatalf("bindings = %+v", payload.Bindings)
	}

	rec = doHandoff(t, f.router, http.MethodPost, "/connectors/c1/handoff/confirm")
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm = %d %s", rec.Code, rec.Body.String())
	}
	updated, _ := f.store.GetConfig("c1")
	if !updated.Policy.Notify.SuppressLocal {
		t.Fatal("confirm must set SuppressLocal")
	}
	view := do(t, f.router, http.MethodGet, "/connectors/c1", "")
	if view.Code != http.StatusOK {
		t.Fatalf("view = %d", view.Code)
	}
	if body := view.Body.String(); len(body) == 0 {
		t.Fatal("empty view")
	}
	var connectorView struct {
		SecretKeys []string `json:"secret_keys"`
		Policy     struct {
			Notify *struct {
				SuppressLocal bool `json:"suppress_local"`
			} `json:"notify"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(view.Body.Bytes(), &connectorView); err != nil {
		t.Fatalf("view decode: %v", err)
	}
	if len(connectorView.SecretKeys) != 1 || connectorView.SecretKeys[0] != "bot_token" {
		t.Fatalf("secret keys = %v", connectorView.SecretKeys)
	}
	if connectorView.Policy.Notify == nil || !connectorView.Policy.Notify.SuppressLocal {
		t.Fatalf("view suppress_local missing: %s", view.Body.String())
	}
	if bodyHasSecret(view.Body.Bytes(), "secret-token") {
		t.Fatal("view must not leak secret value")
	}

	rec = doHandoff(t, f.router, http.MethodPost, "/connectors/c1/handoff/rollback")
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback = %d", rec.Code)
	}
	updated, _ = f.store.GetConfig("c1")
	if updated.Policy.Notify.SuppressLocal {
		t.Fatal("rollback must clear SuppressLocal")
	}
}

func bodyHasSecret(body []byte, secret string) bool {
	return len(secret) > 0 && json.Valid(body) && string(body) != "" && containsString(string(body), secret)
}

func containsString(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
