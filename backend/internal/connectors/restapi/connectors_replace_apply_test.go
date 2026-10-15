package restapi

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
)

// credentialReplaceProvider 是自包含的测试替身（不依赖同包其它测试文件）：
// required 文本 app_id + required secret bot_token，支持轮询。
type credentialReplaceProvider struct {
	starts int32
}

func (p *credentialReplaceProvider) Name() string { return "fakeverify" }

func (p *credentialReplaceProvider) Schema() api.ProviderSchema {
	return api.ProviderSchema{
		Provider: "fakeverify",
		Label:    "Fake Verify",
		Modes:    []string{"polling"},
		Fields: []api.FieldSchema{
			{Key: "app_id", Label: "AppID", Type: api.FieldText, Required: true},
			{Key: "bot_token", Label: "Bot Token", Type: api.FieldSecret, Required: true},
			{Key: "label", Label: "备注", Type: api.FieldText},
		},
	}
}

func (p *credentialReplaceProvider) Activate(context.Context, connectors.Config) (connectors.Activation, error) {
	return connectors.Activation{}, nil
}

func (p *credentialReplaceProvider) Deactivate(context.Context, connectors.Config) error { return nil }

func (p *credentialReplaceProvider) Send(context.Context, connectors.Config, string, []connectors.OutboundMessage) error {
	return nil
}

func (p *credentialReplaceProvider) SupportsPolling(connectors.Config) bool { return true }

func (p *credentialReplaceProvider) RunPolling(ctx context.Context, _ connectors.Config, _ connectors.InboundHandler, _ connectors.PollingState) error {
	atomic.AddInt32(&p.starts, 1)
	<-ctx.Done()
	return nil
}

func credentialReplaceSeedConfig() connectors.Config {
	return connectors.Config{
		ID: "c1", Provider: "fakeverify", Name: "c1", Enabled: true, Mode: "polling",
		Settings: map[string]string{"app_id": "app-1"},
		Secrets:  map[string]string{"bot_token": "secret-token"},
	}
}

// TestPatchCredentialReplacementTakesOverLiveOwner 覆盖「扫码/换凭据保存后立即
// 生效」：保存发生在非持有宿主时，PATCH 走 ApplyCredentialReplacement 强制接管
// 运行归属，而不是留下「未运行（由 X 持有）」的静默失联（2026-10-15 微信复盘）。
func TestPatchCredentialReplacementTakesOverLiveOwner(t *testing.T) {
	store := connectors.NewMemoryStore(credentialReplaceSeedConfig())
	provider := &credentialReplaceProvider{}
	service := connectors.New(connectors.Deps{
		Store:   store,
		Backend: &fakeBackend{},
		HostID:  "aicli:node-9",
		Logf:    func(string, ...any) {},
	})
	if err := service.Register(provider); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Stop(context.Background()) })
	// 另一个存活宿主先持有归属（心跳新鲜）：旧流程下本次保存只会被拒并记 warn。
	if _, _, err := store.ClaimConnectorOwner("c1", "runtime-server", time.Now().UTC(), time.Minute); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(Deps{Service: service, Store: store, Authorize: allowAll, Logf: func(string, ...any) {}})

	recorder := do(t, router, http.MethodPatch, "/connectors/c1", `{"secrets":{"bot_token":"new-token"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PATCH = %d %s", recorder.Code, recorder.Body.String())
	}
	record, ok := store.OwnerOf("c1")
	if !ok || record.Owner != "aicli:node-9" {
		t.Fatalf("owner after credential replacement = %+v ok=%v, want aicli:node-9", record, ok)
	}
	waitFor(t, "polling start after credential replacement", func() bool {
		return atomic.LoadInt32(&provider.starts) == 1
	})
	entries, _ := service.Logs("c1", 20)
	if !containsEntry(entries, "凭据已替换，自动接管运行归属（原持有者 runtime-server）") {
		t.Fatalf("expected auto-takeover ring entry, got %+v", entries)
	}
}

// TestPatchPlainConfigReloadKeepsOwnership 覆盖普通微调不走接管：非持有者保存
// 非凭据字段时保持「只读配置」，归属与轮询者不变（避免把配置微调变成抢锁）。
func TestPatchPlainConfigReloadKeepsOwnership(t *testing.T) {
	store := connectors.NewMemoryStore(credentialReplaceSeedConfig())
	provider := &credentialReplaceProvider{}
	service := connectors.New(connectors.Deps{
		Store:   store,
		Backend: &fakeBackend{},
		HostID:  "aicli:node-9",
		Logf:    func(string, ...any) {},
	})
	if err := service.Register(provider); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ClaimConnectorOwner("c1", "runtime-server", time.Now().UTC(), time.Minute); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(Deps{Service: service, Store: store, Authorize: allowAll, Logf: func(string, ...any) {}})

	recorder := do(t, router, http.MethodPatch, "/connectors/c1", `{"name":"新名字"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PATCH = %d %s", recorder.Code, recorder.Body.String())
	}
	record, _ := store.OwnerOf("c1")
	if record.Owner != "runtime-server" {
		t.Fatalf("plain config edit must not steal ownership, owner = %q", record.Owner)
	}
	if starts := atomic.LoadInt32(&provider.starts); starts != 0 {
		t.Fatalf("plain config edit must not start polling on this host, starts = %d", starts)
	}
}

func containsEntry(entries []connectors.LogEntry, substr string) bool {
	for _, entry := range entries {
		if strings.Contains(entry.Summary, substr) {
			return true
		}
	}
	return false
}