package wechat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// 扫码登录（ActionProvider）：
//
//	start_login   → GET get_bot_qrcode（bot_type=3），返回二维码内容 + login_id；
//	login_status  → GET get_qrcode_status（长轮询，40s 超时）；
//	                wait → scanned（可带 redirect_host 切换轮询基址）
//	                → confirmed（回填 bot_token/base_url/account_id/owner_user_id）
//	                → expired（自动换新二维码，最多 refresh 3 次）。
//
// login_status 在 confirmed 时返回合并补丁：{"settings":{...},"secrets":{"bot_token":...}}，
// 由调用方（三端前端）通过现有的 PATCH/PUT /api/connectors/{id} 回填到连接器配置；
// 在此之前进程内缓存已确认凭据（24h），RunPolling/Send 可即时生效。
const (
	loginActionStart  = "start_login"
	loginActionStatus = "login_status"

	// 对外的归一化状态（上游原始值见 loginRaw*）。
	loginStatusWait      = "wait"
	loginStatusScanned   = "scanned"
	loginStatusConfirmed = "confirmed"
	loginStatusExpired   = "expired"

	loginRawWait        = "wait"
	loginRawScanned     = "scaned"
	loginRawRedirecting = "scaned_but_redirect"
	loginRawConfirmed   = "confirmed"
	loginRawExpired     = "expired"

	// loginSessionTTL 是未确认会话的有效期（过期后按 status=expired 返回）。
	loginSessionTTL = 8 * time.Minute
	// confirmedLoginTTL 是已确认凭据在进程内缓存的有效期（回填配置前兜底）。
	confirmedLoginTTL = 24 * time.Hour
	// loginHTTPTimeout 是 get_qrcode_status 长轮询超时（上游同值 40s）。
	loginHTTPTimeout = 40 * time.Second
	// loginBotType 是 get_bot_qrcode 的 bot_type 参数（上游固定 3）。
	loginBotType = 3
	// loginMaxRefreshCount 是二维码过期后的自动刷新上限。
	loginMaxRefreshCount = 3
)

// errLoginNotFound 表示该连接器没有可用的扫码登录会话（未开始或已过期清理）。
var errLoginNotFound = errors.New("wechat: login session was not found（请先执行 start_login）")

type qrCodeResponse struct {
	apiEnvelope
	QRCode          string `json:"qrcode"`
	QRCodeImg       string `json:"qrcode_img_content"`
	QRCodeImgString string `json:"qrcode_img_string"`
}

type qrCodeStatusResponse struct {
	apiEnvelope
	Status       string `json:"status"`
	BotToken     string `json:"bot_token"`
	AccountID    string `json:"ilink_bot_id"`
	BaseURL      string `json:"baseurl"`
	OwnerUser    string `json:"ilink_user_id"`
	QRCode       string `json:"qrcode"`
	RedirectHost string `json:"redirect_host"`
}

// loginSession 是一次扫码登录的进程内状态（按连接器缓存）。
type loginSession struct {
	id                  string
	connectorID         string
	baseURL             string
	pollBaseURL         string
	qrCode              string
	qrCodeContent       string
	status              string
	accountID           string
	userID              string
	botToken            string
	refreshCount        int
	createdAt           time.Time
	updatedAt           time.Time
	expiresAt           time.Time
	credentialExpiresAt time.Time
}

// RunAction 实现 connectors.ActionProvider（微信扫码登录）。
func (p *Provider) RunAction(ctx context.Context, cfg connectors.Config, action string, payload map[string]any) (map[string]any, error) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case loginActionStart:
		return p.startLogin(ctx, cfg, payload)
	case loginActionStatus:
		return p.loginStatus(ctx, cfg, payload)
	default:
		return nil, fmt.Errorf("wechat: unknown action %q（已支持 %s / %s）", action, loginActionStart, loginActionStatus)
	}
}

