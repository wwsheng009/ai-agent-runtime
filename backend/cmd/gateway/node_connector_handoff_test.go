package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
)

type handoffTestProvider struct{ name string }

func (p *handoffTestProvider) Name() string { return p.name }
func (p *handoffTestProvider) Schema() api.ProviderSchema {
	return api.ProviderSchema{Provider: p.name, Modes: []string{"gateway", "polling"}}
}
func (p *handoffTestProvider) Activate(context.Context, connectors.Config) (connectors.Activation, error) {
	return connectors.Activation{}, nil
}
func (p *handoffTestProvider) Deactivate(context.Context, connectors.Config) error { return nil }
func (p *handoffTestProvider) Send(context.Context, connectors.Config, string, []connectors.OutboundMessage) error {
	return nil
}

type handoffTestProxy struct {
	payload       []byte
	confirmStatus int
	confirmCalls  int
	paths         []string
}

// handoffStateProxy 在 handoff 代理基础上补 context-tokens 状态响应。
type handoffStateProxy struct {
	proxy  *handoffTestProxy
	tokens string
}

func (p *handoffStateProxy) ServeProxyPinned(w http.ResponseWriter, r *http.Request, nodeID, path string) {
	if strings.Contains(path, "state/context-tokens") {
		raw, _ := json.Marshal(map[string]string{"context_tokens": p.tokens})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
		return
	}
	if p.proxy != nil {
		p.proxy.ServeProxyPinned(w, r, nodeID, path)
	}
}

