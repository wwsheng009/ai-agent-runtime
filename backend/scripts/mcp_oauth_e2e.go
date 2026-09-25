// Real end-to-end verification of MCP OAuth 2.0 + PKCE（M2，见
// docs/analysis/commandcode-mcp-design-borrowing-20260925.md §4.4）。
//
// 脚本完全离线：本地起一个最小 OAuth 授权服务器 + 受保护的 Streamable HTTP
// MCP 端点，然后用真实 aicli 二进制完成「发现 → 动态注册 → PKCE 授权 →
// 令牌落盘 → 带令牌连接 → 登出」全链路。
//
// 断言：
//
//  1. `mcp auth --no-browser` 打印可复制的授权 URL 并以 0 退出（无浏览器兜底）；
//  2. 授权服务器校验了 PKCE：code_verifier 的 S256 必须等于 code_challenge；
//  3. 令牌落盘到 AICLI_MCP_TOKENS_FILE，且 `mcp auth --list --output json`
//     只输出元数据，不出现 access/refresh token 明文；
//  4. `mcp list` 显示「已连接」——证明 transport 真的注入了 Bearer 令牌；
//  5. `mcp logout` 后令牌被清除，`mcp list` 回到「需认证」。
//
// Run (from backend/):
//
//	go run ./scripts/mcp_oauth_e2e.go [aicli.exe]
//
//go:build ignore

package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\n✅ MCP OAuth e2e 全部通过")
}

func run() error {
	binary, err := resolveAICLI()
	if err != nil {
		return err
	}
	mock := newMockOAuthMCP()
	defer mock.Close()

	tmp, err := os.MkdirTemp("", "aicli-mcp-oauth-e2e-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	configPath := filepath.Join(tmp, "mcp.yaml")
	tokensPath := filepath.Join(tmp, "mcp-tokens.json")
	config := fmt.Sprintf(`mcpServers:
  oauth-e2e:
    name: oauth-e2e
    type: streamable
    url: %s
    auth: oauth
    enabled: true
    timeout: 15s
global:
  connectTimeout: 10s
`, mock.MCPURL())
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		return err
	}

	env := append(os.Environ(), "AICLI_MCP_TOKENS_FILE="+tokensPath)

	// 1) 未登录时必须显示「需认证」，而不是连接失败。
	out, err := runCLI(env, binary, "mcp", "--config-file", configPath, "list")
	if err != nil {
		return fmt.Errorf("mcp list（未登录）: %v\n%s", err, out)
	}
	if !strings.Contains(out, "需认证") {
		return fmt.Errorf("未登录时应显示需认证，实际输出:\n%s", out)
	}

	// 2) --no-browser 授权：脚本扮演浏览器，打开打印出来的 URL。
	out, err = runCLIAuth(env, binary, configPath)
	if err != nil {
		return fmt.Errorf("mcp auth: %v\n%s", err, out)
	}
	if !strings.Contains(out, "授权成功") {
		return fmt.Errorf("授权未成功:\n%s", out)
	}
	if mock.CodeVerifier() == "" {
		return fmt.Errorf("授权服务器未收到 code_verifier（PKCE 未生效）")
	}
	if mock.Registrations() == 0 {
		return fmt.Errorf("未发生动态客户端注册")
	}
	if _, err := os.Stat(tokensPath); err != nil {
		return fmt.Errorf("令牌未落盘: %v", err)
	}

	// 3) 令牌清单不含明文。
	out, err = runCLI(env, binary, "mcp", "--config-file", configPath, "auth", "--list", "--output", "json")
	if err != nil {
		return fmt.Errorf("mcp auth --list: %v\n%s", err, out)
	}
	for _, secret := range mock.Secrets() {
		if secret != "" && strings.Contains(out, secret) {
			return fmt.Errorf("令牌明文出现在 --list 输出中:\n%s", out)
		}
	}
	if !strings.Contains(out, `"hasRefreshToken":true`) {
		return fmt.Errorf("--list 应报告可自动刷新:\n%s", out)
	}

	// 4) 带令牌连接成功 —— 证明 transport 注入了 Bearer 令牌。
	out, err = runCLI(env, binary, "mcp", "--config-file", configPath, "list")
	if err != nil {
		return fmt.Errorf("mcp list（已登录）: %v\n%s", err, out)
	}
	if !strings.Contains(out, "状态: connected") {
		return fmt.Errorf("已登录后应显示已连接:\n%s", out)
	}
	if mock.AuthorizedRequests() == 0 {
		return fmt.Errorf("受保护端点未收到带 Bearer 令牌的请求")
	}

	// 5) 登出后回到「需认证」，且令牌文件被清空。
	out, err = runCLI(env, binary, "mcp", "--config-file", configPath, "logout", "oauth-e2e")
	if err != nil {
		return fmt.Errorf("mcp logout: %v\n%s", err, out)
	}
	out, err = runCLI(env, binary, "mcp", "--config-file", configPath, "list")
	if err != nil {
		return fmt.Errorf("mcp list（登出后）: %v\n%s", err, out)
	}
	if !strings.Contains(out, "需认证") {
		return fmt.Errorf("登出后应回到需认证:\n%s", out)
	}
	return nil
}

