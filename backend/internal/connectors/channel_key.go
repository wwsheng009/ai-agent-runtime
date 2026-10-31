package connectors

import "strings"

// ChannelKey 返回跨宿主稳定的「通道实例」标识：provider + 账号身份。
//
// 用途：同一份 bot 配置可能同时出现在网关与多个节点（共享 config.yaml）。
// 通知交付账本以 ChannelKey（而不是宿主本地的 connector id）为键，才能保证
// 「同一个通道实例不重复投递」。
//
// 身份来源优先级：
//   - wechat: BotIdentity()（ilink_user_id，平台唯一且扫码重建后稳定）；
//   - telegram: bot_token 的 "<bot_id>:" 前缀（token 变化 = 不同 bot）；
//   - qqbot/feishu: settings.app_id；
//   - 其它/缺失：provider + connector id（仅本宿主内稳定）。
func ChannelKey(cfg Config) string {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	switch provider {
	case "wechat":
		if id := strings.TrimSpace(cfg.BotIdentity()); id != "" {
			return provider + ":" + id
		}
	case "telegram":
		if token := strings.TrimSpace(cfg.Secret("bot_token")); token != "" {
			if idx := strings.Index(token, ":"); idx > 0 {
				return provider + ":" + token[:idx]
			}
		}
	case "qqbot", "feishu":
		if id := strings.TrimSpace(cfg.Setting("app_id", "")); id != "" {
			return provider + ":" + id
		}
	}
	if id := strings.TrimSpace(cfg.ID); id != "" {
		return provider + ":conn:" + id
	}
	return provider
}
