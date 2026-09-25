package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

// mockOAuthServer 是一个最小可用的 OAuth 2.0 授权服务器 + 受保护 MCP 端点，
// 用于端到端验证发现、动态注册、PKCE 校验、令牌交换与刷新。
type mockOAuthServer struct {
	t      *testing.T
	server *httptest.Server

	mu             sync.Mutex
	registerCalls  int
	authorizeCalls int
	tokenCalls     int
	refreshCalls   int
	lastVerifier   string
	codes          map[string]string // code → code_challenge
	accessTokens   map[string]bool
	refreshTokens  map[string]bool
	issued         int
	accessTTL      int
}

func newMockOAuthServer(t *testing.T) *mockOAuthServer {
	t.Helper()
	mock := &mockOAuthServer{
		t:             t,
		codes:         map[string]string{},
		accessTokens:  map[string]bool{},
		refreshTokens: map[string]bool{},
		accessTTL:     3600,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", mock.handleMCP)
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", mock.handleResourceMetadata)
	mux.HandleFunc("/.well-known/oauth-authorization-server", mock.handleAuthServerMetadata)
	mux.HandleFunc("/register", mock.handleRegister)
	mux.HandleFunc("/authorize", mock.handleAuthorize)
	mux.HandleFunc("/token", mock.handleToken)
	mock.server = httptest.NewServer(mux)
	t.Cleanup(mock.server.Close)
	return mock
}

func (m *mockOAuthServer) baseURL() string { return m.server.URL }
func (m *mockOAuthServer) mcpURL() string  { return m.server.URL + "/mcp" }

func (m *mockOAuthServer) handleMCP(w http.ResponseWriter, r *http.Request) {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		w.Header().Set("WWW-Authenticate",
			fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp", scope="read write"`, m.baseURL()))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	m.mu.Lock()
	valid := m.accessTokens[strings.TrimPrefix(auth, prefix)]
	m.mu.Unlock()
	if !valid {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","serverInfo":{"name":"mock","version":"1"}}}`)
}

func (m *mockOAuthServer) handleResourceMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]interface{}{
		"resource":                 m.mcpURL(),
		"authorization_servers":    []string{m.baseURL()},
		"scopes_supported":         []string{"read", "write"},
		"bearer_methods_supported": []string{"header"},
	})
}

func (m *mockOAuthServer) handleAuthServerMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]interface{}{
		"issuer":                                m.baseURL(),
		"authorization_endpoint":                m.baseURL() + "/authorize",
		"token_endpoint":                        m.baseURL() + "/token",
		"registration_endpoint":                 m.baseURL() + "/register",
		"scopes_supported":                      []string{"read", "write"},
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
	})
}

func (m *mockOAuthServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.registerCalls++
	m.mu.Unlock()
	var payload map[string]interface{}
	_ = json.NewDecoder(r.Body).Decode(&payload)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]interface{}{
		"client_id":     "dcr-client",
		"redirect_uris": payload["redirect_uris"],
	})
}

func (m *mockOAuthServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	m.mu.Lock()
	m.authorizeCalls++
	m.mu.Unlock()

	redirectURI := query.Get("redirect_uri")
	state := query.Get("state")
	challenge := query.Get("code_challenge")
	if redirectURI == "" || query.Get("client_id") == "" || challenge == "" {
		http.Error(w, "缺少 client_id/redirect_uri/code_challenge", http.StatusBadRequest)
		return
	}

	m.mu.Lock()
	m.issued++
	code := fmt.Sprintf("code-%d", m.issued)
	m.codes[code] = challenge
	m.mu.Unlock()

	location := redirectURI + "?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state)
	http.Redirect(w, r, location, http.StatusFound)
}

func (m *mockOAuthServer) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	m.tokenCalls++
	m.mu.Unlock()

	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		code := r.PostForm.Get("code")
		verifier := r.PostForm.Get("code_verifier")
		m.mu.Lock()
		challenge, ok := m.codes[code]
		m.lastVerifier = verifier
		m.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]string{"error": "invalid_grant"})
			return
		}
		if ChallengeS256(verifier) != challenge {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]string{"error": "invalid_grant", "error_description": "code_verifier 不匹配"})
			return
		}
		m.issueTokens(w)
	case "refresh_token":
		refresh := r.PostForm.Get("refresh_token")
		m.mu.Lock()
		m.refreshCalls++
		ok := m.refreshTokens[refresh]
		m.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]string{"error": "invalid_grant"})
			return
		}
		m.issueTokens(w)
	default:
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": "unsupported_grant_type"})
	}
}

// issueTokens 发放一对新令牌（access 每次递增，便于断言刷新真的发生）。
func (m *mockOAuthServer) issueTokens(w http.ResponseWriter) {
	m.mu.Lock()
	m.issued++
	access := fmt.Sprintf("access-%d", m.issued)
	refresh := fmt.Sprintf("refresh-%d", m.issued)
	m.accessTokens[access] = true
	m.refreshTokens[refresh] = true
	ttl := m.accessTTL
	m.mu.Unlock()

	writeJSON(w, map[string]interface{}{
		"access_token":  access,
		"refresh_token": refresh,
		"token_type":    "Bearer",
		"expires_in":    ttl,
		"scope":         "read",
	})
}

func (m *mockOAuthServer) counters() (register, authorize, token, refresh int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.registerCalls, m.authorizeCalls, m.tokenCalls, m.refreshCalls
}

// hasAccessToken 判断 mock 记录的令牌集合里是否存在该值。
func (m *mockOAuthServer) hasAccessToken(token string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.accessTokens[token]
}

func writeJSON(w http.ResponseWriter, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func testAuthConfig() config.MCPAuthConfig {
	return config.MCPAuthConfig{Type: "oauth"}
}
