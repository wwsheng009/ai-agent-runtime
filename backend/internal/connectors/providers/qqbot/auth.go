package qqbot

// 授权校验（ActionProvider，docs/bots/07-config-ux.md §3.4/§8）：
//
//	check_credentials → 取 App Access Token（校验 app_id/app_secret）+
//	                    GET /users/@me 拉取机器人信息，返回展示载荷（不含密钥）。
//
// 背景：QQ 官方机器人开放平台**没有扫码登录协议**（与微信 iLink 不同），
// 凭据来自 bot.q.qq.com 后台的 app_id/app_secret；因此三端配置页对 QQ 的
// 等价便捷流 = 保存凭据后的「授权校验」动作 + Activate 自动校验。动作随
// ProviderSchema.Actions（kind=button）声明，三端渲染器零改动支持。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// actionCheckCredentials 是 Schema 声明的动作 key（三端按钮据此调用）。
const actionCheckCredentials = "check_credentials"

// meResponse 是 GET /users/@me 的机器人信息（QQ 开放平台字段子集）。
type meResponse struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Avatar   string `json:"avatar"`
}

// RunAction 实现 connectors.ActionProvider。
func (p *Provider) RunAction(ctx context.Context, cfg connectors.Config, action string, payload map[string]any) (map[string]any, error) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case actionCheckCredentials:
		return p.checkCredentials(ctx, cfg)
	default:
		return nil, fmt.Errorf("qqbot: unknown action %q（已支持 %s）", action, actionCheckCredentials)
	}
}

// checkCredentials 校验凭据并返回机器人信息：
//   - token 端点失败（凭据无效/网络错误）→ error（宿主转 400）；
//   - /users/@me 失败 → error；成功 → 展示载荷（app_id/sandbox/api_base/bot/message）。
func (p *Provider) checkCredentials(ctx context.Context, cfg connectors.Config) (map[string]any, error) {
	appID := p.appID(cfg)
	if appID == "" {
		return nil, errors.New("qqbot: app_id is required")
	}
	if p.appSecret(cfg) == "" {
		return nil, errors.New("qqbot: app_secret is required")
	}
	base := p.baseFor(cfg)
	var me meResponse
	if err := p.apiJSON(ctx, cfg, http.MethodGet, base+"/users/@me", nil, &me); err != nil {
		return nil, err
	}
	username := strings.TrimSpace(me.Username)
	if username == "" {
		username = strings.TrimSpace(me.ID)
	}
	view := map[string]any{
		"ok":       true,
		"action":   actionCheckCredentials,
		"app_id":   appID,
		"sandbox":  p.sandbox(cfg),
		"api_base": base,
		"message":  fmt.Sprintf("校验通过：%s（AppID %s）", firstNonEmpty(username, appID), appID),
	}
	bot := map[string]any{}
	if id := strings.TrimSpace(me.ID); id != "" {
		bot["id"] = id
	}
	if username != "" {
		bot["username"] = username
	}
	if avatar := strings.TrimSpace(me.Avatar); avatar != "" {
		bot["avatar"] = avatar
	}
	if len(bot) > 0 {
		view["bot"] = bot
	}
	return view, nil
}

// VerifyCredentials 实现 connectors.CredentialVerifier：保存路径在落盘前调用，
// 用真实请求验证 app_id/app_secret（复用「授权校验」的 token + GET /users/@me
// 检查，失败文本已脱敏）。校验成功会预热访问令牌缓存（缓存命中条件含
// appID/appSecret 一致，凭据变更后不复用旧令牌——见 accessToken）。
func (p *Provider) VerifyCredentials(ctx context.Context, cfg connectors.Config) error {
	_, err := p.checkCredentials(ctx, cfg)
	return err
}

var _ connectors.CredentialVerifier = (*Provider)(nil)
