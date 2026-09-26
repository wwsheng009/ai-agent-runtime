package auth

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// Authenticate 是阻塞式一步完成的授权入口（CLI 行为）：
// BeginAuth 起流程 → 打印授权 URL / 尝试打开浏览器 → 等浏览器回调或 stdin 粘贴 →
// 兑换并持久化令牌。需要在自己的交互节奏里分段完成（chat TUI / Web）请直接用
// BeginAuth + PendingAuth.Wait/TakeCallback/Complete。
func (s *Session) Authenticate(ctx context.Context, opts AuthorizeOptions) (*Token, error) {
	pending, err := s.BeginAuth(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = pending.Close() }()

	fmt.Fprintf(s.stdout, "OAuth 授权：%s\n授权服务器：%s\n", s.serverURL, pending.discovery.AuthServerURL)
	if !opts.NoBrowser {
		if browserErr := pending.BrowserErr(); browserErr != nil {
			fmt.Fprintf(s.stdout, "自动打开浏览器失败（%v），请手动打开以下链接：\n  %s\n", browserErr, pending.AuthURL())
		} else {
			fmt.Fprintf(s.stdout, "已尝试打开浏览器；若未自动跳转，请手动访问：\n  %s\n", pending.AuthURL())
		}
	} else {
		fmt.Fprintf(s.stdout, "请手动打开以下链接完成授权：\n  %s\n", pending.AuthURL())
	}
	fmt.Fprintf(s.stdout, "若回调页面无法自动返回，可把完整的回调 URL（或其中的 code）粘贴到此处后回车：\n")
	go readManualInput(s.stdin, pending.results)

	token, err := pending.Wait(ctx, opts.Timeout)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(s.stdout, "✅ 授权成功：%s（scope: %s）\n", s.name, firstNonEmpty(token.Scope, "(默认)"))
	return token, nil
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
