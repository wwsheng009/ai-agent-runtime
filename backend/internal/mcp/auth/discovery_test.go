package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseWWWAuthenticate(t *testing.T) {
	challenge := parseWWWAuthenticate(`Bearer resource_metadata="https://auth.example.com/.well-known/oauth-protected-resource/mcp", scope="read write"`)
	if challenge == nil {
		t.Fatal("应解析出 Bearer 挑战")
	}
	if challenge.resourceMetadata != "https://auth.example.com/.well-known/oauth-protected-resource/mcp" {
		t.Fatalf("resource_metadata = %q", challenge.resourceMetadata)
	}
	if challenge.scope != "read write" {
		t.Fatalf("scope = %q", challenge.scope)
	}

	if parseWWWAuthenticate(`Basic realm="x"`) != nil {
		t.Fatal("非 Bearer 挑战应被忽略")
	}
	bare := parseWWWAuthenticate("Bearer")
	if bare == nil || bare.resourceMetadata != "" {
		t.Fatalf("裸 Bearer 也应返回空挑战: %#v", bare)
	}
	// 引号内的逗号不能被当作参数分隔符。
	quoted := parseWWWAuthenticate(`Bearer error="invalid_token", scope="a,b c"`)
	if quoted == nil || quoted.scope != "a,b c" {
		t.Fatalf("引号内逗号解析错误: %#v", quoted)
	}
}

func TestWellKnownCandidates(t *testing.T) {
	resourceCandidates := protectedResourceWellKnown("https://mcp.example.com/mcp")
	if len(resourceCandidates) != 2 ||
		resourceCandidates[0] != "https://mcp.example.com/.well-known/oauth-protected-resource/mcp" ||
		resourceCandidates[1] != "https://mcp.example.com/.well-known/oauth-protected-resource" {
		t.Fatalf("受保护资源候选地址 = %#v", resourceCandidates)
	}

	asCandidates := authServerWellKnown("https://auth.example.com/tenant1")
	expected := "https://auth.example.com/.well-known/oauth-authorization-server/tenant1"
	if len(asCandidates) == 0 || asCandidates[0] != expected {
		t.Fatalf("授权服务器候选地址 = %#v", asCandidates)
	}
	found := false
	for _, candidate := range asCandidates {
		if strings.HasSuffix(candidate, "/.well-known/openid-configuration") {
			found = true
		}
	}
	if !found {
		t.Fatalf("应包含 OIDC 回退候选: %#v", asCandidates)
	}
}

func TestDiscoverViaChallengeAndMetadata(t *testing.T) {
	mock := newMockOAuthServer(t)
	discovery, err := Discover(context.Background(), mock.server.Client(), mock.mcpURL(), "")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if discovery.AuthServer == nil {
		t.Fatal("缺少授权服务器 metadata")
	}
	if discovery.AuthServer.TokenEndpoint != mock.baseURL()+"/token" {
		t.Fatalf("token_endpoint = %q", discovery.AuthServer.TokenEndpoint)
	}
	if discovery.AuthServer.RegistrationEndpoint != mock.baseURL()+"/register" {
		t.Fatalf("registration_endpoint = %q", discovery.AuthServer.RegistrationEndpoint)
	}
	if discovery.ChallengeScope != "read write" {
		t.Fatalf("challenge scope = %q", discovery.ChallengeScope)
	}
	if len(discovery.ScopesSupported) < 2 {
		t.Fatalf("ScopesSupported = %#v（应合并资源与授权服务器）", discovery.ScopesSupported)
	}
	if discovery.Resource == nil || discovery.Resource.Resource != mock.mcpURL() {
		t.Fatalf("Resource = %#v", discovery.Resource)
	}
}

func TestDiscoverWithoutMetadataFallsBackToManualHint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := Discover(context.Background(), server.Client(), server.URL+"/mcp", "")
	if err == nil {
		t.Fatal("没有任何 metadata 时应当报错")
	}
	if !strings.Contains(err.Error(), "headers") {
		t.Fatalf("错误信息应给出 headers 手动兜底提示: %v", err)
	}
}

func TestDiscoverHonoursAuthorizationServerOverride(t *testing.T) {
	mock := newMockOAuthServer(t)
	discovery, err := Discover(context.Background(), mock.server.Client(), mock.mcpURL(), mock.baseURL())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if discovery.AuthServerURL != mock.baseURL() {
		t.Fatalf("AuthServerURL 应为 issuer（供刷新复用）: %q", discovery.AuthServerURL)
	}
}
