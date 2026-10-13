package runtimeapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// TestIsLoopbackRequestIgnoresSpoofedForwardedHeaders 覆盖 Fix-S1：转发头
// （X-Forwarded-For / X-Real-IP）只在直连对端（RemoteAddr）本身是环回时才被
// 采纳；远程直连伪造 127.0.0.1 不得把自己伪装成环回请求。
func TestIsLoopbackRequestIgnoresSpoofedForwardedHeaders(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		wantIP     string
		wantLoop   bool
	}{
		{
			name:       "远程直连伪造 XFF=127.0.0.1 被拒绝",
			remoteAddr: "203.0.113.9:40000",
			headers:    map[string]string{"X-Forwarded-For": "127.0.0.1"},
			wantIP:     "203.0.113.9",
			wantLoop:   false,
		},
		{
			name:       "环回直连 + XFF=127.0.0.1（本机反代）保持放行",
			remoteAddr: "127.0.0.1:40000",
			headers:    map[string]string{"X-Forwarded-For": "127.0.0.1"},
			wantIP:     "127.0.0.1",
			wantLoop:   true,
		},
		{
			name:       "远程直连且无转发头",
			remoteAddr: "203.0.113.9:40000",
			wantIP:     "203.0.113.9",
			wantLoop:   false,
		},
		{
			name:       "远程直连伪造 X-Real-IP=127.0.0.1 被拒绝",
			remoteAddr: "203.0.113.9:40000",
			headers:    map[string]string{"X-Real-IP": "127.0.0.1"},
			wantIP:     "203.0.113.9",
			wantLoop:   false,
		},
		{
			name:       "环回直连取 XFF 第一跳",
			remoteAddr: "127.0.0.1:40000",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.7, 127.0.0.1"},
			wantIP:     "203.0.113.7",
			wantLoop:   false,
		},
		{
			name:       "环回直连且无转发头",
			remoteAddr: "127.0.0.1:40000",
			wantIP:     "127.0.0.1",
			wantLoop:   true,
		},
		{
			name:       "IPv6 环回直连",
			remoteAddr: "[::1]:40000",
			wantIP:     "::1",
			wantLoop:   true,
		},
		{
			name:       "远程直连伪造 XFF=::1 被拒绝",
			remoteAddr: "203.0.113.9:40000",
			headers:    map[string]string{"X-Forwarded-For": "::1"},
			wantIP:     "203.0.113.9",
			wantLoop:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/runtime/status", nil)
			req.RemoteAddr = tc.remoteAddr
			for key, value := range tc.headers {
				req.Header.Set(key, value)
			}
			if got := requestRemoteIP(req); got != tc.wantIP {
				t.Fatalf("requestRemoteIP = %q, want %q", got, tc.wantIP)
			}
			if got := isLoopbackRequest(req); got != tc.wantLoop {
				t.Fatalf("isLoopbackRequest = %v, want %v", got, tc.wantLoop)
			}
		})
	}
}

// TestAuthorizeConnectorAdminRejectsSpoofedLoopback 覆盖 Fix-S1 的端到端影响：
// 远程直连伪造 XFF=127.0.0.1 不能获得连接器管理面（与 skills 管理面同源的
// 环回放行口径）的放行；真正的环回直连仍放行。
func TestAuthorizeConnectorAdminRejectsSpoofedLoopback(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)

	remoteReq := httptest.NewRequest(http.MethodGet, "/api/connectors", nil)
	remoteReq.RemoteAddr = "203.0.113.9:40000"
	remoteReq.Header.Set("X-Forwarded-For", "127.0.0.1")
	if err := handler.AuthorizeConnectorAdmin(remoteReq); err == nil {
		t.Fatal("远程直连伪造环回不得通过连接器管理面鉴权")
	}

	localReq := httptest.NewRequest(http.MethodGet, "/api/connectors", nil)
	localReq.RemoteAddr = "127.0.0.1:40000"
	localReq.Header.Set("X-Forwarded-For", "127.0.0.1")
	if err := handler.AuthorizeConnectorAdmin(localReq); err != nil {
		t.Fatalf("环回直连应放行连接器管理面鉴权: %v", err)
	}
}