// startLogin 拉取二维码并创建登录会话。
// payload.base_url 可覆盖 iLink 基址（未配置 base_url/api_base 时必需）。
func (p *Provider) startLogin(ctx context.Context, cfg connectors.Config, payload map[string]any) (map[string]any, error) {
	base := firstNonEmpty(payloadString(payload, "base_url", "baseUrl"), p.baseFor(cfg))
	base = strings.TrimRight(base, "/")
	if err := validateBaseURL(base); err != nil {
		return nil, err
	}

	qrCode, qrCodeContent, err := p.fetchQRCode(ctx, base)
	if err != nil {
		return nil, err
	}

	now := p.now().UTC()
	session := &loginSession{
		id:            randomLoginID(),
		connectorID:   connectorKey(cfg),
		baseURL:       base,
		pollBaseURL:   base,
		qrCode:        qrCode,
		qrCodeContent: qrCodeContent,
		status:        loginStatusWait,
		createdAt:     now,
		updatedAt:     now,
		expiresAt:     now.Add(loginSessionTTL),
	}
	p.storeLogin(session)
	return session.view(), nil
}

// loginStatus 查询登录状态；payload.login_id 缺省时取该连接器最近一次会话。
func (p *Provider) loginStatus(ctx context.Context, cfg connectors.Config, payload map[string]any) (map[string]any, error) {
	session, ok := p.loginSessionFor(connectorKey(cfg), payloadString(payload, "login_id", "loginId"))
	if !ok {
		return nil, errLoginNotFound
	}

	now := p.now().UTC()
	if session.status != loginStatusConfirmed && now.After(session.expiresAt) {
		if updated := p.mutateLogin(session.id, func(s *loginSession) { s.status = loginStatusExpired }); updated != nil {
			return updated.view(), nil
		}
		return session.view(), nil
	}
	if session.status == loginStatusConfirmed || session.status == loginStatusExpired {
		// 终态直接回显，避免对已完成的登录重复轮询上游。
		return session.view(), nil
	}

	status, err := p.fetchQRCodeStatus(ctx, session.pollBaseURLOrBase(), session.qrCode)
	if err != nil {
		if isLoginPollTimeout(err) {
			// 长轮询超时/瞬时网络超时 = 用户尚未操作：保持当前状态返回。
			return session.view(), nil
		}
		return nil, err
	}

	switch normalizeLoginStatus(status.Status) {
	case loginStatusScanned:
		updated := p.mutateLogin(session.id, func(s *loginSession) {
			s.status = loginStatusScanned
			if redirected := redirectPollBaseURL(s.baseURL, status.RedirectHost); redirected != "" {
				s.pollBaseURL = redirected
			}
		})
		if updated == nil {
			return nil, errLoginNotFound
		}
		return updated.view(), nil
	case loginStatusConfirmed:
		updated := p.mutateLogin(session.id, func(s *loginSession) {
			s.status = loginStatusConfirmed
			s.botToken = strings.TrimSpace(status.BotToken)
			s.accountID = strings.TrimSpace(status.AccountID)
			s.userID = strings.TrimSpace(status.OwnerUser)
			s.credentialExpiresAt = now.Add(confirmedLoginTTL)
			s.expiresAt = s.credentialExpiresAt
			if base := normalizeAbsoluteURL(firstNonEmpty(status.BaseURL, s.baseURL)); base != "" {
				s.baseURL = base
				s.pollBaseURL = base
			}
		})
		if updated == nil {
			return nil, errLoginNotFound
		}
		return updated.view(), nil
	case loginStatusExpired:
		if session.refreshCount >= loginMaxRefreshCount {
			updated := p.mutateLogin(session.id, func(s *loginSession) { s.status = loginStatusExpired })
			if updated == nil {
				return nil, errLoginNotFound
			}
			return updated.view(), nil
		}
		return p.refreshLogin(ctx, session)
	default:
		// wait：保持状态
		return session.view(), nil
	}
}

