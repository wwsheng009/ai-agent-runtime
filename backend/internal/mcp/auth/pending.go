package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// PendingAuth 表示一次「已发起、尚未完成」的授权流程。
//
// 与 Session.Authenticate 的区别：Authenticate 是「阻塞式一步完成」（起流程 → 打印 URL →
// 读 stdin / 等回调 → 兑换 → 返回），适合 CLI；PendingAuth 把流程拆成
// 「BeginAuth 起流程并拿到 URL」+「Wait 等浏览器回调」/「TakeCallback 非阻塞取回调」/
// 「Complete 用用户粘贴的回调 URL 或 code 兑换」三段，供无法阻塞终端输入的入口
// （chat TUI、Web 面板）在自己的用户交互节奏里完成授权。
//
// 生命周期：BeginAuth 返回后由调用方持有；用完必须 Close 释放本地回调端口。
// ExpiresAt 只用于提示与清理（不强制中断兑换）。
type PendingAuth struct {
	session   *Session
	discovery *Discovery

	authURL      string
	redirectURI  string
	state        string
	pkce         PKCE
	clientID     string
	clientSecret string
	resource     string
	scopes       []string

	listener net.Listener
	server   *http.Server
	results  chan callbackResult

	// browserErr 记录 BeginAuth 阶段自动打开浏览器的结果，由调用方决定如何提示
	// （CLI 打印「自动打开浏览器失败…」，chat 直接给 URL）。
	browserErr error

	expiresAt time.Time
	closeOnce sync.Once
}

