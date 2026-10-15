// 被动回复次数本地计数：QQ 对同一 msg_id 的被动回复有次数上限（单聊 4 次 /
// 群 5 次；超限后需改走主动消息）。本账本按 (chat, msg_id) 计数，达到上限即
// **预判**改走主动，避免注定失败的被动调用；新入站消息（新 msg_id）自动重新
// 计数。窗口过期（单聊 60min / 群 5min）仍由平台裁决，失败时按既有逻辑降级。
//
// 边界：进程内计数（重启归零、多实例各自计数），平台始终兜底。
package qqbot

import (
	"strings"
	"sync"
)

const (
	// defaultPassiveReplyLimitUser / Group 是官方被动回复次数上限。
	defaultPassiveReplyLimitUser  = 4
	defaultPassiveReplyLimitGroup = 5
)

// passiveReplyLedger 是进程内 (chat → {msg_id, 已用次数}) 账本。
type passiveReplyLedger struct {
	mu      sync.Mutex
	entries map[string]passiveReplyEntry
}

type passiveReplyEntry struct {
	msgID string
	count int
}

// count 返回该会话对指定 msg_id 已用的被动回复次数（msg_id 变化即视为 0）。
func (l *passiveReplyLedger) count(chat, msgID string) int {
	if l == nil || chat == "" || msgID == "" {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[chat]
	if !ok || entry.msgID != msgID {
		return 0
	}
	return entry.count
}

// record 记一次成功的被动回复（msg_id 变化时重新计数）。
func (l *passiveReplyLedger) record(chat, msgID string) {
	if l == nil || chat == "" || msgID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = make(map[string]passiveReplyEntry)
	}
	entry, exists := l.entries[chat]
	if !exists && len(l.entries) >= maxTrackedChats {
		// 防御性上限：极端多会话场景下丢弃整表重新计数。
		l.entries = make(map[string]passiveReplyEntry)
	}
	if !exists || entry.msgID != msgID {
		entry = passiveReplyEntry{msgID: msgID}
	}
	entry.count++
	l.entries[chat] = entry
}

// passiveReplyLimit 返回该会话类型的被动回复次数上限（Option 可覆盖）。
func (p *Provider) passiveReplyLimit(kind string) int {
	if kind == chatKindGroup {
		if p.passiveLimitGroup > 0 {
			return p.passiveLimitGroup
		}
		return defaultPassiveReplyLimitGroup
	}
	if p.passiveLimitUser > 0 {
		return p.passiveLimitUser
	}
	return defaultPassiveReplyLimitUser
}

// passiveReplyAllowed 预检：该会话当前 msg_id 是否还能走被动回复。
func (p *Provider) passiveReplyAllowed(target chatTarget) bool {
	msgID := strings.TrimSpace(p.lastInboundMsgID(target.chat))
	if msgID == "" {
		return false
	}
	return p.passiveReplies.count(target.chat, msgID) < p.passiveReplyLimit(target.kind)
}