func (p *handoffTestProxy) ServeProxyPinned(w http.ResponseWriter, r *http.Request, _ string, path string) {
	p.paths = append(p.paths, r.Method+" "+path)
	switch {
	case strings.HasSuffix(path, "/handoff"):
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(p.payload)
	case strings.HasSuffix(path, "/handoff/confirm"):
		p.confirmCalls++
		status := p.confirmStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":true}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newHandoffTestService(t *testing.T, providers ...string) (*connectors.Service, connectors.Store) {
	t.Helper()
	store := connectors.NewMemoryStore()
	service := connectors.New(connectors.Deps{Store: store, Logf: func(string, ...any) {}})
	for _, name := range providers {
		if err := service.Register(&handoffTestProvider{name: name}); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	return service, store
}

func handoffRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/nodes/node-a/connectors/qq-1/handoff", nil)
	return mux.SetURLVars(req, map[string]string{"node_id": "node-a", "connector_id": "qq-1"})
}

func TestNodeConnectorHandoffImportsOutboundQQ(t *testing.T) {
	service, store := newHandoffTestService(t, "qqbot")
	payload := nodeHandoffPayload{
		ID:       "qq-1",
		Provider: "qqbot",
		Name:     "QQ 运维",
		Mode:     "gateway",
		Settings: map[string]string{"app_id": "app-1"},
		Secrets:  map[string]string{"app_secret": "super-secret"},
		Bindings: []nodeHandoffBinding{{Chat: "group:g1", SessionID: "s1", NodeID: "node-a"}},
	}
	raw, _ := json.Marshal(payload)
	proxy := &handoffTestProxy{payload: raw}

	rec := httptest.NewRecorder()
	handleNodeConnectorHandoff(proxy, service, func(string, ...any) {})(rec, handoffRequest())
	if rec.Code != http.StatusOK {
		t.Fatalf("import = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "super-secret") {
		t.Fatal("response must not leak secrets")
	}
	if proxy.confirmCalls != 1 {
		t.Fatalf("confirm calls = %d, want 1", proxy.confirmCalls)
	}

	id := importedConnectorID("node-a", "qq-1")
	cfg, ok := store.GetConfig(id)
	if !ok {
		t.Fatalf("imported connector %s missing", id)
	}
	if cfg.Provider != "qqbot" || cfg.RuntimeMode != "outbound" || !cfg.Enabled {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.OriginNodeID != "node-a" || cfg.OriginConnectorID != "qq-1" {
		t.Fatalf("origin = %+v", cfg)
	}
	if cfg.Secrets["app_secret"] != "super-secret" || cfg.Settings["app_id"] != "app-1" {
		t.Fatalf("copied fields = %+v / %+v", cfg.Settings, cfg.Secrets)
	}
}

func TestNodeConnectorHandoffImportsWeChatState(t *testing.T) {
	service, store := newHandoffTestService(t, "wechat")
	payload := nodeHandoffPayload{
		ID:       "wx-1",
		Provider: "wechat",
		Name:     "微信 owner",
		Mode:     "polling",
		Settings: map[string]string{"base_url": "https://wx.example"},
		Secrets:  map[string]string{"bot_token": "wx-token"},
		State:    map[string]string{"context_tokens": `{"owner":"ctx"}`},
	}
	raw, _ := json.Marshal(payload)
	proxy := &handoffTestProxy{payload: raw}

	rec := httptest.NewRecorder()
	handleNodeConnectorHandoff(proxy, service, func(string, ...any) {})(rec, handoffRequest())
	if rec.Code != http.StatusOK {
		t.Fatalf("import = %d %s", rec.Code, rec.Body.String())
	}
	id := importedConnectorID("node-a", "qq-1")
	rawState, ok := store.PollingState(id).Get(wechatContextKey)
	if !ok || !strings.Contains(rawState, "ctx") {
		t.Fatalf("wechat context state = %q ok=%v", rawState, ok)
	}
}

func TestNodeConnectorHandoffRollsBackWhenConfirmFails(t *testing.T) {
	service, store := newHandoffTestService(t, "qqbot")
	payload := nodeHandoffPayload{ID: "qq-1", Provider: "qqbot", Mode: "gateway", Secrets: map[string]string{"app_secret": "s"}}
	raw, _ := json.Marshal(payload)
	proxy := &handoffTestProxy{payload: raw, confirmStatus: http.StatusInternalServerError}

	rec := httptest.NewRecorder()
	handleNodeConnectorHandoff(proxy, service, func(string, ...any) {})(rec, handoffRequest())
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("import = %d %s", rec.Code, rec.Body.String())
	}
	id := importedConnectorID("node-a", "qq-1")
	if _, ok := store.GetConfig(id); ok {
		t.Fatal("failed confirm must delete the imported connector")
	}
}

func TestSyncImportedWeChatState(t *testing.T) {
	// The periodic loop waits 20s; directly exercise the single fetch used by it.
	raw, err := fetchNodeContextTokens(context.Background(), &handoffStateProxy{proxy: &handoffTestProxy{}, tokens: `{"owner":"ctx"}`}, "node-a", "wx-1")
	if err != nil || !strings.Contains(raw, "ctx") {
		t.Fatalf("fetch context tokens = %q err=%v", raw, err)
	}
}
// TestWeChatSyncEligibleOnlyForOutboundCopies 锁定节点 token 同步的适用范围：
// 只有「只出站副本」才需要从源节点同步 context_token；「网关自持」的 token 由
// 本机轮询产出，若仍同步就会被节点（离线时为空的）旧值覆盖 —— 这正是「网关
// 自持后通知仍失败」的根因，必须有回归保护。
func TestWeChatSyncEligibleOnlyForOutboundCopies(t *testing.T) {
	base := connectors.Config{
		ID:                "gw-1-wechat",
		Provider:          "wechat",
		Enabled:           true,
		OriginNodeID:      "node-a",
		OriginConnectorID: "wechat-1",
	}

	outbound := base
	outbound.RuntimeMode = connectors.RuntimeModeOutbound
	if !wechatSyncEligible(outbound) {
		t.Fatalf("只出站副本必须从源节点同步 context_token")
	}

	selfHosted := base
	selfHosted.RuntimeMode = connectors.RuntimeModeGateway
	if wechatSyncEligible(selfHosted) {
		t.Fatalf("网关自持不得同步节点 token（会覆盖本机新产出的值）")
	}

	cases := []struct {
		name string
		mut  func(*connectors.Config)
	}{
		{"disabled", func(c *connectors.Config) { c.Enabled = false }},
		{"non-wechat", func(c *connectors.Config) { c.Provider = "qqbot" }},
		{"no-origin-node", func(c *connectors.Config) { c.OriginNodeID = "" }},
		{"no-origin-connector", func(c *connectors.Config) { c.OriginConnectorID = "" }},
	}
	for _, tc := range cases {
		cfg := outbound
		tc.mut(&cfg)
		if wechatSyncEligible(cfg) {
			t.Fatalf("%s: must not be sync-eligible", tc.name)
		}
	}
}