// runCLIAuth 启动 `mcp auth --no-browser`，从 stdout 抓取授权 URL 并扮演浏览器打开它。
func runCLIAuth(env []string, binary, configPath string) (string, error) {
	cmd := exec.Command(binary, "mcp", "--config-file", configPath, "auth", "oauth-e2e", "--no-browser", "--timeout", "30s")
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	cmd.Stderr = cmd.Stdout
	var buf bytes.Buffer
	var mu sync.Mutex
	done := make(chan error, 1)

	if err := cmd.Start(); err != nil {
		return "", err
	}
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			mu.Lock()
			buf.WriteString(line + "\n")
			mu.Unlock()
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
				go openAuthorizeURL(trimmed)
			}
		}
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			return buf.String(), err
		}
		return buf.String(), nil
	case <-time.After(45 * time.Second):
		_ = cmd.Process.Kill()
		return buf.String(), fmt.Errorf("授权超时")
	}
}

func openAuthorizeURL(target string) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(target)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

func runCLI(env []string, binary string, args ...string) (string, error) {
	cmd := exec.Command(binary, args...)
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func resolveAICLI() (string, error) {
	if len(os.Args) > 1 {
		return os.Args[1], nil
	}
	name := "aicli"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(os.TempDir(), "aicli-mcp-oauth-e2e-"+name)
	cmd := exec.Command("go", "build", "-o", target, "./cmd/aicli")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build ./cmd/aicli: %v\n%s", err, out)
	}
	return target, nil
}

// ---- mock OAuth 授权服务器 + 受保护 MCP 端点 ----

type mockOAuthMCP struct {
	server *httptest.Server

	mu            sync.Mutex
	registrations int
	authorized    int
	codes         map[string]string // code → code_challenge
	accessTokens  map[string]bool
	refreshTokens map[string]bool
	lastVerifier  string
	issued        int
	secrets       []string
}

