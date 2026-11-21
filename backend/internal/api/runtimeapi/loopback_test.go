package runtimeapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// TestRequestRemoteIPIgnoresSpoofedForwardedHeaders 覆盖 Fix-S1：转发头
// （X-Forwarded-For / X-Real-IP）只在直连对端（RemoteAddr）本身是环回时才被
// 采纳；远程直连伪造 127.0.0.1 不得把自己伪装成本机来源。
func TestRequestRemoteIPIgnoresSpoofedForwardedHeaders(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		wantIP     string
	}{
		{
			name:       "远程直连伪造 XFF=127.0.0.1 被拒绝",
			remoteAddr: "203.0.113.9:40000",
			headers:    map[string]string{"X-Forwarded-For": "127.0.0.1"},
			wantIP:     "203.0.113.9",
		},
		{
			name:       "环回直连 + XFF=127.0.0.1（本机反代）识别为环回",
			remoteAddr: "127.0.0.1:40000",
			headers:    map[string]string{"X-Forwarded-For": "127.0.0.1"},
			wantIP:     "127.0.0.1",
		},
		{
			name:       "远程直连且无转发头",
			remoteAddr: "203.0.113.9:40000",
			wantIP:     "203.0.113.9",
		},
		{
			name:       "远程直连伪造 X-Real-IP=127.0.0.1 被拒绝",
			remoteAddr: "203.0.113.9:40000",
			headers:    map[string]string{"X-Real-IP": "127.0.0.1"},
			wantIP:     "203.0.113.9",
		},
		{
			name:       "环回直连取 XFF 第一跳",
			remoteAddr: "127.0.0.1:40000",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.7, 127.0.0.1"},
			wantIP:     "203.0.113.7",
		},
		{
			name:       "环回直连且无转发头",
			remoteAddr: "127.0.0.1:40000",
			wantIP:     "127.0.0.1",
		},
		{
			name:       "IPv6 环回直连",
			remoteAddr: "[::1]:40000",
			wantIP:     "::1",
		},
		{
			name:       "远程直连伪造 XFF=::1 被拒绝",
			remoteAddr: "203.0.113.9:40000",
			headers:    map[string]string{"X-Forwarded-For": "::1"},
			wantIP:     "203.0.113.9",
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
		})
	}
}

// TestAuthorizeConnectorAdminRequiresCredentialDespiteLoopbackSource 覆盖 legacy
// （显式开启）模式的两层边界：远程直连伪造 XFF=127.0.0.1 不能伪造来源身份；
// 真正的环回直连也不再获得隐式管理权限，必须携带有效 admin token。
func TestAuthorizeConnectorAdminRequiresCredentialDespiteLoopbackSource(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetAdminAuthRequired(true)
	handler.SetAdminToken(runtimeAdminTestToken)

	remoteReq := httptest.NewRequest(http.MethodGet, "/api/connectors", nil)
	remoteReq.RemoteAddr = "203.0.113.9:40000"
	remoteReq.Header.Set("X-Forwarded-For", "127.0.0.1")
	if err := handler.AuthorizeConnectorAdmin(remoteReq); err == nil {
		t.Fatal("remote request with spoofed loopback headers must not pass connector admin authorization")
	}

	loopbackReq := httptest.NewRequest(http.MethodGet, "/api/connectors", nil)
	loopbackReq.RemoteAddr = "127.0.0.1:40000"
	loopbackReq.Header.Set("X-Forwarded-For", "127.0.0.1")
	if err := handler.AuthorizeConnectorAdmin(loopbackReq); err == nil {
		t.Fatal("loopback source without a credential must not pass connector admin authorization")
	}

	authorizedReq := httptest.NewRequest(http.MethodGet, "/api/connectors", nil)
	authorizedReq.RemoteAddr = "127.0.0.1:40000"
	authorizedReq.Header.Set("X-Forwarded-For", "127.0.0.1")
	addRuntimeAdminTestToken(authorizedReq)
	if err := handler.AuthorizeConnectorAdmin(authorizedReq); err != nil {
		t.Fatalf("loopback request with a valid admin token should pass: %v", err)
	}
}
