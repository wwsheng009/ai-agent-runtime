package auth

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const callbackPath = "/callback"

// AuthorizeOptions 控制一次交互式授权流程。
type AuthorizeOptions struct {
	// NoBrowser 只打印授权 URL，等待用户手动粘贴回调地址/code。
	NoBrowser bool
	// Port 本地回调端口；0 表示使用配置或随机端口。
	Port int
	// Host 回调监听地址，默认 127.0.0.1。
	Host string
	// Timeout 等待授权回调的总超时；0 表示默认 5 分钟。
	Timeout time.Duration
	// Scopes 覆盖配置中的 scope。
	Scopes []string
	// ClientID / ClientSecret 覆盖配置中的客户端凭据。
	ClientID     string
	ClientSecret string
	// AuthServer 覆盖自动发现得到的授权服务器地址。
	AuthServer string
}

// tokenResponse 是 RFC 6749 token 端点的响应（含错误字段）。
type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

type callbackResult struct {
	code  string
	state string
	err   error
}

// Authenticate 执行 Authorization Code + PKCE 流程并持久化令牌。
func (s *Session) Authenticate(ctx context.Context, opts AuthorizeOptions) (*Token, error) {
	if s == nil {
		return nil, fmt.Errorf("OAuth 会话不可用")
	}
	overrideAuthServer := firstNonEmpty(opts.AuthServer, s.cfg.AuthorizationServer)
	discovery, err := Discover(ctx, s.client, s.serverURL, overrideAuthServer)
	if err != nil {
		return nil, err
	}

	scopes := selectScopes(opts.Scopes, s.cfg.Scopes, discovery)
	clientID := firstNonEmpty(opts.ClientID, s.cfg.ClientID)
	clientSecret := firstNonEmpty(opts.ClientSecret, s.cfg.ClientSecret)

	host := strings.TrimSpace(opts.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	port := opts.Port
	if port == 0 {
		port = s.cfg.CallbackPort
	}
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("回调端口无效: %d", port)
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return nil, fmt.Errorf("无法启动本地回调服务器 (%s:%d): %w。可用 --no-browser 手动粘贴回调地址，或在 headers 中配置静态凭证", host, port, err)
	}
	defer func() { _ = listener.Close() }()
	redirectURI := "http://" + listener.Addr().String() + callbackPath

	if clientID == "" {
		if stored, ok := s.storeTokenClient(); ok {
			clientID, clientSecret = stored.ClientID, stored.ClientSecret
		} else if registration := strings.TrimSpace(discovery.AuthServer.RegistrationEndpoint); registration != "" {
			clientID, clientSecret, err = registerClient(ctx, s.client, registration, redirectURI)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, fmt.Errorf("授权服务器未提供动态注册端点（registration_endpoint）且未配置 auth.clientId。%s", manualFallbackHint)
		}
	}

	pkce, err := NewPKCE()
	if err != nil {
		return nil, err
	}
	state, err := NewState()
	if err != nil {
		return nil, err
	}
	resource := firstNonEmpty(s.cfg.Resource, s.serverURL)
	authURL := buildAuthorizeURL(discovery.AuthServer.AuthorizationEndpoint, map[string]string{
		"response_type":         "code",
		"client_id":             clientID,
		"redirect_uri":          redirectURI,
		"code_challenge":        pkce.Challenge,
		"code_challenge_method": pkce.Method,
		"state":                 state,
		"scope":                 strings.Join(scopes, " "),
		"resource":              resource,
	})

	results := make(chan callbackResult, 2)
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		result := parseCallbackQuery(r.URL.Query())
		select {
		case results <- result:
		default:
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if result.err != nil {
			_, _ = io.WriteString(w, failurePage(result.err.Error()))
			return
		}
		_, _ = io.WriteString(w, successPage())
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	fmt.Fprintf(s.stdout, "OAuth 授权：%s\n授权服务器：%s\n", s.serverURL, discovery.AuthServerURL)
	if !opts.NoBrowser {
		if err := s.openBrowser(authURL); err != nil {
			fmt.Fprintf(s.stdout, "自动打开浏览器失败（%v），请手动打开以下链接：\n  %s\n", err, authURL)
		} else {
			fmt.Fprintf(s.stdout, "已尝试打开浏览器；若未自动跳转，请手动访问：\n  %s\n", authURL)
		}
	} else {
		fmt.Fprintf(s.stdout, "请手动打开以下链接完成授权：\n  %s\n", authURL)
	}
	fmt.Fprintf(s.stdout, "若回调页面无法自动返回，可把完整的回调 URL（或其中的 code）粘贴到此处后回车：\n")
	go readManualInput(s.stdin, results)

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = s.flowTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var result callbackResult
	select {
	case result = <-results:
	case <-timer.C:
		return nil, fmt.Errorf("授权超时（%s）。%s", timeout.Round(time.Second), manualFallbackHint)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if result.err != nil {
		return nil, result.err
	}
	if result.state != "" && result.state != state {
		return nil, fmt.Errorf("state 校验失败（可能遭遇 CSRF），请重试")
	}
	if strings.TrimSpace(result.code) == "" {
		return nil, fmt.Errorf("未取得授权码，请重试")
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", result.code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", clientID)
	form.Set("code_verifier", pkce.Verifier)
	form.Set("resource", resource)
	response, err := requestToken(ctx, s.client, discovery.AuthServer.TokenEndpoint, form, clientSecret)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.token = &Token{
		ServerName:   s.name,
		ServerURL:    s.serverURL,
		Resource:     resource,
		AuthServer:   discovery.AuthServerURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURI:  redirectURI,
	}
	s.applyTokenResponseLocked(response, discovery.AuthServerURL, discovery.AuthServer.TokenEndpoint, false)
	if err := s.persistLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.needsAuth = false
	s.reason = ""
	out := *s.token
	s.mu.Unlock()

	fmt.Fprintf(s.stdout, "✅ 授权成功：%s（scope: %s）\n", s.name, firstNonEmpty(out.Scope, "(默认)"))
	return &out, nil
}

// storeTokenClient 复用上次注册/配置的客户端凭据，避免重复注册。
func (s *Session) storeTokenClient() (*Token, bool) {
	if s.store == nil {
		return nil, false
	}
	token, ok := s.store.Get(s.name)
	if !ok || token == nil || strings.TrimSpace(token.ClientID) == "" {
		return nil, false
	}
	if !SameServerURL(token.ServerURL, s.serverURL) {
		return nil, false
	}
	return token, true
}

// buildAuthorizeURL 拼接授权 URL（跳过空参数）。
func buildAuthorizeURL(endpoint string, params map[string]string) string {
	values := url.Values{}
	for key, value := range params {
		if strings.TrimSpace(value) == "" {
			continue
		}
		values.Set(key, value)
	}
	separator := "?"
	if strings.Contains(endpoint, "?") {
		separator = "&"
	}
	return endpoint + separator + values.Encode()
}

// requestToken 调用 token 端点；clientSecret 非空时使用 client_secret_basic。
func requestToken(ctx context.Context, client *http.Client, endpoint string, form url.Values, clientSecret string) (*tokenResponse, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("token 端点为空，无法换取令牌。%s", manualFallbackHint)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(clientSecret) != "" {
		// RFC 6749 §2.3.1：client_id/client_secret 先做 form-urlencode 再 base64。
		basic := base64.StdEncoding.EncodeToString([]byte(
			url.QueryEscape(form.Get("client_id")) + ":" + url.QueryEscape(clientSecret)))
		req.Header.Set("Authorization", "Basic "+basic)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 token 端点失败: %w。%s", err, manualFallbackHint)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取 token 响应失败: %w", err)
	}
	var parsed tokenResponse
	if len(body) > 0 {
		_ = json.Unmarshal(body, &parsed)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if parsed.Error != "" {
			return nil, fmt.Errorf("token 端点返回 %d: %s %s", resp.StatusCode, parsed.Error, parsed.ErrorDescription)
		}
		return nil, fmt.Errorf("token 端点返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if strings.TrimSpace(parsed.AccessToken) == "" {
		return nil, fmt.Errorf("token 端点未返回 access_token")
	}
	return &parsed, nil
}

// registerClient 执行 RFC 7591 动态客户端注册（公共客户端，token_endpoint_auth_method=none）。
func registerClient(ctx context.Context, client *http.Client, endpoint, redirectURI string) (string, string, error) {
	payload := map[string]interface{}{
		"client_name":                "aicli",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"application_type":           "native",
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(raw)))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("动态客户端注册失败: %w。%s", err, manualFallbackHint)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", "", fmt.Errorf("动态客户端注册失败（%d）: %s。可在配置中显式提供 auth.clientId，或%s",
			resp.StatusCode, strings.TrimSpace(string(body)), manualFallbackHint)
	}
	var parsed struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", fmt.Errorf("解析注册响应失败: %w", err)
	}
	if strings.TrimSpace(parsed.ClientID) == "" {
		return "", "", fmt.Errorf("注册响应缺少 client_id")
	}
	return parsed.ClientID, parsed.ClientSecret, nil
}

