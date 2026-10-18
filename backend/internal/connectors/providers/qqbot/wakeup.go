// 互动召回消息（官方：is_wakeup=true）。
//
// 契约（bot.q.qq.com 消息收发概述 / 发送单聊消息）：
//   - 用户主动与机器人对话后 30 天内可下发召回消息；
//   - 周期：当天、1-3 天、3-7 天、7-30 天，共 4 个周期，每个周期 1 条；
//   - is_wakeup 与 msg_id / event_id 互斥；
//   - 超出周期或周期内已发 → 平台返回 40034122 / 40034123。
//
// 本机账本按 (chat, 周期起点) 记录，只做提前量；平台始终兜底。
package qqbot

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// wakeupPeriods 是召回消息的 4 个周期上限（官方：当天、1-3 天、3-7 天、7-30 天）。
var wakeupPeriods = []time.Duration{
	24 * time.Hour,
	3 * 24 * time.Hour,
	7 * 24 * time.Hour,
	30 * 24 * time.Hour,
}

// wakeupQuotaError 是召回消息本机预检失败。
type wakeupQuotaError struct {
	chat   string
	period int
}

func (e *wakeupQuotaError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("qqbot: 会话 %s 当前召回周期（第 %d 个）已使用：每个周期仅 1 条", e.chat, e.period)
}

// wakeupLedger 记录每个会话的召回锚点（用户最近一次主动对话时间）与已用周期。
type wakeupLedger struct {
	mu      sync.Mutex
	entries map[string]wakeupEntry
}

type wakeupEntry struct {
	anchor time.Time
	// used 是已使用的周期下标集合（0..3）。
	used [4]bool
}

// allow 预检并占用当前周期的召回名额。anchor 为空时以 now 作为锚点。
// 返回更新后的锚点（调用方持久化到 ProviderData/状态由上层决定）。
func (l *wakeupLedger) allow(chat string, anchor, now time.Time) error {
	if l == nil || chat == "" {
		return nil
	}
	if anchor.IsZero() {
		anchor = now
	}
	if now.Before(anchor) {
		anchor = now
	}
	elapsed := now.Sub(anchor)
	period := -1
	for i, limit := range wakeupPeriods {
		if elapsed <= limit {
			period = i
			break
		}
	}
	if period < 0 {
		return &wakeupQuotaError{chat: chat, period: len(wakeupPeriods)}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = make(map[string]wakeupEntry)
	}
	entry, exists := l.entries[chat]
	if !exists && len(l.entries) >= maxTrackedChats {
		l.entries = make(map[string]wakeupEntry)
	}
	if !exists || !entry.anchor.Equal(anchor) {
		entry = wakeupEntry{anchor: anchor}
	}
	if entry.used[period] {
		l.entries[chat] = entry
		return &wakeupQuotaError{chat: chat, period: period + 1}
	}
	entry.used[period] = true
	l.entries[chat] = entry
	return nil
}

// WakeupMessage 发送一条互动召回消息（is_wakeup=true，单聊）。
//
// 与 Send 的区别：不带 msg_id/msg_seq（互斥），占用召回配额而非主动消息
// 配额；anchor 是用户最近一次主动对话时间（空=以当前时间起算）。
func (p *Provider) WakeupMessage(ctx context.Context, cfg connectors.Config, chat string, msg connectors.OutboundMessage, anchor time.Time) error {
	target, err := parseChat(chat)
	if err != nil {
		return err
	}
	if target.kind != chatKindUser {
		return fmt.Errorf("qqbot: 互动召回仅支持单聊：%s", chat)
	}
	if p.appID(cfg) == "" || p.appSecret(cfg) == "" {
		return fmt.Errorf("qqbot: app_id and app_secret are required")
	}
	if err := p.wakeup.allow(target.chat, anchor, p.now()); err != nil {
		return err
	}

	visibleText, attachments := connectors.ParseOutboundMediaDirectives(msg.Text, qqAttachmentFenceNames...)
	text := strings.TrimSpace(visibleText)
	if text != "" {
		payload := map[string]any{
			"content":   text,
			"msg_type":  0,
			"is_wakeup": true,
		}
		if p.markdownSupport(cfg) {
			payload = map[string]any{
				"markdown":  map[string]any{"content": text},
				"msg_type":  2,
				"is_wakeup": true,
			}
		}
		if err := p.apiJSON(ctx, cfg, http.MethodPost, messagesEndpoint(p.baseFor(cfg), target), payload, nil); err != nil {
			return err
		}
	}
	// 召回消息的媒体仍需先上传；复用普通媒体发送会带主动消息语义，故只
	// 允许文本召回（官方示例与配额均以文本为主）。
	if len(msg.Media) > 0 || len(attachments) > 0 {
		return fmt.Errorf("qqbot: 互动召回暂不支持富媒体（请改用普通主动消息）")
	}
	return nil
}