func newMockOAuthMCP() *mockOAuthMCP {
	mock := &mockOAuthMCP{
		codes:         map[string]string{},
		accessTokens:  map[string]bool{},
		refreshTokens: map[string]bool{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", mock.handleMCP)
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", mock.handleResourceMetadata)
	mux.HandleFunc("/.well-known/oauth-authorization-server", mock.handleAuthMetadata)
	mux.HandleFunc("/register", mock.handleRegister)
	mux.HandleFunc("/authorize", mock.handleAuthorize)
	mux.HandleFunc("/token", mock.handleToken)
	mock.server = httptest.NewServer(mux)
	return mock
}

func (m *mockOAuthMCP) Close()         { m.server.Close() }
func (m *mockOAuthMCP) MCPURL() string { return m.server.URL + "/mcp" }

func (m *mockOAuthMCP) handleMCP(w http.ResponseWriter, r *http.Request) {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(auth, "Bearer ") {
		w.Header().Set("WWW-Authenticate",
			fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp", scope="read"`, m.server.URL))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	m.mu.Lock()
	valid := m.accessTokens[strings.TrimPrefix(auth, "Bearer ")]
	if valid {
		m.authorized++
	}
	m.mu.Unlock()
	if !valid {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var request struct {
		ID     interface{} `json:"id"`
		Method string      `json:"method"`
	}
	body, _ := readAll(r)
	_ = json.Unmarshal(body, &request)
	if request.ID == nil {
		// 通知（notifications/initialized 等）无需响应体。
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Mcp-Session-Id", "mock-session")
	switch request.Method {
	case "initialize":
		writeJSON(w, map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      request.ID,
			"result": map[string]interface{}{
				"protocolVersion": "2025-03-26",
				"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
				"serverInfo":      map[string]interface{}{"name": "mock-oauth-mcp", "version": "1.0.0"},
			},
		})
	case "tools/list":
		writeJSON(w, map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      request.ID,
			"result": map[string]interface{}{
				"tools": []map[string]interface{}{
					{"name": "echo", "description": "echo tool", "inputSchema": map[string]interface{}{"type": "object"}},
				},
			},
		})
	default:
		writeJSON(w, map[string]interface{}{"jsonrpc": "2.0", "id": request.ID, "result": map[string]interface{}{}})
	}
}

func (m *mockOAuthMCP) handleResourceMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]interface{}{
		"resource":              m.MCPURL(),
		"authorization_servers": []string{m.server.URL},
		"scopes_supported":      []string{"read"},
	})
}

func (m *mockOAuthMCP) handleAuthMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]interface{}{
		"issuer":                           m.server.URL,
		"authorization_endpoint":           m.server.URL + "/authorize",
		"token_endpoint":                   m.server.URL + "/token",
		"registration_endpoint":            m.server.URL + "/register",
		"code_challenge_methods_supported": []string{"S256"},
		"grant_types_supported":            []string{"authorization_code", "refresh_token"},
	})
}

func (m *mockOAuthMCP) handleRegister(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.registrations++
	m.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]interface{}{"client_id": "e2e-client"})
}

func (m *mockOAuthMCP) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	challenge := query.Get("code_challenge")
	if challenge == "" || query.Get("redirect_uri") == "" {
		http.Error(w, "missing pkce params", http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	m.issued++
	code := fmt.Sprintf("e2e-code-%d", m.issued)
	m.codes[code] = challenge
	m.mu.Unlock()
	target := query.Get("redirect_uri") + "?code=" + code + "&state=" + query.Get("state")
	http.Redirect(w, r, target, http.StatusFound)
}

func (m *mockOAuthMCP) handleToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		m.mu.Lock()
		challenge, ok := m.codes[r.PostForm.Get("code")]
		m.lastVerifier = r.PostForm.Get("code_verifier")
		m.mu.Unlock()
		if !ok || challengeOf(m.lastVerifier) != challenge {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]string{"error": "invalid_grant"})
			return
		}
		m.issue(w)
	case "refresh_token":
		m.mu.Lock()
		ok := m.refreshTokens[r.PostForm.Get("refresh_token")]
		m.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]string{"error": "invalid_grant"})
			return
		}
		m.issue(w)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func (m *mockOAuthMCP) issue(w http.ResponseWriter) {
	m.mu.Lock()
	m.issued++
	access := fmt.Sprintf("e2e-access-%d", m.issued)
	refresh := fmt.Sprintf("e2e-refresh-%d", m.issued)
	m.accessTokens[access] = true
	m.refreshTokens[refresh] = true
	m.secrets = append(m.secrets, access, refresh)
	m.mu.Unlock()
	writeJSON(w, map[string]interface{}{
		"access_token":  access,
		"refresh_token": refresh,
		"token_type":    "Bearer",
		"expires_in":    3600,
		"scope":         "read",
	})
}

func (m *mockOAuthMCP) CodeVerifier() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastVerifier
}

func (m *mockOAuthMCP) Registrations() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.registrations
}

func (m *mockOAuthMCP) AuthorizedRequests() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.authorized
}

func (m *mockOAuthMCP) Secrets() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.secrets...)
}

func writeJSON(w http.ResponseWriter, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func readAll(r *http.Request) ([]byte, error) {
	defer func() { _ = r.Body.Close() }()
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}

func challengeOf(verifier string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
