package mcpmount

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

type proxyCall struct {
	nodeID string
	path   string
	mount  string
}

type fakeProxy struct {
	mu      sync.Mutex
	calls   []proxyCall
	status  int
	block   chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (p *fakeProxy) ServeProxyMount(w http.ResponseWriter, r *http.Request, nodeID, path, mount string) {
	p.mu.Lock()
	p.calls = append(p.calls, proxyCall{nodeID: nodeID, path: path, mount: mount})
	p.mu.Unlock()
	if p.entered != nil {
		p.once.Do(func() { close(p.entered) })
	}
	if p.block != nil {
		<-p.block
	}
	status := p.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte("proxied"))
}

func (p *fakeProxy) snapshot() []proxyCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]proxyCall(nil), p.calls...)
}

type fixture struct {
	handler   *Handler
	registry  *Registry
	tokens    *TokenStore
	proxy     *fakeProxy
	plaintext string
	events    *[]Event
}

func newFixture(t *testing.T, mutate func(*HandlerConfig)) *fixture {
	t.Helper()
	registry := NewRegistry()
	if err := registry.Upsert(&Record{
		Slug:    "k7f3qz2a",
		MountID: "node-identity/prod",
		NodeID:  "node-a",
		Kind:    "mcp",
		Limits:  wire.MountLimits{MaxConcurrency: 1},
	}); err != nil {
		t.Fatalf("registry upsert: %v", err)
	}
	tokens := NewTokenStore()
	plaintext, _, err := tokens.Create("ci", []string{"k7f3qz2a"}, 0)
	if err != nil {
		t.Fatalf("token create: %v", err)
	}
	proxy := &fakeProxy{}
	events := make([]Event, 0, 4)
	cfg := HandlerConfig{
		Registry: registry,
		Tokens:   tokens,
		Proxy:    proxy,
		OnEvent:  func(event Event) { events = append(events, event) },
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return &fixture{
		handler:   NewHandler(cfg),
		registry:  registry,
		tokens:    tokens,
		proxy:     proxy,
		plaintext: plaintext,
		events:    &events,
	}
}

func (f *fixture) request(t *testing.T, method, target, token, slug, localPath string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeMount(rec, req, slug, localPath)
	return rec
}

func TestHandlerDeniesUnauthenticated(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.request(t, http.MethodPost, "/m/k7f3qz2a/mcp", "", "k7f3qz2a", "/mcp")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "aicli-gateway-mount") {
		t.Fatalf("WWW-Authenticate = %q", got)
	}
	// Even an unknown slug answers 401 first: no enumeration oracle.
	rec = f.request(t, http.MethodPost, "/m/unknown/mcp", "", "unknown", "/mcp")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown slug without token: status = %d, want 401", rec.Code)
	}
	if calls := f.proxy.snapshot(); len(calls) != 0 {
		t.Fatalf("proxy was called for a rejected request: %v", calls)
	}
}