// refreshLogin 二维码过期后重新拉取（最多 loginMaxRefreshCount 次）。
func (p *Provider) refreshLogin(ctx context.Context, session *loginSession) (map[string]any, error) {
	qrCode, qrCodeContent, err := p.fetchQRCode(ctx, firstNonEmpty(session.baseURL, session.pollBaseURL))
	if err != nil {
		return nil, err
	}
	now := p.now().UTC()
	updated := p.mutateLogin(session.id, func(s *loginSession) {
		s.qrCode = qrCode
		s.qrCodeContent = qrCodeContent
		s.pollBaseURL = s.baseURL
		s.status = loginStatusWait
		s.expiresAt = now.Add(loginSessionTTL)
		s.refreshCount++
	})
	if updated == nil {
		return nil, errLoginNotFound
	}
	p.logf("wechat: 登录二维码已过期，已自动刷新（第 %d/%d 次）", updated.refreshCount, loginMaxRefreshCount)
	return updated.view(), nil
}

// ── 登录会话缓存（按 connectorID，带过期） ──────────────────────────────────

func (p *Provider) storeLogin(session *loginSession) {
	if session == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.logins == nil {
		p.logins = make(map[string]*loginSession)
	}
	now := p.now().UTC()
	for key, existing := range p.logins {
		if existing == nil {
			delete(p.logins, key)
			continue
		}
		if existing.status == loginStatusConfirmed {
			if now.Before(existing.credentialExpiresAt) {
				continue // 已确认凭据仍在缓存期内
			}
			delete(p.logins, key)
			continue
		}
		if now.After(existing.expiresAt) {
			delete(p.logins, key)
		}
	}
	p.logins[session.connectorID] = session
}

// confirmedSession 返回该连接器最近一次已确认且凭据未过期的登录会话（副本）。
func (p *Provider) confirmedSession(connectorID string) *loginSession {
	connectorID = strings.TrimSpace(connectorID)
	if connectorID == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	session, ok := p.logins[connectorID]
	if !ok || session == nil || session.status != loginStatusConfirmed {
		return nil
	}
	if strings.TrimSpace(session.botToken) == "" || p.now().UTC().After(session.credentialExpiresAt) {
		return nil
	}
	clone := *session
	return &clone
}

// loginSessionFor 查会话：loginID 为空时取该连接器最近更新的会话。
func (p *Provider) loginSessionFor(connectorID, loginID string) (*loginSession, bool) {
	connectorID = strings.TrimSpace(connectorID)
	loginID = strings.TrimSpace(loginID)
	p.mu.Lock()
	defer p.mu.Unlock()

	if loginID != "" {
		for _, session := range p.logins {
			if session != nil && session.id == loginID && session.connectorID == connectorID {
				clone := *session
				return &clone, true
			}
		}
		return nil, false
	}
	session, ok := p.logins[connectorID]
	if !ok || session == nil {
		return nil, false
	}
	clone := *session
	return &clone, true
}

// mutateLogin 在锁内更新指定会话并返回副本（update 返回 nil 表示会话已不存在）。
func (p *Provider) mutateLogin(id string, update func(*loginSession)) *loginSession {
	id = strings.TrimSpace(id)
	if id == "" || update == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, session := range p.logins {
		if session == nil || session.id != id {
			continue
		}
		update(session)
		session.updatedAt = p.now().UTC()
		clone := *session
		return &clone
	}
	return nil
}

func (s *loginSession) pollBaseURLOrBase() string {
	return firstNonEmpty(s.pollBaseURL, s.baseURL)
}

func (s *loginSession) credentialReady() bool {
	return strings.TrimSpace(s.botToken) != "" && strings.TrimSpace(s.accountID) != "" && strings.TrimSpace(s.userID) != ""
}

// view 是对外 JSON 载荷（含 confirmed 时的 settings/secrets 合并补丁）。
func (s *loginSession) view() map[string]any {
	if s == nil {
		return map[string]any{}
	}
	status := normalizeLoginStatus(s.status)
	if status == "" {
		status = loginStatusWait
	}
	view := map[string]any{
		"login_id":        s.id,
		"status":          status,
		"base_url":        s.baseURL,
		"qr_code":         s.qrCode,
		"qr_code_content": s.qrCodeContent,
		"expires_at":      s.expiresAt.UTC().Format(time.RFC3339),
	}
	if s.refreshCount > 0 {
		view["refresh_count"] = s.refreshCount
	}
	if status == loginStatusConfirmed {
		view["account_id"] = s.accountID
		view["user_id"] = s.userID
		view["credential_ready"] = s.credentialReady()
		view["expires_at"] = s.credentialExpiresAt.UTC().Format(time.RFC3339)
		if s.credentialReady() {
			// 回填约定：调用方把这两个对象分别合并进连接器 settings / secrets
			// （PATCH/PUT /api/connectors/{id}），secrets 留空=保持、__clear__=清除。
			view["settings"] = map[string]any{
				"base_url":      s.baseURL,
				"account_id":    s.accountID,
				"owner_user_id": s.userID,
			}
			view["secrets"] = map[string]any{"bot_token": s.botToken}
		}
	}
	return view
}