func parseCallbackQuery(values url.Values) callbackResult {
	if errCode := strings.TrimSpace(values.Get("error")); errCode != "" {
		desc := strings.TrimSpace(values.Get("error_description"))
		if desc != "" {
			return callbackResult{err: fmt.Errorf("授权被拒绝: %s（%s）", errCode, desc)}
		}
		return callbackResult{err: fmt.Errorf("授权被拒绝: %s", errCode)}
	}
	code := strings.TrimSpace(values.Get("code"))
	if code == "" {
		return callbackResult{err: errors.New("回调缺少 code 参数")}
	}
	return callbackResult{code: code, state: strings.TrimSpace(values.Get("state"))}
}

// readManualInput 读取一行输入：完整回调 URL 或裸 code。
func readManualInput(stdin io.Reader, results chan<- callbackResult) {
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !scanner.Scan() {
		return
	}
	line := strings.TrimSpace(scanner.Text())
	if line == "" {
		return
	}
	result := parseManualInput(line)
	select {
	case results <- result:
	default:
	}
}

// parseManualInput 解析用户粘贴的内容（回调 URL 或裸 code）。
func parseManualInput(line string) callbackResult {
	line = strings.TrimSpace(line)
	if parsed, err := url.Parse(line); err == nil && parsed.RawQuery != "" {
		return parseCallbackQuery(parsed.Query())
	}
	if idx := strings.Index(line, "code="); idx >= 0 {
		if parsed, err := url.Parse("http://localhost/?" + line[strings.Index(line, "code="):]); err == nil {
			return parseCallbackQuery(parsed.Query())
		}
	}
	return callbackResult{code: line}
}

// selectScopes 选择本次授权请求的 scope：显式 > 挑战提示 > metadata 建议。
func selectScopes(explicit, configured []string, discovery *Discovery) []string {
	if len(explicit) > 0 {
		return explicit
	}
	if len(configured) > 0 {
		return configured
	}
	if discovery != nil {
		if scope := strings.Fields(discovery.ChallengeScope); len(scope) > 0 {
			return scope
		}
		if len(discovery.ScopesSupported) > 0 {
			return discovery.ScopesSupported
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func successPage() string {
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>授权成功</title></head>
<body style="font-family:system-ui,sans-serif;padding:2rem">
<h2>✅ 授权成功</h2><p>可以关闭此页面并返回 aicli。</p></body></html>`
}

func failurePage(reason string) string {
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>授权失败</title></head>
<body style="font-family:system-ui,sans-serif;padding:2rem">
<h2>❌ 授权失败</h2><p>` + htmlEscape(reason) + `</p></body></html>`
}

func htmlEscape(input string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return replacer.Replace(input)
}
