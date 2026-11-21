package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

func TestShouldInjectAnalyticsAdminTokenAllowlist(t *testing.T) {
	allowed := []string{
		"/api/runtime/analytics/sessions",
		"/api/runtime/analytics/overview",
		"/api/runtime/analytics/summary",
		"/api/runtime/analytics/dimensions",
		"/api/runtime/analytics/requests",
		"/api/runtime/analytics/sessions/session-1",
		"/api/runtime/analytics/sessions/session-1/usage",
		"/api/runtime/analytics/sessions/session-1/turns",
		"/api/runtime/analytics/tools",
		"/api/runtime/analytics/tools/tool-1",
		"/api/runtime/analytics/subagents",
		"/api/runtime/analytics/errors",
		"/api/runtime/analytics/routing",
		"/api/runtime/analytics/routing/events",
		"/api/runtime/analytics/lsp/baseline",
		"/api/runtime/analytics/overview?from=2026-01-01&to=2026-01-31",
	}
	for _, path := range allowed {
		if !shouldInjectAnalyticsAdminToken(http.MethodGet, path) {
			t.Errorf("GET %q was not allowed", path)
		}
	}
	rejected := []struct {
		method string
		path   string
	}{
		{http.MethodHead, "/api/runtime/analytics/overview"},
		{http.MethodPost, "/api/runtime/analytics/overview"},
		{http.MethodPut, "/api/runtime/analytics/sessions/session-1"},
		{http.MethodGet, "/api/runtime/analyticsx/overview"},
		{http.MethodGet, "/api/runtime/analytics"},
		{http.MethodGet, "/api/runtime/analytics/sessions/session-1/delete"},
		{http.MethodGet, "/api/runtime/analytics/sessions//usage"},
		{http.MethodGet, "/api/runtime/analytics/sessions/session-1/"},
		{http.MethodGet, "/api/runtime/analytics//overview"},
		{http.MethodGet, "/api/runtime/analytics/sessions/../skills"},
		{http.MethodGet, "/api/runtime/analytics/sessions/%2e%2e/skills"},
		{http.MethodGet, "/api/runtime/analytics/sessions/session%2f1"},
		{http.MethodGet, "/api/runtime/analytics/overview#fragment"},
	}
	for _, tc := range rejected {
		if shouldInjectAnalyticsAdminToken(tc.method, tc.path) {
			t.Errorf("%s %q was unexpectedly allowed", tc.method, tc.path)
		}
	}
}

func TestForwardAnalyticsAdminTokenIsLocalAndRouteScoped(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		path           string
		mount          string
		wantAdminToken string
		wantLocalToken string
	}{
		{
			name:           "analytics read",
			method:         http.MethodGet,
			path:           "/api/runtime/analytics/overview",
			wantAdminToken: "node-admin-secret",
			wantLocalToken: "node-write-secret",
		},
		{
			name:           "other runtime route",
			method:         http.MethodGet,
			path:           "/api/runtime/skills",
			wantLocalToken: "node-write-secret",
		},
		{
			name:   "mount target",
			method: http.MethodGet,
			path:   "/api/runtime/analytics/overview",
			mount:  "mcp-mount",
		},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			type observedRequest struct {
				adminToken   string
				accessToken  string
				gatewayToken string
				writeToken   string
				auth         string
			}
			observed := make(chan observedRequest, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				observed <- observedRequest{
					adminToken:   r.Header.Get("X-Skills-Admin-Token"),
					accessToken:  r.Header.Get("X-AICLI-Access-Token"),
					gatewayToken: r.Header.Get("X-AICLI-Gateway-Token"),
					writeToken:   r.Header.Get("X-AICLI-Token"),
					auth:         r.Header.Get("Authorization"),
				}
				// An accidental upstream echo must not expose node-local
				// credentials in response headers sent back through the tunnel.
				w.Header().Set("X-Skills-Admin-Token", "echoed-admin-secret")
				w.Header().Set("X-AICLI-Access-Token", "echoed-access-secret")
				w.Header().Set("X-AICLI-Gateway-Token", "echoed-gateway-secret")
				w.Header().Set("X-AICLI-Token", "echoed-write-secret")
				w.WriteHeader(http.StatusNoContent)
			}))
			defer upstream.Close()

			clientHeaders := map[string][]string{
				"Authorization":         {"Bearer caller-token"},
				"X-Skills-Admin-Token":  {"caller-admin-token"},
				"X-AICLI-Access-Token":  {"caller-access-token"},
				"X-AICLI-Gateway-Token": {"caller-gateway-token"},
				"X-AICLI-Token":         {"caller-write-token"},
			}
			open := wire.StreamOpen{
				Method:  tc.method,
				Path:    tc.path,
				Headers: clientHeaders,
			}
			target := streamTarget{
				baseURL: upstream.URL,
				path:    tc.path,
				mount:   tc.mount,
			}
			frames := forwardTestStream(t, idForTest(i), ForwardConfig{
				LocalBaseURL:        upstream.URL,
				LocalToken:          "node-write-secret",
				AnalyticsAdminToken: "node-admin-secret",
				GatewayToken:        "node-gateway-secret",
			}, open, target)

			select {
			case got := <-observed:
				if got.adminToken != tc.wantAdminToken {
					t.Errorf("upstream X-Skills-Admin-Token = %q, want %q", got.adminToken, tc.wantAdminToken)
				}
				if got.writeToken != tc.wantLocalToken {
					t.Errorf("upstream X-AICLI-Token = %q, want %q", got.writeToken, tc.wantLocalToken)
				}
				if got.auth != "" {
					t.Errorf("caller Authorization reached upstream: %q", got.auth)
				}
				if got.accessToken != "" {
					t.Errorf("caller X-AICLI-Access-Token reached upstream: %q", got.accessToken)
				}
				if tc.mount == "" && got.gatewayToken != "node-gateway-secret" {
					t.Errorf("injected X-AICLI-Gateway-Token = %q, want node-gateway-secret", got.gatewayToken)
				}
				if tc.mount != "" && got.gatewayToken != "" {
					t.Errorf("mount request received gateway trust token: %q", got.gatewayToken)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("upstream did not receive the forwarded request")
			}

			if len(frames) < 2 || frames[0].Type != wire.TypeStreamData {
				t.Fatalf("forwarded frames = %+v, want response headers followed by stream end", frames)
			}
			headers, err := wire.DecodePayload[wire.ResponseHeaders](frames[0])
			if err != nil {
				t.Fatalf("decode response headers: %v", err)
			}
			for _, secretHeader := range []string{"X-Skills-Admin-Token", "X-AICLI-Access-Token", "X-AICLI-Gateway-Token", "X-AICLI-Token"} {
				for name := range headers.Headers {
					if strings.EqualFold(name, secretHeader) {
						t.Errorf("node-local credential header %s was returned through the tunnel", name)
					}
				}
			}
		})
	}
}

