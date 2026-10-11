package tunnel_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// startProxy exposes the tunnel's ServeProxy for a fixed node id so the test can
// exercise the full gateway → tunnel → agent → local-service path.
func startProxy(t *testing.T, server *tunnel.Server, nodeID string) *httptest.Server {
	t.Helper()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.ServeProxy(w, r, nodeID, r.URL.Path)
	}))
	t.Cleanup(proxy.Close)
	return proxy
}

func dialAgent(t *testing.T, server *tunnel.Server, nodeID, localBaseURL string) *agent.WSConnector {
	t.Helper()
	tunnelHTTP := httptest.NewServer(http.HandlerFunc(server.ServeHTTP))
	t.Cleanup(tunnelHTTP.Close)

	connector := testConnector(nodeID)
	connector.Forward = agent.ForwardConfig{LocalBaseURL: localBaseURL}
	conn, err := connector.Dial(context.Background(), agent.Config{Enabled: true, URL: tunnelHTTP.URL, Token: "agw_x"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	waitFor(t, func() bool { _, ok := server.Registry().Get(nodeID); return ok })
	return connector
}

func TestProxyRoundTrip(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Local", "yes")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "hello from local")
	}))
	t.Cleanup(upstream.Close)

	server := tunnel.NewServer(tunnel.Config{ServerVersion: "test", PublicBaseURL: "https://gw.example.com"})
	dialAgent(t, server, "node-e2e", upstream.URL)

	proxy := startProxy(t, server, "node-e2e")
	resp, err := http.Get(proxy.URL + "/hello?x=1")
	if err != nil {
		t.Fatalf("proxy get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}
	if string(body) != "hello from local" {
		t.Fatalf("body = %q", body)
	}
	if resp.Header.Get("X-Local") != "yes" {
		t.Fatalf("missing forwarded header X-Local: %v", resp.Header)
	}
}

func TestProxyForwardsPostBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, r.Method+":"+string(body))
	}))
	t.Cleanup(upstream.Close)

	server := tunnel.NewServer(tunnel.Config{ServerVersion: "test"})
	dialAgent(t, server, "node-post", upstream.URL)

	proxy := startProxy(t, server, "node-post")
	resp, err := http.Post(proxy.URL+"/echo", "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("proxy post: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "POST:payload" {
		t.Fatalf("body = %q, want POST:payload", body)
	}
}

func TestProxyNodeOffline(t *testing.T) {
	server := tunnel.NewServer(tunnel.Config{ServerVersion: "test"})
	proxy := startProxy(t, server, "missing-node")
	resp, err := http.Get(proxy.URL + "/anything")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}

func TestProxyForwardsPrefixHeader(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-Prefix", r.Header.Get("X-Forwarded-Prefix"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	server := tunnel.NewServer(tunnel.Config{ServerVersion: "test"})
	dialAgent(t, server, "node-prefix", upstream.URL)

	proxy := startProxy(t, server, "node-prefix")
	resp, err := http.Get(proxy.URL + "/x")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Seen-Prefix"); got != "/n/node-prefix" {
		t.Fatalf("upstream saw X-Forwarded-Prefix = %q, want /n/node-prefix", got)
	}
}

func testRegisterFunc(nodeID string) func() wire.Register {
	return func() wire.Register {
		return wire.Register{NodeID: nodeID, Kind: "aicli", DeviceName: "laptop", Routes: []string{"/"}}
	}
}

func TestProxyRouteNotAllowed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "should not reach")
	}))
	t.Cleanup(upstream.Close)

	server := tunnel.NewServer(tunnel.Config{ServerVersion: "test"})
	tunnelHTTP := httptest.NewServer(http.HandlerFunc(server.ServeHTTP))
	t.Cleanup(tunnelHTTP.Close)

	connector := testConnector("node-routes")
	connector.Register = func() wire.Register {
		return wire.Register{NodeID: "node-routes", Kind: "aicli", Routes: []string{"/web"}}
	}
	connector.Forward = agent.ForwardConfig{LocalBaseURL: upstream.URL}
	conn, err := connector.Dial(context.Background(), agent.Config{Enabled: true, URL: tunnelHTTP.URL, Token: "agw_x"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	waitFor(t, func() bool { _, ok := server.Registry().Get("node-routes"); return ok })

	proxy := startProxy(t, server, "node-routes")
	resp, err := http.Get(proxy.URL + "/api/runtime/secret")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestProxyEmptyRoutesDenied(t *testing.T) {
	server := tunnel.NewServer(tunnel.Config{ServerVersion: "test"})
	tunnelHTTP := httptest.NewServer(http.HandlerFunc(server.ServeHTTP))
	t.Cleanup(tunnelHTTP.Close)

	connector := testConnector("node-noroute")
	connector.Register = func() wire.Register {
		return wire.Register{NodeID: "node-noroute", Kind: "aicli"}
	}
	conn, err := connector.Dial(context.Background(), agent.Config{Enabled: true, URL: tunnelHTTP.URL, Token: "agw_x"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	waitFor(t, func() bool { _, ok := server.Registry().Get("node-noroute"); return ok })

	proxy := startProxy(t, server, "node-noroute")
	resp, err := http.Get(proxy.URL + "/anything")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (empty allowlist is fail-closed)", resp.StatusCode)
	}
}

// TestProxyThroughControllerForward validates that the controller's
// ForwardProvider wiring (SetForward before dial) reaches the local service.
func TestProxyThroughControllerForward(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "via controller")
	}))
	t.Cleanup(upstream.Close)

	server := tunnel.NewServer(tunnel.Config{ServerVersion: "test"})
	tunnelHTTP := httptest.NewServer(http.HandlerFunc(server.ServeHTTP))
	t.Cleanup(tunnelHTTP.Close)

	controller := agent.NewController(agent.Config{Enabled: true, URL: tunnelHTTP.URL, Token: "agw_x"}, testRegisterFunc("node-ctl"))
	controller.SetForward(agent.ForwardConfig{LocalBaseURL: upstream.URL})
	if err := controller.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = controller.Disconnect(context.Background()) })
	waitFor(t, func() bool { _, ok := server.Registry().Get("node-ctl"); return ok })

	proxy := startProxy(t, server, "node-ctl")
	resp, err := http.Get(proxy.URL + "/x")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "via controller" {
		t.Fatalf("body = %q, want via controller", body)
	}
}

func TestSessionUpdateReconcile(t *testing.T) {
	server := tunnel.NewServer(tunnel.Config{ServerVersion: "test"})
	tunnelHTTP := httptest.NewServer(http.HandlerFunc(server.ServeHTTP))
	t.Cleanup(tunnelHTTP.Close)

	controller := agent.NewController(agent.Config{Enabled: true, URL: tunnelHTTP.URL, Token: "agw_x"}, testRegisterFunc("node-sess"))
	if err := controller.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = controller.Disconnect(context.Background()) })
	waitFor(t, func() bool { _, ok := server.Registry().Get("node-sess"); return ok })

	controller.UpdateSessions([]wire.SessionSummary{
		{SessionID: "s1", Title: "one"},
		{SessionID: "s2", Title: "two"},
	})
	waitFor(t, func() bool {
		node, ok := server.Registry().Get("node-sess")
		return ok && len(node.Sessions) == 2
	})
}