// ── 上游调用 ────────────────────────────────────────────────────────────────

func (p *Provider) fetchQRCode(ctx context.Context, base string) (string, string, error) {
	query := url.Values{}
	query.Set("bot_type", strconv.Itoa(loginBotType))
	var payload qrCodeResponse
	if err := p.getJSON(ctx, defaultHTTPTimeout, base, "", "/ilink/bot/get_bot_qrcode", query, &payload); err != nil {
		return "", "", err
	}
	qrCode := strings.TrimSpace(payload.QRCode)
	qrCodeContent := firstNonEmpty(payload.QRCodeImg, payload.QRCodeImgString)
	if qrCode == "" || qrCodeContent == "" {
		return "", "", errors.New("wechat: qrcode response was incomplete")
	}
	return qrCode, qrCodeContent, nil
}

func (p *Provider) fetchQRCodeStatus(ctx context.Context, base, qrCode string) (qrCodeStatusResponse, error) {
	query := url.Values{}
	query.Set("qrcode", strings.TrimSpace(qrCode))
	var payload qrCodeStatusResponse
	if err := p.getJSON(ctx, loginHTTPTimeout, base, "", "/ilink/bot/get_qrcode_status", query, &payload); err != nil {
		return qrCodeStatusResponse{}, err
	}
	return payload, nil
}

// normalizeLoginStatus 把上游状态归一为 wait/scanned/confirmed/expired
// （scaned_but_redirect 视为 scanned 并触发轮询基址切换；未知状态按 wait）。
func normalizeLoginStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", loginRawWait:
		return loginStatusWait
	case loginRawScanned, loginRawRedirecting, loginStatusScanned:
		return loginStatusScanned
	case loginRawConfirmed:
		return loginStatusConfirmed
	case loginRawExpired:
		return loginStatusExpired
	default:
		return loginStatusWait
	}
}

// redirectPollBaseURL 处理 scaned_but_redirect：redirect_host 可能是完整 URL
// 或仅主机名（沿用当前基址的 scheme）。
func redirectPollBaseURL(currentBaseURL, redirectHost string) string {
	redirectHost = strings.TrimSpace(redirectHost)
	if redirectHost == "" {
		return ""
	}
	if normalized := normalizeAbsoluteURL(redirectHost); normalized != "" {
		return normalized
	}
	scheme := "https"
	if parsed, err := url.Parse(strings.TrimSpace(currentBaseURL)); err == nil && strings.TrimSpace(parsed.Scheme) != "" {
		scheme = parsed.Scheme
	}
	return (&url.URL{Scheme: scheme, Host: redirectHost}).String()
}

func normalizeAbsoluteURL(value string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(value), "/")
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return trimmed
}

// isLoginPollTimeout 报告错误是否属于「长轮询正常超时」（用户尚未操作）。
func isLoginPollTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var requestErr *requestError
	if errors.As(err, &requestErr) && requestErr != nil && requestErr.cause != nil {
		if errors.Is(requestErr.cause, context.Canceled) {
			return false
		}
		if errors.Is(requestErr.cause, context.DeadlineExceeded) {
			return true
		}
		var netErr net.Error
		if errors.As(requestErr.cause, &netErr) && netErr.Timeout() {
			return true
		}
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func randomLoginID() string {
	buffer := make([]byte, 10)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("wechat_login_%d", time.Now().UnixNano())
	}
	return "wechat_login_" + hex.EncodeToString(buffer)
}

// payloadString 读取可选 JSON 参数（支持 snake_case 与 camelCase 键）。
func payloadString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		if text, ok := value.(string); ok {
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}