func TestForwardAnalyticsAdminTokenDoesNotFollowRedirect(t *testing.T) {
	redirected := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			redirected <- struct{}{}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, "/redirected", http.StatusFound)
	}))
	defer upstream.Close()

	path := "/api/runtime/analytics/overview"
	frames := forwardTestStream(t, 99, ForwardConfig{
		LocalBaseURL:        upstream.URL,
		AnalyticsAdminToken: "node-admin-secret",
	}, wire.StreamOpen{Method: http.MethodGet, Path: path}, streamTarget{
		baseURL: upstream.URL,
		path:    path,
	})
	if len(frames) < 2 || frames[0].Type != wire.TypeStreamData {
		t.Fatalf("forwarded frames = %+v, want redirect response headers and stream end", frames)
	}
	headers, err := wire.DecodePayload[wire.ResponseHeaders](frames[0])
	if err != nil {
		t.Fatalf("decode response headers: %v", err)
	}
	if headers.Status != http.StatusFound {
		t.Fatalf("response status = %d, want %d", headers.Status, http.StatusFound)
	}
	select {
	case <-redirected:
		t.Fatal("analytics request followed a redirect")
	case <-time.After(100 * time.Millisecond):
	}
}

func idForTest(i int) uint32 { return uint32(i + 1) }

func forwardTestStream(t *testing.T, id uint32, cfg ForwardConfig, open wire.StreamOpen, target streamTarget) []wire.Frame {
	t.Helper()
	frameCh := make(chan wire.Frame, 8)
	upgrader := websocket.Upgrader{}
	tunnelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverConn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer serverConn.Close()
		for {
			_, message, err := serverConn.ReadMessage()
			if err != nil {
				return
			}
			frame, err := wire.Decode(message)
			if err != nil {
				return
			}
			frameCh <- frame
			if frame.Type == wire.TypeStreamEnd || frame.Type == wire.TypeStreamReset {
				return
			}
		}
	}))
	defer tunnelServer.Close()

	wsURL := "ws" + strings.TrimPrefix(tunnelServer.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial test tunnel: %v", err)
	}
	defer ws.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn := &wsConn{
		ws:         ws,
		forward:    cfg,
		streams:    make(map[uint32]*inboundStream),
		sendCredit: map[uint32]int64{id: 1 << 20},
		flowSignal: make(chan struct{}, 1),
	}
	conn.forwardStream(id, &inboundStream{open: open, target: target, ctx: ctx})

	frames := make([]wire.Frame, 0, 3)
	for {
		select {
		case frame := <-frameCh:
			frames = append(frames, frame)
			if frame.Type == wire.TypeStreamEnd || frame.Type == wire.TypeStreamReset {
				return frames
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for forwarded stream frames")
			return nil
		}
	}
}
