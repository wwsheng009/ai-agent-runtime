package tunnel_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// TestMountRegistrationAndProxy covers the mount path end to end at the tunnel
// layer: REGISTER carries mounts, the callbacks run in the documented order, the
// agent resolves X-AICLI-Mount to the local upstream and injects the local
// credential, and an unknown mount fails closed instead of hitting the default
// target.
func TestMountRegistrationAndProxy(t *testing.T) {
	const (
		nodeID     = "node-mount"
		nodeStable = "stable-identity"
		slug       = "k7f3qz2a"
	)

	var activated, deactivated atomic.Int32
	var preparedRoutes atomic.Value

	server := tunnel.NewServer(tunnel.Config{
		ServerVersion: "test",
		PublicBaseURL: "https://gw.example.com",
		Registry:      tunnel.NewRegistry(),
		OnMountsPrepare: func(node, stable string, routes []string, mounts []wire.MountSpec) ([]wire.MountAck, []wire.MountAck) {
			preparedRoutes.Store(append([]string(nil), routes...))
			if node != nodeID || stable != nodeStable {
				t.Errorf("prepare identity = %q/%q", node, stable)
			}
			accepted := make([]wire.MountAck, 0, len(mounts))
			for _, mount := range mounts {
				accepted = append(accepted, wire.MountAck{
					Slug:      mount.Slug,
					PublicURL: "https://gw.example.com/m/" + mount.Slug + "/mcp",
				})
			}
			return accepted, nil
		},
		OnMountsActivate: func(node, stable string) {
			if node == nodeID && stable == nodeStable {
				activated.Add(1)
			}
		},
		OnMountsDeactivate: func(node, stable string) {
			if node == nodeID && stable == nodeStable {
				deactivated.Add(1)
			}
		},
	})

	upstreamCalls := make(chan *http.Request, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls <- r
		if r.URL.Path != "/mcp" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer local-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`)
	}))
	t.Cleanup(upstream.Close)

	// The default target must stay reachable for /n/-style traffic. A caller
	// that forges X-AICLI-Mount must not be able to redirect that traffic.
	defaultCalls := make(chan *http.Request, 4)
	defaultUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defaultCalls <- r
		_, _ = io.WriteString(w, "default-ok")
	}))
	t.Cleanup(defaultUpstream.Close)

	tunnelHTTP := httptest.NewServer(http.HandlerFunc(server.ServeHTTP))
	t.Cleanup(tunnelHTTP.Close)

	connector := testConnector(nodeID)
	connector.Register = func() wire.Register {
		return wire.Register{
			NodeID:       nodeID,
			NodeStable:   nodeStable,
			Kind:         "remote-tools",
			Capabilities: []string{"mcp-mount"},
			Routes:       []string{"/mcp"},
			Mounts: []wire.MountSpec{{
				ID:       "self",
				Name:     "self",
				Slug:     slug,
				Kind:     "mcp",
				Upstream: upstream.URL + "/mcp",
				Auth:     &wire.MountAuth{Type: "bearer", TokenEnv: "MOUNT_TOKEN"},
			}},
		}
	}
	connector.Forward = agent.ForwardConfig{
		LocalBaseURL: defaultUpstream.URL,
		Mounts: []agent.MountForward{{
			Slug:     slug,
			BaseURL:  upstream.URL,
			TokenEnv: "MOUNT_TOKEN",
		}},
		Getenv: func(name string) string {
			if name == "MOUNT_TOKEN" {
				return "local-secret"
			}
			return ""
		},
	}

	conn, err := connector.Dial(context.Background(), agent.Config{Enabled: true, URL: tunnelHTTP.URL, Token: "agw_x"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	waitFor(t, func() bool { return activated.Load() == 1 })
	waitFor(t, func() bool { _, ok := server.Registry().Get(nodeID); return ok })

	reporter, ok := conn.(agent.MountReporter)
	if !ok {
		t.Fatalf("connection does not report mount status")
	}
	mounts := reporter.Mounts()
	if len(mounts) != 1 || mounts[0].Slug != slug || mounts[0].PublicURL != "https://gw.example.com/m/"+slug+"/mcp" {
		t.Fatalf("ack mounts = %+v", mounts)
	}
	if routes, _ := preparedRoutes.Load().([]string); len(routes) != 1 || routes[0] != "/mcp" {
		t.Fatalf("prepare routes = %v", routes)
	}

	// Mount entry: the gateway writes the slug (the caller never controls it), the
	// agent resolves upstream and injects the local bearer token.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.ServeProxyMount(w, r, nodeID, r.URL.Path, slug)
	}))
	t.Cleanup(proxy.Close)

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// A forged header must be overwritten by the gateway.
	req.Header.Set("X-AICLI-Mount", "forged-slug")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("mount request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("body = %s", body)
	}
	select {
	case seen := <-upstreamCalls:
		if seen.URL.Path != "/mcp" {
			t.Fatalf("upstream path = %q", seen.URL.Path)
		}
		if got := seen.Header.Get("X-AICLI-Mount"); got != "" {
			t.Fatalf("internal mount header leaked upstream: %q", got)
		}
		if got := seen.Header.Get("X-AICLI-Token"); got != "" {
			t.Fatalf("local write token leaked to a mount: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("upstream never received the request")
	}

	// A default-target request that forges X-AICLI-Mount must reach the default
	// upstream, never the mount: the gateway strips caller-supplied internal
	// headers and only ServeProxyMount can set them.
	defaultReq, _ := http.NewRequest(http.MethodPost, defaultProxyURL(t, server, nodeID)+"/mcp", strings.NewReader(`{}`))
	defaultReq.Header.Set("X-AICLI-Mount", slug)
	defaultResp, err := http.DefaultClient.Do(defaultReq)
	if err != nil {
		t.Fatalf("default request: %v", err)
	}
	defaultBody, _ := io.ReadAll(defaultResp.Body)
	_ = defaultResp.Body.Close()
	if defaultResp.StatusCode != http.StatusOK || string(defaultBody) != "default-ok" {
		t.Fatalf("default target = %d %s, want 200 default-ok", defaultResp.StatusCode, defaultBody)
	}
	select {
	case seen := <-defaultCalls:
		if got := seen.Header.Get("X-AICLI-Mount"); got != "" {
			t.Fatalf("forged mount header reached the default upstream: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("default upstream was never called")
	}

	// Unknown mount fails closed at the agent (reset), so the gateway surfaces
	// 502 — it must never fall back to another local service.
	badProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.ServeProxyMount(w, r, nodeID, r.URL.Path, "revoked-slug")
	}))
	t.Cleanup(badProxy.Close)
	badReq, _ := http.NewRequest(http.MethodPost, badProxy.URL+"/mcp", strings.NewReader(`{}`))
	badResp, err := http.DefaultClient.Do(badReq)
	if err != nil {
		t.Fatalf("unknown mount request: %v", err)
	}
	defer badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadGateway {
		t.Fatalf("unknown mount status = %d, want 502", badResp.StatusCode)
	}

	_ = conn.Close()
	waitFor(t, func() bool { return deactivated.Load() == 1 })
}

// defaultProxyURL exposes the default (non-mount) proxy entry for one node.
func defaultProxyURL(t *testing.T, server *tunnel.Server, nodeID string) string {
	t.Helper()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.ServeProxy(w, r, nodeID, r.URL.Path)
	}))
	t.Cleanup(proxy.Close)
	return proxy.URL
}