// BeginAuth 启动一次授权流程：发现授权服务器、确定客户端凭据（配置 / 缓存 / 动态注册）、
// 监听本地回调端口、生成 PKCE 与 state、拼出授权 URL。它不打印任何内容、不读取 stdin，
// 因此可以在任意入口调用；NoBrowser 仅影响是否自动打开浏览器。
func (s *Session) BeginAuth(ctx context.Context, opts AuthorizeOptions) (*PendingAuth, error) {
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
	redirectURI := "http://" + listener.Addr().String() + callbackPath

	if clientID == "" {
		if stored, ok := s.storeTokenClient(); ok {
			clientID, clientSecret = stored.ClientID, stored.ClientSecret
		} else if registration := strings.TrimSpace(discovery.AuthServer.RegistrationEndpoint); registration != "" {
			clientID, clientSecret, err = registerClient(ctx, s.client, registration, redirectURI)
			if err != nil {
				_ = listener.Close()
				return nil, err
			}
		} else {
			_ = listener.Close()
			return nil, fmt.Errorf("授权服务器未提供动态注册端点（registration_endpoint）且未配置 auth.clientId。%s", manualFallbackHint)
		}
	}

	pkce, err := NewPKCE()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	state, err := NewState()
	if err != nil {
		_ = listener.Close()
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

	pending := &PendingAuth{
		session:      s,
		discovery:    discovery,
		authURL:      authURL,
		redirectURI:  redirectURI,
		state:        state,
		pkce:         pkce,
		clientID:     clientID,
		clientSecret: clientSecret,
		resource:     resource,
		scopes:       scopes,
		listener:     listener,
		results:      make(chan callbackResult, 2),
		expiresAt:    s.now().Add(s.flowTimeout),
	}
	pending.serveCallback()
	if !opts.NoBrowser {
		pending.browserErr = s.openBrowser(authURL)
	}
	return pending, nil
}

// serveCallback 启动本地回调服务器：浏览器命中回调地址时把 code/state 送入 results。
func (p *PendingAuth) serveCallback() {
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		result := parseCallbackQuery(r.URL.Query())
		select {
		case p.results <- result:
		default:
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if result.err != nil {
			_, _ = w.Write([]byte(failurePage(result.err.Error())))
			return
		}
		_, _ = w.Write([]byte(successPage()))
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	p.server = server
	go func() { _ = server.Serve(p.listener) }()
}

// AuthURL 返回需要用户打开（或已自动打开）的授权链接。
func (p *PendingAuth) AuthURL() string {
	if p == nil {
		return ""
	}
	return p.authURL
}

// RedirectURI 返回本地回调地址（手动粘贴场景下用户会把浏览器最终地址与它对照）。
func (p *PendingAuth) RedirectURI() string {
	if p == nil {
		return ""
	}
	return p.redirectURI
}

// Scopes 返回本次请求的 scope。
func (p *PendingAuth) Scopes() []string {
	if p == nil {
		return nil
	}
	return append([]string(nil), p.scopes...)
}

// ExpiresAt 返回该流程的提示性过期时间（默认 BeginAuth 后 5 分钟）。
func (p *PendingAuth) ExpiresAt() time.Time {
	if p == nil {
		return time.Time{}
	}
	return p.expiresAt
}

// Expired 报告流程是否已超过提示性有效期。
func (p *PendingAuth) Expired() bool {
	if p == nil || p.session == nil || p.expiresAt.IsZero() {
		return false
	}
	return p.session.now().After(p.expiresAt)
}

// BrowserErr 返回自动打开浏览器的结果（nil 表示已尝试且未报错）。
func (p *PendingAuth) BrowserErr() error {
	if p == nil {
		return nil
	}
	return p.browserErr
}

// Wait 阻塞等待浏览器回调（或 ctx/超时结束），成功后兑换并持久化令牌。
func (p *PendingAuth) Wait(ctx context.Context, timeout time.Duration) (*Token, error) {
	if p == nil {
		return nil, fmt.Errorf("授权流程不可用")
	}
	if timeout <= 0 {
		timeout = p.session.flowTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-p.results:
		if result.err != nil {
			return nil, result.err
		}
		return p.finish(ctx, result.code, result.state)
	case <-timer.C:
		return nil, fmt.Errorf("授权超时（%s）。%s", timeout.Round(time.Second), manualFallbackHint)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TakeCallback 非阻塞取出已到达的浏览器回调并兑换令牌；没有回调时返回 ok=false。
func (p *PendingAuth) TakeCallback(ctx context.Context) (token *Token, ok bool, err error) {
	if p == nil {
		return nil, false, fmt.Errorf("授权流程不可用")
	}
	select {
	case result := <-p.results:
		if result.err != nil {
			return nil, true, result.err
		}
		token, err = p.finish(ctx, result.code, result.state)
		return token, true, err
	default:
		return nil, false, nil
	}
}

// Complete 用用户粘贴的内容（完整回调 URL 或裸 code）完成兑换。
func (p *PendingAuth) Complete(ctx context.Context, input string) (*Token, error) {
	if p == nil {
		return nil, fmt.Errorf("授权流程不可用")
	}
	result := parseManualInput(strings.TrimSpace(input))
	if result.err != nil {
		return nil, result.err
	}
	return p.finish(ctx, result.code, result.state)
}

// finish 校验 state、用授权码兑换令牌并持久化（不打印任何内容）。
func (p *PendingAuth) finish(ctx context.Context, code, state string) (*Token, error) {
	code = strings.TrimSpace(code)
	if state != "" && state != p.state {
		return nil, fmt.Errorf("state 校验失败（可能遭遇 CSRF），请重试")
	}
	if code == "" {
		return nil, fmt.Errorf("未取得授权码，请重试")
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", p.redirectURI)
	form.Set("client_id", p.clientID)
	form.Set("code_verifier", p.pkce.Verifier)
	form.Set("resource", p.resource)
	response, err := requestToken(ctx, p.session.client, p.discovery.AuthServer.TokenEndpoint, form, p.clientSecret)
	if err != nil {
		return nil, err
	}

	session := p.session
	session.mu.Lock()
	session.token = &Token{
		ServerName:   session.name,
		ServerURL:    session.serverURL,
		Resource:     p.resource,
		AuthServer:   p.discovery.AuthServerURL,
		ClientID:     p.clientID,
		ClientSecret: p.clientSecret,
		RedirectURI:  p.redirectURI,
	}
	session.applyTokenResponseLocked(response, p.discovery.AuthServerURL, p.discovery.AuthServer.TokenEndpoint, false)
	if err := session.persistLocked(); err != nil {
		session.mu.Unlock()
		return nil, err
	}
	session.needsAuth = false
	session.reason = ""
	out := *session.token
	session.mu.Unlock()
	return &out, nil
}

// Close 关闭本地回调服务器与监听端口；可重复调用。
func (p *PendingAuth) Close() error {
	if p == nil {
		return nil
	}
	var err error
	p.closeOnce.Do(func() {
		if p.server != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = p.server.Shutdown(shutdownCtx)
		}
		if p.listener != nil {
			err = p.listener.Close()
		}
	})
	return err
}
