package channels

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/notify"
)

// IM 是连接器 IM 出站通道的适配器（docs/bots/06-target-architecture.md §3
// 「IM 作为 notify Channel」）：按 kind+会话 映射 AttentionNotice，收集接受
// 连接器（D5 过滤）后经 NotifyScoped 出站——与 gateway 投影 hook 同口径
// （同一实现从 connector_bootstrap 内联逻辑收敛到本适配器）。
type IM struct {
	// Store 用于 D5 逐连接器过滤（AcceptsNotice）与接受集合收集；nil = 纯测试
	// 口径（不查绑定，全量投递）。
	Store connectors.Store
	// NotifyScoped 是连接器侧 scoped 出站（生产实现 Service.NotifyAttentionScoped）。
	NotifyScoped func(ctx context.Context, connectorIDs []string, notice connectors.AttentionNotice) error
}

// Name 实现 notify.Channel。
func (c *IM) Name() string { return "im" }

// Deliver 实现 notify.Channel：ev.Target = 会话 id（SessionRef.SessionID），
// ev.Kind = 投影词表（approval/question/completed/error/waiting）。
// store 非空且无接受连接器 → 静默跳过；未知 kind（AttentionNoticeForProjection
// 不映射）→ 跳过。
func (c *IM) Deliver(ctx context.Context, ev notify.Event) error {
	if c == nil || c.NotifyScoped == nil {
		return nil
	}
	sessionID := strings.TrimSpace(ev.Target)
	if sessionID == "" {
		return nil
	}
	notice, _, ok := connectors.AttentionNoticeForProjection(sessionID, ev.Kind)
	if !ok {
		return nil
	}
	accepted := make([]string, 0, 2)
	if c.Store != nil {
		for _, cfg := range c.Store.ListConfigs() {
			if !cfg.Enabled {
				continue
			}
			if cfg.Policy.AcceptsNotice(notice, func(session connectors.SessionRef) bool {
				for _, binding := range c.Store.ListBindings(cfg.ID) {
					if binding.Session == session {
						return true
					}
				}
				return false
			}) {
				accepted = append(accepted, cfg.ID)
			}
		}
		if len(accepted) == 0 {
			return nil
		}
	}
	return c.NotifyScoped(ctx, accepted, notice)
}
