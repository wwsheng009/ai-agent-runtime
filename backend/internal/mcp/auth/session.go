package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

// 默认参数：刷新提前量 60s，交互式授权整体超时 5 分钟。
const (
	DefaultExpirySkew  = 60 * time.Second
	DefaultFlowTimeout = 5 * time.Minute
)

// SessionOptions 是会话的可选依赖（便于测试注入）。
type SessionOptions struct {
	Store       *TokenStore
	HTTPClient  *http.Client
	Now         func() time.Time
	OpenBrowser func(url string) error
	Stdin       io.Reader
	Stdout      io.Writer
	// ExpirySkew 在到期前多久视为过期（默认 60s）。
	ExpirySkew time.Duration
	// FlowTimeout 交互式授权等待回调的总超时（默认 5 分钟）。
	FlowTimeout time.Duration
}

// Session 持有单个 MCP server 的 OAuth 状态，并实现 config.AccessTokenProvider。
//
// 生命周期：manager 在每次 Start/Reload 时为 oauth server 构造会话；
// 会话内部串行化刷新，令牌变更立即落盘。
type Session struct {
	name      string
	serverURL string
	cfg       config.MCPAuthConfig

	store       *TokenStore
	client      *http.Client
	now         func() time.Time
	openBrowser func(string) error
	stdin       io.Reader
	stdout      io.Writer
	expirySkew  time.Duration
	flowTimeout time.Duration

	mu        sync.Mutex
	token     *Token
	needsAuth bool
	reason    string
}