func TestHandlerRejectsBadToken(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.request(t, http.MethodPost, "/m/k7f3qz2a/mcp", "amt_wrong", "k7f3qz2a", "/mcp")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandlerScopeDisabledStaleAndMissing(t *testing.T) {
	t.Run("scope", func(t *testing.T) {
		f := newFixture(t, nil)
		other, _, err := f.tokens.Create("other", []string{"different-mount"}, 0)
		if err != nil {
			t.Fatalf("create scoped token: %v", err)
		}
		rec := f.request(t, http.MethodPost, "/m/k7f3qz2a/mcp", other, "k7f3qz2a", "/mcp")
		assertError(t, rec, http.StatusForbidden, "token_scope")
	})
	t.Run("disabled", func(t *testing.T) {
		f := newFixture(t, nil)
		f.registry.SetDisabled("k7f3qz2a", true)
		rec := f.request(t, http.MethodPost, "/m/k7f3qz2a/mcp", f.plaintext, "k7f3qz2a", "/mcp")
		assertError(t, rec, http.StatusForbidden, "mount_disabled")
	})
	t.Run("stale", func(t *testing.T) {
		f := newFixture(t, nil)
		f.registry.MarkStale("k7f3qz2a")
		rec := f.request(t, http.MethodPost, "/m/k7f3qz2a/mcp", f.plaintext, "k7f3qz2a", "/mcp")
		assertError(t, rec, http.StatusBadGateway, "node_offline")
		if rec.Header().Get("Retry-After") == "" {
			t.Fatalf("offline response is missing Retry-After")
		}
	})
	t.Run("node live hook", func(t *testing.T) {
		f := newFixture(t, func(cfg *HandlerConfig) {
			cfg.NodeLive = func(string) bool { return false }
		})
		rec := f.request(t, http.MethodPost, "/m/k7f3qz2a/mcp", f.plaintext, "k7f3qz2a", "/mcp")
		assertError(t, rec, http.StatusBadGateway, "node_offline")
	})
	t.Run("missing", func(t *testing.T) {
		f := newFixture(t, nil)
		rec := f.request(t, http.MethodPost, "/m/unknown/mcp", f.plaintext, "unknown", "/mcp")
		assertError(t, rec, http.StatusNotFound, "mount_not_found")
	})
}

func TestHandlerProxiesLocalPathOnly(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.request(t, http.MethodPost, "/m/k7f3qz2a/mcp?session=1", f.plaintext, "k7f3qz2a", "/mcp")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	calls := f.proxy.snapshot()
	if len(calls) != 1 {
		t.Fatalf("proxy calls = %d, want 1", len(calls))
	}
	if calls[0].nodeID != "node-a" {
		t.Fatalf("proxied node = %q, want node-a", calls[0].nodeID)
	}
	if calls[0].path != "/mcp" {
		t.Fatalf("proxied path = %q, want /mcp (query belongs to ServeProxy)", calls[0].path)
	}
	if calls[0].mount != "k7f3qz2a" {
		t.Fatalf("proxied mount = %q, want k7f3qz2a", calls[0].mount)
	}
	if len(*f.events) != 1 || (*f.events)[0].Status != http.StatusOK || (*f.events)[0].TokenID == "" {
		t.Fatalf("event = %+v", *f.events)
	}
}

func TestHandlerRejectsTraversal(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.request(t, http.MethodPost, "/m/k7f3qz2a/../admin", f.plaintext, "k7f3qz2a", "/../admin")
	assertError(t, rec, http.StatusForbidden, "bad_local_path")
	if calls := f.proxy.snapshot(); len(calls) != 0 {
		t.Fatalf("proxy was called for a traversal attempt: %v", calls)
	}
}

func TestHandlerLimitsConcurrencyPerMount(t *testing.T) {
	f := newFixture(t, nil)
	f.proxy.block = make(chan struct{})
	f.proxy.entered = make(chan struct{})

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		req := httptest.NewRequest(http.MethodPost, "/m/k7f3qz2a/mcp", nil)
		req.Header.Set("Authorization", "Bearer "+f.plaintext)
		f.handler.ServeMount(httptest.NewRecorder(), req, "k7f3qz2a", "/mcp")
	}()
	<-f.proxy.entered

	rec := f.request(t, http.MethodPost, "/m/k7f3qz2a/mcp", f.plaintext, "k7f3qz2a", "/mcp")
	assertError(t, rec, http.StatusTooManyRequests, "mount_busy")
	if rec.Header().Get("Retry-After") == "" {
		t.Fatalf("429 is missing Retry-After")
	}

	close(f.proxy.block)
	<-firstDone
}

func TestHandlerFailsClosedWithoutProxy(t *testing.T) {
	f := newFixture(t, func(cfg *HandlerConfig) { cfg.Proxy = nil })
	rec := f.request(t, http.MethodPost, "/m/k7f3qz2a/mcp", f.plaintext, "k7f3qz2a", "/mcp")
	assertError(t, rec, http.StatusBadGateway, "proxy_unavailable")
}

func TestHandlerFailsClosedWithoutTokenStore(t *testing.T) {
	f := newFixture(t, func(cfg *HandlerConfig) { cfg.Tokens = nil })
	rec := f.request(t, http.MethodPost, "/m/k7f3qz2a/mcp", "amt_anything", "k7f3qz2a", "/mcp")
	assertError(t, rec, http.StatusUnauthorized, "token_store_unavailable")
}

func assertError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, status, rec.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, rec.Body.String())
	}
	if payload["error"] != code {
		t.Fatalf("error = %q, want %q", payload["error"], code)
	}
}
