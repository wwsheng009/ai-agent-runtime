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

// ── 测试替身：可轮询通道 ───────────────────────────────────────────────────

type pollProvider struct {
	starts int32
}

func (p *pollProvider) Name() string { return "pollweb" }

func (p *pollProvider) Schema() api.ProviderSchema {
	return api.ProviderSchema{Provider: "pollweb", Label: "Poll Web", Modes: []string{"polling"}}
}

func (p *pollProvider) Activate(context.Context, connectors.Config) (connectors.Activation, error) {
	return connectors.Activation{}, nil
}

func (p *pollProvider) Deactivate(context.Context, connectors.Config) error { return nil }

func (p *pollProvider) Send(context.Context, connectors.Config, string, []connectors.OutboundMessage) error {
	return nil
}

func (p *pollProvider) SupportsPolling(connectors.Config) bool { return true }

func (p *pollProvider) RunPolling(ctx context.Context, _ connectors.Config, _ connectors.InboundHandler, _ connectors.PollingState) error {
	atomic.AddInt32(&p.starts, 1)
	<-ctx.Done()
	return nil
}

func (p *pollProvider) startCount() int32 { return atomic.LoadInt32(&p.starts) }

// ── 测试助手 ───────────────────────────────────────────────────────────────

type ownerFixture struct {
	store    *connectors.MemoryStore
	service  *connectors.Service
	provider *pollProvider
	router   http.Handler
}

func newOwnerFixture(t *testing.T, hostID string, configs ...connectors.Config) *ownerFixture {
	t.Helper()
	store := connectors.NewMemoryStore(configs...)
	service := connectors.New(connectors.Deps{Store: store, Backend: &fakeBackend{}, Logf: func(string, ...any) {}, HostID: hostID})
	provider := &pollProvider{}
	if err := service.Register(provider); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(Deps{
		Service:   service,
		Store:     store,
		Authorize: allowAll,
		Logf:      func(string, ...any) {},
		HostID:    hostID,
	})
	t.Cleanup(func() { service.Stop(context.Background()) })
	return &ownerFixture{store: store, service: service, provider: provider, router: router}
}

func (f *ownerFixture) pollStarts() int32 { return f.provider.startCount() }

// ── 用例 ───────────────────────────────────────────────────────────────────

// TestConnectorViewOwnershipFields 覆盖视图 additive 字段：active_owner 与
// owned_by_current_host（列表与详情一致；未认领时 active_owner 省略）。
func TestConnectorViewOwnershipFields(t *testing.T) {
	fx := newOwnerFixture(t, "runtime-server", baseConfig("c1", "pollweb"))
	foreign := NewRouter(Deps{
		Service:   fx.service,
		Store:     fx.store,
		Authorize: allowAll,
		Logf:      func(string, ...any) {},
		HostID:    "aicli",
	})

	// 未认领：active_owner 省略（omitempty），owned_by_current_host=false 仍在。
	list := do(t, fx.router, http.MethodGet, "/connectors", "").Body.String()
	if strings.Contains(list, `"active_owner"`) || !strings.Contains(list, `"owned_by_current_host":false`) {
		t.Fatalf("unclaimed list view unexpected: %s", list)
	}

	if _, _, err := fx.store.ClaimConnectorOwner("c1", "runtime-server", time.Now().UTC(), time.Hour); err != nil {
		t.Fatal(err)
	}

	currentList := do(t, fx.router, http.MethodGet, "/connectors", "").Body.String()
	if !strings.Contains(currentList, `"active_owner":"runtime-server"`) || !strings.Contains(currentList, `"owned_by_current_host":true`) {
		t.Fatalf("current-host list view unexpected: %s", currentList)
	}
	foreignList := do(t, foreign, http.MethodGet, "/connectors", "").Body.String()
	if !strings.Contains(foreignList, `"active_owner":"runtime-server"`) || !strings.Contains(foreignList, `"owned_by_current_host":false`) {
		t.Fatalf("foreign-host list view unexpected: %s", foreignList)
	}
	detail := do(t, foreign, http.MethodGet, "/connectors/c1", "").Body.String()
	if !strings.Contains(detail, `"active_owner":"runtime-server"`) || !strings.Contains(detail, `"owned_by_current_host":false`) {
		t.Fatalf("foreign-host detail view unexpected: %s", detail)
	}
}

// TestTakeoverEndpointClaimsAndStartsPolling 覆盖 POST /connectors/{id}/takeover：
// 强制覆盖存活归属、返回本宿主持有的视图、并启动轮询。
func TestTakeoverEndpointClaimsAndStartsPolling(t *testing.T) {
	fx := newOwnerFixture(t, "aicli", baseConfig("c1", "pollweb"))
	if _, _, err := fx.store.ClaimConnectorOwner("c1", "runtime-server", time.Now().UTC(), time.Hour); err != nil {
		t.Fatal(err)
	}

	recorder := do(t, fx.router, http.MethodPost, "/connectors/c1/takeover", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("takeover = %d (%s), want 200", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"active_owner":"aicli"`) || !strings.Contains(body, `"owned_by_current_host":true`) {
		t.Fatalf("takeover view unexpected: %s", body)
	}
	waitFor(t, "takeover polling", func() bool { return fx.pollStarts() == 1 })
	if record, ok := fx.store.OwnerOf("c1"); !ok || record.Owner != "aicli" {
		t.Fatalf("owner after takeover = %+v ok=%v, want aicli", record, ok)
	}
}

func TestTakeoverEndpointErrors(t *testing.T) {
	t.Run("no host id", func(t *testing.T) {
		fx := newOwnerFixture(t, "", baseConfig("c1", "pollweb"))
		recorder := do(t, fx.router, http.MethodPost, "/connectors/c1/takeover", "")
		if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "HostID") {
			t.Fatalf("no host id takeover = %d (%s), want 503 mentioning HostID", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("unknown connector", func(t *testing.T) {
		fx := newOwnerFixture(t, "aicli")
		recorder := do(t, fx.router, http.MethodPost, "/connectors/missing/takeover", "")
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("unknown takeover = %d, want 404", recorder.Code)
		}
	})

	t.Run("disabled connector", func(t *testing.T) {
		cfg := baseConfig("c1", "pollweb")
		cfg.Enabled = false
		fx := newOwnerFixture(t, "aicli", cfg)
		recorder := do(t, fx.router, http.MethodPost, "/connectors/c1/takeover", "")
		if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "停用") {
			t.Fatalf("disabled takeover = %d (%s), want 409", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("policy owned by another host", func(t *testing.T) {
		cfg := baseConfig("c1", "pollweb")
		cfg.Policy.RuntimeOwner = "gateway"
		fx := newOwnerFixture(t, "aicli", cfg)
		recorder := do(t, fx.router, http.MethodPost, "/connectors/c1/takeover", "")
		if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "runtime_owner") {
			t.Fatalf("policy mismatch takeover = %d (%s), want 409", recorder.Code, recorder.Body.String())
		}
	})
}