// NewSession 构造会话并从令牌存储加载已有令牌（URL 不一致时视为未登录）。
func NewSession(name, serverURL string, cfg config.MCPAuthConfig, opts SessionOptions) (*Session, error) {
	name = strings.TrimSpace(name)
	serverURL = strings.TrimSpace(serverURL)
	if name == "" {
		return nil, fmt.Errorf("MCP server 名不能为空")
	}
	if serverURL == "" {
		return nil, fmt.Errorf("MCP server URL 为空，无法执行 OAuth")
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	browser := opts.OpenBrowser
	if browser == nil {
		browser = defaultOpenBrowser
	}
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stdin := opts.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	skew := opts.ExpirySkew
	if skew <= 0 {
		skew = DefaultExpirySkew
	}
	flowTimeout := opts.FlowTimeout
	if flowTimeout <= 0 {
		flowTimeout = DefaultFlowTimeout
	}

	session := &Session{
		name:        name,
		serverURL:   serverURL,
		cfg:         cfg,
		store:       opts.Store,
		client:      client,
		now:         now,
		openBrowser: browser,
		stdin:       stdin,
		stdout:      stdout,
		expirySkew:  skew,
		flowTimeout: flowTimeout,
	}
	session.loadToken()
	return session, nil
}

// Name 返回 server 名。
func (s *Session) Name() string { return s.name }

// ServerURL 返回 server URL。
func (s *Session) ServerURL() string { return s.serverURL }

// Token 返回当前令牌副本（可能为 nil）。
func (s *Session) Token() *Token {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == nil {
		return nil
	}
	clone := *s.token
	return &clone
}

// NeedsAuth 返回是否处于「需要用户重新授权」状态及原因。
func (s *Session) NeedsAuth() (bool, string) {
	if s == nil {
		return true, "OAuth 会话不可用"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.needsAuth, s.reason
}

// Ready 表示本地已有未过期的访问令牌（不保证远端接受，401 时仍会自动刷新）。
func (s *Session) Ready() bool {
	if s == nil {
		return false
	}
	status := s.Status()
	return status.Authenticated && !status.NeedsAuth
}

// AccessToken 返回可用令牌；临近过期时自动刷新。
func (s *Session) AccessToken(ctx context.Context) (string, error) {
	if s == nil {
		return "", fmt.Errorf("%w：OAuth 会话不可用。%s", ErrNotAuthenticated, manualFallbackHint)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token == nil || strings.TrimSpace(s.token.AccessToken) == "" {
		return "", s.markNeedsAuthLocked(fmt.Sprintf("尚未完成 OAuth 授权，请运行 `aicli mcp auth %s`", s.name))
	}
	if s.token.Expired(s.now(), s.expirySkew) {
		if strings.TrimSpace(s.token.RefreshToken) == "" {
			return "", s.markNeedsAuthLocked(fmt.Sprintf("访问令牌已过期且无 refresh token，请重新运行 `aicli mcp auth %s`", s.name))
		}
		if err := s.refreshLocked(ctx); err != nil {
			return "", s.markNeedsAuthLocked(fmt.Sprintf("刷新令牌失败（%v），请重新运行 `aicli mcp auth %s`", err, s.name))
		}
	}
	s.needsAuth = false
	s.reason = ""
	return s.token.AccessToken, nil
}

// ForceRefresh 在收到 401/403 后强制刷新一次。
func (s *Session) ForceRefresh(ctx context.Context) (string, error) {
	if s == nil {
		return "", fmt.Errorf("%w：OAuth 会话不可用。%s", ErrNotAuthenticated, manualFallbackHint)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token == nil || strings.TrimSpace(s.token.RefreshToken) == "" {
		return "", s.markNeedsAuthLocked(fmt.Sprintf("访问令牌被拒绝且无 refresh token，请运行 `aicli mcp auth %s`", s.name))
	}
	if err := s.refreshLocked(ctx); err != nil {
		return "", s.markNeedsAuthLocked(fmt.Sprintf("刷新令牌失败（%v），请重新运行 `aicli mcp auth %s`", err, s.name))
	}
	s.needsAuth = false
	s.reason = ""
	return s.token.AccessToken, nil
}

// MarkNeedsAuth 允许传输层在观测到 401/403 时驱动状态（供状态展示用）。
func (s *Session) MarkNeedsAuth(reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.markNeedsAuthLocked(reason)
}

// ClearNeedsAuth 清除「需认证」标记（登录成功后调用）。
func (s *Session) ClearNeedsAuth() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.needsAuth = false
	s.reason = ""
}

// SessionStatus 是给 CLI/状态展示用的快照（不含令牌明文）。
type SessionStatus struct {
	ServerName      string    `json:"serverName"`
	ServerURL       string    `json:"serverUrl"`
	AuthServer      string    `json:"authorizationServer,omitempty"`
	Scope           string    `json:"scope,omitempty"`
	Authenticated   bool      `json:"authenticated"`
	NeedsAuth       bool      `json:"requiresAuth"`
	Reason          string    `json:"reason,omitempty"`
	ExpiresAt       time.Time `json:"expiresAt,omitempty"`
	HasRefreshToken bool      `json:"hasRefreshToken"`
}

// Status 返回会话状态快照。
func (s *Session) Status() SessionStatus {
	status := SessionStatus{ServerName: s.name, ServerURL: s.serverURL}
	if s == nil {
		status.NeedsAuth = true
		status.Reason = "OAuth 会话不可用"
		return status
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	status.NeedsAuth = s.needsAuth
	status.Reason = s.reason
	if s.token != nil {
		status.AuthServer = s.token.AuthServer
		status.Scope = s.token.Scope
		status.ExpiresAt = s.token.ExpiresAt
		status.HasRefreshToken = strings.TrimSpace(s.token.RefreshToken) != ""
		status.Authenticated = strings.TrimSpace(s.token.AccessToken) != "" && !s.token.Expired(s.now(), s.expirySkew)
	}
	return status
}

// Logout 删除本地令牌（返回是否确实存在）。
func (s *Session) Logout() (bool, error) {
	if s == nil {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = nil
	s.needsAuth = false
	s.reason = ""
	if s.store == nil {
		return false, nil
	}
	return s.store.Delete(s.name)
}

// loadToken 从存储加载并校验 server URL 未变。
func (s *Session) loadToken() {
	if s.store == nil {
		return
	}
	token, ok := s.store.Get(s.name)
	if !ok || token == nil {
		return
	}
	if !SameServerURL(token.ServerURL, s.serverURL) {
		// 同一个名字换了地址：视为未登录，避免把旧站令牌发给新站。
		return
	}
	s.mu.Lock()
	s.token = token
	s.mu.Unlock()
}

// refreshLocked 使用 refresh_token 换取新令牌并落盘。必须持有 s.mu。
func (s *Session) refreshLocked(ctx context.Context) error {
	if s.token == nil {
		return ErrNotAuthenticated
	}
	authServer := strings.TrimSpace(s.token.AuthServer)
	if authServer == "" {
		authServer = strings.TrimSpace(s.cfg.AuthorizationServer)
	}
	if authServer == "" {
		return fmt.Errorf("缺少授权服务器地址，无法刷新令牌")
	}
	meta, _, err := fetchAuthServerMetadata(ctx, s.client, authServer)
	if err != nil {
		return err
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", s.token.RefreshToken)
	form.Set("client_id", s.token.ClientID)
	if scope := strings.TrimSpace(s.token.Scope); scope != "" {
		form.Set("scope", scope)
	}
	response, err := requestToken(ctx, s.client, meta.TokenEndpoint, form, s.token.ClientSecret)
	if err != nil {
		return err
	}
	s.applyTokenResponseLocked(response, meta.Issuer, meta.TokenEndpoint, true)
	return s.persistLocked()
}

// applyTokenResponseLocked 合并 token 端点的响应。必须持有 s.mu。
func (s *Session) applyTokenResponseLocked(resp *tokenResponse, authServer, tokenEndpoint string, keepRefresh bool) {
	now := s.now()
	if s.token == nil {
		s.token = &Token{ServerName: s.name, ServerURL: s.serverURL}
	}
	s.token.AccessToken = resp.AccessToken
	if resp.RefreshToken != "" {
		s.token.RefreshToken = resp.RefreshToken
	} else if !keepRefresh {
		s.token.RefreshToken = ""
	}
	s.token.TokenType = strings.TrimSpace(resp.TokenType)
	if scope := strings.TrimSpace(resp.Scope); scope != "" {
		s.token.Scope = scope
	}
	if resp.ExpiresIn > 0 {
		s.token.ExpiresAt = now.Add(time.Duration(resp.ExpiresIn) * time.Second)
	} else {
		s.token.ExpiresAt = time.Time{}
	}
	s.token.ObtainedAt = now
	if strings.TrimSpace(authServer) != "" {
		s.token.AuthServer = authServer
	}
	_ = tokenEndpoint
}

// persistLocked 把当前令牌写入存储。必须持有 s.mu（store 自身也有锁）。
func (s *Session) persistLocked() error {
	if s.store == nil || s.token == nil {
		return nil
	}
	return s.store.Put(s.name, s.token)
}

// markNeedsAuthLocked 标记需认证并返回可行动的错误。必须持有 s.mu。
func (s *Session) markNeedsAuthLocked(reason string) error {
	s.needsAuth = true
	if strings.TrimSpace(reason) != "" {
		s.reason = reason
	} else {
		s.reason = "需要重新完成 OAuth 授权"
	}
	return fmt.Errorf("%w：%s。%s", ErrNotAuthenticated, s.reason, manualFallbackHint)
}
